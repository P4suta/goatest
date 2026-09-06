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

const (
	aggregatePrimaryTargets   = 3
	aggregateSecondaryTargets = 2
	expectedAggregateGroups   = 2

	aggregateRequestsWithRemeasuredRetry = expectedAggregateGroups + 1
	aggregateUnboundedTargetCount        = 65
	aggregateLongTargetNameBytes         = 9_000
	aggregateFastDuration                = 10 * time.Second
	aggregateSlowDuration                = 30 * time.Second
	aggregateSlowProbeDuration           = 41 * time.Second
	aggregateSuiteSampleDuration         = 11 * time.Second
	aggregateSingleSuiteDuration         = 20 * time.Second
	aggregateControlBaselineA            = 2 * time.Millisecond
	aggregateControlBaselineB            = 3 * time.Millisecond
	aggregateControlProbeA               = 5 * time.Millisecond
	aggregateControlProbeB               = 7 * time.Millisecond
	aggregateExactControlDuration        = 11 * time.Millisecond
	aggregatePriorDeadline               = aggregateControlBaselineA + aggregateControlBaselineB + aggregateControlProbeA + aggregateControlProbeB
	aggregateMutantDeadline              = aggregatePriorDeadline + aggregateExactControlDuration
	expectedTargetTimeout                = 82 * time.Second
	expectedCombinedTimeout              = 104 * time.Second
	expectedSingleCombinedTimeout        = 41 * time.Second
)

func aggregateTargets() []TargetEvidence {
	targets := make([]TargetEvidence, 0, aggregatePrimaryTargets+aggregateSecondaryTargets)
	for index := range cap(targets) {
		target := internalTarget(fmt.Sprintf("Test%02d", index), goanalysis.KindTest, time.Duration(index+1)*time.Millisecond)
		if index >= aggregatePrimaryTargets {
			target.Target.Package = "fixture.example/other"
			target.Environment = []string{"DB=other"}
		}
		targets = append(targets, target)
	}
	return targets
}

func TestMutationSeedExecutesEachExactEnvironmentGroupOnce(t *testing.T) {
	mutant := internalMutation("mutant-a")
	targets := aggregateTargets()
	session := &mutationUnitSession{exec: func(request gomutants.ExecRequest) (gomutants.MutantResult, error) {
		outcome := gomutants.OutcomeSurvived
		if request.Package == "fixture.example/other" {
			outcome = gomutants.OutcomeKilled
		}
		return gomutants.MutantResult{ID: mutant.ID, Outcome: outcome}, nil
	}}

	seed := evaluateMutationSeed(t.Context(), session, mutant, targets, mutationOptionsForTest(MutationOptions{}))
	if seed.err != nil || !seed.resolved || len(session.requests) != expectedAggregateGroups {
		t.Fatalf("seed = %+v, requests = %+v", seed, session.requests)
	}
	if got := session.requests[0]; got.Package != "fixture.example/module" || !slices.Equal(got.Args, []string{"-test.run=^(Test00|Test01|Test02)$"}) || len(got.Env) != 0 {
		t.Fatalf("primary group = %+v", got)
	}
	if got := session.requests[1]; got.Package != "fixture.example/other" || !slices.Equal(got.Args, []string{"-test.run=^(Test03|Test04)$"}) || !slices.Equal(got.Env, []string{"DB=other"}) {
		t.Fatalf("secondary group = %+v", got)
	}
	if got := seed.evaluation.Evidence[0].Detail; got != "fixture.example/other (2 related targets)" {
		t.Fatalf("kill detail = %q", got)
	}
}

func TestLaterGroupKillDominatesEarlierUnknown(t *testing.T) {
	mutant := internalMutation("late-kill")
	targets := aggregateTargets()
	session := &mutationUnitSession{exec: func(request gomutants.ExecRequest) (gomutants.MutantResult, error) {
		outcome := gomutants.OutcomeTimedOut
		if request.Package == "fixture.example/other" {
			outcome = gomutants.OutcomeKilled
		}
		return gomutants.MutantResult{ID: mutant.ID, Outcome: outcome}, nil
	}}
	seed := evaluateMutationSeed(t.Context(), session, mutant, targets, mutationOptionsForTest(MutationOptions{}))
	if seed.err != nil || !seed.resolved || len(session.requests) != aggregateRequestsWithRemeasuredRetry || len(seed.evaluation.Findings) != 0 || seed.evaluation.Evidence[0].Status != "killed" {
		t.Fatalf("seed = %+v, requests = %+v", seed, session.requests)
	}
}

