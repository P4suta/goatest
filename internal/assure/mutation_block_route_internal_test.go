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

// blockTarget is one measured target that reached value.go and knows which
// blocks of it it ran.
func blockTarget(name string, duration time.Duration, blocks ...goanalysis.CoverageBlock) TargetEvidence {
	return TargetEvidence{
		Target: goanalysis.Target{
			ID: "target-" + name, Name: name, Kind: goanalysis.KindTest, Package: "fixture.example/module",
		},
		CoveredFiles: []string{"value.go"},
		Covered:      []goanalysis.FileCoverage{{Path: "value.go", Blocks: blocks}},
		Duration:     duration,
	}
}

// resumedBlockTarget is a target restored from a checkpoint: it reached
// value.go, but the checkpoint kept no blocks, so it says it knows none.
func resumedBlockTarget(name string, duration time.Duration) TargetEvidence {
	return TargetEvidence{
		Target: goanalysis.Target{
			ID: "target-" + name, Name: name, Kind: goanalysis.KindTest, Package: "fixture.example/module",
		},
		CoveredFiles: []string{"value.go"},
		Duration:     duration,
	}
}

// blockRoutingTargets are the three targets every block routing test decides
// between: one that ran the early block, one that ran the late block, and one
// restored from a checkpoint that cannot say which block it ran.
func blockRoutingTargets() []TargetEvidence {
	return []TargetEvidence{
		blockTarget("TestEarly", 3*time.Millisecond, goanalysis.CoverageBlock{StartLine: 7, StartColumn: 2, EndLine: 9, EndColumn: 3}),
		blockTarget("TestLate", time.Millisecond, goanalysis.CoverageBlock{StartLine: 12, StartColumn: 2, EndLine: 14, EndColumn: 3}),
		resumedBlockTarget("TestResumed", 2*time.Millisecond),
	}
}

// blockRoutingInstrumentation is every block the baseline compiled
// instrumentation for, which leaves lines 10 and 11 outside every block.
func blockRoutingInstrumentation() []goanalysis.FileCoverage {
	return []goanalysis.FileCoverage{{Path: "value.go", Blocks: []goanalysis.CoverageBlock{
		{StartLine: 7, StartColumn: 2, EndLine: 9, EndColumn: 3},
		{StartLine: 12, StartColumn: 2, EndLine: 14, EndColumn: 3},
	}}}
}

// routedNames names the targets a route reaches, in the order they will run.
func routedNames(route mutationRoute) []string {
	names := make([]string, 0, len(route.reaching))
	for _, target := range route.reaching {
		names = append(names, target.Target.Name)
	}
	return names
}

func TestRouteMutantUsesBlockEvidenceWhenThePositionIsKnown(t *testing.T) {
	t.Parallel()
	mutant := gomutants.Mutant{Path: "value.go", Line: 8, Column: 5, Package: "fixture.example/module"}
	route := routeMutant(mutant, blockRoutingTargets(), blockRoutingInstrumentation())
	// The resumed target has no blocks to argue with, so it stays in; the late
	// target proved it never ran line 8 and drops out.
	if want := []string{"TestResumed", "TestEarly"}; !slices.Equal(routedNames(route), want) {
		t.Fatalf("reaching = %v, want %v", routedNames(route), want)
	}
	if route.granularity != trace.GranularityBlock || route.fallback != "" || route.fileCandidates != 3 {
		t.Fatalf("route = %+v, want block granularity over three file candidates", route)
	}
}

func TestRouteMutantFallsBackToTheFileWhenThePositionIsUnknown(t *testing.T) {
	t.Parallel()
	for _, position := range []gomutants.Mutant{
		{Path: "value.go", Line: 0, Column: 0},
		{Path: "value.go", Line: 8, Column: 0},
		{Path: "value.go", Line: 0, Column: 5},
		{Path: "value.go", Line: -1, Column: -1},
	} {
		route := routeMutant(position, blockRoutingTargets(), blockRoutingInstrumentation())
		want := []string{"TestLate", "TestResumed", "TestEarly"}
		if !slices.Equal(routedNames(route), want) {
			t.Errorf("reaching for %+v = %v, want %v", position, routedNames(route), want)
		}
		if route.granularity != trace.GranularityFile || route.fallback != trace.FallbackPositionUnknown || route.fileCandidates != 3 {
			t.Errorf("route for %+v = %+v, want the whole file", position, route)
		}
	}
}

