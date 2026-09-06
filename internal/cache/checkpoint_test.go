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
	"github.com/P4suta/goatest/internal/filemode"
	"github.com/P4suta/goatest/internal/report"
)

func TestCheckpointStoreIsAtomicStrictAndIndependentOfCompletedReport(t *testing.T) {
	root := t.TempDir()
	digest := cacheTestDigest("a")
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

	if pending, err := store.PendingCheckpoint(); err != nil || pending {
		t.Fatalf("empty cache pending = (%t, %v)", pending, err)
	}
	digest := cacheTestDigest("c")
	if err := store.Put(digest, report.Report{Schema: report.SchemaV1, Verdict: report.VerdictAssured, Snapshot: digest}); err != nil {
		t.Fatal(err)
	}

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
	digest := cacheTestDigest("b")
	store := New(root)
	directory := filepath.Join(root, "v1", digest)
	if err := os.MkdirAll(directory, filemode.ReadableDirectory); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, ".checkpoint-cut.tmp"), []byte(`{"schema":`), filemode.PrivateFile); err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.GetCheckpoint(digest); err != nil || found {
		t.Fatalf("interrupted temporary = found %t err %v", found, err)
	}
	if err := os.WriteFile(filepath.Join(directory, CheckpointFileName), []byte(`{"schema":`), filemode.PrivateFile); err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.GetCheckpoint(digest); err == nil || found {
		t.Fatalf("corrupt final = found %t err %v", found, err)
	}
}

