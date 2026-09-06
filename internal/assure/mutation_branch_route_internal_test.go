// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package assure

import (
	"context"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	gomutants "github.com/P4suta/go-mutants"
	goanalysis "github.com/P4suta/goatest/internal/golang"
	"github.com/P4suta/goatest/internal/report"
	"github.com/P4suta/goatest/internal/trace"
)

func bracedBodyBlocks() (header, body, tail goanalysis.CoverageBlock) {
	return goanalysis.CoverageBlock{StartLine: 3, StartColumn: 34, EndLine: 4, EndColumn: 12},
		goanalysis.CoverageBlock{StartLine: 4, StartColumn: 12, EndLine: 6, EndColumn: 3},
		goanalysis.CoverageBlock{StartLine: 6, StartColumn: 3, EndLine: 7, EndColumn: 14}
}

func statementBodyBlocks() (header, body, tail goanalysis.CoverageBlock) {
	return goanalysis.CoverageBlock{StartLine: 4, StartColumn: 2, EndLine: 4, EndColumn: 12},
		goanalysis.CoverageBlock{StartLine: 5, StartColumn: 3, EndLine: 6, EndColumn: 1},
		goanalysis.CoverageBlock{StartLine: 7, StartColumn: 2, EndLine: 7, EndColumn: 14}
}

func narrowedBranchMutant() gomutants.Mutant {
	mutant := gomutants.Mutant{
		ID: "mutant-a", DisplayID: "le-to-lt#1", Accepted: true, Rule: "le-to-lt",
		Original: "<=", Replacement: "<",
		Path: "value.go", Line: 4, Column: 5, Package: "fixture.example/module",
	}
	mutant.Branch = &gomutants.BranchProof{
		Direction:     gomutants.BranchDecreasing,
		BodyStartLine: 4, BodyStartColumn: 12, BodyEndLine: 6, BodyEndColumn: 2,
	}
	return mutant
}

func unprovedBranchMutant() gomutants.Mutant {
	mutant := narrowedBranchMutant()
	mutant.Branch = nil
	return mutant
}

func narrowedBranchTarget(name string, kind goanalysis.TargetKind, duration time.Duration, blocks ...goanalysis.CoverageBlock) TargetEvidence {
	target := blockTarget(name, duration, blocks...)
	target.Target.Kind = kind
	return target
}

func narrowedBranchTargets(header, body, tail goanalysis.CoverageBlock) []TargetEvidence {
	return []TargetEvidence{
		narrowedBranchTarget("TestTakesIt", goanalysis.KindTest, 3*time.Millisecond, header, body),
		narrowedBranchTarget("TestSkipsIt", goanalysis.KindTest, time.Millisecond, header, tail),
		narrowedBranchTarget("FuzzSkipsIt", goanalysis.KindFuzz, 2*time.Millisecond, header, tail),
		inexactBlockTarget("TestInexact", 5*time.Millisecond),
	}
}

func narrowedBranchInstrumentation(blocks ...goanalysis.CoverageBlock) []goanalysis.FileCoverage {
	return []goanalysis.FileCoverage{{Path: "value.go", Blocks: blocks}}
}

func TestRouteMutantDischargesATargetThatNeverTakesTheNarrowedBranch(t *testing.T) {
	t.Parallel()
	for _, blocks := range []func() (goanalysis.CoverageBlock, goanalysis.CoverageBlock, goanalysis.CoverageBlock){
		bracedBodyBlocks, statementBodyBlocks,
	} {
		header, body, tail := blocks()
		targets := narrowedBranchTargets(header, body, tail)
		route := routeMutant(narrowedBranchMutant(), targets, narrowedBranchInstrumentation(header, body, tail))

		if want := []string{"TestTakesIt", "TestInexact"}; !slices.Equal(routedNames(route), want) {
			t.Errorf("reaching over %+v = %v, want %v", body, routedNames(route), want)
		}
		want := []trace.Discharge{
			{Target: "target-TestSkipsIt", Reason: trace.DischargeBranchNeverTaken},
			{Target: "target-FuzzSkipsIt", Reason: trace.DischargeBranchNeverTaken},
		}
		if !reflect.DeepEqual(route.discharged, want) {
			t.Errorf("discharged over %+v = %+v, want %+v", body, route.discharged, want)
		}
		if route.granularity != trace.GranularityBlock || route.fallback != "" || route.fileCandidates != 4 {
			t.Errorf("route over %+v = %+v, want block granularity over four file candidates", body, route)
		}
	}
}

