// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package assure

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/P4suta/goatest/internal/config"
	"github.com/P4suta/goatest/internal/evidence"
	goanalysis "github.com/P4suta/goatest/internal/golang"
	"github.com/P4suta/goatest/internal/mutationbridge"
	"github.com/P4suta/goatest/internal/report"
)

func TestRunCoordinatorClosesSnapshotOnEveryPreBaselineFailure(t *testing.T) {
	cause := errors.New("phase failed")
	for _, test := range []struct {
		name       string
		change     func(*runCoordinatorHarness)
		wantCloses int
	}{
		{name: "open", change: func(h *runCoordinatorHarness) {
			h.dependencies.openWorkspace = func(context.Context, string, mutationbridge.Options) (*mutationbridge.Workspace, error) {
				return nil, cause
			}
		}},
		{name: "inspect", wantCloses: 1, change: func(h *runCoordinatorHarness) {
			h.dependencies.inspectWorkspace = func(context.Context, CommandWorkspace) (roundMetadata, error) { return roundMetadata{}, cause }
		}},
		{name: "initial inputs", wantCloses: 1, change: func(h *runCoordinatorHarness) {
			h.dependencies.assuranceInputs = func(string, string, Options, config.Config, roundMetadata) (evidence.Inputs, string, error) {
				return evidence.Inputs{}, "", cause
			}
		}},
		{name: "discover targets", wantCloses: 1, change: func(h *runCoordinatorHarness) {
			h.dependencies.discoverTargets = func(string, []goanalysis.Package) ([]goanalysis.Target, error) { return nil, cause }
		}},
		{name: "resources", wantCloses: 1, change: func(h *runCoordinatorHarness) {
			h.dependencies.acquireResources = func(context.Context, config.Config, []goanalysis.Target, []string) (runRoundCloser, []BaselineTarget, []report.Evidence, []string, error) {
				return nil, nil, nil, nil, cause
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			harness := newRunCoordinatorHarness(t)
			test.change(harness)
			result, err := harness.run(Options{})
			if !errors.Is(err, cause) || !reflect.DeepEqual(result, report.Report{}) || harness.workspaceCloses != test.wantCloses {
				t.Fatalf("run = (%+v, %v), closes=%d", result, err, harness.workspaceCloses)
			}
			if test.name != "resources" && harness.resourceCalls != 0 {
				t.Fatalf("resources started after %s failure", test.name)
			}
			if (test.name == "open" || test.name == "inspect" || test.name == "initial inputs") && harness.discoverCalls != 0 {
				t.Fatalf("targets discovered after %s failure", test.name)
			}
		})
	}
}

func TestRunCoordinatorHandlesScratchBaselineAndCleanupFailures(t *testing.T) {
	cause := errors.New("baseline failed")
	for _, test := range []struct {
		name           string
		scratchErr     error
		baselineErr    error
		removeErr      error
		wantBaseline   int
		wantRemove     int
		wantRoundClose int
	}{
		{name: "scratch", scratchErr: cause, wantRoundClose: 1},
		{name: "baseline", baselineErr: cause, wantBaseline: 1, wantRemove: 1, wantRoundClose: 1},
		{name: "remove", removeErr: cause, wantBaseline: 1, wantRemove: 1, wantRoundClose: 1},
		{name: "both", baselineErr: cause, removeErr: errors.New("remove failed"), wantBaseline: 1, wantRemove: 1, wantRoundClose: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			harness := newRunCoordinatorHarness(t)
			removeCalls := 0
			harness.dependencies.makeBaselineScratch = func(string, string) (string, error) { return "scratch", test.scratchErr }
			harness.dependencies.collectBaseline = func(context.Context, CommandWorkspace, goanalysis.Model, []BaselineTarget, BaselineOptions) (BaselineResult, error) {
				harness.baselineCalls++
				return BaselineResult{}, test.baselineErr
			}
			harness.dependencies.removeBaselineScratch = func(string) error { removeCalls++; return test.removeErr }
			result, err := harness.run(Options{})
			if err == nil || !reflect.DeepEqual(result, report.Report{}) || harness.baselineCalls != test.wantBaseline || removeCalls != test.wantRemove ||
				harness.manager.calls != test.wantRoundClose || harness.workspaceCloses != test.wantRoundClose {
				t.Fatalf("run = (%+v, %v), baseline=%d remove=%d manager=%d workspace=%d", result, err, harness.baselineCalls, removeCalls, harness.manager.calls, harness.workspaceCloses)
			}
			if test.scratchErr != nil && !strings.Contains(err.Error(), "create baseline scratch") {
				t.Fatalf("scratch error = %v", err)
			}
			if test.baselineErr != nil && !errors.Is(err, test.baselineErr) {
				t.Fatalf("baseline error = %v", err)
			}
			if test.removeErr != nil && !errors.Is(err, test.removeErr) {
				t.Fatalf("remove error = %v", err)
			}
		})
	}
}

