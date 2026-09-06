// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/P4suta/goatest/internal/trace"
)

func routeEvent(mutantID string, reaching int, reason, granularity, fallback string, candidates int) trace.Event {
	targets := make([]string, 0, reaching)
	for index := range reaching {
		targets = append(targets, fmt.Sprintf("target-%02d", index))
	}
	return trace.Event{Type: trace.TypeRoute, Route: &trace.RouteRecord{
		MutantID:        mutantID,
		Path:            "internal/assure/plan.go",
		ReachingTargets: targets,
		Reason:          reason,
		Granularity:     granularity,
		Fallback:        fallback,
		FileCandidates:  candidates,
	}}
}

func dischargedRouteEvent(mutantID string, reaching int, discharged ...trace.Discharge) trace.Event {
	event := routeEvent(mutantID, reaching, trace.ReasonCoverageReaching, trace.GranularityBlock, "", reaching+len(discharged))
	event.Route.Discharged = discharged
	return event
}

func branchDischarge(target string) trace.Discharge {
	return trace.Discharge{Target: target, Reason: trace.DischargeBranchNeverTaken}
}

func infectionDischarge(target string) trace.Discharge {
	return trace.Discharge{Target: target, Reason: trace.DischargeNeverInfected}
}

func countOf(counts []labelCount, label string) int {
	for _, count := range counts {
		if count.label == label {
			return count.count
		}
	}
	return -1
}

func TestRouteTotalsBucketFanOutAndCountReasonsGranularitiesAndFallbacks(t *testing.T) {
	t.Parallel()
	events := []trace.Event{
		routeEvent("mutant-a", 1, trace.ReasonCoverageReaching, trace.GranularityBlock, "", 4),
		routeEvent("mutant-a", 3, trace.ReasonCoverageReaching, trace.GranularityBlock, "", 4),
		routeEvent("mutant-b", 9, trace.ReasonCoverageReaching, trace.GranularityFile, trace.FallbackPositionUnknown, 9),
		routeEvent("mutant-c", 0, trace.ReasonUnreached, trace.GranularityBlock, "", 2),
		routeEvent("mutant-d", 2, trace.ReasonCoverageReaching, trace.GranularityFile, trace.FallbackOutsideBlocks, 2),
		routeEvent("mutant-e", 5, trace.ReasonCoverageReaching, trace.GranularityBlock, "", 0),
		{Type: trace.TypeRoute, Route: nil},
		{Type: trace.TypeMutantExec, Mutant: &trace.MutantRecord{ID: "mutant-a"}},
	}
	totals := routeTotals(events)
	if totals.routes != routePayloadCount {
		t.Errorf("routes = %d, want the 6 routes that carried a payload", totals.routes)
	}
	if totals.mutants != routeMutantCount {
		t.Errorf("mutants = %d, want the 5 identities the routes name", totals.mutants)
	}
	if got := countOf(totals.reasons, trace.ReasonCoverageReaching); got != coverageReachingRouteCount {
		t.Errorf("coverage-reaching routes = %d, want 5", got)
	}
	if got := countOf(totals.reasons, trace.ReasonUnreached); got != 1 {
		t.Errorf("unreached routes = %d, want 1", got)
	}
	if got := countOf(totals.granularities, trace.GranularityBlock); got != blockRouteCount {
		t.Errorf("block routes = %d, want 3", got)
	}
	if got := countOf(totals.granularities, trace.GranularityFile); got != fileRouteCount {
		t.Errorf("file routes = %d, want 2", got)
	}
	if got := countOf(totals.fallbacks, trace.FallbackPositionUnknown); got != 1 {
		t.Errorf("position-unknown fallbacks = %d, want 1", got)
	}
	if got := countOf(totals.fallbacks, trace.FallbackOutsideBlocks); got != 1 {
		t.Errorf("outside-blocks fallbacks = %d, want 1", got)
	}

	want := []int{1, 1, 2, 1, 1, 0, 0, 0}
	if len(totals.fanOut) != len(want) {
		t.Fatalf("fan-out has %d buckets, want %d", len(totals.fanOut), len(want))
	}
	for index, count := range want {
		if totals.fanOut[index] != count {
			t.Errorf("bucket %q holds %d routes, want %d", fanOutBucketLabels()[index], totals.fanOut[index], count)
		}
	}
	if totals.reaching != routeReachingTargetCount {
		t.Errorf("reaching targets = %d, want 20", totals.reaching)
	}
	if totals.candidates != routeFileCandidateCount {
		t.Errorf("file candidates = %d, want 21", totals.candidates)
	}
}