func TestRouteMutantKeepsEveryTargetWhenTheBodyWasNeverInstrumented(t *testing.T) {
	t.Parallel()
	header, body, tail := bracedBodyBlocks()
	targets := narrowedBranchTargets(header, body, tail)

	route := routeMutant(narrowedBranchMutant(), targets, narrowedBranchInstrumentation(header, tail))
	if want := []string{"TestSkipsIt", "FuzzSkipsIt", "TestTakesIt", "TestInexact"}; !slices.Equal(routedNames(route), want) {
		t.Fatalf("reaching = %v, want %v", routedNames(route), want)
	}
	if route.discharged != nil {
		t.Fatalf("discharged = %+v, want nothing discharged over an uninstrumented body", route.discharged)
	}
	if route.granularity != trace.GranularityBlock || route.fallback != "" || route.fileCandidates != 4 {
		t.Fatalf("route = %+v, want block granularity over four file candidates", route)
	}
}

func TestRouteMutantKeepsEveryTargetWithoutAProof(t *testing.T) {
	t.Parallel()
	header, body, tail := bracedBodyBlocks()
	route := routeMutant(unprovedBranchMutant(), narrowedBranchTargets(header, body, tail),
		narrowedBranchInstrumentation(header, body, tail))
	if want := []string{"TestSkipsIt", "FuzzSkipsIt", "TestTakesIt", "TestInexact"}; !slices.Equal(routedNames(route), want) {
		t.Fatalf("reaching = %v, want %v", routedNames(route), want)
	}
	if route.discharged != nil {
		t.Fatalf("discharged = %+v, want nothing discharged without a proof", route.discharged)
	}
}

func TestRouteMutantKeepsEveryTargetWhenTheProofIsMalformed(t *testing.T) {
	t.Parallel()
	header, body, tail := bracedBodyBlocks()
	targets := narrowedBranchTargets(header, body, tail)
	instrumented := narrowedBranchInstrumentation(header, body, tail)
	for _, test := range []struct {
		name  string
		proof gomutants.BranchProof
	}{
		{name: "an end before its start", proof: gomutants.BranchProof{BodyStartLine: 6, BodyStartColumn: 2, BodyEndLine: 4, BodyEndColumn: 12}},
		{name: "an end before its start on one line", proof: gomutants.BranchProof{BodyStartLine: 4, BodyStartColumn: 30, BodyEndLine: 4, BodyEndColumn: 12}},
		{name: "no start line", proof: gomutants.BranchProof{BodyStartColumn: 12, BodyEndLine: 6, BodyEndColumn: 2}},
		{name: "no start column", proof: gomutants.BranchProof{BodyStartLine: 4, BodyEndLine: 6, BodyEndColumn: 2}},
		{name: "no end line", proof: gomutants.BranchProof{BodyStartLine: 4, BodyStartColumn: 12, BodyEndColumn: 2}},
		{name: "no end column", proof: gomutants.BranchProof{BodyStartLine: 4, BodyStartColumn: 12, BodyEndLine: 6}},
		{name: "a body that starts at the edit", proof: gomutants.BranchProof{BodyStartLine: 4, BodyStartColumn: 5, BodyEndLine: 6, BodyEndColumn: 2}},
		{name: "a body that starts before the edit", proof: gomutants.BranchProof{BodyStartLine: 4, BodyStartColumn: 4, BodyEndLine: 6, BodyEndColumn: 2}},
		{name: "a body on an earlier line than the edit", proof: gomutants.BranchProof{BodyStartLine: 3, BodyStartColumn: 34, BodyEndLine: 6, BodyEndColumn: 2}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			mutant := narrowedBranchMutant()
			proof := test.proof
			proof.Direction = gomutants.BranchDecreasing
			mutant.Branch = &proof
			route := routeMutant(mutant, targets, instrumented)
			if want := []string{"TestSkipsIt", "FuzzSkipsIt", "TestTakesIt", "TestInexact"}; !slices.Equal(routedNames(route), want) {
				t.Fatalf("reaching = %v, want %v", routedNames(route), want)
			}
			if route.discharged != nil {
				t.Fatalf("discharged = %+v, want nothing discharged on a proof routing cannot read", route.discharged)
			}
		})
	}
}

