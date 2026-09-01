// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package cache_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/P4suta/goatest/internal/cache"
)

func TestCollectExpiresThenBoundsCacheAndStatusIsAuditable(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	writeCacheEntry(t, root, "expired", 20, now.Add(-31*24*time.Hour))
	writeCacheEntry(t, root, "old", 30, now.Add(-2*time.Hour))
	writeCacheEntry(t, root, "new", 40, now.Add(-time.Hour))

	before, err := cache.Inspect(root)
	if err != nil || before.Entries != 3 || before.Bytes != 90 {
		t.Fatalf("Inspect = %+v, %v", before, err)
	}
	result, err := cache.Collect(root, 40, 30*24*time.Hour, now)
	if err != nil {
		t.Fatal(err)
	}
	if result.RemovedEntries != 2 || result.RemovedBytes != 50 || result.After.Entries != 1 || result.After.Bytes != 40 {
		t.Fatalf("Collect = %+v", result)
	}
	if _, err := os.Stat(filepath.Join(root, "v1", "new", "report.json")); err != nil {
		t.Fatalf("newest bounded entry was removed: %v", err)
	}
}

func TestCollectRejectsNegativePolicyAndConfinedIrregularEntries(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if _, err := cache.Collect(root, -1, 0, time.Now()); err == nil {
		t.Fatal("negative capacity accepted")
	}
	path := filepath.Join(root, "v1", "not-a-directory")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("unexpected"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := cache.Inspect(root); err == nil {
		t.Fatal("irregular cache entry accepted")
	}
}

func writeCacheEntry(t *testing.T, root, id string, size int, modified time.Time) {
	t.Helper()
	path := filepath.Join(root, "v1", id, "report.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, make([]byte, size), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, modified, modified); err != nil {
		t.Fatal(err)
	}
}