func TestRouteMutantFallsBackToTheFileOutsideEveryInstrumentedBlock(t *testing.T) {
	t.Parallel()
	mutant := gomutants.Mutant{Path: "value.go", Line: 10, Column: 1, Package: "fixture.example/module"}
	route := routeMutant(mutant, blockRoutingTargets(), blockRoutingInstrumentation())
	want := []string{"TestLate", "TestResumed", "TestEarly"}
	if !slices.Equal(routedNames(route), want) {
		t.Fatalf("reaching = %v, want %v", routedNames(route), want)
	}
	if route.granularity != trace.GranularityFile || route.fallback != trace.FallbackOutsideBlocks || route.fileCandidates != 3 {
		t.Fatalf("route = %+v, want the whole file for a position no block owns", route)
	}
}

func TestRouteMutantLeavesAnInstrumentedButUncoveredBlockUnreached(t *testing.T) {
	t.Parallel()
	targets := []TargetEvidence{
		blockTarget("TestEarly", 3*time.Millisecond, goanalysis.CoverageBlock{StartLine: 7, StartColumn: 2, EndLine: 9, EndColumn: 3}),
		blockTarget("TestAlsoEarly", time.Millisecond, goanalysis.CoverageBlock{StartLine: 7, StartColumn: 2, EndLine: 9, EndColumn: 3}),
	}
	mutant := gomutants.Mutant{Path: "value.go", Line: 13, Column: 3, Package: "fixture.example/module"}
	route := routeMutant(mutant, targets, blockRoutingInstrumentation())
	if len(route.reaching) != 0 {
		t.Fatalf("reaching = %v, want none", routedNames(route))
	}
	if route.granularity != trace.GranularityBlock || route.fallback != "" || route.fileCandidates != 2 {
		t.Fatalf("route = %+v, want block granularity over two file candidates", route)
	}
}

func TestRouteMutantTreatsAMissingFileAsOutsideEveryBlock(t *testing.T) {
	t.Parallel()
	mutant := gomutants.Mutant{Path: "other/absent.go", Line: 8, Column: 5, Package: "fixture.example/module"}
	route := routeMutant(mutant, blockRoutingTargets(), blockRoutingInstrumentation())
	if len(route.reaching) != 0 || route.fileCandidates != 0 {
		t.Fatalf("route = %+v, want nothing reaching a file no target linked", route)
	}
	if route.granularity != trace.GranularityFile || route.fallback != trace.FallbackOutsideBlocks {
		t.Fatalf("route = %+v, want the whole file for an unknown file", route)
	}
}

func TestRouteMutantNeverReachesBeyondTheFileCandidates(t *testing.T) {
	t.Parallel()
	targets := blockRoutingTargets()
	for line := range 16 {
		for column := range 6 {
			mutant := gomutants.Mutant{Path: "value.go", Line: line, Column: column, Package: "fixture.example/module"}
			route := routeMutant(mutant, targets, blockRoutingInstrumentation())
			if len(route.reaching) > route.fileCandidates {
				t.Fatalf("route at %d.%d = %+v, want at most %d targets", line, column, route, route.fileCandidates)
			}
			for _, reached := range route.reaching {
				if !slices.Contains(reached.CoveredFiles, "value.go") {
					t.Fatalf("route at %d.%d reached %s, which never ran the file", line, column, reached.Target.Name)
				}
			}
		}
	}
}