func TestRouteMutantNeverDischargesOnAFileGranularityRoute(t *testing.T) {
	t.Parallel()
	header, body, tail := bracedBodyBlocks()
	targets := narrowedBranchTargets(header, body, tail)
	instrumented := narrowedBranchInstrumentation(header, body, tail)
	for _, test := range []struct {
		name     string
		mutant   gomutants.Mutant
		fallback string
	}{
		{name: "position unknown", mutant: gomutants.Mutant{Path: "value.go", Line: 0, Column: 0}, fallback: trace.FallbackPositionUnknown},
		{name: "outside every block", mutant: gomutants.Mutant{Path: "value.go", Line: 2, Column: 1}, fallback: trace.FallbackOutsideBlocks},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			mutant := test.mutant
			proved := narrowedBranchMutant()
			mutant.Branch = proved.Branch
			route := routeMutant(mutant, targets, instrumented)
			if route.granularity != trace.GranularityFile || route.fallback != test.fallback {
				t.Fatalf("route = %+v, want the whole file", route)
			}
			if route.discharged != nil || len(route.reaching) != 4 {
				t.Fatalf("route = %+v, want every candidate kept and nothing discharged", route)
			}
		})
	}
}

func TestRouteMutantNeverNamesATargetOnBothSides(t *testing.T) {
	t.Parallel()
	header, body, tail := bracedBodyBlocks()
	all := []goanalysis.CoverageBlock{header, body, tail}
	kinds := []goanalysis.TargetKind{goanalysis.KindTest, goanalysis.KindFuzz, goanalysis.KindTest}

	for layout := range 1 << (len(all) * len(kinds)) {
		targets := make([]TargetEvidence, 0, len(kinds))
		for index := range kinds {
			blocks := make([]goanalysis.CoverageBlock, 0, len(all))
			for position, block := range all {
				if layout>>(len(all)*index+position)&1 == 1 {
					blocks = append(blocks, block)
				}
			}
			targets = append(targets, narrowedBranchTarget(
				"Target"+strings.Repeat("X", index+1), kinds[index], time.Duration(index+1)*time.Millisecond, blocks...))
		}
		instrumented := narrowedBranchInstrumentation(all...)
		proved := routeMutant(narrowedBranchMutant(), targets, instrumented)
		plain := routeMutant(unprovedBranchMutant(), targets, instrumented)
		if len(proved.reaching)+len(proved.discharged) != len(plain.reaching) {
			t.Fatalf("layout %d routed %d reaching and %d discharged, want the %d the same route reaches unproved",
				layout, len(proved.reaching), len(proved.discharged), len(plain.reaching))
		}
		reaching := make(map[string]bool, len(proved.reaching))
		for _, target := range proved.reaching {
			reaching[target.Target.ID] = true
		}
		for _, discharge := range proved.discharged {
			if reaching[discharge.Target] {
				t.Fatalf("layout %d discharged %s and reaches it: %+v", layout, discharge.Target, proved)
			}
			if !slices.ContainsFunc(plain.reaching, func(target TargetEvidence) bool {
				return target.Target.ID == discharge.Target
			}) {
				t.Fatalf("layout %d discharged %s, which the unproved route never reached", layout, discharge.Target)
			}
		}
	}
}