func TestRunCoordinatorReturnsAndCachesEachBaselineFindingVerdict(t *testing.T) {
	for _, test := range []struct {
		kind string
		want report.Verdict
	}{
		{kind: "baseline-failure", want: report.VerdictDefect},
		{kind: "baseline-timeout", want: report.VerdictDefect},
		{kind: "flaky-baseline", want: report.VerdictInsufficient},
	} {
		t.Run(test.kind, func(t *testing.T) {
			harness := newRunCoordinatorHarness(t)
			finding := report.Finding{ID: "finding-a", Kind: test.kind, Summary: "baseline issue"}
			harness.baseline.Findings = []report.Finding{finding}
			result, err := harness.run(Options{})
			if err != nil || result.Verdict != test.want || !reflect.DeepEqual(result.Findings, []report.Finding{finding}) || len(harness.cache.puts) != 1 ||
				harness.manager.calls != 1 || harness.workspaceCloses != 1 || harness.raceCalls != 0 || harness.prepareCalls != 0 {
				t.Fatalf("run = (%+v, %v), harness=%+v", result, err, harness)
			}
		})
	}
	for _, test := range []struct {
		name     string
		closeErr error
		cacheErr error
	}{
		{name: "round close", closeErr: errors.New("round close failed")},
		{name: "cache write", cacheErr: errors.New("cache write failed")},
	} {
		t.Run(test.name, func(t *testing.T) {
			harness := newRunCoordinatorHarness(t)
			harness.baseline.Findings = []report.Finding{{ID: "finding-a", Kind: "baseline-failure"}}
			harness.manager.err = test.closeErr
			harness.cache.putErr = test.cacheErr
			result, err := harness.run(Options{})
			want := test.closeErr
			if want == nil {
				want = test.cacheErr
			}
			if !errors.Is(err, want) || !reflect.DeepEqual(result, report.Report{}) {
				t.Fatalf("run = (%+v, %v), want %v", result, err, want)
			}
			if test.closeErr != nil && len(harness.cache.puts) != 0 {
				t.Fatal("cached report after close failure")
			}
		})
	}
}

func TestRunCoordinatorUsesRelevantRaceScopeAndHandlesConcurrencyFailures(t *testing.T) {
	for _, test := range []struct {
		contract     string
		wantPackages []string
		wantDetail   string
	}{
		{contract: "standard-v1", wantPackages: []string{"fixture.example/module"}, wantDetail: "1 packages"},
		{contract: "deep-v1", wantPackages: []string{"fixture.example/module"}, wantDetail: "2 packages"},
	} {
		t.Run(test.contract, func(t *testing.T) {
			harness := newRunCoordinatorHarness(t)
			_, err := harness.run(Options{Contract: test.contract})
			if err != nil || !slices.Equal(harness.racePackages, test.wantPackages) {
				t.Fatalf("race packages = %v, err=%v", harness.racePackages, err)
			}
			found := false
			for _, event := range harness.events {
				if event.Kind == "race" && event.Detail == test.wantDetail {
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("race event = %+v, want %q", harness.events, test.wantDetail)
			}
		})
	}
	cause := errors.New("concurrency scan failed")
	harness := newRunCoordinatorHarness(t)
	harness.dependencies.concurrencyPackages = func(string, []goanalysis.Package) ([]string, error) { return nil, cause }
	result, err := harness.run(Options{})
	if !errors.Is(err, cause) || !reflect.DeepEqual(result, report.Report{}) || harness.manager.calls != 1 || harness.workspaceCloses != 1 || harness.raceCalls != 0 {
		t.Fatalf("concurrency failure = (%+v, %v), harness=%+v", result, err, harness)
	}
}

func TestRunCoordinatorHandlesRaceExecutionAndFindingTerminals(t *testing.T) {
	t.Run("execution error", func(t *testing.T) {
		harness := newRunCoordinatorHarness(t)
		cause := errors.New("race failed")
		harness.dependencies.collectRaceWithOptions = func(context.Context, CommandWorkspace, goanalysis.Model, []string, string, RaceOptions) (RaceResult, error) {
			return RaceResult{}, cause
		}
		result, err := harness.run(Options{})
		if !errors.Is(err, cause) || !reflect.DeepEqual(result, report.Report{}) || harness.manager.calls != 1 || harness.workspaceCloses != 1 {
			t.Fatalf("race failure = (%+v, %v), harness=%+v", result, err, harness)
		}
	})
	t.Run("finding", func(t *testing.T) {
		harness := newRunCoordinatorHarness(t)
		finding := report.Finding{ID: "race-a", Kind: "race", Summary: "data race"}
		harness.race.Findings = []report.Finding{finding}
		result, err := harness.run(Options{})
		if err != nil || result.Verdict != report.VerdictDefect || !reflect.DeepEqual(result.Findings, []report.Finding{finding}) || len(harness.cache.puts) != 1 ||
			harness.manager.calls != 1 || harness.workspaceCloses != 1 || harness.prepareCalls != 0 {
			t.Fatalf("race finding = (%+v, %v), harness=%+v", result, err, harness)
		}
	})
	for _, test := range []struct {
		name     string
		closeErr error
		cacheErr error
	}{
		{name: "round close", closeErr: errors.New("round close failed")},
		{name: "cache write", cacheErr: errors.New("cache write failed")},
	} {
		t.Run(test.name, func(t *testing.T) {
			harness := newRunCoordinatorHarness(t)
			harness.race.Findings = []report.Finding{{ID: "race-a", Kind: "race"}}
			harness.manager.err = test.closeErr
			harness.cache.putErr = test.cacheErr
			result, err := harness.run(Options{})
			want := test.closeErr
			if want == nil {
				want = test.cacheErr
			}
			if !errors.Is(err, want) || !reflect.DeepEqual(result, report.Report{}) {
				t.Fatalf("race terminal error = (%+v, %v), want %v", result, err, want)
			}
			if test.closeErr != nil && len(harness.cache.puts) != 0 {
				t.Fatal("cached race result after close failure")
			}
		})
	}
}
