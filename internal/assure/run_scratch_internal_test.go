// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package assure

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/P4suta/goatest/internal/buildcache"
	"github.com/P4suta/goatest/internal/filemode"
	goanalysis "github.com/P4suta/goatest/internal/golang"
	"github.com/P4suta/goatest/internal/report"
	"github.com/P4suta/goatest/internal/tempowner"
	"github.com/P4suta/goatest/internal/trace"
)

func eventDetail(events []Event, kind string) (string, bool) {
	for _, event := range events {
		if event.Kind == kind {
			return event.Detail, true
		}
	}
	return "", false
}

func TestARunPutsEveryTemporaryDirectoryBelowOneRunScratch(t *testing.T) {
	harness := newRunCoordinatorHarness(t)
	temporary := t.TempDir()
	result, err := harness.run(Options{TempDirectory: temporary})
	if err != nil || result.Verdict != report.VerdictAssured {
		t.Fatalf("run = (%+v, %v)", result, err)
	}
	if harness.runScratchParent != temporary || harness.runScratchPattern != "goatest-run-" {
		t.Fatalf("run scratch made in %q as %q, want one below %q named for the run",
			harness.runScratchParent, harness.runScratchPattern, temporary)
	}

	if harness.baselineParent != harness.runScratch || harness.baselinePattern != "baseline-" {
		t.Fatalf("baseline scratch made in %q as %q, want one below the run scratch %q",
			harness.baselineParent, harness.baselinePattern, harness.runScratch)
	}
	validator := harness.generationOptions.RepositoryValidator
	if validator.scratch == nil || validator.scratch.dir != harness.runScratch {
		t.Fatalf("candidate scratch = %+v, want the run scratch %q", validator.scratch, harness.runScratch)
	}
	if harness.workspaceOptions.TempDirectory != harness.runScratch {
		t.Fatalf("mutation workspace temporary root = %q, want the run scratch %q", harness.workspaceOptions.TempDirectory, harness.runScratch)
	}
}

