// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package assure

import (
	"path/filepath"
	"reflect"
	"testing"

	"github.com/P4suta/goatest/internal/report"
	"github.com/P4suta/goatest/internal/trace"
)

// recordedArtifacts returns the artifacts of a recording in emission order,
// which is what a run says it left on the disk.
func recordedArtifacts(sink *trace.MemorySink) []trace.ArtifactRecord {
	var records []trace.ArtifactRecord
	for _, event := range sink.Events() {
		if event.Type == trace.TypeArtifact && event.Artifact != nil {
			records = append(records, *event.Artifact)
		}
	}
	return records
}

// A round removes the scratch directory it collected its baseline in, unless it
// was asked to keep it. What it keeps it names in the recording, because a
// directory a run left behind and never mentioned is litter rather than
// evidence a developer can find.
func TestKeepTempPreservesTheBaselineScratchAndSaysWhereItIs(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name     string
		keep     bool
		removals int
		kept     bool
	}{
		{name: "removed by default", removals: 1},
		{name: "kept on request", keep: true, kept: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			harness := newRunCoordinatorHarness(t)
			sink := harness.record()
			temporary := t.TempDir()
			result, err := harness.run(Options{KeepTemp: test.keep, TempDirectory: temporary})
			// Keeping a temporary directory is a debugging aid. It decides
			// nothing about the run that kept it.
			if err != nil || result.Verdict != report.VerdictAssured {
				t.Fatalf("run = (%+v, %v)", result, err)
			}
			if harness.scratchRemovals != test.removals {
				t.Fatalf("scratch removals = %d, want %d", harness.scratchRemovals, test.removals)
			}
			var want []trace.ArtifactRecord
			if test.kept {
				// The round's scratch is named while the round runs, and the
				// run scratch it was made below when the run has ended and
				// there is nothing left to put in it.
				want = []trace.ArtifactRecord{
					{Kind: "baseline-scratch", Path: filepath.Join(harness.runScratch, "baseline-scratch")},
					{Kind: "run-scratch", Path: harness.runScratch},
				}
			}
			if got := recordedArtifacts(sink); !reflect.DeepEqual(got, want) {
				t.Fatalf("recorded artifacts = %+v, want %+v", got, want)
			}
		})
	}
}
