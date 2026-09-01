// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package assure

import (
	"context"
	"reflect"
	"testing"

	"github.com/P4suta/goatest/internal/provider"
	"github.com/P4suta/goatest/internal/trace"
)

// A candidate is validated in an isolated copy of the repository, which the
// validator removes when it is done with it. A validator asked to keep its
// temporary directories keeps that copy instead — the tree a rejected candidate
// was rejected in is the one a developer wants to look at — and names it in the
// recording so that the run accounts for what it left behind.
//
// The test replaces process-wide seams and therefore does not run in parallel.
func TestRepositoryValidatorKeepsTheCandidateTreeItWasAskedToKeep(t *testing.T) {
	candidate := provider.Candidate{Kind: "patch", Path: "value_test.go", Content: []byte("candidate")}
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
			preserveCandidateLifecycleSeams(t)
			removed := 0
			makeCandidateTemp = func(string, string) (string, error) { return "isolated-root", nil }
			removeCandidateTemp = func(root string) error {
				removed++
				if root != "isolated-root" {
					t.Fatalf("RemoveAll(%q)", root)
				}
				return nil
			}
			copyCandidateRepository = func(string, string) error { return nil }
			writeCandidateRepositoryFile = func(string, provider.Candidate) error { return nil }
			sink, recorder := newTraceRecording()
			validator := NewRepositoryValidator(RepositoryValidatorOptions{
				Root: "source-root", TempDirectory: "temporary-parent", KeepTemp: test.keep, Trace: recorder,
			})
			acted := 0
			err := validator.withCandidate(t.Context(), candidate, func(_ context.Context, root string) error {
				acted++
				if root != "isolated-root" {
					t.Fatalf("action root = %q", root)
				}
				return nil
			})
			// Keeping the tree changes nothing about the validation that ran
			// in it.
			if err != nil || acted != 1 {
				t.Fatalf("withCandidate = %v, acted=%d", err, acted)
			}
			if removed != test.removals {
				t.Fatalf("candidate removals = %d, want %d", removed, test.removals)
			}
			var want []trace.ArtifactRecord
			if test.kept {
				want = []trace.ArtifactRecord{{Kind: "candidate-tree", Path: "isolated-root"}}
			}
			if got := recordedArtifacts(sink); !reflect.DeepEqual(got, want) {
				t.Fatalf("recorded artifacts = %+v, want %+v", got, want)
			}
		})
	}
}
