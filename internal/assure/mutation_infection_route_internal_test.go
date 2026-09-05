// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package assure

import (
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	gomutants "github.com/P4suta/go-mutants"
	goanalysis "github.com/P4suta/goatest/internal/golang"
	"github.com/P4suta/goatest/internal/trace"
)

// infectionBlock is the block of value.go every infection routing test places
// its mutant in, and the one every target below ran.
func infectionBlock() goanalysis.CoverageBlock {
	return goanalysis.CoverageBlock{StartLine: 7, StartColumn: 2, EndLine: 9, EndColumn: 3}
}

// infectionTarget is one target that ran that block, carrying the facts the
// probe pass left on it: whether the pass measured it at all, and, when it did,
// the catalogue indices of the mutants it made differ.
func infectionTarget(name string, duration time.Duration, probed bool, infected ...uint32) TargetEvidence {
	target := blockTarget(name, duration, infectionBlock())
	target.Probed, target.Infected = probed, infected
	return target
}

// infectionRoutingTargets are the three targets every infection routing test
// decides between: one the pass measured and saw make the site differ, one it
// measured and never saw make it differ, and one it could not measure at all.
func infectionRoutingTargets() []TargetEvidence {
	return []TargetEvidence{
		infectionTarget("TestInfects", 3*time.Millisecond, true, probedMutantIndex),
		infectionTarget("TestNeverInfects", time.Millisecond, true, 2, 9),
		infectionTarget("TestUnmeasured", 2*time.Millisecond, false),
	}
}

// probedMutantIndex is the catalogue index a measurement names this mutant by.
const probedMutantIndex = 7

// probedMutant is the edit inside that block as the engine reports it once it
// has compiled a probe form of it into the probe tree.
func probedMutant() gomutants.Mutant {
	return gomutants.Mutant{
		Index: probedMutantIndex, ID: "mutant-a", DisplayID: "arithmetic#1", Accepted: true, Rule: "arithmetic",
		Path: "value.go", Line: 8, Column: 5, Package: "fixture.example/module", Probed: true,
	}
}

func TestRouteDischargesAnUninfectedTarget(t *testing.T) {
	t.Parallel()
	route := routeMutant(probedMutant(), infectionRoutingTargets(), blockRoutingInstrumentation())
	// The unmeasured target carries no facts to argue with and stays; the
	// measured one that never made the site differ ran both programs through
	// identical states and drops out.
	if want := []string{"TestUnmeasured", "TestInfects"}; !slices.Equal(routedNames(route), want) {
		t.Fatalf("reaching = %v, want %v", routedNames(route), want)
	}
	want := []trace.Discharge{{Target: "target-TestNeverInfects", Reason: trace.DischargeNeverInfected}}
	if !reflect.DeepEqual(route.discharged, want) {
		t.Fatalf("discharged = %+v, want %+v", route.discharged, want)
	}
	if route.granularity != trace.GranularityBlock || route.fallback != "" || route.fileCandidates != 3 {
		t.Fatalf("route = %+v, want block granularity over three file candidates", route)
	}
}

func TestRouteKeepsAnUnprobedMutant(t *testing.T) {
	t.Parallel()
	// The engine compiled no probe form of this mutant, so no measurement could
	// ever have named it and its absence from one says nothing at all.
	mutant := probedMutant()
	mutant.Probed = false
	route := routeMutant(mutant, infectionRoutingTargets(), blockRoutingInstrumentation())
	want := []string{"TestNeverInfects", "TestUnmeasured", "TestInfects"}
	if !slices.Equal(routedNames(route), want) {
		t.Fatalf("reaching = %v, want %v", routedNames(route), want)
	}
	if route.discharged != nil {
		t.Fatalf("discharged = %+v, want nothing discharged for a mutant with no probe form", route.discharged)
	}
}

func TestRouteKeepsEveryTargetWhenTheProbePassFailed(t *testing.T) {
	t.Parallel()
	// The pass measured nothing, so every target is exactly what it was before
	// the pass existed: a target that infects everything it reaches.
	targets := infectionRoutingTargets()
	for index := range targets {
		targets[index].Probed, targets[index].Infected = false, nil
	}
	route := routeMutant(probedMutant(), targets, blockRoutingInstrumentation())
	want := []string{"TestNeverInfects", "TestUnmeasured", "TestInfects"}
	if !slices.Equal(routedNames(route), want) {
		t.Fatalf("reaching = %v, want %v", routedNames(route), want)
	}
	if route.discharged != nil {
		t.Fatalf("discharged = %+v, want nothing discharged by a pass that measured nothing", route.discharged)
	}
}