func TestEvaluateMutationsResolvesAFullyDischargedMutantWithoutRunningAnything(t *testing.T) {
	t.Parallel()
	header, body, tail := bracedBodyBlocks()
	mutant := narrowedBranchMutant()
	sink, recorder := newTraceRecording()
	session := &mutationUnitSession{catalog: gomutants.Catalog{Mutants: []gomutants.Mutant{mutant}}}
	checkpointed := make(map[string]MutationEvaluation)
	evaluation, err := evaluateMutationsForTest(t.Context(), session, []TargetEvidence{
		narrowedBranchTarget("TestSkipsIt", goanalysis.KindTest, time.Millisecond, header, tail),
	}, MutationOptions{
		Trace:        recorder,
		Instrumented: narrowedBranchInstrumentation(header, body, tail),
		Checkpoint:   func(id string, unit MutationEvaluation) { checkpointed[id] = unit },
	})

	if err != nil {
		t.Fatal(err)
	}
	if len(session.requests) != 0 {
		t.Fatalf("executions = %+v, want none for a mutant every reaching test was discharged for", session.requests)
	}
	wantSummary := "no reaching test was run: every one was discharged because none takes the branch this mutation narrows"
	if len(evaluation.Findings) != 1 || evaluation.Findings[0].Kind != "surviving-mutant" ||
		evaluation.Findings[0].Summary != wantSummary {
		t.Fatalf("evaluation = %+v, want one surviving-mutant finding summarised %q", evaluation, wantSummary)
	}
	if evaluation.Accounting.Survived != 1 || evaluation.Mutants[0].Status != report.MutantSurvived {
		t.Fatalf("accounting = %+v, dispositions = %+v", evaluation.Accounting, evaluation.Mutants)
	}
	want := trace.RouteRecord{
		MutantID: "mutant-a", Rule: "le-to-lt", Path: "value.go", Line: 4, Column: 5,
		Reason: trace.ReasonCoverageReaching, Granularity: trace.GranularityBlock, FileCandidates: 1,
		Discharged: []trace.Discharge{{Target: "target-TestSkipsIt", Reason: trace.DischargeBranchNeverTaken}},
	}
	if routes := recordedRoutes(sink); len(routes) != 1 || !reflect.DeepEqual(routes[0], want) {
		t.Fatalf("routes = %+v, want [%+v]", recordedRoutes(sink), want)
	}
	unit, saved := checkpointed["mutant-a"]
	if !saved || !reflect.DeepEqual(unit.Findings, evaluation.Findings) {
		t.Fatalf("checkpointed = %+v (saved=%t), want the reported %+v", unit, saved, evaluation.Findings)
	}
}

func TestEvaluateMutationsRunsOnlyTheTargetsAProofLeavesAndCountsTheRest(t *testing.T) {
	t.Parallel()
	header, body, tail := bracedBodyBlocks()
	mutant := narrowedBranchMutant()
	sink, recorder := newTraceRecording()
	session := &mutationUnitSession{catalog: gomutants.Catalog{Mutants: []gomutants.Mutant{mutant}}}
	evaluation, err := evaluateMutationsForTest(t.Context(), session, []TargetEvidence{
		narrowedBranchTarget("TestTakesIt", goanalysis.KindTest, 3*time.Millisecond, header, body),
		narrowedBranchTarget("TestSkipsIt", goanalysis.KindTest, time.Millisecond, header, tail),
	}, MutationOptions{
		Trace:        recorder,
		Instrumented: narrowedBranchInstrumentation(header, body, tail),
	})

	if err != nil {
		t.Fatal(err)
	}
	var ran []string
	for _, request := range session.requests {
		ran = append(ran, strings.Join(request.Args, " "))
	}
	if !slices.Equal(ran, []string{"-test.run=^TestTakesIt$"}) {
		t.Fatalf("executions = %v, want only the target the proof left", ran)
	}
	wantSummary := "all reaching tests passed with this mutation active; 1 more discharged without running because none takes the branch this mutation narrows"
	if len(evaluation.Findings) != 1 || evaluation.Findings[0].Kind != "surviving-mutant" ||
		evaluation.Findings[0].Summary != wantSummary {
		t.Fatalf("evaluation = %+v, want one surviving-mutant finding summarised %q", evaluation, wantSummary)
	}
	want := trace.RouteRecord{
		MutantID: "mutant-a", Rule: "le-to-lt", Path: "value.go", Line: 4, Column: 5,
		ReachingTargets: []string{"target-TestTakesIt"}, Plan: []string{"individual:TestTakesIt"},
		Reason: trace.ReasonCoverageReaching, Granularity: trace.GranularityBlock, FileCandidates: 2,
		Discharged: []trace.Discharge{{Target: "target-TestSkipsIt", Reason: trace.DischargeBranchNeverTaken}},
	}
	if routes := recordedRoutes(sink); len(routes) != 1 || !reflect.DeepEqual(routes[0], want) {
		t.Fatalf("routes = %+v, want [%+v]", recordedRoutes(sink), want)
	}
}

