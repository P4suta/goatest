// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package retention

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCollectExpiresThenBoundsDiagnosticDirectoriesDeterministically(t *testing.T) {
	root := t.TempDir()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for index, name := range []string{"old", "middle", "new"} {
		directory := filepath.Join(root, name)
		if err := os.Mkdir(directory, 0o755); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(directory, "trace.jsonl")
		if err := os.WriteFile(path, []byte("1234567890"), 0o600); err != nil {
			t.Fatal(err)
		}
		moment := base.Add(time.Duration(index) * time.Hour)
		if err := os.Chtimes(path, moment, moment); err != nil {
			t.Fatal(err)
		}
	}
	result, err := Collect(root, 10, 90*time.Minute, base.Add(3*time.Hour))
	if err != nil || result.Before.Entries != 3 || result.RemovedEntries != 2 || result.After.Entries != 1 || result.After.Bytes != 10 {
		t.Fatalf("retention collect = (%+v, %v)", result, err)
	}
	if _, err := os.Stat(filepath.Join(root, "new")); err != nil {
		t.Fatalf("newest recording was not retained: %v", err)
	}
}

func TestCollectExpiresEmptyArtifactDirectoryByDirectoryTimestamp(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, "empty")
	if err := os.Mkdir(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	old := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := os.Chtimes(directory, old, old); err != nil {
		t.Fatal(err)
	}
	result, err := Collect(root, 0, time.Hour, old.Add(2*time.Hour))
	if err != nil || result.Before.Entries != 1 || result.Before.Bytes != 0 || result.RemovedEntries != 1 || result.After.Entries != 0 {
		t.Fatalf("empty retention collect = (%+v, %v)", result, err)
	}
	if _, err := os.Stat(directory); !os.IsNotExist(err) {
		t.Fatalf("expired empty directory remained: %v", err)
	}
}

// retainedDirectory makes one child of root holding a ten-byte file stamped at
// moment, which is the shape of a report run directory as far as the count
// bound is concerned.
func retainedDirectory(t *testing.T, root, name string, moment time.Time) {
	t.Helper()
	directory := filepath.Join(root, name)
	if err := os.Mkdir(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "assurance-report-v1.json")
	if err := os.WriteFile(path, []byte("1234567890"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, moment, moment); err != nil {
		t.Fatal(err)
	}
}

func TestKeepRemovesOldestFirstAndTiesByName(t *testing.T) {
	root := t.TempDir()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	retainedDirectory(t, root, "run-a", base)
	// Two runs of the same age: the order has to stay total, so the name breaks
	// the tie exactly as it does for a byte budget.
	retainedDirectory(t, root, "run-c", base.Add(time.Hour))
	retainedDirectory(t, root, "run-b", base.Add(time.Hour))
	retainedDirectory(t, root, "run-d", base.Add(2*time.Hour))
	result, err := Keep(root, 2, nil, base.Add(3*time.Hour))
	if err != nil || result.Before.Entries != 4 || result.RemovedEntries != 2 || result.RemovedBytes != 20 || result.After.Entries != 2 {
		t.Fatalf("keep = (%+v, %v)", result, err)
	}
	for _, name := range []string{"run-c", "run-d"} {
		if _, err := os.Stat(filepath.Join(root, name)); err != nil {
			t.Fatalf("entry %s inside the bound was collected: %v", name, err)
		}
	}
	for _, name := range []string{"run-a", "run-b"} {
		if _, err := os.Stat(filepath.Join(root, name)); !os.IsNotExist(err) {
			t.Fatalf("entry %s beyond the bound remained: %v", name, err)
		}
	}
}

func TestKeepSparesAProtectedEntryOnTopOfTheBound(t *testing.T) {
	root := t.TempDir()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for index, name := range []string{"run-a", "run-b", "run-c", "run-d"} {
		retainedDirectory(t, root, name, base.Add(time.Duration(index)*time.Hour))
	}
	// The oldest entry is the one something still points at, so the bound keeps
	// the newest two and this one as well rather than instead of one of them.
	result, err := Keep(root, 2, func(name string) bool { return name == "run-a" }, base.Add(4*time.Hour))
	if err != nil || result.RemovedEntries != 1 || result.After.Entries != 3 {
		t.Fatalf("protected keep = (%+v, %v)", result, err)
	}
	if _, err := os.Stat(filepath.Join(root, "run-a")); err != nil {
		t.Fatalf("protected entry was collected: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "run-b")); !os.IsNotExist(err) {
		t.Fatalf("unprotected entry beyond the bound remained: %v", err)
	}
}

func TestKeepWithoutASurplusOrWithoutABoundRemovesNothing(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, keep := range []int{0, -1, 2, 5} {
		root := t.TempDir()
		retainedDirectory(t, root, "run-a", base)
		retainedDirectory(t, root, "run-b", base.Add(time.Hour))
		result, err := Keep(root, keep, nil, base.Add(2*time.Hour))
		if err != nil || result.RemovedEntries != 0 || result.After.Entries != 2 {
			t.Fatalf("keep %d = (%+v, %v), want an untouched root", keep, result, err)
		}
	}
	// A root nothing has written yet is an empty history rather than a failure.
	result, err := Keep(filepath.Join(t.TempDir(), "absent"), 1, nil, base)
	if err != nil || result.Before.Entries != 0 || result.RemovedEntries != 0 {
		t.Fatalf("absent root keep = (%+v, %v)", result, err)
	}
}

func TestKeepRefusesAChildThatIsNotADirectory(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "notes.txt"), []byte("hand written"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Keep(root, 1, nil, time.Time{}); err == nil {
		t.Fatal("the count bound accepted a root holding something other than run directories")
	}
}

func TestRetentionRefusesSymlinkedEntries(t *testing.T) {
	root := t.TempDir()
	target := t.TempDir()
	if err := os.Symlink(target, filepath.Join(root, "linked")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := Inspect(root); err == nil {
		t.Fatal("retention followed a symbolic link")
	}
}
