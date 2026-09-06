// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package assure

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/P4suta/goatest/internal/filemode"
	"github.com/P4suta/goatest/internal/keptledger"
	"github.com/P4suta/goatest/internal/provider"
	"github.com/P4suta/goatest/internal/tempowner"
	"github.com/P4suta/goatest/internal/trace"
)

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
			validator.options.scratch = &runScratch{dir: "temporary-parent"}
			acted := 0
			err := validator.withCandidate(t.Context(), candidate, func(_ context.Context, root, temporary string) error {
				acted++
				if root != "isolated-root" || temporary != "temporary-parent" {
					t.Fatalf("action = (%q, %q)", root, temporary)
				}
				return nil
			})

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

func TestStandaloneRepositoryValidatorOwnsAndRemovesOneRunTopology(t *testing.T) {
	t.Parallel()
	repository := t.TempDir()
	if err := os.WriteFile(filepath.Join(repository, "value.go"), []byte("package fixture\n"), filemode.PrivateFile); err != nil {
		t.Fatal(err)
	}
	temporary := t.TempDir()
	validator := NewRepositoryValidator(RepositoryValidatorOptions{Root: repository, TempDirectory: temporary})
	var candidateRoot, runRoot string
	err := validator.withCandidate(t.Context(), provider.Candidate{
		Kind: "patch", Path: "value_test.go", Content: []byte("package fixture\n"),
	}, func(_ context.Context, root, scratch string) error {
		candidateRoot, runRoot = root, scratch
		marker, err := tempowner.ReadMarker(scratch)
		if err != nil || marker.RunID != filepath.Base(scratch) || marker.Root != repository || marker.Kept {
			t.Fatalf("live run owner = (%+v, %v)", marker, err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(runRoot) != temporary || !strings.HasPrefix(filepath.Base(runRoot), runScratchPrefix) {
		t.Fatalf("standalone run scratch = %q, want an owned run below %q", runRoot, temporary)
	}
	if filepath.Dir(candidateRoot) != runRoot || !strings.HasPrefix(filepath.Base(candidateRoot), candidateTreeName) {
		t.Fatalf("standalone candidate = %q, want it below %q", candidateRoot, runRoot)
	}
	if _, err := os.Stat(runRoot); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("standalone run scratch after validation = %v, want it removed", err)
	}
	if _, err := os.Stat(keptledger.Path(repository)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("standalone ledger after removal = %v, want none", err)
	}
}

func TestStandaloneRepositoryValidatorKeepsAndLedgersOnlyItsRunRoot(t *testing.T) {
	t.Parallel()
	repository := t.TempDir()
	if err := os.WriteFile(filepath.Join(repository, "value.go"), []byte("package fixture\n"), filemode.PrivateFile); err != nil {
		t.Fatal(err)
	}
	temporary := t.TempDir()
	moment := time.Date(2026, time.September, 6, 12, 0, 0, 0, time.UTC)
	sink, recorder := newTraceRecording()
	validator := NewRepositoryValidator(RepositoryValidatorOptions{
		Root: repository, TempDirectory: temporary, KeepTemp: true, Trace: recorder,
		Now: func() time.Time { return moment },
	})
	var candidateRoot, runRoot string
	err := validator.withCandidate(t.Context(), provider.Candidate{
		Kind: "patch", Path: "value_test.go", Content: []byte("package fixture\n"),
	}, func(_ context.Context, root, scratch string) error {
		candidateRoot, runRoot = root, scratch
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	marker, err := tempowner.ReadMarker(runRoot)
	if err != nil || !marker.Kept || marker.RunID != filepath.Base(runRoot) || marker.Root != repository {
		t.Fatalf("kept standalone owner = (%+v, %v)", marker, err)
	}
	ledger, err := keptledger.Load(keptledger.Path(repository))
	if err != nil {
		t.Fatal(err)
	}
	if len(ledger.Entries) != 1 || ledger.Entries[0].Path != runRoot || ledger.Entries[0].RunID != marker.RunID || !ledger.Entries[0].KeptAt.Equal(moment) {
		t.Fatalf("standalone kept ledger = %+v, want only %q", ledger.Entries, runRoot)
	}
	want := []trace.ArtifactRecord{
		{Kind: artifactCandidateTree, Path: candidateRoot},
		{Kind: artifactRunScratch, Path: runRoot},
	}
	if got := recordedArtifacts(sink); !reflect.DeepEqual(got, want) {
		t.Fatalf("standalone kept artifacts = %+v, want %+v", got, want)
	}
}
