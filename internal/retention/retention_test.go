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
