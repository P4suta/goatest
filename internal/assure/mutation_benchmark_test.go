// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package assure

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"

	gomutants "github.com/P4suta/go-mutants"
	"github.com/P4suta/goatest/internal/evidence"
	goanalysis "github.com/P4suta/goatest/internal/golang"
	"github.com/P4suta/goatest/internal/report"
)

const (
	mutationAccountingBenchmarkSize = 10_000
	evidenceKeyBenchmarkFileCount   = 10_000
	evidenceKeyBenchmarkTargetCount = 400
)

var mutationEvidenceKeyBenchmarkResult string

func BenchmarkMutationAccounting(b *testing.B) {
	catalog := gomutants.Catalog{Mutants: make([]gomutants.Mutant, mutationAccountingBenchmarkSize)}
	evaluation := MutationEvaluation{Evidence: make([]report.Evidence, mutationAccountingBenchmarkSize)}
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

func BenchmarkMutationEvidenceKeyConstruction(b *testing.B) {
	base := targetKeyFixture()
	for index := range evidenceKeyBenchmarkFileCount {
		name := filepath.ToSlash(fmt.Sprintf("unrelated/package-%04d/input-%04d.txt", index, index))
		base.inputs.Files[name] = digestText(name)
	}
	sources := newTargetKeySources(base.inputs, base.model, base.contract, Options{}, map[string]bool{evidenceModule: true})
	targets := make([]TargetEvidence, 0, evidenceKeyBenchmarkTargetCount)
	inventory := make([]report.TargetDisposition, 0, evidenceKeyBenchmarkTargetCount)
	for index := range evidenceKeyBenchmarkTargetCount {
		target := evidenceTarget(fmt.Sprintf("TestKey%04d", index), goanalysis.KindTest, time.Millisecond)
		targets = append(targets, target)
		inventory = append(inventory, report.TargetDisposition{
			Name: target.Target.Name, Kind: string(target.Target.Kind), Package: target.Target.Package, Status: "passed",
		})
	}
	b.Run("narrow-startup", func(b *testing.B) {
		for range b.N {
			index := newRunMutationEvidence(evidence.MutationStore{}, sources, targets, inventory, nil, digestText("snapshot"))
			mutationEvidenceKeyBenchmarkResult = index.keys[identify(targets[0].Target)]
		}
	})
	b.Run("first-whole-target", func(b *testing.B) {
		whole := targets[0]
		whole.WholeTree = true
		for range b.N {
			index := newRunMutationEvidence(evidence.MutationStore{}, sources, targets, inventory, nil, digestText("snapshot"))
			mutationEvidenceKeyBenchmarkResult, _ = index.targetKey(whole)
		}
	})
}