func TestUnknownGroupsAreAggregatedAfterEveryGroupRuns(t *testing.T) {
	mutant := internalMutation("unknown-groups")
	targets := aggregateTargets()
	session := &mutationUnitSession{exec: func(request gomutants.ExecRequest) (gomutants.MutantResult, error) {
		outcome := gomutants.OutcomeTimedOut
		if request.Package == "fixture.example/other" {
			outcome = gomutants.OutcomeInconclusive
		}
		return gomutants.MutantResult{ID: mutant.ID, Outcome: outcome}, nil
	}}
	seed := evaluateMutationSeed(t.Context(), session, mutant, targets, mutationOptionsForTest(MutationOptions{}))
	if seed.err != nil || !seed.resolved || len(session.requests) != aggregateRequestsWithRemeasuredRetry || len(seed.evaluation.Findings) != 1 {
		t.Fatalf("seed = %+v, requests = %+v", seed, session.requests)
	}
	finding := seed.evaluation.Findings[0]
	if finding.Kind != "mutation-inconclusive" || !strings.Contains(finding.Summary, "2 of 2 compatible execution groups") || !strings.Contains(finding.Summary, "fixture.example/other") {
		t.Fatalf("finding = %+v", finding)
	}
}

func TestAggregateKillAndInconclusiveAreTerminal(t *testing.T) {
	mutant := internalMutation("terminal")
	targets := aggregateTargets()[:aggregatePrimaryTargets]
	for _, outcome := range []gomutants.Outcome{gomutants.OutcomeKilled, gomutants.OutcomeInconclusive} {
		t.Run(string(outcome), func(t *testing.T) {
			session := &mutationUnitSession{exec: func(gomutants.ExecRequest) (gomutants.MutantResult, error) {
				return gomutants.MutantResult{ID: mutant.ID, Outcome: outcome}, nil
			}}
			seed := evaluateMutationSeed(t.Context(), session, mutant, targets, mutationOptionsForTest(MutationOptions{}))
			if seed.err != nil || !seed.resolved || len(session.requests) != 1 {
				t.Fatalf("seed = %+v, requests = %+v", seed, session.requests)
			}
			if outcome == gomutants.OutcomeInconclusive && seed.evaluation.Findings[0].Kind != "mutation-inconclusive" {
				t.Fatalf("findings = %+v", seed.evaluation.Findings)
			}
		})
	}
}

func TestMutationTargetGroupsHaveNoHeuristicSizeOrArgumentBoundary(t *testing.T) {
	targets := make([]TargetEvidence, 0, aggregateUnboundedTargetCount)
	for index := range cap(targets) {
		name := fmt.Sprintf("Test%03d%s", index, strings.Repeat("X", aggregateLongTargetNameBytes))
		targets = append(targets, internalTarget(name, goanalysis.KindTest, time.Hour))
	}
	groups := mutationTargetGroups(targets)
	if len(groups) != 1 || len(groups[0]) != len(targets) {
		t.Fatalf("group sizes = %d/%d", len(groups), len(groups[0]))
	}
	if argument := batchRunArgument(groups[0]); !strings.Contains(argument, targets[0].Target.Name) || !strings.Contains(argument, targets[len(targets)-1].Target.Name) {
		t.Fatal("aggregate run argument omitted a boundary target")
	}
}

func TestMutationTargetGroupsCanonicalizeSelectorsAndScheduleShortestGroupFirst(t *testing.T) {
	first := internalTarget("TestZulu", goanalysis.KindTest, time.Hour)
	second := internalTarget("TestAlpha", goanalysis.KindTest, time.Hour)
	fast := internalTarget("TestBeta", goanalysis.KindTest, time.Millisecond)
	fast.Environment = []string{"RESOURCE=fast"}
	groups := mutationTargetGroups([]TargetEvidence{first, fast, second})
	if len(groups) != expectedAggregateGroups {
		t.Fatalf("groups = %+v", groups)
	}
	if groups[0][0].Target.Name != fast.Target.Name {
		t.Fatalf("first group = %+v, want shortest", groups[0])
	}
	if got := batchRunArgument(groups[1]); got != "-test.run=^(TestAlpha|TestZulu)$" {
		t.Fatalf("canonical selector = %q", got)
	}
}