func TestTheRunScratchIsRemovedWhenTheRunEnds(t *testing.T) {
	harness := newRunCoordinatorHarness(t)
	if _, err := harness.run(Options{TempDirectory: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	if harness.runScratchRemovals != 1 {
		t.Fatalf("run scratch removals = %d, want the one the run made", harness.runScratchRemovals)
	}
	if _, err := os.Stat(harness.runScratch); !os.IsNotExist(err) {
		t.Fatalf("stat the run scratch after the run = %v, want it gone", err)
	}
}

func TestTheRunScratchIsOwnedForAsLongAsTheRunUsesIt(t *testing.T) {
	harness := newRunCoordinatorHarness(t)
	root := harness.root
	collect := harness.dependencies.collectBaseline
	var marker tempowner.Marker
	var contended error
	harness.dependencies.collectBaseline = func(ctx context.Context, workspace CommandWorkspace, model goanalysis.Model, targets []BaselineTarget, options BaselineOptions) (BaselineResult, error) {
		marker, _ = tempowner.ReadMarker(harness.runScratch)
		_, contended = tempowner.Claim(harness.runScratch, tempowner.Marker{RunID: "another run"}, time.Now())
		return collect(ctx, workspace, model, targets, options)
	}
	if _, err := harness.run(Options{TempDirectory: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	if marker.Schema != tempowner.Schema || marker.RunID != filepath.Base(harness.runScratch) || marker.Root != root || marker.Kept {
		t.Fatalf("marker during the run = %+v, want this run named as the owner of %q", marker, harness.runScratch)
	}

	if !errors.Is(contended, tempowner.ErrOwned) {
		t.Fatalf("a second claim during the run = %v, want it refused as owned", contended)
	}
}

func TestKeepTempKeepsTheRunScratchAndSaysWhereItIs(t *testing.T) {
	harness := newRunCoordinatorHarness(t)
	sink := harness.record()
	result, err := harness.run(Options{TempDirectory: t.TempDir(), KeepTemp: true})
	if err != nil || result.Verdict != report.VerdictAssured {
		t.Fatalf("run = (%+v, %v)", result, err)
	}
	if harness.runScratchRemovals != 0 {
		t.Fatalf("run scratch removals = %d, want the kept directory left alone", harness.runScratchRemovals)
	}
	marker, err := tempowner.ReadMarker(harness.runScratch)
	if err != nil || !marker.Kept {
		t.Fatalf("marker of a kept run scratch = (%+v, %v), want it recorded as kept on purpose", marker, err)
	}
	kept := trace.ArtifactRecord{Kind: "run-scratch", Path: harness.runScratch}
	if got := recordedArtifacts(sink); !slices.Contains(got, kept) {
		t.Fatalf("recorded artifacts = %+v, want %+v among them", got, kept)
	}
}

func TestARunCollectsWhatEarlierRunsLeftBehindBeforeItWritesAnything(t *testing.T) {
	harness := newRunCoordinatorHarness(t)
	temporary := t.TempDir()
	harness.sweepResult = tempowner.Result{Removed: []string{"/tmp/goatest-run-dead"}, RemovedBytes: 4096, Live: 1, Kept: 2}
	if _, err := harness.run(Options{TempDirectory: temporary}); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(harness.sweepParents, []string{temporary}) {
		t.Fatalf("sweeps = %v, want the one temporary root the run was given", harness.sweepParents)
	}

	want := []string{"goatest-run-"}
	if !slices.Equal(harness.sweptPrefixes, want) {
		t.Fatalf("swept prefixes = %v, want %v", harness.sweptPrefixes, want)
	}
	detail, reported := eventDetail(harness.events, "temp-sweep")
	if !reported || detail != "removed=1 bytes=4096 live=1 kept=2" {
		t.Fatalf("temp-sweep note = (%q, %t), want what the sweep reclaimed", detail, reported)
	}
	if index := slices.IndexFunc(harness.events, func(event Event) bool { return event.Kind == "temp-sweep" }); index != 0 {
		t.Fatalf("temp-sweep was note %d of the run, want it before anything the run did", index)
	}
}

func TestARunNeverSweepsATemporaryDirectoryNobodyNamed(t *testing.T) {
	harness := newRunCoordinatorHarness(t)
	harness.sweepResult = tempowner.Result{Removed: []string{"/tmp/goatest-run-somebody-elses"}}
	if _, err := harness.run(Options{}); err != nil {
		t.Fatal(err)
	}
	if len(harness.sweepParents) != 0 {
		t.Fatalf("sweeps = %q, want none from a run that was given no temporary directory", harness.sweepParents)
	}
	if detail, reported := eventDetail(harness.events, "temp-sweep"); reported {
		t.Fatalf("temp-sweep note = %q, want none from a run that swept nothing", detail)
	}
}

func TestASweepThatFoundNothingSaysNothing(t *testing.T) {
	harness := newRunCoordinatorHarness(t)
	harness.sweepResult = tempowner.Result{Live: 3, Kept: 1}
	if _, err := harness.run(Options{TempDirectory: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	if detail, reported := eventDetail(harness.events, "temp-sweep"); reported {
		t.Fatalf("temp-sweep note = %q, want none from a sweep that reclaimed nothing", detail)
	}
}

func TestRunScratchHousekeepingCannotFailARun(t *testing.T) {
	for _, test := range []struct {
		name    string
		arrange func(*runCoordinatorHarness)
		kind    string
	}{
		{
			name:    "the sweep failed",
			arrange: func(harness *runCoordinatorHarness) { harness.sweepErr = errors.New("permission denied") },
			kind:    "temp-sweep",
		},
		{
			name:    "the scratch could not be removed",
			arrange: func(harness *runCoordinatorHarness) { harness.removeScratchErr = errors.New("device or resource busy") },
			kind:    "temp-unavailable",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			harness := newRunCoordinatorHarness(t)
			test.arrange(harness)
			result, err := harness.run(Options{TempDirectory: t.TempDir()})
			if err != nil || result.Verdict != report.VerdictAssured {
				t.Fatalf("run = (%+v, %v), want the verdict the run established", result, err)
			}
			if _, reported := eventDetail(harness.events, test.kind); !reported {
				t.Fatalf("progress notes = %+v, want a %s note", harness.events, test.kind)
			}
		})
	}
}

func TestARunWithoutAnOwnedScratchStopsBeforeMakingTemporaryChildren(t *testing.T) {
	harness := newRunCoordinatorHarness(t)
	cause := errors.New("no space left on device")
	harness.runScratchErr = cause
	temporary := t.TempDir()
	if _, err := harness.run(Options{TempDirectory: temporary}); !errors.Is(err, cause) {
		t.Fatalf("run error = %v, want %v", err, cause)
	}
	if harness.baselineParent != "" || harness.baselinePattern != "" {
		t.Fatalf("baseline scratch made in %q as %q after the run scratch failed", harness.baselineParent, harness.baselinePattern)
	}
	if _, reported := eventDetail(harness.events, "temp-unavailable"); !reported {
		t.Fatalf("progress notes = %+v, want a temp-unavailable note", harness.events)
	}
}

func TestBuildCacheScratchesHaveSafeOwnersOnTheirRequiredFilesystems(t *testing.T) {
	t.Parallel()
	scratch := runScratch{dir: t.TempDir()}
	base := t.TempDir()
	cache, err := openRunBuildCache("/opt/goatest", base, "", scratch, 2<<30)
	if err != nil || !cache.serves() {
		t.Fatalf("openRunBuildCache = (%+v, %v)", cache, err)
	}
	if cache.scratch != filepath.Join(scratch.dir, "build") {
		t.Fatalf("build cache scratch = %q, want the build directory of %q", cache.scratch, scratch.dir)
	}
	if filepath.Dir(cache.fallback) != cache.scratch || filepath.Base(cache.fallback) != goCacheScratchName {
		t.Fatalf("external backing cache = %q, want it inside %q", cache.fallback, cache.scratch)
	}
	if filepath.Dir(cache.native) != filepath.Dir(base) || !strings.HasPrefix(filepath.Base(cache.native), buildcache.NativeDirectoryPrefix) || cache.nativeOwner == nil {
		t.Fatalf("native build cache = %q, want an owned projection beside %q", cache.native, base)
	}
	if err := releaseBuildCache(Options{}, cache, scratch, time.Now()); err != nil {
		t.Fatal(err)
	}
}

func TestARunWillNotUseAScratchItCouldNotClaim(t *testing.T) {
	for _, test := range []struct {
		name    string
		spoil   func(*testing.T, string)
		removed bool
	}{
		{
			name: "the owner pair cannot be written",

			spoil: func(t *testing.T, dir string) {
				t.Helper()
				if err := os.Mkdir(tempowner.LockPath(dir), filemode.PrivateDirectory); err != nil {
					t.Fatal(err)
				}
			},
			removed: true,
		},
		{
			name: "somebody else holds it",

			spoil: func(t *testing.T, dir string) {
				t.Helper()
				owner, err := tempowner.Claim(dir, tempowner.Marker{RunID: "another run"}, time.Now())
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = owner.Release() })
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			harness := newRunCoordinatorHarness(t)
			temporary := t.TempDir()
			harness.dependencies.makeRunScratch = func(_, pattern string) (string, error) {
				directory, err := os.MkdirTemp(harness.temporary, pattern)
				if err != nil {
					return "", err
				}
				harness.runScratch = directory
				test.spoil(t, directory)
				return directory, nil
			}
			if _, err := harness.run(Options{TempDirectory: temporary}); err == nil {
				t.Fatal("run succeeded with an unclaimed scratch")
			}
			if _, reported := eventDetail(harness.events, "temp-unavailable"); !reported {
				t.Fatalf("progress notes = %+v, want a temp-unavailable note", harness.events)
			}

			if harness.baselineParent != "" || harness.baselinePattern != "" {
				t.Fatalf("baseline scratch made in %q as %q after the run scratch claim failed",
					harness.baselineParent, harness.baselinePattern)
			}
			_, statErr := os.Stat(harness.runScratch)
			if test.removed && !os.IsNotExist(statErr) {
				t.Fatalf("stat the unclaimed directory = %v, want the empty directory taken back", statErr)
			}
			if !test.removed && statErr != nil {
				t.Fatalf("stat the directory somebody else owns = %v, want it left alone", statErr)
			}
		})
	}
}
