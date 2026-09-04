// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package cache

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/P4suta/goatest/internal/checkpoint"
	"github.com/P4suta/goatest/internal/report"
)

func TestCheckpointStoreIsAtomicStrictAndIndependentOfCompletedReport(t *testing.T) {
	root := t.TempDir()
	digest := strings.Repeat("a", 64)
	store := New(root)
	state := checkpoint.State{Schema: checkpoint.SchemaV1, InputDigest: digest, Attempts: 1}
	if err := store.PutCheckpoint(digest, state); err != nil {
		t.Fatal(err)
	}
	loaded, found, err := store.GetCheckpoint(digest)
	if err != nil || !found || loaded.Attempts != 1 {
		t.Fatalf("checkpoint = (%+v, %t, %v)", loaded, found, err)
	}
	if _, found, err := store.Get(digest); err != nil || found {
		t.Fatalf("checkpoint appeared as completed report: found=%t err=%v", found, err)
	}
	completed := report.Report{Schema: report.SchemaV1, Verdict: report.VerdictAssured, Snapshot: digest}
	if err := store.Put(digest, completed); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteCheckpoint(digest); err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.GetCheckpoint(digest); err != nil || found {
		t.Fatalf("deleted checkpoint = found %t err %v", found, err)
	}
	if loaded, found, err := store.Get(digest); err != nil || !found || loaded.Snapshot != digest {
		t.Fatalf("completed report after checkpoint delete = (%+v, %t, %v)", loaded, found, err)
	}
}

func TestPendingCheckpointAnswersForTheWholeCacheRatherThanOneDigest(t *testing.T) {
	root := t.TempDir()
	store := New(root)
	// A cache nothing has written yet holds no interrupted run, and saying so is
	// not a failure: it is what every fresh repository looks like.
	if pending, err := store.PendingCheckpoint(); err != nil || pending {
		t.Fatalf("empty cache pending = (%t, %v)", pending, err)
	}
	digest := strings.Repeat("c", 64)
	if err := store.Put(digest, report.Report{Schema: report.SchemaV1, Verdict: report.VerdictAssured, Snapshot: digest}); err != nil {
		t.Fatal(err)
	}
	// A completed report is a run that finished. Only a checkpoint says a run
	// could still come back and ask for what it left outside the cache.
	if pending, err := store.PendingCheckpoint(); err != nil || pending {
		t.Fatalf("completed report pending = (%t, %v)", pending, err)
	}
	if err := store.PutCheckpoint(digest, checkpoint.State{Schema: checkpoint.SchemaV1, InputDigest: digest, Attempts: 1}); err != nil {
		t.Fatal(err)
	}
	if pending, err := store.PendingCheckpoint(); err != nil || !pending {
		t.Fatalf("checkpointed cache pending = (%t, %v)", pending, err)
	}
	if err := store.DeleteCheckpoint(digest); err != nil {
		t.Fatal(err)
	}
	if pending, err := store.PendingCheckpoint(); err != nil || pending {
		t.Fatalf("cleared cache pending = (%t, %v)", pending, err)
	}
}

func TestCheckpointStoreTreatsCorruptFinalAndInterruptedTemporarySafely(t *testing.T) {
	root := t.TempDir()
	digest := strings.Repeat("b", 64)
	store := New(root)
	directory := filepath.Join(root, "v1", digest)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, ".checkpoint-cut.tmp"), []byte(`{"schema":`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.GetCheckpoint(digest); err != nil || found {
		t.Fatalf("interrupted temporary = found %t err %v", found, err)
	}
	if err := os.WriteFile(filepath.Join(directory, CheckpointFileName), []byte(`{"schema":`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.GetCheckpoint(digest); err == nil || found {
		t.Fatalf("corrupt final = found %t err %v", found, err)
	}
}

func TestCheckpointSharesCacheRetentionEntry(t *testing.T) {
	root := t.TempDir()
	digest := strings.Repeat("c", 64)
	store := New(root)
	if err := store.PutCheckpoint(digest, checkpoint.State{Schema: checkpoint.SchemaV1, InputDigest: digest, Attempts: 1}); err != nil {
		t.Fatal(err)
	}
	old := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	path := filepath.Join(root, "v1", digest, CheckpointFileName)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	result, err := Collect(root, 0, time.Hour, old.Add(2*time.Hour))
	if err != nil || result.RemovedEntries != 1 || result.After.Entries != 0 {
		t.Fatalf("checkpoint GC = (%+v, %v)", result, err)
	}
}

func TestCheckpointWriteDefersPolicyCollection(t *testing.T) {
	root := t.TempDir()
	digest := strings.Repeat("d", 64)
	store := NewWithPolicy(root, 1, time.Hour)
	if err := store.PutCheckpoint(digest, checkpoint.State{Schema: checkpoint.SchemaV1, InputDigest: digest, Attempts: 1}); err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.GetCheckpoint(digest); err != nil || !found {
		t.Fatalf("checkpoint was collected during publication: found=%t err=%v", found, err)
	}
}
