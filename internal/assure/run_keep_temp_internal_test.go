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

	"github.com/P4suta/goatest/internal/report"
	"github.com/P4suta/goatest/internal/trace"
)

func recordedArtifacts(sink *trace.MemorySink) []trace.ArtifactRecord {
	var records []trace.ArtifactRecord
	for _, event := range sink.Events() {
		if event.Type == trace.TypeArtifact && event.Artifact != nil {
			records = append(records, *event.Artifact)
		}
	}
	return records
}

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

			if err != nil || result.Verdict != report.VerdictAssured {
				t.Fatalf("run = (%+v, %v)", result, err)
			}
			if harness.scratchRemovals != test.removals {
				t.Fatalf("scratch removals = %d, want %d", harness.scratchRemovals, test.removals)
			}
			var want []trace.ArtifactRecord
			if test.kept {
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

func TestReleaseBaselineScratchSelectsExactlyKeepOrRemove(t *testing.T) {
	sentinel := errors.New("remove failed")
	removed := false
	remove := func(path string) error {
		removed = path == "baseline"
		return sentinel
	}
	if err := releaseBaselineScratch(Options{}, remove, "baseline"); !errors.Is(err, sentinel) || !removed {
		t.Fatalf("removed baseline scratch = (removed=%t, err=%v)", removed, err)
	}

	sink, recorder := newTraceRecording()
	removed = false
	if err := releaseBaselineScratch(Options{KeepTemp: true, Trace: recorder}, remove, "baseline"); err != nil || removed {
		t.Fatalf("kept baseline scratch = (removed=%t, err=%v)", removed, err)
	}
	want := []trace.ArtifactRecord{{Kind: artifactBaselineScratch, Path: "baseline"}}
	if got := recordedArtifacts(sink); !reflect.DeepEqual(got, want) {
		t.Fatalf("baseline artifacts = %+v, want %+v", got, want)
	}
}

func TestReleaseBuildCacheRemovesAServingCacheScratch(t *testing.T) {
	directory := t.TempDir()
	cache := runBuildCache{plain: "program", scratch: directory}
	if err := releaseBuildCache(Options{}, cache, runScratch{}, time.Time{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(directory); !os.IsNotExist(err) {
		t.Fatalf("released build cache scratch = %v", err)
	}
}
