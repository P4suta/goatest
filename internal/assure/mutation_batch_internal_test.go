// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package assure

import (
	"fmt"
	"strings"
	"testing"
	"time"

	gomutants "github.com/P4suta/go-mutants"
	goanalysis "github.com/P4suta/goatest/internal/golang"
)

func TestMutationSeedBatchesRemainingRelevantTargetsByPackageAndEnvironment(t *testing.T) {
	const individualLimit = 8
	mutant := internalMutation("mutant-a")
	targets := make([]TargetEvidence, 0, individualLimit+4)
	for index := range individualLimit + 4 {
		target := internalTarget(fmt.Sprintf("Test%02d", index), goanalysis.KindTest, time.Duration(index+1)*time.Millisecond)
		if index >= individualLimit+2 {
			target.Target.Package = "fixture.example/other"
			target.Environment = []string{"DB=other"}
		}
		targets = append(targets, target)
	}

	session := &mutationUnitSession{exec: func(request gomutants.ExecRequest) (gomutants.MutantResult, error) {
		outcome := gomutants.OutcomeSurvived
		if request.Package == "fixture.example/other" && strings.Contains(request.Args[0], "|") {
			outcome = gomutants.OutcomeKilled
		}
		return gomutants.MutantResult{ID: mutant.ID, Outcome: outcome}, nil
	}}
	seed := evaluateMutationSeed(t.Context(), session, mutant, targets, MutationOptions{Contract: "standard-v1"})

	if !seed.resolved || seed.err != nil || len(seed.evaluation.Evidence) != 1 {
		t.Fatalf("seed = %+v", seed)
	}
	if got, want := len(session.requests), individualLimit+2; got != want {
		t.Fatalf("request count = %d, want %d: %+v", got, want, session.requests)
	}
	for index := range individualLimit {
		want := fmt.Sprintf("-test.run=^Test%02d$", index)
		if got := session.requests[index].Args; len(got) != 1 || got[0] != want {
			t.Fatalf("individual request %d = %+v, want %q", index, got, want)
		}
	}
	firstBatch := session.requests[individualLimit]
	if firstBatch.Package != "fixture.example/module" || len(firstBatch.Args) != 1 || firstBatch.Args[0] != "-test.run=^(Test08|Test09)$" || len(firstBatch.Env) != 0 {
		t.Fatalf("first batch = %+v", firstBatch)
	}
	secondBatch := session.requests[individualLimit+1]
	if secondBatch.Package != "fixture.example/other" || len(secondBatch.Args) != 1 || secondBatch.Args[0] != "-test.run=^(Test10|Test11)$" || !strings.EqualFold(strings.Join(secondBatch.Env, "\x00"), "DB=other") {
		t.Fatalf("second batch = %+v", secondBatch)
	}
	if got := seed.evaluation.Evidence[0].Detail; got != "fixture.example/other (2 related targets)" {
		t.Fatalf("kill detail = %q", got)
	}
}

func TestMutationTargetBatchesBoundTheNumberOfNamesInOneCommand(t *testing.T) {
	const batchLimit = 64
	targets := make([]TargetEvidence, 0, batchLimit*2+2)
	for index := range batchLimit*2 + 2 {
		targets = append(targets, internalTarget(fmt.Sprintf("TestBatch%03d", index), goanalysis.KindTest, time.Millisecond))
	}

	batches := mutationTargetBatches(targets)
	if got, want := len(batches), 3; got != want {
		t.Fatalf("batch count = %d, want %d", got, want)
	}
	wantSizes := []int{batchLimit, batchLimit, 2}
	for index, batch := range batches {
		if got := len(batch); got != wantSizes[index] {
			t.Fatalf("batch %d size = %d, want %d", index, got, wantSizes[index])
		}
	}
}

