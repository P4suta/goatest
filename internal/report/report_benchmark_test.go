// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package report

import (
	"fmt"
	"testing"
)

func BenchmarkReportGeneration(b *testing.B) {
	input := Report{Schema: SchemaV1, Verdict: VerdictInsufficient, Contract: "standard-v1", Snapshot: "benchmark"}
	for index := range 5_000 {
		id := fmt.Sprintf("mutant-%05d", index)
		input.Mutants = append(input.Mutants, MutantDisposition{ID: id, Status: MutantKilled, Path: "internal/value.go", Line: index + 1, Package: "example.test/project/internal", Rule: "comparison"})
		input.Evidence = append(input.Evidence, Evidence{Kind: "mutation", ID: id, Status: "killed"})
	}
	b.ResetTimer()
	for range b.N {
		_ = JSON(input)
		_ = HTML(input)
	}
}