func TestRoutingBlockReportsGranularityAndReduction(t *testing.T) {
	t.Parallel()

	lines := strings.Join(routingBlock([]trace.Event{
		routeEvent("mutant-a", 2, trace.ReasonCoverageReaching, trace.GranularityBlock, "", 8),
		routeEvent("mutant-b", 2, trace.ReasonCoverageReaching, trace.GranularityFile, trace.FallbackOutsideBlocks, 2),
	}), "\n")
	for _, want := range []string{
		"granularity: block 1, file 1",
		"fallbacks: position-unknown 0, outside-blocks 1",
		"reduction: file candidates 10 -> reaching 4 (60.0% fewer)",
	} {
		if !strings.Contains(lines, want) {
			t.Errorf("the block does not carry %q:\n%s", want, lines)
		}
	}

	if strings.Contains(lines, "64+") {
		t.Errorf("the block prints a bucket no route landed in:\n%s", lines)
	}
}

func TestRoutingBlockReportsAZeroCandidateCountAsAMeasuredReduction(t *testing.T) {
	t.Parallel()

	lines := strings.Join(routingBlock([]trace.Event{
		routeEvent("mutant-a", 0, trace.ReasonUnreached, trace.GranularityFile, trace.FallbackOutsideBlocks, 0),
	}), "\n")
	if want := "reduction: file candidates 0 -> reaching 0 (nothing to reduce)"; !strings.Contains(lines, want) {
		t.Errorf("the block of a route without candidates does not carry %q:\n%s", want, lines)
	}
}

func TestRouteTotalsCountDischargesByReason(t *testing.T) {
	t.Parallel()
	totals := routeTotals([]trace.Event{
		dischargedRouteEvent("mutant-a", 2, branchDischarge("TestSkipped"), infectionDischarge("TestOther")),
		dischargedRouteEvent("mutant-b", 1, branchDischarge("TestSkipped")),
		routeEvent("mutant-c", 3, trace.ReasonCoverageReaching, trace.GranularityBlock, "", 3),
	})
	if got := countOf(totals.discharges, trace.DischargeBranchNeverTaken); got != branchDischargeCount {
		t.Errorf("branch-never-taken discharges = %d, want the 2 targets that proof removed", got)
	}
	if got := countOf(totals.discharges, trace.DischargeNeverInfected); got != 1 {
		t.Errorf("never-infected discharges = %d, want the 1 target the probe facts removed", got)
	}
	if totals.dischargedRoutes != dischargedRouteCount {
		t.Errorf("routes carrying a discharge = %d, want 2", totals.dischargedRoutes)
	}

	if totals.reaching != dischargedReachingCount {
		t.Errorf("reaching targets = %d, want the 6 the routes still reach", totals.reaching)
	}
}

func TestRoutingBlockReportsDischargedTargetsAndRoutes(t *testing.T) {
	t.Parallel()
	lines := strings.Join(routingBlock([]trace.Event{
		dischargedRouteEvent("mutant-a", 2, branchDischarge("TestSkipped"), infectionDischarge("TestOther")),
		dischargedRouteEvent("mutant-b", 1, branchDischarge("TestSkipped")),
	}), "\n")

	if want := "discharged: 3 targets across 2 routes (branch-never-taken 2, never-infected 1)"; !strings.Contains(lines, want) {
		t.Errorf("the block does not carry %q:\n%s", want, lines)
	}
}

func TestRoutingBlockReportsWholeSuiteCoverageDecisions(t *testing.T) {
	t.Parallel()
	unreached := routeEvent("mutant-a", 0, trace.ReasonUnreached, trace.GranularityBlock, "", 2)
	unreached.Route.SuiteCoverage = "package-suite-coverage:example.com/app"
	reached := routeEvent("mutant-b", 0, trace.ReasonUnreached, trace.GranularityBlock, "", 2)
	reached.Route.SuiteCoverage = "package-suite-coverage:example.com/app"
	reached.Route.SuiteReached = true
	lines := strings.Join(routingBlock([]trace.Event{unreached, reached}), "\n")
	if want := "suite coverage: 2 routes (1 reached, 1 unreached)"; !strings.Contains(lines, want) {
		t.Errorf("the block does not carry %q:\n%s", want, lines)
	}
}

func TestRoutingBlockOmitsTheDischargeLineWhenNoneWereRecorded(t *testing.T) {
	t.Parallel()

	lines := strings.Join(routingBlock([]trace.Event{
		routeEvent("mutant-a", 2, trace.ReasonCoverageReaching, trace.GranularityBlock, "", 4),
		routeEvent("mutant-b", 0, trace.ReasonUnreached, "", "", 0),
	}), "\n")
	if strings.Contains(lines, "discharged") {
		t.Errorf("the block names discharges no route recorded:\n%s", lines)
	}
}

func TestRoutingBlockNamesARecordingWithoutRoutes(t *testing.T) {
	t.Parallel()
	lines := routingBlock([]trace.Event{{Type: trace.TypeRunStart}, {Type: trace.TypeRoute, Route: nil}})
	if len(lines) != 2 || !strings.Contains(lines[1], "no mutant was routed") {
		t.Fatalf("block without routes = %q", lines)
	}
}

