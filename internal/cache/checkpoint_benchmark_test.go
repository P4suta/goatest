// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package cache

import (
	"strings"
	"testing"

	"github.com/P4suta/goatest/internal/checkpoint"
)

func BenchmarkCheckpointIO(b *testing.B) {
	store := New(b.TempDir())
	digest := strings.Repeat("a", 64)
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
