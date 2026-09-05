// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package evidence

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInspectMutationReportsEveryReusableOutcome(t *testing.T) {
	t.Parallel()
	digest := func(character string) string { return strings.Repeat(character, 64) }
	target := TargetKey{Package: "example/module/pkg", Name: "TestValue", Kind: "test", Key: digest("1")}
	finding := &FindingSeed{Kind: "surviving-mutant", Summary: "mutation was not rejected"}
	store := mutationFixture()
	store.Records = append(store.Records,
		MutationRecord{
			MutantID: digest("b"), Path: "value.go", Package: target.Package,
			Outcome: MutationOutcomeSurvived, Provenance: "snapshot=" + digest("f"),
			Exhausted: []TargetKey{target}, Finding: finding,
		},
		MutationRecord{
			MutantID: digest("c"), Path: "value.go", Package: target.Package,
			Outcome: MutationOutcomeUnreached, Provenance: "snapshot=" + digest("f"),
			Suite: &SuiteKey{Package: target.Package, Key: digest("2")}, Finding: finding,
		},
		MutationRecord{
			MutantID: digest("d"), Path: "value.go", Package: target.Package,
			Outcome: MutationOutcomeTimedOut, Provenance: "snapshot=" + digest("f"),
			Exhausted: []TargetKey{target}, Finding: finding,
		},
	)
	path := filepath.Join(t.TempDir(), MutationFileName)
	if err := SaveMutation(path, store); err != nil {
		t.Fatal(err)
	}

	status, err := InspectMutation(path)
	if err != nil {
		t.Fatal(err)
	}
	if !status.Present || !status.Valid || !status.Removable || status.ModulePath != store.ModulePath ||
		status.Records != 4 || status.Killed != 1 || status.Survived != 1 ||
		status.Unreached != 1 || status.TimedOut != 1 || status.Bytes == 0 || status.Modified.IsZero() || status.Problem != "" {
		t.Fatalf("InspectMutation = %+v", status)
	}
}

func TestInspectAndFlushMutationHandleMissingAndMalformedStores(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), MutationFileName)
	missing, err := InspectMutation(path)
	if err != nil || missing.Present || missing.Valid || missing.Removable {
		t.Fatalf("missing InspectMutation = %+v, %v", missing, err)
	}
	if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	invalid, err := InspectMutation(path)
	if err != nil || !invalid.Present || invalid.Valid || !invalid.Removable || !strings.Contains(invalid.Problem, "decode mutation evidence") {
		t.Fatalf("invalid InspectMutation = %+v, %v", invalid, err)
	}

	flushed, err := FlushMutation(path)
	if err != nil || !flushed.Removed || !flushed.Before.Present || flushed.After.Present {
		t.Fatalf("FlushMutation = %+v, %v", flushed, err)
	}
	again, err := FlushMutation(path)
	if err != nil || again.Removed || again.Before.Present || again.After.Present {
		t.Fatalf("second FlushMutation = %+v, %v", again, err)
	}
}

func TestFlushMutationNeverTraversesAStoredSymlink(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.WriteFile(target, []byte("keep me"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, MutationFileName)
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symbolic links unavailable: %v", err)
	}
	status, err := InspectMutation(link)
	if err != nil || !status.Present || status.Valid || !status.Removable || !strings.Contains(status.Problem, "symbolic link") {
		t.Fatalf("symlink InspectMutation = %+v, %v", status, err)
	}
	flushed, err := FlushMutation(link)
	if err != nil || !flushed.Removed {
		t.Fatalf("symlink FlushMutation = %+v, %v", flushed, err)
	}
	if contents, err := os.ReadFile(target); err != nil || string(contents) != "keep me" {
		t.Fatalf("symlink target = %q, %v", contents, err)
	}
}

func TestFlushMutationRefusesADirectory(t *testing.T) {
	t.Parallel()
	directory := filepath.Join(t.TempDir(), "directory-evidence")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := FlushMutation(directory); err == nil || !strings.Contains(err.Error(), "refusing") {
		t.Fatalf("directory FlushMutation error = %v", err)
	}
	if info, err := os.Stat(directory); err != nil || !info.IsDir() {
		t.Fatalf("evidence directory was changed: %+v, %v", info, err)
	}
}

func TestMutationMaintenancePropagatesMetadataAndRemovalFailures(t *testing.T) {
	t.Parallel()
	metadataFailure := errors.New("metadata failure")
	status, err := inspectMutationWithHooks("mutation.json", mutationHooks{
		lstat: func(string) (os.FileInfo, error) { return nil, metadataFailure },
	})
	if !errors.Is(err, metadataFailure) || status != (MutationStatus{}) {
		t.Fatalf("metadata failure = %+v, %v", status, err)
	}

	path := filepath.Join(t.TempDir(), MutationFileName)
	if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	readFailure := errors.New("read failure")
	status, err = inspectMutationWithHooks(path, mutationHooks{
		readStore: func(string) ([]byte, error) { return nil, readFailure },
	})
	if err != nil || !status.Present || status.Valid || !strings.Contains(status.Problem, readFailure.Error()) {
		t.Fatalf("read failure = %+v, %v", status, err)
	}
	removeFailure := errors.New("remove failure")
	flushed, err := flushMutationWithHooks(path, mutationHooks{
		remove: func(string) error { return removeFailure },
	})
	if !errors.Is(err, removeFailure) || flushed != (MutationFlushResult{}) {
		t.Fatalf("remove failure = %+v, %v", flushed, err)
	}
}