func TestEvaluateMutationsReportsADischargedKillExactlyAsAnUndischargedOne(t *testing.T) {
	t.Parallel()
	header, body, tail := bracedBodyBlocks()
	targets := []TargetEvidence{
		narrowedBranchTarget("TestTakesIt", goanalysis.KindTest, 3*time.Millisecond, header, body),
		narrowedBranchTarget("TestSkipsIt", goanalysis.KindTest, time.Millisecond, header, tail),
	}
	kill := func(request gomutants.ExecRequest) (gomutants.MutantResult, error) {
		if slices.ContainsFunc(request.Args, func(argument string) bool { return strings.Contains(argument, "TestTakesIt") }) {
			return gomutants.MutantResult{ID: request.Mutant, Outcome: gomutants.OutcomeKilled}, nil
		}
		return gomutants.MutantResult{ID: request.Mutant, Outcome: gomutants.OutcomeSurvived}, nil
	}
	evaluate := func(mutant gomutants.Mutant) MutationEvaluation {
		t.Helper()
		session := &mutationUnitSession{catalog: gomutants.Catalog{Mutants: []gomutants.Mutant{mutant}}, exec: kill}
		evaluation, err := evaluateMutationsForTest(t.Context(), session, targets, MutationOptions{
			Instrumented: narrowedBranchInstrumentation(header, body, tail),
		})

		if err != nil {
			t.Fatal(err)
		}
		return evaluation
	}

	proved, plain := evaluate(narrowedBranchMutant()), evaluate(unprovedBranchMutant())
	if proved.Accounting.Killed != 1 || plain.Accounting.Killed != 1 || proved.Evidence[0].Status != plain.Evidence[0].Status {
		t.Fatalf("discharged evaluation = %+v, undischarged = %+v", proved, plain)
	}
}

func TestEvaluateMutationsDischargesFuzzSeedTargetsWithDeterministicCoverage(t *testing.T) {
	t.Parallel()
	header, body, tail := bracedBodyBlocks()
	mutant := narrowedBranchMutant()
	session := &mutationUnitSession{catalog: gomutants.Catalog{Mutants: []gomutants.Mutant{mutant}}}
	evaluation, err := evaluateMutationsForTest(t.Context(), session, []TargetEvidence{
		narrowedBranchTarget("FuzzSkipsIt", goanalysis.KindFuzz, 2*time.Millisecond, header, tail),
		narrowedBranchTarget("TestSkipsIt", goanalysis.KindTest, time.Millisecond, header, tail),
	}, MutationOptions{
		Timeout: time.Second,
		OriginalControl: func(context.Context, gomutants.ExecRequest) (gomutants.CommandResult, error) {
			return gomutants.CommandResult{Duration: time.Millisecond}, nil
		},
		Instrumented: narrowedBranchInstrumentation(header, body, tail),
	})

	if err != nil {
		t.Fatal(err)
	}
	if len(session.requests) != 0 {
		t.Fatalf("executions = %+v, want both targets discharged", session.requests)
	}

	wantSummary := "no reaching test was run: every one was discharged because none takes the branch this mutation narrows"
	if len(evaluation.Findings) != 1 || evaluation.Findings[0].Kind != "surviving-mutant" ||
		evaluation.Findings[0].Summary != wantSummary {
		t.Fatalf("evaluation = %+v, want one surviving-mutant finding summarised %q", evaluation, wantSummary)
	}
}