func TestCheckpointSharesCacheRetentionEntry(t *testing.T) {
	root := t.TempDir()
	digest := cacheTestDigest("c")
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
	digest := cacheTestDigest("d")
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
	digest := cacheTestDigest("e")
	store := New(root)
	state := checkpoint.State{
		Schema: checkpoint.SchemaV1, InputDigest: digest, Attempts: 1,
		Baseline: checkpoint.Baseline{BuildVetComplete: true},
		Mutation: &checkpoint.Mutation{CatalogFingerprint: cacheTestDigest("f")},
	}
	if err := store.PutCheckpoint(digest, state); err != nil {
		t.Fatal(err)
	}
	target := checkpoint.BaselineTarget{
		ID: "target-a", Executed: true,
		Inventory: report.TargetDisposition{ID: "target-a", Name: "TestA", Status: "passed"},
	}
	suite := checkpoint.BaselineSuite{Package: "example.test/project", Measured: false}
	mutantZ := checkpoint.MutationResult{
		ID: "mutant-z", Findings: []report.Finding{{ID: "finding-z", Kind: "surviving-mutant", MutantID: "mutant-z", Summary: "survived"}},
	}
	mutantA := checkpoint.MutationResult{
		ID: "mutant-a", Evidence: []report.Evidence{{Kind: "mutation", ID: "mutant-a", Status: "killed"}},
	}
	if err := store.AppendBaselineCheckpoint(digest, target); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendBaselineSuiteCheckpoint(digest, suite); err != nil {
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
		!slices.EqualFunc(loaded.Baseline.Suites, []checkpoint.BaselineSuite{suite}, func(left, right checkpoint.BaselineSuite) bool { return left.Package == right.Package }) ||
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
	digest := cacheTestDigest("9")
	store := New(root)
	state := checkpoint.State{
		Schema: checkpoint.SchemaV1, InputDigest: digest, Attempts: 1,
		Mutation: &checkpoint.Mutation{CatalogFingerprint: cacheTestDigest("8")},
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
	journal, err := os.OpenFile(journalPath, os.O_APPEND|os.O_WRONLY, filemode.PrivateFile)
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
	if err := os.WriteFile(journalPath, append([]byte(`{}`), '\n'), filemode.PrivateFile); err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.GetCheckpoint(digest); err == nil || found {
		t.Fatalf("complete corrupt journal = found %t, error %v", found, err)
	}
}

func TestCheckpointJournalReportsAReadErrorEvenWhenNoDataWasRead(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	digest := cacheTestDigest("7")
	store := New(root)
	state := checkpoint.State{Schema: checkpoint.SchemaV1, InputDigest: digest, Attempts: 1}
	if err := store.PutCheckpoint(digest, state); err != nil {
		t.Fatal(err)
	}
	journalPath := filepath.Join(root, "v1", digest, CheckpointJournalFileName)
	if err := os.Mkdir(journalPath, filemode.PrivateDirectory); err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.GetCheckpoint(digest); err == nil || found ||
		!strings.Contains(err.Error(), "read checkpoint journal") {
		t.Fatalf("unreadable journal = found %t, error %v", found, err)
	}
}

func TestCheckpointJournalRejectsDuplicateCurrentBaseUnits(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		append func(*Store, string) error
	}{
		{name: "baseline target", append: func(store *Store, digest string) error {
			return store.AppendBaselineCheckpoint(digest, checkpoint.BaselineTarget{
				ID: "target-a", Executed: true,
				Inventory: report.TargetDisposition{ID: "target-a", Name: "TestA", Status: "passed"},
			})
		}},
		{name: "baseline suite", append: func(store *Store, digest string) error {
			return store.AppendBaselineSuiteCheckpoint(digest, checkpoint.BaselineSuite{Package: "example.test/project"})
		}},
		{name: "mutant", append: func(store *Store, digest string) error {
			return store.AppendMutationCheckpoint(digest, checkpoint.MutationResult{
				ID: "mutant-a", Evidence: []report.Evidence{{Kind: "mutation", ID: "mutant-a", Status: "killed"}},
			})
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			digest := cacheTestDigest("4")
			store := New(root)
			state := checkpoint.State{
				Schema: checkpoint.SchemaV1, InputDigest: digest, Attempts: 1,
				Baseline: checkpoint.Baseline{BuildVetComplete: true},
				Mutation: &checkpoint.Mutation{CatalogFingerprint: cacheTestDigest("3")},
			}
			if err := store.PutCheckpoint(digest, state); err != nil {
				t.Fatal(err)
			}
			if err := test.append(store, digest); err != nil {
				t.Fatal(err)
			}
			if err := test.append(store, digest); err != nil {
				t.Fatal(err)
			}
			if _, found, err := store.GetCheckpoint(digest); err == nil || found || !strings.Contains(err.Error(), "duplicates") {
				t.Fatalf("duplicate journal unit = found %t, error %v", found, err)
			}
		})
	}
}

func TestCheckpointJournalSkipsOnlyAStalePrefix(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	digest := cacheTestDigest("6")
	store := New(root)
	state := checkpoint.State{
		Schema: checkpoint.SchemaV1, InputDigest: digest, Attempts: 1,
		Mutation: &checkpoint.Mutation{CatalogFingerprint: cacheTestDigest("5")},
	}
	if err := store.PutCheckpoint(digest, state); err != nil {
		t.Fatal(err)
	}
	stale := checkpoint.MutationResult{
		ID: "mutant-stale", Evidence: []report.Evidence{{Kind: "mutation", ID: "mutant-stale", Status: "killed"}},
	}
	if err := store.AppendMutationCheckpoint(digest, stale); err != nil {
		t.Fatal(err)
	}
	journalPath := filepath.Join(root, "v1", digest, CheckpointJournalFileName)
	staleData, err := os.ReadFile(journalPath)
	if err != nil {
		t.Fatal(err)
	}

	state.Attempts = state.Attempts + 1
	if err := store.PutCheckpoint(digest, state); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(journalPath, staleData, filemode.PrivateFile); err != nil {
		t.Fatal(err)
	}
	current := checkpoint.MutationResult{
		ID: "mutant-current", Evidence: []report.Evidence{{Kind: "mutation", ID: "mutant-current", Status: "killed"}},
	}
	if err := store.AppendMutationCheckpoint(digest, current); err != nil {
		t.Fatal(err)
	}
	loaded, found, err := store.GetCheckpoint(digest)
	if err != nil || !found || loaded.Attempts != 2 || len(loaded.Mutation.Results) != 1 ||
		loaded.Mutation.Results[0].ID != current.ID {
		t.Fatalf("checkpoint after stale prefix = (%+v, %t, %v)", loaded, found, err)
	}

	journal, err := os.OpenFile(journalPath, os.O_APPEND|os.O_WRONLY, filemode.PrivateFile)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := journal.Write(staleData); err != nil {
		_ = journal.Close()
		t.Fatal(err)
	}
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.GetCheckpoint(digest); err == nil || found ||
		!strings.Contains(err.Error(), "changed base identity") {
		t.Fatalf("stale journal suffix = found %t, error %v", found, err)
	}
}
