// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package cache

import (
	"os"
	"path/filepath"
	"testing"
)

// A process that ignores the advisory lease can replace an entry after both
// inspections. Removal must unlink that replacement, never traverse it to a
// file outside the descriptor-rooted cache directory.
func TestFlushDoesNotFollowAnEntryReplacedAfterValidation(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	entry := filepath.Join(root, "v1", "entry")
	if err := os.MkdirAll(entry, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(entry, "report.json"), []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	victim := t.TempDir()
	victimReport := filepath.Join(victim, "report.json")
	if err := os.WriteFile(victimReport, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	probe := filepath.Join(root, "symlink-probe")
	if err := os.Symlink(victim, probe); err != nil {
		t.Skipf("symbolic links unavailable: %v", err)
	}
	if err := os.Remove(probe); err != nil {
		t.Fatal(err)
	}

	flushed, err := flushWithHook(root, func() {
		if err := os.Remove(filepath.Join(entry, "report.json")); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(entry); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(victim, entry); err != nil {
			t.Fatal(err)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if flushed.RemovedEntries != 1 || flushed.After.Entries != 0 {
		t.Fatalf("Flush = %+v", flushed)
	}
	if contents, err := os.ReadFile(victimReport); err != nil || string(contents) != "outside" {
		t.Fatalf("outside file = %q, %v", contents, err)
	}
	if _, err := os.Lstat(entry); !os.IsNotExist(err) {
		t.Fatalf("replacement symlink remains: %v", err)
	}
}