func TestFanOutBucketIndexCoversTheBoundaries(t *testing.T) {
	t.Parallel()
	cases := []struct {
		reaching int
		want     int
	}{
		{0, 0}, {1, 1}, {2, 2}, {3, 2}, {4, 3}, {7, 3}, {8, 4}, {15, 4},
		{16, 5}, {31, 5}, {32, 6}, {63, 6}, {64, 7}, {1000, 7},
	}
	labels := fanOutBucketLabels()
	for _, testCase := range cases {
		if got := fanOutBucketIndex(testCase.reaching); got != testCase.want {
			t.Errorf("fanOutBucketIndex(%d) = %d (%q), want %d (%q)",
				testCase.reaching, got, labels[got], testCase.want, labels[testCase.want])
		}
	}
}

func TestDispositionTotalsChargeEveryExecutionToTheMutantsLastOutcome(t *testing.T) {
	t.Parallel()

	events := []trace.Event{
		mutantExecEvent("mutant-b", 5, "killed"),
		mutantExecEvent("mutant-a", 10, "killed"),
		mutantExecEvent("mutant-a", 30, "survived"),
		{Type: trace.TypeMutantExec, Mutant: nil},
		{Type: trace.TypeExec, Exec: &trace.ExecRecord{}},
	}
	totals := dispositionTotals(events)
	want := []string{"survived", "killed"}
	if len(totals) != len(want) {
		t.Fatalf("dispositions = %+v, want survived and killed", totals)
	}
	if totals[0].disposition != want[0] || totals[0].mutants != 1 || totals[0].executions != dischargedRouteCount || totals[0].duration != 40 {
		t.Errorf("first disposition = %+v, want survived with 1 mutant, 2 executions and 40ms", totals[0])
	}
	if totals[1].disposition != want[1] || totals[1].mutants != 1 || totals[1].executions != 1 || totals[1].duration != 5 {
		t.Errorf("second disposition = %+v, want killed with 1 mutant, 1 execution and 5ms", totals[1])
	}
}

func TestDispositionTotalsOrderTiesDeterministically(t *testing.T) {
	t.Parallel()
	totals := dispositionTotals([]trace.Event{
		mutantExecEvent("mutant-a", 3, "survived"),
		mutantExecEvent("mutant-b", 3, "killed"),
		mutantExecEvent("mutant-c", 3, ""),
	})
	if len(totals) != tiedDispositionCount {
		t.Fatalf("dispositions = %+v, want three", totals)
	}
	got := []string{totals[0].disposition, totals[1].disposition, totals[2].disposition}
	want := []string{noOutcome, "killed", "survived"}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("tied dispositions = %q, want %q; the name is the last resort order", got, want)
		}
	}
}

func TestMutantBlockChargesEveryExecutionToADisposition(t *testing.T) {
	t.Parallel()
	lines := mutantBlock([]trace.Event{
		mutantExecEvent("mutant-a", 10, "killed"),
		mutantExecEvent("mutant-a", 30, "survived"),
	})
	heading := slices.Index(lines, "executions by final disposition")
	if heading < 0 || heading+2 >= len(lines) {
		t.Fatalf("the mutant block carries no disposition table:\n%s", strings.Join(lines, "\n"))
	}
	if !strings.HasPrefix(lines[heading+1], "disposition") {
		t.Errorf("the disposition table opens with %q, want its column headings", lines[heading+1])
	}
	if !strings.HasPrefix(lines[heading+2], "survived") || !strings.Contains(lines[heading+2], "40ms") {
		t.Errorf("the disposition row = %q, want the two executions charged to survived", lines[heading+2])
	}
	if heading+3 < len(lines) && lines[heading+3] != "" {
		t.Errorf("the table carries a second row %q; a revisited mutant is disposed of once", lines[heading+3])
	}
}

func reusedRouteEvent(mutantID string, reaching int) trace.Event {
	event := routeEvent(mutantID, reaching, trace.ReasonCoverageReaching, trace.GranularityBlock, "", reaching)
	event.Route.Plan, event.Route.Reused = []string{"reused"}, true
	return event
}

func TestRoutingBlockCountsReusedRoutes(t *testing.T) {
	t.Parallel()
	lines := strings.Join(routingBlock([]trace.Event{
		reusedRouteEvent("mutant-a", 2),
		reusedRouteEvent("mutant-b", 1),
		routeEvent("mutant-c", 2, trace.ReasonCoverageReaching, trace.GranularityBlock, "", 4),
	}), "\n")
	if want := "reused: 2 routes of 3"; !strings.Contains(lines, want) {
		t.Errorf("the block does not carry %q:\n%s", want, lines)
	}
}

func TestRoutingBlockOmitsTheReuseLineWhenNothingWasReused(t *testing.T) {
	t.Parallel()
	lines := strings.Join(routingBlock([]trace.Event{
		routeEvent("mutant-a", 2, trace.ReasonCoverageReaching, trace.GranularityBlock, "", 4),
	}), "\n")
	if strings.Contains(lines, "reused") {
		t.Errorf("the block names a reuse no route recorded:\n%s", lines)
	}
}
