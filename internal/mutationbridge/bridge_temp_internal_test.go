// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package mutationbridge

import (
	"context"
	"errors"
	"slices"
	"testing"

	gomutants "github.com/P4suta/go-mutants"
)

// The engine copies the whole module into its snapshot, so the tree a mutant
// actually ran in is the one place the question "what did this look like" can
// be answered. A run asked to keep its temporary directories has to be able to
// keep that one too, and the request has exactly one way to reach the engine.
func TestOpenTellsTheEngineWhetherToKeepItsTemporaryDirectories(t *testing.T) {
	for _, keep := range []bool{false, true} {
		original := openMutationWorkspace
		t.Cleanup(func() { openMutationWorkspace = original })
		var opened gomutants.OpenOptions
		openMutationWorkspace = func(_ context.Context, _ string, options gomutants.OpenOptions) (mutationWorkspace, error) {
			opened = options
			return &fakeMutationWorkspace{}, nil
		}
		if _, err := Open(context.Background(), "repository", Options{KeepTemp: keep}); err != nil {
			t.Fatal(err)
		}
		if opened.KeepTemp != keep {
			t.Fatalf("engine KeepTemp = %t, want %t", opened.KeepTemp, keep)
		}
	}
}

// What the engine collected on the way in and what it left behind on the way
// out are facts about the machine that no report carries, so the bridge passes
// them through unchanged for the run to report as progress and as artifacts.
func TestTheWorkspacePassesOnWhatTheEngineSweptAndPreserved(t *testing.T) {
	t.Parallel()
	failure := errors.New("permission denied")
	engine := &fakeMutationWorkspace{
		swept: gomutants.SweepResult{
			Removed: []string{"/tmp/go-mutants-dead"}, RemovedBytes: 8192, Live: 1, Kept: 2, Err: failure,
		},
		preserved: []string{"/tmp/go-mutants-snapshot", "/tmp/go-mutants-scratch"},
	}
	workspace := &Workspace{inner: engine}
	swept := workspace.Swept()
	if !slices.Equal(swept.Removed, []string{"/tmp/go-mutants-dead"}) || swept.RemovedBytes != 8192 ||
		swept.Live != 1 || swept.Kept != 2 || !errors.Is(swept.Err, failure) {
		t.Fatalf("Swept = %+v, want what the engine reported", swept)
	}
	if !slices.Equal(workspace.Preserved(), []string{"/tmp/go-mutants-snapshot", "/tmp/go-mutants-scratch"}) {
		t.Fatalf("Preserved = %v, want the paths the engine kept", workspace.Preserved())
	}
	// A workspace that was never opened swept nothing and kept nothing, which
	// is what every caller that reports on both has to be able to ask.
	for _, empty := range []*Workspace{nil, {}} {
		if got := empty.Swept(); got.Removed != nil || got.Err != nil {
			t.Fatalf("Swept of an unopened workspace = %+v, want nothing", got)
		}
		if got := empty.Preserved(); got != nil {
			t.Fatalf("Preserved of an unopened workspace = %v, want nothing", got)
		}
	}
}