func TestRouteKeepsAFuzzTargetWhateverTheProbeSays(t *testing.T) {
	t.Parallel()
	// The pass never probes a fuzz target, so one arrives without facts and is
	// kept beside the measured test the same mutant's facts discharge.
	fuzz := infectionTarget("FuzzValue", 4*time.Millisecond, false)
	fuzz.Target.Kind = goanalysis.KindFuzz
	route := routeMutant(probedMutant(), append(infectionRoutingTargets(), fuzz), blockRoutingInstrumentation())
	if want := []string{"TestUnmeasured", "TestInfects", "FuzzValue"}; !slices.Equal(routedNames(route), want) {
		t.Fatalf("reaching = %v, want %v", routedNames(route), want)
	}
	want := []trace.Discharge{{Target: "target-TestNeverInfects", Reason: trace.DischargeNeverInfected}}
	if !reflect.DeepEqual(route.discharged, want) {
		t.Fatalf("discharged = %+v, want %+v", route.discharged, want)
	}
}

// bothProofsMutant is the narrowed-branch edit the engine also compiled a probe
// form of, so both proofs have something to say about the same mutation.
func bothProofsMutant() gomutants.Mutant {
	mutant := narrowedBranchMutant()
	mutant.Index, mutant.Probed = probedMutantIndex, true
	return mutant
}

// narrowedBranchProbedTarget is one measured target of the narrowed-branch file
// carrying both halves of the evidence: the blocks it ran, and the mutants the
// probe pass saw it make differ.
func narrowedBranchProbedTarget(name string, duration time.Duration, infected []uint32, blocks ...goanalysis.CoverageBlock) TargetEvidence {
	target := narrowedBranchTarget(name, goanalysis.KindTest, duration, blocks...)
	target.Probed, target.Infected = true, infected
	return target
}

func TestRouteRecordsBothProofsInRunOrder(t *testing.T) {
	t.Parallel()
	header, body, tail := bracedBodyBlocks()
	// The cheapest target never entered the gated body, the middle one entered
	// it and made the site differ, and the most expensive entered it and never
	// made it differ: one proof each, and one target left to run.
	targets := []TargetEvidence{
		narrowedBranchProbedTarget("TestSkipsIt", time.Millisecond, []uint32{probedMutantIndex}, header, tail),
		narrowedBranchProbedTarget("TestTakesIt", 2*time.Millisecond, []uint32{probedMutantIndex}, header, body),
		narrowedBranchProbedTarget("TestSeesNothing", 3*time.Millisecond, nil, header, body),
	}
	route := routeMutant(bothProofsMutant(), targets, narrowedBranchInstrumentation(header, body, tail))
	if want := []string{"TestTakesIt"}; !slices.Equal(routedNames(route), want) {
		t.Fatalf("reaching = %v, want %v", routedNames(route), want)
	}
	// The discharges keep the order the targets would have run in, whichever
	// proof removed each of them.
	want := []trace.Discharge{
		{Target: "target-TestSkipsIt", Reason: trace.DischargeBranchNeverTaken},
		{Target: "target-TestSeesNothing", Reason: trace.DischargeNeverInfected},
	}
	if !reflect.DeepEqual(route.discharged, want) {
		t.Fatalf("discharged = %+v, want %+v", route.discharged, want)
	}
}

func TestRouteNamesTheBranchProofWhenBothProofsDischargeATarget(t *testing.T) {
	t.Parallel()
	header, body, tail := bracedBodyBlocks()
	// This target neither entered the gated body nor ever made the mutated
	// value differ, so either proof would remove it. The branch proof is asked
	// first, which keeps every recording made so far comparable.
	targets := []TargetEvidence{
		narrowedBranchProbedTarget("TestSkipsIt", time.Millisecond, nil, header, tail),
		narrowedBranchProbedTarget("TestTakesIt", 2*time.Millisecond, []uint32{probedMutantIndex}, header, body),
	}
	route := routeMutant(bothProofsMutant(), targets, narrowedBranchInstrumentation(header, body, tail))
	if want := []string{"TestTakesIt"}; !slices.Equal(routedNames(route), want) {
		t.Fatalf("reaching = %v, want %v", routedNames(route), want)
	}
	want := []trace.Discharge{{Target: "target-TestSkipsIt", Reason: trace.DischargeBranchNeverTaken}}
	if !reflect.DeepEqual(route.discharged, want) {
		t.Fatalf("discharged = %+v, want %+v", route.discharged, want)
	}
}

