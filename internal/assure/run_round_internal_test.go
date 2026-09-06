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
	"time"

	"github.com/P4suta/goatest/internal/config"
	"github.com/P4suta/goatest/internal/evidence"
	goanalysis "github.com/P4suta/goatest/internal/golang"
	"github.com/P4suta/goatest/internal/mutationbridge"
	"github.com/P4suta/goatest/internal/report"
)

const combinedBaselineCollectionCount = 2

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
			h.dependencies.inspectWorkspace = func(context.Context, CommandWorkspace, string, []string, []string, time.Duration) (roundMetadata, error) {
				return roundMetadata{}, cause
			}
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
			if test.name == "initial inputs" && (len(harness.cache.gets) != 0 || len(harness.cache.puts) != 0) {
				t.Fatalf("cache used after identity failure: gets=%v puts=%v", harness.cache.gets, harness.cache.puts)
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
		wantWorkspaces int
	}{
		{name: "scratch", scratchErr: cause, wantWorkspaces: 1},
		{name: "baseline", baselineErr: cause, wantBaseline: 1, wantRemove: 1, wantWorkspaces: completedRoundWorkspaceCount},
		{name: "remove", removeErr: cause, wantBaseline: combinedBaselineCollectionCount, wantRemove: 1, wantWorkspaces: completedRoundWorkspaceCount},
		{name: "both", baselineErr: cause, removeErr: errors.New("remove failed"), wantBaseline: 1, wantRemove: 1, wantWorkspaces: completedRoundWorkspaceCount},
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
				harness.manager.calls != 1 || harness.workspaceCloses != test.wantWorkspaces {
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

func TestRunCoordinatorPrefersBaselineErrorsAndCancelsPreparation(t *testing.T) {
	baselineCause := errors.New("baseline failed")
	preparationCause := errors.New("preparation stopped")
	harness := newRunCoordinatorHarness(t)
	preparationStopped := make(chan struct{})
	harness.dependencies.prepareSession = func(ctx context.Context, _ *mutationbridge.Workspace, _ mutationbridge.PrepareOptions) (MutationSession, error) {
		harness.prepareCalls++
		<-ctx.Done()
		close(preparationStopped)
		return nil, preparationCause
	}
	harness.dependencies.collectBaseline = func(context.Context, CommandWorkspace, goanalysis.Model, []BaselineTarget, BaselineOptions) (BaselineResult, error) {
		harness.baselineCalls++
		return BaselineResult{}, baselineCause
	}
	result, err := harness.run(Options{})
	if !errors.Is(err, baselineCause) || errors.Is(err, preparationCause) || !reflect.DeepEqual(result, report.Report{}) ||
		harness.prepareCalls != 1 || harness.baselineCalls != 1 || harness.workspaceCloses != completedRoundWorkspaceCount {
		t.Fatalf("run = (%+v, %v), harness=%+v", result, err, harness)
	}
	select {
	case <-preparationStopped:
	default:
		t.Fatal("mutation preparation was not joined")
	}
}

func TestRunCoordinatorPublishesStructuralFindingsAndCancelsPreparation(t *testing.T) {
	preparationCause := errors.New("preparation stopped")
	finding := report.Finding{ID: "vet-failure", Kind: "vet-failure", Summary: "go vet rejected the project"}
	harness := newRunCoordinatorHarness(t)
	preparationStopped := make(chan struct{})
	harness.dependencies.prepareSession = func(ctx context.Context, _ *mutationbridge.Workspace, _ mutationbridge.PrepareOptions) (MutationSession, error) {
		harness.prepareCalls++
		<-ctx.Done()
		close(preparationStopped)
		return nil, preparationCause
	}
	harness.dependencies.collectBaseline = func(context.Context, CommandWorkspace, goanalysis.Model, []BaselineTarget, BaselineOptions) (BaselineResult, error) {
		harness.baselineCalls++
		return BaselineResult{Findings: []report.Finding{finding}}, nil
	}
	result, err := harness.run(Options{})
	if err != nil || result.Verdict != report.VerdictDefect || !reflect.DeepEqual(result.Findings, []report.Finding{finding}) ||
		harness.prepareCalls != 1 || harness.baselineCalls != 1 || harness.raceCalls != 0 || harness.mutationCalls != 0 ||
		harness.workspaceCloses != completedRoundWorkspaceCount {
		t.Fatalf("run = (%+v, %v), harness=%+v", result, err, harness)
	}
	select {
	case <-preparationStopped:
	default:
		t.Fatal("mutation preparation was not joined")
	}
}

func TestRunCoordinatorCancelsPreparationWhenThePristineWorkspaceCannotOpen(t *testing.T) {
	openCause := errors.New("pristine workspace failed")
	preparationCause := errors.New("preparation stopped")
	harness := newRunCoordinatorHarness(t)
	openWorkspace := harness.dependencies.openWorkspace
	preparationStopped := make(chan struct{})
	harness.dependencies.openWorkspace = func(ctx context.Context, root string, options mutationbridge.Options) (*mutationbridge.Workspace, error) {
		if harness.openCalls+1 == preparedAndPristineWorkspaceCount {
			harness.openCalls++
			return nil, openCause
		}
		return openWorkspace(ctx, root, options)
	}
	harness.dependencies.prepareSession = func(ctx context.Context, _ *mutationbridge.Workspace, _ mutationbridge.PrepareOptions) (MutationSession, error) {
		harness.prepareCalls++
		<-ctx.Done()
		close(preparationStopped)
		return nil, preparationCause
	}
	result, err := harness.run(Options{})
	if !errors.Is(err, openCause) || errors.Is(err, preparationCause) || !reflect.DeepEqual(result, report.Report{}) ||
		harness.prepareCalls != 1 || harness.baselineCalls != 0 || harness.manager.calls != 1 || harness.workspaceCloses != 1 || harness.scratchRemovals != 1 {
		t.Fatalf("run = (%+v, %v), harness=%+v", result, err, harness)
	}
	select {
	case <-preparationStopped:
	default:
		t.Fatal("mutation preparation was not joined")
	}
}

func TestRunCoordinatorReturnsEachBaselineFindingVerdictWithoutCachingIt(t *testing.T) {
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
			if err != nil || result.Verdict != test.want || !reflect.DeepEqual(result.Findings, []report.Finding{finding}) || len(harness.cache.puts) != 0 ||
				harness.cache.checkpointDeletes != 1 || harness.manager.calls != 1 || harness.workspaceCloses != completedRoundWorkspaceCount || harness.raceCalls != 0 || harness.prepareCalls != 1 {
				t.Fatalf("run = (%+v, %v), harness=%+v", result, err, harness)
			}
		})
	}
	t.Run("round close error", func(t *testing.T) {
		harness := newRunCoordinatorHarness(t)
		harness.baseline.Findings = []report.Finding{{ID: "finding-a", Kind: "baseline-failure"}}
		cause := errors.New("round close failed")
		harness.manager.err = cause
		result, err := harness.run(Options{})
		if !errors.Is(err, cause) || !reflect.DeepEqual(result, report.Report{}) || len(harness.cache.puts) != 0 || harness.cache.checkpointDeletes != 1 {
			t.Fatalf("run = (%+v, %v), harness=%+v", result, err, harness)
		}
	})
}

