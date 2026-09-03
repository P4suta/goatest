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

	gomutants "github.com/P4suta/go-mutants"
	goanalysis "github.com/P4suta/goatest/internal/golang"
	"github.com/P4suta/goatest/internal/report"
	"github.com/P4suta/goatest/internal/tempowner"
	"github.com/P4suta/goatest/internal/trace"
)

// eventDetail is the detail of the first progress note of one kind, and whether
// the run reported one at all.
func eventDetail(events []Event, kind string) (string, bool) {
	for _, event := range events {
		if event.Kind == kind {
			return event.Detail, true
		}
	}
	return "", false
}

// A run writes hundreds of megabytes outside the repository, and until now each
// of those directories was made beside the others in the system temporary
// directory, where nothing tied them together. One scratch directory per run is
// what makes the whole of a run's disk footprint one thing: one owner, one
// removal, and one path a developer looks in.
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
	// Below the scratch the names are short: what a directory is for is the
	// question, and which tool made it is already answered by the parent.
	if harness.baselineParent != harness.runScratch || harness.baselinePattern != "baseline-" {
		t.Fatalf("baseline scratch made in %q as %q, want one below the run scratch %q",
			harness.baselineParent, harness.baselinePattern, harness.runScratch)
	}
	validator := harness.generationOptions.RepositoryValidator
	if validator.TempDirectory != harness.runScratch || validator.TempPrefix != "candidate-" {
		t.Fatalf("candidate trees made in %q as %q, want them below the run scratch %q",
			validator.TempDirectory, validator.TempPrefix, harness.runScratch)
	}
}

// The directory is removed when the run ends, which is the whole point of
// making one: a run that finishes leaves the machine as it found it.
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

// While the run is using it, the directory says who is using it. That is what
// lets the next run tell a live directory from the leftovers of a killed one,
// and it has to be true from before the first byte is written into it.
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
	// The lock is the liveness signal, so a second claim while the run holds it
	// has to be refused however the claimant came by the path.
	if !errors.Is(contended, tempowner.ErrOwned) {
		t.Fatalf("a second claim during the run = %v, want it refused as owned", contended)
	}
}

// Keeping is a debugging aid: the directory stays where it was made, it says it
// was kept on purpose so the next sweep passes it by, and the run records where
// it left it, because a directory left behind and never named is litter.
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

// The sweep runs before this run writes anything, so that a machine holding the
// leftovers of a killed run has the disk back before this one asks for it.
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
	// go-mutants sweeps its own directories behind its own owner files, so
	// goatest sweeps its names and never those.
	want := []string{"goatest-run-", "goatest-baseline-", "goatest-candidate-", "goatest-control-fuzz-", "goatest-build-cache-"}
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

// A run whose caller named no temporary directory sweeps nothing at all. It
// still makes its scratch where the operating system puts one, because creating
// a directory there is what every temporary directory has always done — but
// collecting there on nobody's instruction would reach every goatest directory
// on the machine, including the ones live runs are working in.
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

// A clean machine has nothing to say, and a note nobody can act on is noise in
// every single run of a repository that never crashes.
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

// Everything about this feature is housekeeping, and housekeeping never decides
// a verdict: a run whose sweep failed, whose scratch could not be made, or
// whose scratch could not be removed still measures every mutant and reports
// what it found.
func TestNothingAboutTheRunScratchCanFailARun(t *testing.T) {
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
			name:    "the scratch could not be made",
			arrange: func(harness *runCoordinatorHarness) { harness.runScratchErr = errors.New("no space left on device") },
			kind:    "temp-unavailable",
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

// A run that could not make a scratch directory makes its temporary
// directories where it always did, under the names the next sweep knows: a
// nameless leftover is exactly what this whole change exists to end.
func TestARunWithoutAScratchFallsBackToTheNamesTheSweepKnows(t *testing.T) {
	harness := newRunCoordinatorHarness(t)
	harness.runScratchErr = errors.New("no space left on device")
	temporary := t.TempDir()
	if _, err := harness.run(Options{TempDirectory: temporary}); err != nil {
		t.Fatal(err)
	}
	if harness.baselineParent != temporary || harness.baselinePattern != "goatest-baseline-" {
		t.Fatalf("baseline scratch made in %q as %q, want one beside the run scratch it could not make",
			harness.baselineParent, harness.baselinePattern)
	}
	validator := harness.generationOptions.RepositoryValidator
	if validator.TempDirectory != temporary || validator.TempPrefix != "goatest-candidate-" {
		t.Fatalf("candidate trees made in %q as %q, want them beside the run scratch",
			validator.TempDirectory, validator.TempPrefix)
	}
}

// The fuzz cache an original-control execution needs was the one directory a
// run made in the system temporary directory whatever it had been told, so it
// survived a run that was asked to keep everything and outlived one that was
// not. It belongs below the run scratch like everything else.
func TestTheControlFuzzCacheIsMadeBelowTheRunScratch(t *testing.T) {
	t.Parallel()
	scratch := runScratch{dir: t.TempDir(), fallback: t.TempDir()}
	workspace := &recordingWorkspace{}
	if _, err := runOriginalMutationControl(t.Context(), workspace, gomutants.ExecRequest{
		Package: "./...", Args: []string{"-test.fuzz=FuzzValue"},
	}, nil, scratch); err != nil {
		t.Fatal(err)
	}
	if len(workspace.commands) != 1 {
		t.Fatalf("commands = %+v, want the one control execution", workspace.commands)
	}
	var cache string
	for _, argument := range workspace.commands[0].Argv {
		if directory, found := strings.CutPrefix(argument, "-test.fuzzcachedir="); found {
			cache = directory
		}
	}
	if cache == "" || filepath.Dir(cache) != scratch.dir || !strings.HasPrefix(filepath.Base(cache), "control-fuzz-") {
		t.Fatalf("fuzz cache = %q, want a control-fuzz- directory below the run scratch %q", cache, scratch.dir)
	}
	// It is removed when the control execution returns, exactly as it was.
	if _, err := os.Stat(cache); !os.IsNotExist(err) {
		t.Fatalf("stat the fuzz cache after the execution = %v, want it gone", err)
	}
}

// The build cache's scratch layer is the one directory below the run scratch
// with a fixed name: a run has exactly one of them, and a random suffix would
// only make the layer harder to find.
func TestTheBuildCacheScratchIsTheBuildDirectoryOfTheRunScratch(t *testing.T) {
	t.Parallel()
	scratch := runScratch{dir: t.TempDir(), fallback: t.TempDir()}
	cache, err := openRunBuildCache("/opt/goatest", t.TempDir(), scratch, 2<<30)
	if err != nil || !cache.serves() {
		t.Fatalf("openRunBuildCache = (%+v, %v)", cache, err)
	}
	if cache.scratch != filepath.Join(scratch.dir, "build") {
		t.Fatalf("build cache scratch = %q, want the build directory of %q", cache.scratch, scratch.dir)
	}
	if err := releaseBuildCache(Options{}, cache); err != nil {
		t.Fatal(err)
	}
}