func TestMutationTargetGroupsUseEffectiveEnvironmentIdentity(t *testing.T) {
	first := internalTarget("TestAlpha", goanalysis.KindTest, time.Second)
	first.Environment = []string{"SECOND=value", "FIRST=value", "SECOND=final"}
	second := internalTarget("TestBeta", goanalysis.KindTest, time.Second)
	second.Environment = []string{"FIRST=value", "SECOND=final"}
	targets := []TargetEvidence{first, second}
	groups := mutationTargetGroups(targets)
	if len(groups) != 1 || len(groups[0]) != len(targets) {
		t.Fatalf("groups = %+v", groups)
	}
}

func TestAggregateMutationTimeoutUsesEveryMeasuredControl(t *testing.T) {
	targets := []TargetEvidence{
		internalTarget("TestFast", goanalysis.KindTest, aggregateFastDuration),
		internalTarget("TestSlow", goanalysis.KindTest, aggregateSlowDuration),
	}
	targets[0].ProbeDuration = time.Second
	targets[1].ProbeDuration = aggregateSlowProbeDuration
	withoutSuite := aggregateMutationTimeout(targets, MutationOptions{})
	withSuite := aggregateMutationTimeout(targets, MutationOptions{
		SuiteCoverage: map[string]PackageSuiteCoverage{"fixture.example/module": {Duration: aggregateSuiteSampleDuration}},
		SuiteProbes:   map[string]PackageProbeEvidence{"fixture.example/module": {Measured: true, Duration: aggregateSuiteSampleDuration}},
	})
	if withoutSuite != expectedTargetTimeout || withSuite != expectedCombinedTimeout {
		t.Fatalf("aggregate deadlines = (%s, %s)", withoutSuite, withSuite)
	}
	single := aggregateMutationTimeout(targets[:1], MutationOptions{
		SuiteCoverage: map[string]PackageSuiteCoverage{"fixture.example/module": {Duration: aggregateSingleSuiteDuration}},
		SuiteProbes:   map[string]PackageProbeEvidence{"fixture.example/module": {Measured: true, Duration: aggregateFastDuration}},
	})
	if single != expectedSingleCombinedTimeout {
		t.Fatalf("single deadline = %s", single)
	}
}

func TestExactOriginalRunsUnderTheContainmentCeiling(t *testing.T) {
	mutant := internalMutation("derived-control")
	targets := []TargetEvidence{
		internalTarget("TestAlpha", goanalysis.KindTest, aggregateControlBaselineA),
		internalTarget("TestBeta", goanalysis.KindTest, aggregateControlBaselineB),
	}
	targets[0].ProbeDuration = aggregateControlProbeA
	targets[1].ProbeDuration = aggregateControlProbeB
	var controlDeadline time.Duration
	options := mutationOptionsForTest(MutationOptions{
		Timeout: time.Hour,
		OriginalControl: func(_ context.Context, request gomutants.ExecRequest) (gomutants.CommandResult, error) {
			controlDeadline = request.Timeout
			return gomutants.CommandResult{Duration: aggregateExactControlDuration}, nil
		},
	})
	session := &mutationUnitSession{exec: func(request gomutants.ExecRequest) (gomutants.MutantResult, error) {
		if request.Timeout != aggregateMutantDeadline {
			t.Fatalf("mutant deadline = %s, want %s", request.Timeout, aggregateMutantDeadline)
		}
		return gomutants.MutantResult{ID: request.Mutant, Outcome: gomutants.OutcomeSurvived}, nil
	}}
	seed := evaluateMutationSeed(t.Context(), session, mutant, targets, options)
	if seed.err != nil || controlDeadline != time.Hour {
		t.Fatalf("seed = %+v, control deadline = %s, want the containment ceiling %s", seed, controlDeadline, time.Hour)
	}
}