func TestRouteLeavesInfectionOutOfAFileRoute(t *testing.T) {
	t.Parallel()
	// A route the blocks could not decide is the conservative one, and the plan
	// keeps infection off it: nothing narrows a reaching set that was never
	// narrowed in the first place.
	for _, test := range []struct {
		name     string
		line     int
		column   int
		fallback string
	}{
		{name: "a position the catalog could not report", fallback: trace.FallbackPositionUnknown},
		{name: "a position no instrumented block contains", line: 10, column: 1, fallback: trace.FallbackOutsideBlocks},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			mutant := probedMutant()
			mutant.Line, mutant.Column = test.line, test.column
			route := routeMutant(mutant, infectionRoutingTargets(), blockRoutingInstrumentation())
			if route.granularity != trace.GranularityFile || route.fallback != test.fallback {
				t.Fatalf("route = %+v, want the whole file", route)
			}
			if route.discharged != nil || len(route.reaching) != 3 {
				t.Fatalf("route = %+v, want every candidate kept and nothing discharged", route)
			}
		})
	}
}

func TestEvaluateMutationsSkipsADischargedTargetAndRecordsTheProof(t *testing.T) {
	t.Parallel()
	mutant := probedMutant()
	sink, recorder := newTraceRecording()
	session := &mutationUnitSession{catalog: gomutants.Catalog{Mutants: []gomutants.Mutant{mutant}}}
	evaluation, err := EvaluateMutations(t.Context(), session, []TargetEvidence{
		infectionTarget("TestInfects", 3*time.Millisecond, true, probedMutantIndex),
		infectionTarget("TestNeverInfects", time.Millisecond, true, 2, 9),
	}, MutationOptions{
		Root: t.TempDir(), Contract: "standard-v1", Trace: recorder, Instrumented: blockRoutingInstrumentation(),
	})
	if err != nil {
		t.Fatal(err)
	}
	var ran []string
	for _, request := range session.requests {
		ran = append(ran, strings.Join(request.Args, " "))
	}
	if !slices.Equal(ran, []string{"-test.run=^TestInfects$"}) {
		t.Fatalf("executions = %v, want only the target the probe pass left", ran)
	}
	wantSummary := "all reaching tests passed with this mutation active; 1 more discharged without running because none makes the mutated value differ"
	if len(evaluation.Findings) != 1 || evaluation.Findings[0].Kind != "surviving-mutant" ||
		evaluation.Findings[0].Summary != wantSummary {
		t.Fatalf("evaluation = %+v, want one surviving-mutant finding summarised %q", evaluation, wantSummary)
	}
	want := trace.RouteRecord{
		MutantID: "mutant-a", Rule: "arithmetic", Path: "value.go", Line: 8, Column: 5,
		ReachingTargets: []string{"target-TestInfects"}, Plan: []string{"individual:TestInfects"},
		Reason: trace.ReasonCoverageReaching, Granularity: trace.GranularityBlock, FileCandidates: 2,
		Discharged: []trace.Discharge{{Target: "target-TestNeverInfects", Reason: trace.DischargeNeverInfected}},
		Probed:     true,
	}
	if routes := recordedRoutes(sink); len(routes) != 1 || !reflect.DeepEqual(routes[0], want) {
		t.Fatalf("routes = %+v, want [%+v]", routes, want)
	}
}

