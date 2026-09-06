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

	if err := workspace.Close(); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(workspace.Preserved(), []string{"/tmp/go-mutants-snapshot", "/tmp/go-mutants-scratch"}) {
		t.Fatalf("Preserved after Close = %v, want the paths the engine kept", workspace.Preserved())
	}
	if got := workspace.Swept(); !slices.Equal(got.Removed, []string{"/tmp/go-mutants-dead"}) {
		t.Fatalf("Swept after Close = %+v, want what the engine reported", got)
	}

	for _, empty := range []*Workspace{nil, {}} {
		if got := empty.Swept(); got.Removed != nil || got.Err != nil {
			t.Fatalf("Swept of an unopened workspace = %+v, want nothing", got)
		}
		if got := empty.Preserved(); got != nil {
			t.Fatalf("Preserved of an unopened workspace = %v, want nothing", got)
		}
	}
}
