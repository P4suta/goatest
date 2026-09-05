// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package cache

import (
	"os"
	"path/filepath"
	"slices"
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

func TestCheckpointJournalReplaysCompleteUnitsAndCompactsDeterministically(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	digest := strings.Repeat("e", 64)
	store := New(root)
	state := checkpoint.State{
		Schema: checkpoint.SchemaV1, InputDigest: digest, Attempts: 1,
		Baseline: checkpoint.Baseline{BuildVetComplete: true},
		Mutation: &checkpoint.Mutation{CatalogFingerprint: strings.Repeat("f", 64)},
	}
	if err := store.PutCheckpoint(digest, state); err != nil {
		t.Fatal(err)
	}
	target := checkpoint.BaselineTarget{
		ID: "target-a", Executed: true,
		Inventory: report.TargetDisposition{ID: "target-a", Name: "TestA", Status: "passed"},
	}
	mutantZ := checkpoint.MutationResult{
		ID: "mutant-z", Findings: []report.Finding{{ID: "finding-z", Kind: "surviving-mutant", MutantID: "mutant-z", Summary: "survived"}},
	}
	mutantA := checkpoint.MutationResult{
		ID: "mutant-a", Evidence: []report.Evidence{{Kind: "mutation", ID: "mutant-a", Status: "killed"}},
	}
	if err := store.AppendBaselineCheckpoint(digest, target); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendMutationCheckpoint(digest, mutantZ); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendMutationCheckpoint(digest, mutantA); err != nil {
		t.Fatal(err)
	}

	directory := filepath.Join(root, "v1", digest)
	baseData, err := os.ReadFile(filepath.Join(directory, CheckpointFileName))
	if err != nil {
		t.Fatal(err)
	}
	base, err := checkpoint.Decode(baseData)
	if err != nil || len(base.Baseline.Targets) != 0 || len(base.Mutation.Results) != 0 {
		t.Fatalf("journal rewrote base = (%+v, %v)", base, err)
	}
	loaded, found, err := store.GetCheckpoint(digest)
	if err != nil || !found || len(loaded.Baseline.Targets) != 1 || loaded.Baseline.Targets[0].ID != target.ID ||
		!slices.EqualFunc(loaded.Mutation.Results, []checkpoint.MutationResult{mutantA, mutantZ}, func(left, right checkpoint.MutationResult) bool { return left.ID == right.ID }) {
		t.Fatalf("journal replay = (%+v, %t, %v)", loaded, found, err)
	}
	if err := store.PutCheckpoint(digest, loaded); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(directory, CheckpointJournalFileName)); !os.IsNotExist(err) {
		t.Fatalf("compacted journal still exists: %v", err)
	}
	reloaded, found, err := store.GetCheckpoint(digest)
	if err != nil || !found || !slices.EqualFunc(reloaded.Mutation.Results, loaded.Mutation.Results, func(left, right checkpoint.MutationResult) bool { return left.ID == right.ID }) {
		t.Fatalf("compacted checkpoint = (%+v, %t, %v)", reloaded, found, err)
	}
}

func TestCheckpointJournalIgnoresOnlyAnUncommittedTail(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	digest := strings.Repeat("9", 64)
	store := New(root)
	state := checkpoint.State{
		Schema: checkpoint.SchemaV1, InputDigest: digest, Attempts: 1,
		Mutation: &checkpoint.Mutation{CatalogFingerprint: strings.Repeat("8", 64)},
	}
	if err := store.PutCheckpoint(digest, state); err != nil {
		t.Fatal(err)
	}
	unit := checkpoint.MutationResult{
		ID: "mutant-a", Evidence: []report.Evidence{{Kind: "mutation", ID: "mutant-a", Status: "killed"}},
	}
	if err := store.AppendMutationCheckpoint(digest, unit); err != nil {
		t.Fatal(err)
	}
	journalPath := filepath.Join(root, "v1", digest, CheckpointJournalFileName)
	journal, err := os.OpenFile(journalPath, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := journal.WriteString(`{"schema":`); err != nil {
		_ = journal.Close()
		t.Fatal(err)
	}
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}
	loaded, found, err := store.GetCheckpoint(digest)
	if err != nil || !found || len(loaded.Mutation.Results) != 1 || loaded.Mutation.Results[0].ID != unit.ID {
		t.Fatalf("checkpoint with interrupted journal tail = (%+v, %t, %v)", loaded, found, err)
	}
	if err := os.WriteFile(journalPath, append([]byte(`{}`), '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.GetCheckpoint(digest); err == nil || found {
		t.Fatalf("complete corrupt journal = found %t, error %v", found, err)
	}
}