func TestRunCoordinatorUsesRelevantRaceScopeAndHandlesConcurrencyFailures(t *testing.T) {
	for _, test := range []struct {
		name         string
		contract     string
		excludes     []string
		wantPackages []string
		wantDetail   string
		wantExcluded int
	}{
		{name: "standard", contract: "standard-v1", wantPackages: []string{"fixture.example/module"}, wantDetail: "1 packages", wantExcluded: 1},
		{name: "deep", contract: "deep-v1", wantPackages: []string{"fixture.example/module", "fixture.example/other"}, wantDetail: "2 packages"},
		{name: "deep project exclude", contract: "deep-v1", excludes: []string{"other/**"}, wantPackages: []string{"fixture.example/module"}, wantDetail: "1 packages", wantExcluded: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			harness := newRunCoordinatorHarness(t)
			harness.loaded.Project.Exclude = test.excludes
			result, err := harness.run(Options{Contract: test.contract})
			if err != nil || !slices.Equal(harness.racePackages, test.wantPackages) ||
				result.Accounting.Race.Selected != len(test.wantPackages) || result.Accounting.Race.Excluded != test.wantExcluded ||
				test.contract == "deep-v1" && !slices.Equal(modelPackagePaths(harness.raceModel), test.wantPackages) {
				t.Fatalf("race packages = %v, model=%v, accounting=%+v, err=%v", harness.racePackages, modelPackagePaths(harness.raceModel), result.Accounting.Race, err)
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
	if !errors.Is(err, cause) || !reflect.DeepEqual(result, report.Report{}) || harness.manager.calls != 1 || harness.workspaceCloses != completedRoundWorkspaceCount || harness.raceCalls != 0 {
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
		if !errors.Is(err, cause) || !reflect.DeepEqual(result, report.Report{}) || harness.manager.calls != 1 || harness.workspaceCloses != completedRoundWorkspaceCount {
			t.Fatalf("race failure = (%+v, %v), harness=%+v", result, err, harness)
		}
	})
	t.Run("finding", func(t *testing.T) {
		harness := newRunCoordinatorHarness(t)
		finding := report.Finding{ID: "race-a", Kind: "race", Summary: "data race"}
		harness.race.Findings = []report.Finding{finding}
		result, err := harness.run(Options{})
		if err != nil || result.Verdict != report.VerdictDefect || !reflect.DeepEqual(result.Findings, []report.Finding{finding}) || len(harness.cache.puts) != 0 ||
			harness.cache.checkpointDeletes != 1 || harness.manager.calls != 1 || harness.workspaceCloses != completedRoundWorkspaceCount || harness.prepareCalls != 1 {
			t.Fatalf("race finding = (%+v, %v), harness=%+v", result, err, harness)
		}
	})
	t.Run("round close error", func(t *testing.T) {
		harness := newRunCoordinatorHarness(t)
		harness.race.Findings = []report.Finding{{ID: "race-a", Kind: "race"}}
		cause := errors.New("round close failed")
		harness.manager.err = cause
		result, err := harness.run(Options{})
		if !errors.Is(err, cause) || !reflect.DeepEqual(result, report.Report{}) || len(harness.cache.puts) != 0 || harness.cache.checkpointDeletes != 1 {
			t.Fatalf("race terminal = (%+v, %v), harness=%+v", result, err, harness)
		}
	})
}
