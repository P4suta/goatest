// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package cache

import (
	"strconv"
	"testing"

	"github.com/P4suta/goatest/internal/checkpoint"
	"github.com/P4suta/goatest/internal/report"
)

func BenchmarkCheckpointIO(b *testing.B) {
	store := New(b.TempDir())
	digest := cacheTestDigest("a")
	state := checkpoint.State{Schema: checkpoint.SchemaV1, InputDigest: digest, Attempts: 1}
	b.ResetTimer()
	for range b.N {
		if err := store.PutCheckpoint(digest, state); err != nil {
			b.Fatal(err)
		}
		if _, found, err := store.GetCheckpoint(digest); err != nil || !found {
			b.Fatalf("checkpoint read = found %t, error %v", found, err)
		}
	}
}

func BenchmarkCheckpointJournalAppend(b *testing.B) {
	store := New(b.TempDir())
	digest := cacheTestDigest("b")
	state := checkpoint.State{
		Schema: checkpoint.SchemaV1, InputDigest: digest, Attempts: 1,
		Mutation: &checkpoint.Mutation{CatalogFingerprint: cacheTestDigest("c")},
	}
	if err := store.PutCheckpoint(digest, state); err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for index := range b.N {
		id := "mutant-" + strconv.Itoa(index)
		if err := store.AppendMutationCheckpoint(digest, checkpoint.MutationResult{
			ID: id, Evidence: []report.Evidence{{Kind: "mutation", ID: id, Status: "killed"}},
		}); err != nil {
			b.Fatal(err)
		}
	}
}