func TestEvaluateMutationsResolvesAMutantNoReachingTargetInfectsWithoutRunning(t *testing.T) {
	t.Parallel()
	mutant := probedMutant()
	sink, recorder := newTraceRecording()
	session := &mutationUnitSession{catalog: gomutants.Catalog{Mutants: []gomutants.Mutant{mutant}}}
	evaluation, err := EvaluateMutations(t.Context(), session, []TargetEvidence{
		infectionTarget("TestNeverInfects", time.Millisecond, true, 2, 9),
		infectionTarget("TestAlsoNeverInfects", 2*time.Millisecond, true),
	}, MutationOptions{
		Root: t.TempDir(), Contract: "standard-v1", Trace: recorder, Instrumented: blockRoutingInstrumentation(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(session.requests) != 0 {
		t.Fatalf("executions = %+v, want none for a mutant no reaching target can observe", session.requests)
	}
	wantSummary := "no reaching test was run: every one was discharged because none makes the mutated value differ"
	if len(evaluation.Findings) != 1 || evaluation.Findings[0].Kind != "surviving-mutant" ||
		evaluation.Findings[0].Summary != wantSummary {
		t.Fatalf("evaluation = %+v, want one surviving-mutant finding summarised %q", evaluation, wantSummary)
	}
	want := trace.RouteRecord{
		MutantID: "mutant-a", Rule: "arithmetic", Path: "value.go", Line: 8, Column: 5,
		Reason: trace.ReasonCoverageReaching, Granularity: trace.GranularityBlock, FileCandidates: 2,
		Discharged: []trace.Discharge{
			{Target: "target-TestNeverInfects", Reason: trace.DischargeNeverInfected},
			{Target: "target-TestAlsoNeverInfects", Reason: trace.DischargeNeverInfected},
		},
		Probed: true,
	}
	if routes := recordedRoutes(sink); len(routes) != 1 || !reflect.DeepEqual(routes[0], want) {
		t.Fatalf("routes = %+v, want [%+v]", routes, want)
	}
}

func TestEvaluateMutationsUsesAWholeSuiteProbeToResolveAnUnreachedMutant(t *testing.T) {
	t.Parallel()
	mutant := probedMutant()
	target := blockTarget("TestLate", time.Second,
		goanalysis.CoverageBlock{StartLine: 12, StartColumn: 2, EndLine: 14, EndColumn: 3})
	target.Probed = true
	sink, recorder := newTraceRecording()
	session := &mutationUnitSession{
		catalog: gomutants.Catalog{Mutants: []gomutants.Mutant{mutant}},
		exec: func(gomutants.ExecRequest) (gomutants.MutantResult, error) {
			t.Fatal("a package suite whose control never infected the mutant was executed")
			return gomutants.MutantResult{}, nil
		},
	}
	evaluation, err := EvaluateMutations(t.Context(), session, []TargetEvidence{target}, MutationOptions{
		Root: t.TempDir(), Contract: "standard-v1", Trace: recorder,
		Instrumented: blockRoutingInstrumentation(),
		SuiteProbes: map[string]PackageProbeEvidence{
			mutant.Package: {Measured: true, Duration: 1500 * time.Millisecond},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(session.requests) != 0 {
		t.Fatalf("executions = %+v, want the package-suite proof to execute none", session.requests)
	}
	if len(evaluation.Findings) != 1 || evaluation.Findings[0].Kind != "unreached-mutant" ||
		evaluation.Findings[0].Summary != mutationSuiteUninfectedSummary {
		t.Fatalf("evaluation = %+v", evaluation)
	}
	want := trace.RouteRecord{
		MutantID: mutant.ID, Rule: mutant.Rule, Path: mutant.Path, Line: mutant.Line, Column: mutant.Column,
		Reason: trace.ReasonUnreached, Granularity: trace.GranularityBlock, FileCandidates: 1,
		SuiteProbe: packageSuiteProbeTarget(mutant.Package), Probed: true,
	}
	if routes := recordedRoutes(sink); len(routes) != 1 || !reflect.DeepEqual(routes[0], want) {
		t.Fatalf("routes = %+v, want [%+v]", routes, want)
	}
}

func TestEvaluateMutationsUsesWholeSuiteCoverageForAnUnprobedMutant(t *testing.T) {
	t.Parallel()
	mutant := probedMutant()
	mutant.Probed = false
	target := blockTarget("TestElsewhere", time.Second,
		goanalysis.CoverageBlock{StartLine: 12, StartColumn: 2, EndLine: 14, EndColumn: 3})
	sink, recorder := newTraceRecording()
	session := &mutationUnitSession{
		catalog: gomutants.Catalog{Mutants: []gomutants.Mutant{mutant}},
		exec: func(gomutants.ExecRequest) (gomutants.MutantResult, error) {
			t.Fatal("a package suite whose coverage never reached the mutant was executed")
			return gomutants.MutantResult{}, nil
		},
	}
	evaluation, err := EvaluateMutations(t.Context(), session, []TargetEvidence{target}, MutationOptions{
		Root: t.TempDir(), Contract: "standard-v1", Trace: recorder,
		Instrumented: blockRoutingInstrumentation(),
		SuiteCoverage: map[string]PackageSuiteCoverage{
			mutant.Package: {
				Instrumented: blockRoutingInstrumentation(), Duration: 2 * time.Second, WholeTree: true,
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(session.requests) != 0 || len(evaluation.Findings) != 1 ||
		evaluation.Findings[0].Kind != "unreached-mutant" ||
		evaluation.Findings[0].Summary != mutationSuiteUnreachedSummary {
		t.Fatalf("evaluation = %+v, executions = %+v", evaluation, session.requests)
	}
	want := trace.RouteRecord{
		MutantID: mutant.ID, Rule: mutant.Rule, Path: mutant.Path, Line: mutant.Line, Column: mutant.Column,
		Reason: trace.ReasonUnreached, Granularity: trace.GranularityBlock, FileCandidates: 1,
		SuiteCoverage: packageSuiteCoverageTarget(mutant.Package),
	}
	if routes := recordedRoutes(sink); len(routes) != 1 || !reflect.DeepEqual(routes[0], want) {
		t.Fatalf("routes = %+v, want [%+v]", routes, want)
	}
}

func TestPositiveTargetProbeOverridesSilentSuiteCoverage(t *testing.T) {
	t.Parallel()
	mutant := probedMutant()
	target := blockTarget("TestEnvironmentSpecific", 100*time.Millisecond,
		goanalysis.CoverageBlock{StartLine: 12, StartColumn: 2, EndLine: 14, EndColumn: 3})
	target.Probed, target.Infected = true, []uint32{mutant.Index}
	session := &mutationUnitSession{
		catalog: gomutants.Catalog{Mutants: []gomutants.Mutant{mutant}},
		exec: func(request gomutants.ExecRequest) (gomutants.MutantResult, error) {
			if !slices.Equal(request.Args, []string{"-test.run=^TestEnvironmentSpecific$"}) {
				t.Fatalf("counterexample request = %+v", request)
			}
			return gomutants.MutantResult{ID: mutant.ID, Outcome: gomutants.OutcomeSurvived}, nil
		},
	}
	evaluation, err := EvaluateMutations(t.Context(), session, []TargetEvidence{target}, MutationOptions{
		Root: t.TempDir(), Contract: "standard-v1", Instrumented: blockRoutingInstrumentation(),
		SuiteCoverage: map[string]PackageSuiteCoverage{
			mutant.Package: {Instrumented: blockRoutingInstrumentation(), Duration: 2 * time.Second},
		},
	})
	if err != nil || len(session.requests) != 1 || len(evaluation.Findings) != 1 ||
		evaluation.Findings[0].Kind != "surviving-mutant" {
		t.Fatalf("evaluation = (%+v, %v), executions = %+v", evaluation, err, session.requests)
	}
}

func TestPositiveTargetProbeSurvivesMissingSuiteFacts(t *testing.T) {
	t.Parallel()
	mutant := probedMutant()
	target := blockTarget("TestHidden", time.Millisecond,
		goanalysis.CoverageBlock{StartLine: 12, StartColumn: 2, EndLine: 14, EndColumn: 3})
	target.Probed, target.Infected = true, []uint32{mutant.Index}
	for _, testCase := range []struct {
		name   string
		suites map[string]PackageProbeEvidence
	}{
		{name: "no suite probe requested"},
		{name: "suite probe unavailable", suites: map[string]PackageProbeEvidence{
			mutant.Package: {Measured: false},
		}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			route := routeMutant(mutant, []TargetEvidence{target}, blockRoutingInstrumentation())
			route = applyProbeRouting(mutant, []TargetEvidence{target}, route, testCase.suites)
			if want := []string{target.Target.Name}; !slices.Equal(routedNames(route), want) ||
				!slices.Equal(route.probeReaching, []string{target.Target.ID}) {
				t.Fatalf("route with absent suite facts = %+v, want positive target %v", route, want)
			}
		})
	}
}

func TestNeededProbeSuitesExcludeAnsweredAndUnprobedFallbacks(t *testing.T) {
	t.Parallel()
	needed := probedMutant()
	needed.Package = "fixture.example/needed"
	answered := probedMutant()
	answered.ID, answered.Package = "answered", "fixture.example/answered"
	unprobed := probedMutant()
	unprobed.ID, unprobed.Package, unprobed.Probed = "unprobed", "fixture.example/unprobed", false
	suites := map[string]PackageSuiteCoverage{
		answered.Package: {Instrumented: blockRoutingInstrumentation()},
	}
	got := neededProbeSuitePackages(gomutants.Catalog{Mutants: []gomutants.Mutant{
		unprobed, answered, needed,
	}}, nil, blockRoutingInstrumentation(), suites)
	if want := []string{needed.Package}; !slices.Equal(got, want) {
		t.Fatalf("needed package suites = %v, want %v", got, want)
	}
}

func TestEvaluateMutationsCalibratesAnUnreachedMutantFromItsSuiteProbe(t *testing.T) {
	t.Parallel()
	mutant := probedMutant()
	// This positive target fact is deliberately coverage-blind. Because the
	// whole suite also infected the mutant, the suite is the compact execution
	// that retains TestMain and cross-test interactions.
	target := blockTarget("TestLate", 100*time.Millisecond,
		goanalysis.CoverageBlock{StartLine: 12, StartColumn: 2, EndLine: 14, EndColumn: 3})
	target.Probed, target.Infected = true, []uint32{probedMutantIndex}
	sink, recorder := newTraceRecording()
	session := &mutationUnitSession{
		catalog: gomutants.Catalog{Mutants: []gomutants.Mutant{mutant}},
		exec: func(request gomutants.ExecRequest) (gomutants.MutantResult, error) {
			if len(request.Args) != 0 || request.Package != mutant.Package || request.Timeout != 4*time.Second {
				t.Fatalf("suite request = %+v, want the two-second control doubled", request)
			}
			return gomutants.MutantResult{ID: mutant.ID, Outcome: gomutants.OutcomeSurvived}, nil
		},
	}
	evaluation, err := EvaluateMutations(t.Context(), session, []TargetEvidence{target}, MutationOptions{
		Root: t.TempDir(), Contract: "standard-v1", Trace: recorder,
		Instrumented: blockRoutingInstrumentation(),
		SuiteProbes: map[string]PackageProbeEvidence{
			mutant.Package: {Measured: true, Infected: []uint32{probedMutantIndex}, Duration: 2 * time.Second},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(session.requests) != 1 || len(evaluation.Findings) != 1 ||
		evaluation.Findings[0].Kind != "unreached-mutant" {
		t.Fatalf("evaluation = %+v, executions = %+v", evaluation, session.requests)
	}
	want := trace.RouteRecord{
		MutantID: mutant.ID, Rule: mutant.Rule, Path: mutant.Path, Line: mutant.Line, Column: mutant.Column,
		Plan: []string{mutationPlanPackageSuite}, Reason: trace.ReasonUnreached,
		Granularity: trace.GranularityBlock, FileCandidates: 1,
		SuiteProbe: packageSuiteProbeTarget(mutant.Package), Probed: true,
	}
	if routes := recordedRoutes(sink); len(routes) != 1 || !reflect.DeepEqual(routes[0], want) {
		t.Fatalf("routes = %+v, want [%+v]", routes, want)
	}
}

func TestEvaluateMutationsRecoversAReachableTargetFromAPositiveProbe(t *testing.T) {
	t.Parallel()
	mutant := probedMutant()
	target := blockTarget("TestHidden", 100*time.Millisecond,
		goanalysis.CoverageBlock{StartLine: 12, StartColumn: 2, EndLine: 14, EndColumn: 3})
	target.Probed, target.Infected, target.ProbeDuration = true, []uint32{probedMutantIndex}, 50*time.Millisecond
	sink, recorder := newTraceRecording()
	session := &mutationUnitSession{
		catalog: gomutants.Catalog{Mutants: []gomutants.Mutant{mutant}},
		exec: func(request gomutants.ExecRequest) (gomutants.MutantResult, error) {
			if !slices.Equal(request.Args, []string{"-test.run=^TestHidden$"}) ||
				request.Timeout != 1100*time.Millisecond {
				t.Fatalf("recovered target request = %+v", request)
			}
			return gomutants.MutantResult{ID: mutant.ID, Outcome: gomutants.OutcomeSurvived}, nil
		},
	}
	evaluation, err := EvaluateMutations(t.Context(), session, []TargetEvidence{target}, MutationOptions{
		Root: t.TempDir(), Contract: "standard-v1", Trace: recorder,
		Instrumented: blockRoutingInstrumentation(),
		SuiteProbes: map[string]PackageProbeEvidence{
			mutant.Package: {Measured: true, Duration: time.Second},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(session.requests) != 1 || len(evaluation.Findings) != 1 ||
		evaluation.Findings[0].Kind != "surviving-mutant" {
		t.Fatalf("evaluation = %+v, executions = %+v", evaluation, session.requests)
	}
	want := trace.RouteRecord{
		MutantID: mutant.ID, Rule: mutant.Rule, Path: mutant.Path, Line: mutant.Line, Column: mutant.Column,
		ReachingTargets: []string{target.Target.ID}, Plan: []string{mutationPlanIndividual + target.Target.Name},
		Reason: trace.ReasonProbeReaching, Granularity: trace.GranularityBlock, FileCandidates: 1,
		ProbeReaching: []string{target.Target.ID}, SuiteProbe: packageSuiteProbeTarget(mutant.Package), Probed: true,
	}
	if routes := recordedRoutes(sink); len(routes) != 1 || !reflect.DeepEqual(routes[0], want) {
		t.Fatalf("routes = %+v, want [%+v]", routes, want)
	}
}

func TestMutationSurvivalSummaryNamesEachProof(t *testing.T) {
	t.Parallel()
	branch := trace.Discharge{Target: "target-TestSkipsIt", Reason: trace.DischargeBranchNeverTaken}
	alsoBranch := trace.Discharge{Target: "target-TestAlsoSkipsIt", Reason: trace.DischargeBranchNeverTaken}
	infection := trace.Discharge{Target: "target-TestNeverInfects", Reason: trace.DischargeNeverInfected}
	survived := "all reaching tests passed with this mutation active"
	for _, test := range []struct {
		name       string
		discharged []trace.Discharge
		want       string
	}{
		{name: "nothing discharged", want: survived},
		{
			name: "the branch proof alone", discharged: []trace.Discharge{branch},
			want: survived + "; 1 more discharged without running because none takes the branch this mutation narrows",
		},
		{
			name: "the infection facts alone", discharged: []trace.Discharge{infection},
			want: survived + "; 1 more discharged without running because none makes the mutated value differ",
		},
		{
			name: "both proofs", discharged: []trace.Discharge{branch, alsoBranch, infection},
			want: survived + "; 3 more discharged without running because 2 take no branch this mutation narrows and 1 never make the mutated value differ",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := mutationSurvivalSummary(test.discharged); got != test.want {
				t.Errorf("mutationSurvivalSummary(%+v) = %q, want %q", test.discharged, got, test.want)
			}
		})
	}
}

func TestMutationDischargedSummaryNamesEachProof(t *testing.T) {
	t.Parallel()
	branch := trace.Discharge{Target: "target-TestSkipsIt", Reason: trace.DischargeBranchNeverTaken}
	infection := trace.Discharge{Target: "target-TestNeverInfects", Reason: trace.DischargeNeverInfected}
	alsoInfection := trace.Discharge{Target: "target-TestAlsoNeverInfects", Reason: trace.DischargeNeverInfected}
	opening := "no reaching test was run: every one was discharged because "
	for _, test := range []struct {
		name       string
		discharged []trace.Discharge
		want       string
	}{
		{
			name: "the branch proof alone", discharged: []trace.Discharge{branch},
			want: opening + "none takes the branch this mutation narrows",
		},
		{
			name: "the infection facts alone", discharged: []trace.Discharge{infection, alsoInfection},
			want: opening + "none makes the mutated value differ",
		},
		{
			name: "both proofs", discharged: []trace.Discharge{branch, infection, alsoInfection},
			want: opening + "1 take no branch this mutation narrows and 2 never make the mutated value differ",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := mutationDischargedSummary(test.discharged); got != test.want {
				t.Errorf("mutationDischargedSummary(%+v) = %q, want %q", test.discharged, got, test.want)
			}
		})
	}
}