func TestEvaluateMutationsRoutesByBlockAndRecordsItInTheTrace(t *testing.T) {
	t.Parallel()
	mutant := gomutants.Mutant{
		ID: "mutant-a", DisplayID: "arithmetic#1", Accepted: true, Rule: "arithmetic",
		Path: "value.go", Line: 8, Column: 5, Package: "fixture.example/module",
	}
	sink, recorder := newTraceRecording()
	session := &mutationUnitSession{catalog: gomutants.Catalog{Mutants: []gomutants.Mutant{mutant}}}
	targets := []TargetEvidence{
		blockTarget("TestEarly", 3*time.Millisecond, goanalysis.CoverageBlock{StartLine: 7, StartColumn: 2, EndLine: 9, EndColumn: 3}),
		blockTarget("TestLate", time.Millisecond, goanalysis.CoverageBlock{StartLine: 12, StartColumn: 2, EndLine: 14, EndColumn: 3}),
	}
	evaluation, err := EvaluateMutations(t.Context(), session, targets, MutationOptions{
		Root: t.TempDir(), Contract: "standard-v1", Trace: recorder, Instrumented: blockRoutingInstrumentation(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(evaluation.Findings) != 1 || evaluation.Findings[0].Kind != "surviving-mutant" {
		t.Fatalf("evaluation = %+v", evaluation)
	}
	want := trace.RouteRecord{
		MutantID: "mutant-a", Rule: "arithmetic", Path: "value.go", Line: 8, Column: 5,
		ReachingTargets: []string{"target-TestEarly"}, Plan: []string{"individual:TestEarly"},
		Reason: trace.ReasonCoverageReaching, Granularity: trace.GranularityBlock, FileCandidates: 2,
	}
	if routes := recordedRoutes(sink); len(routes) != 1 || !reflect.DeepEqual(routes[0], want) {
		t.Fatalf("routes = %+v, want [%+v]", routes, want)
	}
	var ran []string
	for _, request := range session.requests {
		ran = append(ran, strings.Join(request.Args, " "))
	}
	if !slices.Equal(ran, []string{"-test.run=^TestEarly$"}) {
		t.Fatalf("executions = %v, want only the target that ran the block", ran)
	}
}

func TestEvaluateMutationsRunsThePackageSuiteForAMutantInAnUncoveredBlock(t *testing.T) {
	t.Parallel()
	mutant := gomutants.Mutant{
		ID: "mutant-b", DisplayID: "arithmetic#2", Accepted: true, Rule: "arithmetic",
		Path: "value.go", Line: 13, Column: 3, Package: "fixture.example/module",
	}
	sink, recorder := newTraceRecording()
	session := &mutationUnitSession{catalog: gomutants.Catalog{Mutants: []gomutants.Mutant{mutant}}}
	targets := []TargetEvidence{
		blockTarget("TestEarly", 3*time.Millisecond, goanalysis.CoverageBlock{StartLine: 7, StartColumn: 2, EndLine: 9, EndColumn: 3}),
	}
	evaluation, err := EvaluateMutations(t.Context(), session, targets, MutationOptions{
		Root: t.TempDir(), Contract: "standard-v1", Trace: recorder, Instrumented: blockRoutingInstrumentation(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(evaluation.Findings) != 1 || evaluation.Findings[0].Kind != "unreached-mutant" {
		t.Fatalf("evaluation = %+v", evaluation)
	}
	want := trace.RouteRecord{
		MutantID: "mutant-b", Rule: "arithmetic", Path: "value.go", Line: 13, Column: 3,
		Plan: []string{"package-suite"}, Reason: trace.ReasonUnreached,
		Granularity: trace.GranularityBlock, FileCandidates: 1,
	}
	if routes := recordedRoutes(sink); len(routes) != 1 || !reflect.DeepEqual(routes[0], want) {
		t.Fatalf("routes = %+v, want [%+v]", routes, want)
	}
	if len(session.requests) != 1 || len(session.requests[0].Args) != 0 {
		t.Fatalf("executions = %+v, want one package suite run", session.requests)
	}
}