func TestMutationTargetBatchesBoundTheRunArgumentBytes(t *testing.T) {
	targets := make([]TargetEvidence, 0, 3)
	for index := range 3 {
		name := "Test" + strings.Repeat(string(rune('A'+index)), 3_000)
		targets = append(targets, internalTarget(name, goanalysis.KindTest, time.Millisecond))
	}

	batches := mutationTargetBatches(targets)
	if got, want := len(batches), 2; got != want {
		t.Fatalf("batch count = %d, want %d", got, want)
	}
	if len(batches[0]) != 2 || len(batches[1]) != 1 {
		t.Fatalf("batch sizes = %d, %d, want 2, 1", len(batches[0]), len(batches[1]))
	}
}

func TestMutationTargetBatchesKeepAnArgumentExactlyAtTheByteLimit(t *testing.T) {
	const fixedBytes = len("-test.run=^(") + len("|") + len(")$")
	firstNameBytes := (maximumMutationRunArgumentBytes - fixedBytes) / 2
	secondNameBytes := maximumMutationRunArgumentBytes - fixedBytes - firstNameBytes
	targets := []TargetEvidence{
		internalTarget(strings.Repeat("A", firstNameBytes), goanalysis.KindTest, time.Millisecond),
		internalTarget(strings.Repeat("B", secondNameBytes), goanalysis.KindTest, time.Millisecond),
	}
	if got := len(batchRunArgument(targets)); got != maximumMutationRunArgumentBytes {
		t.Fatalf("run argument bytes = %d, want %d", got, maximumMutationRunArgumentBytes)
	}
	if batches := mutationTargetBatches(targets); len(batches) != 1 || len(batches[0]) != 2 {
		t.Fatalf("exact-limit batches = %+v, want one two-target batch", batches)
	}
}

func TestMutationSeedExecutionsHandleARemainderLargerThanTheIndividualPrefix(t *testing.T) {
	mutant := internalMutation("mutant-a")
	targets := make([]TargetEvidence, 0, individualMutationTargetLimit*2+1)
	for index := range individualMutationTargetLimit*2 + 1 {
		targets = append(targets, internalTarget(fmt.Sprintf("Test%02d", index), goanalysis.KindTest, time.Millisecond))
	}

	executions := mutationSeedExecutions(mutant, targets, MutationOptions{Contract: "standard-v1"})
	if got, want := len(executions), individualMutationTargetLimit+1; got != want {
		t.Fatalf("execution count = %d, want %d", got, want)
	}
	if got := executions[len(executions)-1].detail; got != "fixture.example/module (9 related targets)" {
		t.Fatalf("batch detail = %q", got)
	}
}

func TestBatchMutationDurationSumsPositiveValuesIgnoresNegativeAndSaturates(t *testing.T) {
	targets := []TargetEvidence{
		internalTarget("TestFirst", goanalysis.KindTest, time.Second),
		internalTarget("TestUnknown", goanalysis.KindTest, -time.Hour),
		internalTarget("TestSecond", goanalysis.KindTest, 2*time.Second),
	}
	if got, want := batchMutationDuration(targets), 3*time.Second; got != want {
		t.Fatalf("batch duration = %s, want %s", got, want)
	}
	overflowing := []TargetEvidence{
		internalTarget("TestLong", goanalysis.KindTest, deepMutationTimeoutLimit-1),
		internalTarget("TestOverflow", goanalysis.KindTest, time.Duration(1<<63-1)),
	}
	if got := batchMutationDuration(overflowing); got != deepMutationTimeoutLimit {
		t.Fatalf("saturated duration = %s, want %s", got, deepMutationTimeoutLimit)
	}
}

func TestBatchMutationDetailKeepsASingleTargetName(t *testing.T) {
	target := internalTarget("TestOnly", goanalysis.KindTest, time.Second)
	if got := batchMutationDetail([]TargetEvidence{target}); got != target.Target.Name {
		t.Fatalf("single-target detail = %q, want %q", got, target.Target.Name)
	}
}
