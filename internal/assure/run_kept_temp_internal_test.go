// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package assure

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	gomutants "github.com/P4suta/go-mutants"
	"github.com/P4suta/goatest/internal/keptledger"
	"github.com/P4suta/goatest/internal/report"
	"github.com/P4suta/goatest/internal/trace"
)

// keptAt is the moment the runs below keep their directories at.
func keptAt() time.Time { return time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC) }

// The tree a mutant ran in is the engine's to make and remove, so a run asked
// to keep everything could not keep the one directory that answers what a
// mutant actually saw.
func TestTheKeepRequestReachesTheMutationEngine(t *testing.T) {
	for _, keep := range []bool{false, true} {
		harness := newRunCoordinatorHarness(t)
		if _, err := harness.run(Options{TempDirectory: t.TempDir(), KeepTemp: keep}); err != nil {
			t.Fatal(err)
		}
		if harness.workspaceOptions.KeepTemp != keep {
			t.Fatalf("mutation workspace KeepTemp = %t, want %t", harness.workspaceOptions.KeepTemp, keep)
		}
	}
}

// A kept directory was named only in the recording, so a successful untraced
// run left gigabytes that only the temporary directory itself listed. The
// ledger is the record that outlives the run, and it is what `cache status`
// lists and `cache gc` collects.
func TestARunRecordsTheDirectoryItKeptInTheLedger(t *testing.T) {
	harness := newRunCoordinatorHarness(t)
	moment := keptAt()
	if _, err := harness.run(Options{
		TempDirectory: t.TempDir(), KeepTemp: true, Now: func() time.Time { return moment },
	}); err != nil {
		t.Fatal(err)
	}
	ledger, err := keptledger.Load(keptledger.Path(harness.root))
	if err != nil {
		t.Fatal(err)
	}
	if len(ledger.Entries) != 1 {
		t.Fatalf("ledger entries = %+v, want the one directory the run kept", ledger.Entries)
	}
	entry := ledger.Entries[0]
	if entry.Path != harness.runScratch || entry.RunID != filepath.Base(harness.runScratch) || !entry.KeptAt.Equal(moment) {
		t.Fatalf("ledger entry = %+v, want the run scratch, named for this run, kept at %s", entry, moment)
	}
	// The size is measured rather than assumed: a kept run scratch holds at
	// least its own owner pair, and what a person deciding whether to care
	// reads is how much disk it is costing them.
	if entry.Bytes <= 0 {
		t.Fatalf("ledger entry bytes = %d, want what the directory held", entry.Bytes)
	}
}

// A run that kept nothing has nothing to record, and a ledger listing no
// directories would still have to be read, written, and explained.
func TestARunThatKeptNothingWritesNoLedger(t *testing.T) {
	harness := newRunCoordinatorHarness(t)
	if _, err := harness.run(Options{TempDirectory: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(keptledger.Path(harness.root)); !os.IsNotExist(err) {
		t.Fatalf("stat the ledger of a run that kept nothing = %v, want no ledger", err)
	}
}

// The ledger is a record of housekeeping, so a ledger that cannot be written
// costs the disk and never the verdict.
func TestALedgerThatCannotBeWrittenDoesNotFailTheRun(t *testing.T) {
	harness := newRunCoordinatorHarness(t)
	// A directory where the file belongs is refused by every read and every
	// rename, on every platform.
	if err := os.MkdirAll(keptledger.Path(harness.root), 0o700); err != nil {
		t.Fatal(err)
	}
	result, err := harness.run(Options{TempDirectory: t.TempDir(), KeepTemp: true})
	if err != nil || result.Verdict != report.VerdictAssured {
		t.Fatalf("run = (%+v, %v), want the verdict the run established", result, err)
	}
	if _, reported := eventDetail(harness.events, "kept-temp-unrecorded"); !reported {
		t.Fatalf("progress notes = %+v, want a kept-temp-unrecorded note", harness.events)
	}
}

// What the engine kept is recorded exactly as what the run itself kept is: an
// artifact of the recording for a developer reading a trace, and a ledger entry
// for the command that has to collect it later.
func TestWhatTheEngineKeptIsNamedAndRecorded(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	kept := filepath.Join(t.TempDir(), "go-mutants-snapshot")
	if err := os.MkdirAll(kept, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(kept, "payload"), make([]byte, 1024), 0o600); err != nil {
		t.Fatal(err)
	}
	sink, recorder := newTraceRecording()
	moment := keptAt()
	recordKept(Options{Trace: recorder}, runScratch{root: root, id: "goatest-run-fixture"},
		artifactMutationWorkspace, []string{kept}, moment)
	want := []trace.ArtifactRecord{{Kind: "mutation-workspace", Path: kept}}
	if got := recordedArtifacts(sink); !reflect.DeepEqual(got, want) {
		t.Fatalf("recorded artifacts = %+v, want %+v", got, want)
	}
	ledger, err := keptledger.Load(keptledger.Path(root))
	if err != nil {
		t.Fatal(err)
	}
	// The size is what a person deciding whether to care reads, so it is
	// measured when the directory is recorded rather than guessed later.
	wantEntries := []keptledger.Entry{{Path: kept, RunID: "goatest-run-fixture", KeptAt: moment, Bytes: 1024}}
	if !reflect.DeepEqual(ledger.Entries, wantEntries) {
		t.Fatalf("ledger entries = %+v, want %+v", ledger.Entries, wantEntries)
	}
}

// The engine sweeps the leftovers of its own killed runs whenever a workspace
// is opened. What it reclaimed is a fact about the machine that no report
// carries, so the run reports it as progress — and says nothing at all when
// there was nothing to reclaim.
func TestTheEngineSweepIsReportedOnlyWhenItDidSomething(t *testing.T) {
	t.Parallel()
	failure := errors.New("permission denied")
	for _, test := range []struct {
		name   string
		swept  gomutants.SweepResult
		detail string
	}{
		{name: "nothing to reclaim", swept: gomutants.SweepResult{Live: 2, Kept: 1}},
		{
			name:   "directories reclaimed",
			swept:  gomutants.SweepResult{Removed: []string{"/tmp/go-mutants-dead"}, RemovedBytes: 4096, Live: 1, Kept: 2},
			detail: "removed=1 bytes=4096 live=1 kept=2",
		},
		{
			name:   "the sweep could not finish",
			swept:  gomutants.SweepResult{Live: 1, Err: failure},
			detail: "removed=0 bytes=0 live=1 kept=0 error=permission denied",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var events []Event
			reportMutationSweep(Options{Progress: func(event Event) { events = append(events, event) }}, test.swept)
			if test.detail == "" {
				if len(events) != 0 {
					t.Fatalf("progress notes = %+v, want none", events)
				}
				return
			}
			want := []Event{{Kind: "mutation-temp-sweep", Detail: test.detail}}
			if !reflect.DeepEqual(events, want) {
				t.Fatalf("progress notes = %+v, want %+v", events, want)
			}
		})
	}
}
