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
	"github.com/P4suta/goatest/internal/filemode"
	"github.com/P4suta/goatest/internal/keptledger"
	"github.com/P4suta/goatest/internal/report"
	"github.com/P4suta/goatest/internal/trace"
)

const keptFixturePayloadBytes = 1024

func keptAt() time.Time { return time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC) }

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

	if entry.Bytes <= 0 {
		t.Fatalf("ledger entry bytes = %d, want what the directory held", entry.Bytes)
	}
}

func TestARunThatKeptNothingWritesNoLedger(t *testing.T) {
	harness := newRunCoordinatorHarness(t)
	if _, err := harness.run(Options{TempDirectory: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(keptledger.Path(harness.root)); !os.IsNotExist(err) {
		t.Fatalf("stat the ledger of a run that kept nothing = %v, want no ledger", err)
	}
}

func TestALedgerThatCannotBeWrittenDoesNotFailTheRun(t *testing.T) {
	harness := newRunCoordinatorHarness(t)

	if err := os.MkdirAll(keptledger.Path(harness.root), filemode.PrivateDirectory); err != nil {
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

func TestNestedEngineTemporariesAreNamedWithoutDuplicateLedgerEntries(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	kept := filepath.Join(t.TempDir(), "go-mutants-snapshot")
	if err := os.MkdirAll(kept, filemode.PrivateDirectory); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(kept, "payload"), make([]byte, keptFixturePayloadBytes), filemode.PrivateFile); err != nil {
		t.Fatal(err)
	}
	sink, recorder := newTraceRecording()
	recordTemporaryArtifacts(Options{Trace: recorder}, artifactMutationWorkspace, []string{kept})
	want := []trace.ArtifactRecord{{Kind: "mutation-workspace", Path: kept}}
	if got := recordedArtifacts(sink); !reflect.DeepEqual(got, want) {
		t.Fatalf("recorded artifacts = %+v, want %+v", got, want)
	}
	if _, err := os.Stat(keptledger.Path(root)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("nested engine temporary ledger = %v, want only the run root ledgered", err)
	}
}

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
