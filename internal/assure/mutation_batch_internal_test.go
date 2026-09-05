// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package assure

import (
	"context"
	"fmt"
	"slices"
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
	if got, want := len(session.requests), individualLimit+4; got != want {
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
	for index, name := range []string{"Test10", "Test11"} {
		request := session.requests[individualLimit+2+index]
		if request.Package != "fixture.example/other" || !slices.Equal(request.Args, []string{"-test.run=^" + name + "$"}) {
			t.Fatalf("kill refinement %d = %+v", index, request)
		}
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

func TestMutationTargetBatchesKeepSlowTargetsOutOfFastBatches(t *testing.T) {
	targets := []TargetEvidence{
		internalTarget("TestFastA", goanalysis.KindTest, 500*time.Millisecond),
		internalTarget("TestFastB", goanalysis.KindTest, 500*time.Millisecond),
		internalTarget("TestOverBoundary", goanalysis.KindTest, 500*time.Millisecond),
		internalTarget("TestSlowE2E", goanalysis.KindTest, 2*time.Second),
	}

	batches := mutationTargetBatches(targets)
	wantSizes := []int{2, 1, 1}
	if len(batches) != len(wantSizes) {
		t.Fatalf("batch count = %d, want %d", len(batches), len(wantSizes))
	}
	for index, want := range wantSizes {
		if got := len(batches[index]); got != want {
			t.Fatalf("batch %d size = %d, want %d", index, got, want)
		}
	}
}

func TestMutationTargetBatchesPreferPreparedProbeCostToCoverageCost(t *testing.T) {
	cheapAfterPreparation := []TargetEvidence{
		internalTarget("TestA", goanalysis.KindTest, 10*time.Second),
		internalTarget("TestB", goanalysis.KindTest, 10*time.Second),
	}
	for index := range cheapAfterPreparation {
		cheapAfterPreparation[index].ProbeDuration = 400 * time.Millisecond
	}
	if batches := mutationTargetBatches(cheapAfterPreparation); len(batches) != 1 || len(batches[0]) != 2 {
		t.Fatalf("prepared-cheap batches = %+v, want one two-target batch", batches)
	}

	expensiveAfterPreparation := []TargetEvidence{
		internalTarget("TestA", goanalysis.KindTest, time.Millisecond),
		internalTarget("TestB", goanalysis.KindTest, time.Millisecond),
	}
	for index := range expensiveAfterPreparation {
		expensiveAfterPreparation[index].ProbeDuration = 2 * time.Second
	}
	if batches := mutationTargetBatches(expensiveAfterPreparation); len(batches) != 2 || len(batches[0]) != 1 || len(batches[1]) != 1 {
		t.Fatalf("prepared-expensive batches = %+v, want two singleton batches", batches)
	}
}

func TestAggregateSlowMutationBatchesCombinesOnlyCompatibleSingletonTail(t *testing.T) {
	targets := []TargetEvidence{
		internalTarget("TestFastA", goanalysis.KindTest, 500*time.Millisecond),
		internalTarget("TestFastB", goanalysis.KindTest, 500*time.Millisecond),
	}
	for index := range 6 {
		duration := 2 * time.Second
		if index == 0 {
			duration = 500 * time.Millisecond
		}
		targets = append(targets, internalTarget(fmt.Sprintf("TestTail%d", index), goanalysis.KindTest, duration))
	}
	batches := aggregateSlowMutationBatches(mutationTargetBatches(targets))
	if len(batches) != 2 || len(batches[0]) != 2 || len(batches[1]) != 6 {
		t.Fatalf("aggregate batches = %+v, want the cheap pair and one exact tail", batches)
	}

	other := internalTarget("TestOther", goanalysis.KindTest, 2*time.Second)
	other.Environment = []string{"DB=other"}
	batches = aggregateSlowMutationBatches(mutationTargetBatches(append(targets, other)))
	if len(batches) != 3 || len(batches[1]) != 6 || len(batches[2]) != 1 {
		t.Fatalf("cross-environment aggregate batches = %+v", batches)
	}
}

func TestIndividualMutationPrefixIsBoundedByMeasuredExecutionCost(t *testing.T) {
	targets := make([]TargetEvidence, 0, individualMutationTargetLimit+1)
	for index := range individualMutationTargetLimit + 1 {
		target := internalTarget(fmt.Sprintf("Test%02d", index), goanalysis.KindTest, 10*time.Second)
		target.ProbeDuration = 500 * time.Millisecond
		targets = append(targets, target)
	}
	if got, want := individualMutationTargetCount(targets), 4; got != want {
		t.Fatalf("individual prefix = %d, want %d within the two-second probe budget", got, want)
	}

	for index := range individualMutationTargetLimit {
		targets[index].ProbeDuration = time.Millisecond
	}
	if got, want := individualMutationTargetCount(targets), individualMutationTargetLimit; got != want {
		t.Fatalf("cheap individual prefix = %d, want the count cap %d", got, want)
	}

	unknown := internalTarget("TestUnknown", goanalysis.KindTest, 0)
	if got := individualMutationTargetCount([]TargetEvidence{unknown, targets[0]}); got != 1 {
		t.Fatalf("unmeasured individual prefix = %d, want one attributable witness", got)
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

func TestAggregateMutationTimeoutUsesAWholeSuiteOnlyAsAShorterProofAttempt(t *testing.T) {
	targets := []TargetEvidence{
		internalTarget("TestSlowA", goanalysis.KindTest, 20*time.Second),
		internalTarget("TestSlowB", goanalysis.KindTest, 20*time.Second),
	}
	targets[0].ProbeDuration = 21 * time.Second
	targets[1].ProbeDuration = 21 * time.Second
	pkg := targets[0].Target.Package

	withoutSuite := aggregateMutationTimeout(targets, MutationOptions{Contract: "standard-v1"})
	withSuite := aggregateMutationTimeout(targets, MutationOptions{
		Contract:      "standard-v1",
		SuiteCoverage: map[string]PackageSuiteCoverage{pkg: {Duration: 10 * time.Second}},
		SuiteProbes:   map[string]PackageProbeEvidence{pkg: {Measured: true, Duration: 12 * time.Second}},
	})
	if withoutSuite != 84*time.Second || withSuite != 28*time.Second {
		t.Fatalf("aggregate deadlines = (%s, %s), want (1m24s, 28s)", withoutSuite, withSuite)
	}

	single := aggregateMutationTimeout(targets[:1], MutationOptions{
		Contract:      "standard-v1",
		SuiteCoverage: map[string]PackageSuiteCoverage{pkg: {Duration: time.Millisecond}},
	})
	if single != 42*time.Second {
		t.Fatalf("single-target deadline = %s, want its exact controls' 42s", single)
	}
}

func TestAggregateTimeoutRefinesUntilOneTargetOwnsTheTimeout(t *testing.T) {
	mutant := internalMutation("mutant-a")
	targets := make([]TargetEvidence, 0, individualMutationTargetLimit+2)
	for index := range individualMutationTargetLimit + 2 {
		targets = append(targets, internalTarget(fmt.Sprintf("Test%02d", index), goanalysis.KindTest, time.Millisecond))
	}
	session := &mutationUnitSession{exec: func(request gomutants.ExecRequest) (gomutants.MutantResult, error) {
		outcome := gomutants.OutcomeSurvived
		if strings.Contains(request.Args[0], "|") || request.Args[0] == "-test.run=^Test09$" {
			outcome = gomutants.OutcomeTimedOut
		}
		return gomutants.MutantResult{ID: mutant.ID, Outcome: outcome}, nil
	}}

	seed := evaluateMutationSeed(t.Context(), session, mutant, targets, MutationOptions{Contract: "standard-v1"})
	if seed.err != nil || !seed.resolved || len(seed.evaluation.Findings) != 1 ||
		seed.evaluation.Findings[0].Kind != "mutation-timeout" {
		t.Fatalf("seed = %+v, want the attributable Test09 timeout", seed)
	}
	if got, want := len(session.requests), individualMutationTargetLimit+3; got != want {
		t.Fatalf("requests = %d, want %d including the aggregate and both refinements", got, want)
	}
	last := session.requests[len(session.requests)-1]
	if !slices.Equal(last.Args, []string{"-test.run=^Test09$"}) {
		t.Fatalf("last request = %+v, want the one target that timed out", last)
	}
}

func TestAggregateNonDecisiveOutcomesRefineWithoutManufacturingAFinding(t *testing.T) {
	t.Parallel()
	for _, outcome := range []gomutants.Outcome{
		gomutants.OutcomeInconclusive,
		gomutants.OutcomeErrored,
		gomutants.OutcomeNotRun,
	} {
		outcome := outcome
		t.Run(string(outcome), func(t *testing.T) {
			t.Parallel()
			mutant := internalMutation("mutant-a")
			targets := make([]TargetEvidence, 0, individualMutationTargetLimit+2)
			for index := range cap(targets) {
				targets = append(targets, internalTarget(fmt.Sprintf("Test%02d", index), goanalysis.KindTest, time.Millisecond))
			}
			session := &mutationUnitSession{exec: func(request gomutants.ExecRequest) (gomutants.MutantResult, error) {
				result := gomutants.OutcomeSurvived
				if strings.Contains(request.Args[0], "|") {
					result = outcome
				}
				return gomutants.MutantResult{ID: mutant.ID, Outcome: result}, nil
			}}

			seed := evaluateMutationSeed(t.Context(), session, mutant, targets, MutationOptions{Contract: "standard-v1"})
			if seed.err != nil || !seed.resolved || len(seed.evaluation.Findings) != 1 ||
				seed.evaluation.Findings[0].Kind != "surviving-mutant" {
				t.Fatalf("seed = %+v, want exact refined survival", seed)
			}
			if got, want := len(session.requests), individualMutationTargetLimit+3; got != want {
				t.Fatalf("requests = %d, want prefix, ambiguous aggregate, and two exact refinements (%d)", got, want)
			}
		})
	}
}

func TestLargeAggregateTimeoutBisectsOnlyTheAmbiguousHalf(t *testing.T) {
	mutant := internalMutation("mutant-a")
	targets := make([]TargetEvidence, 0, individualMutationTargetLimit+8)
	for index := range cap(targets) {
		duration := time.Millisecond
		if index >= individualMutationTargetLimit {
			duration = 2 * time.Second
		}
		targets = append(targets, internalTarget(fmt.Sprintf("Test%02d", index), goanalysis.KindTest, duration))
	}
	session := &mutationUnitSession{exec: func(request gomutants.ExecRequest) (gomutants.MutantResult, error) {
		outcome := gomutants.OutcomeSurvived
		if strings.Contains(request.Args[0], "Test15") {
			outcome = gomutants.OutcomeTimedOut
		}
		return gomutants.MutantResult{ID: mutant.ID, Outcome: outcome}, nil
	}}

	seed := evaluateMutationSeed(t.Context(), session, mutant, targets, MutationOptions{Contract: "standard-v1"})
	if seed.err != nil || !seed.resolved || len(seed.evaluation.Findings) != 1 ||
		seed.evaluation.Findings[0].Kind != "mutation-timeout" {
		t.Fatalf("seed = %+v, want the attributable Test15 timeout", seed)
	}
	wantTail := []string{
		"-test.run=^(Test08|Test09|Test10|Test11|Test12|Test13|Test14|Test15)$",
		"-test.run=^(Test08|Test09|Test10|Test11)$",
		"-test.run=^(Test12|Test13|Test14|Test15)$",
		"-test.run=^(Test12|Test13)$",
		"-test.run=^(Test14|Test15)$",
		"-test.run=^Test14$",
		"-test.run=^Test15$",
	}
	gotTail := make([]string, 0, len(session.requests)-individualMutationTargetLimit)
	for _, request := range session.requests[individualMutationTargetLimit:] {
		gotTail = append(gotTail, request.Args[0])
	}
	if !slices.Equal(gotTail, wantTail) {
		t.Fatalf("timeout refinement = %+v, want logarithmic proof path %+v", gotTail, wantTail)
	}
}

func TestSlowTailKillRefinesToACheapNamedKillerBeforeConfirmation(t *testing.T) {
	mutant := internalMutation("mutant-a")
	targets := make([]TargetEvidence, 0, individualMutationTargetLimit+6)
	for index := range cap(targets) {
		duration := time.Millisecond
		if index >= individualMutationTargetLimit {
			duration = 2 * time.Second
		}
		targets = append(targets, internalTarget(fmt.Sprintf("Test%02d", index), goanalysis.KindTest, duration))
	}
	controls := 0
	session := &mutationUnitSession{exec: func(request gomutants.ExecRequest) (gomutants.MutantResult, error) {
		outcome := gomutants.OutcomeSurvived
		if strings.Contains(request.Args[0], "Test13") {
			outcome = gomutants.OutcomeKilled
		}
		return gomutants.MutantResult{ID: mutant.ID, Outcome: outcome}, nil
	}}
	seed := evaluateMutationSeed(t.Context(), session, mutant, targets, MutationOptions{
		Contract: "standard-v1",
		OriginalControl: func(_ context.Context, request gomutants.ExecRequest) (gomutants.CommandResult, error) {
			controls++
			if !slices.Equal(request.Args, []string{"-test.run=^Test13$"}) {
				t.Fatalf("control request = %+v, want only the refined killer", request)
			}
			return gomutants.CommandResult{Duration: time.Millisecond}, nil
		},
	})
	if seed.err != nil || !seed.resolved || len(seed.evaluation.Evidence) != 1 ||
		seed.evaluation.Evidence[0].Detail != "Test13 (paired confirmation)" || controls != 1 {
		t.Fatalf("seed = %+v, controls=%d", seed, controls)
	}
	if got, want := len(session.requests), cap(targets)+2; got != want {
		// Every planned witness, one aggregate attempt, two refinements, and
		// the repeated Test13 confirmation are cap(targets)+2 executions.
		t.Fatalf("requests = %d, want %d: %+v", got, want, session.requests)
	}
}

func TestSlowTailInteractionKillConfirmsTheExactAggregate(t *testing.T) {
	mutant := internalMutation("mutant-a")
	targets := make([]TargetEvidence, 0, individualMutationTargetLimit+6)
	for index := range cap(targets) {
		duration := time.Millisecond
		if index >= individualMutationTargetLimit {
			duration = 2 * time.Second
		}
		targets = append(targets, internalTarget(fmt.Sprintf("Test%02d", index), goanalysis.KindTest, duration))
	}
	aggregateArgument := "-test.run=^(Test08|Test09|Test10|Test11|Test12|Test13)$"
	controls := 0
	session := &mutationUnitSession{exec: func(request gomutants.ExecRequest) (gomutants.MutantResult, error) {
		outcome := gomutants.OutcomeSurvived
		if slices.Contains(request.Args, aggregateArgument) {
			outcome = gomutants.OutcomeKilled
		}
		return gomutants.MutantResult{ID: mutant.ID, Outcome: outcome}, nil
	}}
	seed := evaluateMutationSeed(t.Context(), session, mutant, targets, MutationOptions{
		Contract: "standard-v1",
		OriginalControl: func(_ context.Context, request gomutants.ExecRequest) (gomutants.CommandResult, error) {
			controls++
			if !slices.Equal(request.Args, []string{aggregateArgument}) {
				t.Fatalf("control request = %+v, want the exact aggregate", request)
			}
			return gomutants.CommandResult{Duration: time.Millisecond}, nil
		},
	})
	wantDetail := "fixture.example/module (6 related targets) (paired confirmation)"
	if seed.err != nil || !seed.resolved || len(seed.evaluation.Evidence) != 1 ||
		seed.evaluation.Evidence[0].Detail != wantDetail || controls != 1 {
		t.Fatalf("seed = %+v, controls=%d", seed, controls)
	}
	if got, want := len(session.requests), individualMutationTargetLimit+1+2+1; got != want {
		// The cheap prefix, the first aggregate, its two passing halves, and
		// the exact aggregate confirmation are the complete proof.
		t.Fatalf("requests = %d, want %d: %+v", got, want, session.requests)
	}
	if got := session.requests[len(session.requests)-1].Args; !slices.Equal(got, []string{aggregateArgument}) {
		t.Fatalf("last request = %+v, want exact aggregate confirmation", got)
	}
}

func TestSlowTailAttributionTimeoutFallsBackToTheExactAggregateKill(t *testing.T) {
	mutant := internalMutation("mutant-a")
	targets := make([]TargetEvidence, 0, individualMutationTargetLimit+6)
	for index := range cap(targets) {
		duration := time.Millisecond
		if index >= individualMutationTargetLimit {
			duration = 2 * time.Second
		}
		targets = append(targets, internalTarget(fmt.Sprintf("Test%02d", index), goanalysis.KindTest, duration))
	}
	aggregateArgument := "-test.run=^(Test08|Test09|Test10|Test11|Test12|Test13)$"
	leftArgument := "-test.run=^(Test08|Test09|Test10)$"
	session := &mutationUnitSession{exec: func(request gomutants.ExecRequest) (gomutants.MutantResult, error) {
		outcome := gomutants.OutcomeSurvived
		switch {
		case slices.Contains(request.Args, aggregateArgument):
			outcome = gomutants.OutcomeKilled
		case slices.Contains(request.Args, leftArgument):
			outcome = gomutants.OutcomeTimedOut
		}
		return gomutants.MutantResult{ID: mutant.ID, Outcome: outcome}, nil
	}}
	controls := 0
	seed := evaluateMutationSeed(t.Context(), session, mutant, targets, MutationOptions{
		Contract: "standard-v1",
		OriginalControl: func(_ context.Context, request gomutants.ExecRequest) (gomutants.CommandResult, error) {
			controls++
			if !slices.Equal(request.Args, []string{aggregateArgument}) {
				t.Fatalf("control request = %+v, want the exact aggregate", request)
			}
			return gomutants.CommandResult{Duration: time.Millisecond}, nil
		},
	})
	wantDetail := "fixture.example/module (6 related targets) (paired confirmation)"
	if seed.err != nil || !seed.resolved || len(seed.evaluation.Evidence) != 1 ||
		seed.evaluation.Evidence[0].Detail != wantDetail || len(seed.evaluation.Findings) != 0 || controls != 1 {
		t.Fatalf("seed = %+v, controls=%d", seed, controls)
	}
	if got, want := len(session.requests), individualMutationTargetLimit+3; got != want {
		// The first attribution timeout goes directly to aggregate
		// confirmation; the rest of the tail never needs to run alone.
		t.Fatalf("requests = %d, want %d: %+v", got, want, session.requests)
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
