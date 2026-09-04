// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package assure

import (
	"fmt"
	"testing"

	gomutants "github.com/P4suta/go-mutants"
	"github.com/P4suta/goatest/internal/report"
)

func BenchmarkMutationAccounting(b *testing.B) {
	catalog := gomutants.Catalog{Mutants: make([]gomutants.Mutant, 10_000)}
	evaluation := MutationEvaluation{Evidence: make([]report.Evidence, 10_000)}
	for index := range catalog.Mutants {
		id := fmt.Sprintf("mutant-%05d", index)
		catalog.Mutants[index] = gomutants.Mutant{ID: id, Path: "internal/value.go", Package: "example.test/project/internal", Rule: "comparison", Line: index + 1, Accepted: true}
		evaluation.Evidence[index] = report.Evidence{Kind: "mutation", ID: id, Status: "killed", Detail: "TestValue"}
	}
	b.ResetTimer()
	for range b.N {
		_, _ = mutationAccounting(catalog, "", evaluation, nil, nil)
	}
}
