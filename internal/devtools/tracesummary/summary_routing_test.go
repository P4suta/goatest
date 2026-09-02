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

// routeEvent is one route of a synthetic recording. The reaching targets are
// generated rather than named, because the routing block reads how many there
// were and never which ones.
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

// dischargedRouteEvent is one route a proof removed targets from. The
// discharged targets are named rather than generated, because a discharged
// target is one the route no longer reaches and must not collide with the
// reaching ones.
func dischargedRouteEvent(mutantID string, reaching int, targets ...string) trace.Event {
	event := routeEvent(mutantID, reaching, trace.ReasonCoverageReaching, trace.GranularityBlock, "", reaching+len(targets))
	discharged := make([]trace.Discharge, 0, len(targets))
	for _, target := range targets {
		discharged = append(discharged, trace.Discharge{Target: target, Reason: trace.DischargeBranchNeverTaken})
	}
	event.Route.Discharged = discharged
	return event
}

// countOf is what one label of a fixed-order tally counted, and -1 when the
// tally does not carry the label at all.
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
		routeEvent("mutant-e", 5, trace.ReasonCoverageReaching, "", "", 0),
		{Type: trace.TypeRoute, Route: nil},
		{Type: trace.TypeMutantExec, Mutant: &trace.MutantRecord{ID: "mutant-a"}},
	}
	totals := routeTotals(events)
	if totals.routes != 6 {
		t.Errorf("routes = %d, want the 6 routes that carried a payload", totals.routes)
	}
	if totals.mutants != 5 {
		t.Errorf("mutants = %d, want the 5 identities the routes name", totals.mutants)
	}
	if got := countOf(totals.reasons, trace.ReasonCoverageReaching); got != 5 {
		t.Errorf("coverage-reaching routes = %d, want 5", got)
	}
	if got := countOf(totals.reasons, trace.ReasonUnreached); got != 1 {
		t.Errorf("unreached routes = %d, want 1", got)
	}
	if got := countOf(totals.granularities, trace.GranularityBlock); got != 3 {
		t.Errorf("block routes = %d, want 3", got)
	}
	if got := countOf(totals.granularities, trace.GranularityFile); got != 2 {
		t.Errorf("file routes = %d, want 2", got)
	}
	if got := countOf(totals.granularities, unrecorded); got != 1 {
		t.Errorf("routes without a granularity = %d, want 1", got)
	}
	if got := countOf(totals.fallbacks, trace.FallbackPositionUnknown); got != 1 {
		t.Errorf("position-unknown fallbacks = %d, want 1", got)
	}
	if got := countOf(totals.fallbacks, trace.FallbackOutsideBlocks); got != 1 {
		t.Errorf("outside-blocks fallbacks = %d, want 1", got)
	}
	// 0, 1, 2-3 twice (3 and 2), 4-7 (5) and 8-15 (9).
	want := []int{1, 1, 2, 1, 1, 0, 0, 0}
	if len(totals.fanOut) != len(want) {
		t.Fatalf("fan-out has %d buckets, want %d", len(totals.fanOut), len(want))
	}
	for index, count := range want {
		if totals.fanOut[index] != count {
			t.Errorf("bucket %q holds %d routes, want %d", fanOutBucketLabels()[index], totals.fanOut[index], count)
		}
	}
	if totals.reaching != 20 {
		t.Errorf("reaching targets = %d, want 20", totals.reaching)
	}
	if totals.candidates != 21 {
		t.Errorf("file candidates = %d, want 21", totals.candidates)
	}
}

func TestRoutingBlockReportsUnrecordedGranularityAndReduction(t *testing.T) {
	t.Parallel()
	// A recording made before the routing fields existed carries none of
	// them, so the block says so rather than claiming a reduction of nothing.
	older := strings.Join(routingBlock([]trace.Event{
		routeEvent("mutant-a", 2, trace.ReasonCoverageReaching, "", "", 0),
		routeEvent("mutant-b", 0, trace.ReasonUnreached, "", "", 0),
	}), "\n")
	for _, want := range []string{
		"routes: 2 across 2 mutants",
		"reasons: coverage-reaching 1, unreached 1",
		"granularity: block 0, file 0, (unrecorded) 2",
		"reduction: not recorded",
		"reaching targets per route",
	} {
		if !strings.Contains(older, want) {
			t.Errorf("the block of an older recording does not carry %q:\n%s", want, older)
		}
	}
	if strings.Contains(older, "fallbacks:") {
		t.Errorf("the block names fallbacks no route took:\n%s", older)
	}

	recent := strings.Join(routingBlock([]trace.Event{
		routeEvent("mutant-a", 2, trace.ReasonCoverageReaching, trace.GranularityBlock, "", 8),
		routeEvent("mutant-b", 2, trace.ReasonCoverageReaching, trace.GranularityFile, trace.FallbackOutsideBlocks, 2),
	}), "\n")
	for _, want := range []string{
		"granularity: block 1, file 1, (unrecorded) 0",
		"fallbacks: position-unknown 0, outside-blocks 1",
		"reduction: file candidates 10 -> reaching 4 (60.0% fewer)",
	} {
		if !strings.Contains(recent, want) {
			t.Errorf("the block of a recent recording does not carry %q:\n%s", want, recent)
		}
	}
	// Only the buckets a route landed in are printed, so the table stays the
	// shape of the recording rather than of the histogram.
	if strings.Contains(recent, "64+") {
		t.Errorf("the block prints a bucket no route landed in:\n%s", recent)
	}
}

func TestRoutingBlockReadsGranularityAsTheMarkerOfRecordedRoutingMetadata(t *testing.T) {
	t.Parallel()
	// A route that names no granularity recorded no routing metadata, so
	// whatever its other fields decode to is not a measurement of anything.
	older := strings.Join(routingBlock([]trace.Event{
		routeEvent("mutant-a", 2, trace.ReasonCoverageReaching, "", "", 6),
		routeEvent("mutant-b", 0, trace.ReasonUnreached, "", "", 3),
	}), "\n")
	if want := "reduction: not recorded"; !strings.Contains(older, want) {
		t.Errorf("the block of a recording without granularity does not carry %q:\n%s", want, older)
	}
	// A recording holding both kinds of route reduces the routes that
	// recorded their routing over the candidates those same routes recorded,
	// so a route that recorded nothing cannot move the totals.
	mixed := strings.Join(routingBlock([]trace.Event{
		routeEvent("mutant-a", 1, trace.ReasonCoverageReaching, trace.GranularityBlock, "", 4),
		routeEvent("mutant-b", 3, trace.ReasonCoverageReaching, "", "", 7),
	}), "\n")
	if want := "reduction: file candidates 4 -> reaching 1 (75.0% fewer)"; !strings.Contains(mixed, want) {
		t.Errorf("the block of a mixed recording does not carry %q:\n%s", want, mixed)
	}
}

func TestRoutingBlockReportsAZeroCandidateCountAsAMeasuredReduction(t *testing.T) {
	t.Parallel()
	// A file no test binary was ever linked against has no candidate at all.
	// The route recorded its granularity, so the zero is a count the routing
	// took rather than a field the recording never carried.
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
		dischargedRouteEvent("mutant-a", 2, "TestSkipped", "TestOther"),
		dischargedRouteEvent("mutant-b", 1, "TestSkipped"),
		routeEvent("mutant-c", 3, trace.ReasonCoverageReaching, trace.GranularityBlock, "", 3),
	})
	if got := countOf(totals.discharges, trace.DischargeBranchNeverTaken); got != 3 {
		t.Errorf("branch-never-taken discharges = %d, want the 3 targets the proofs removed", got)
	}
	if totals.dischargedRoutes != 2 {
		t.Errorf("routes carrying a discharge = %d, want 2", totals.dischargedRoutes)
	}
	// A discharged target is one the route no longer reaches, so it is counted
	// beside the reaching set rather than inside it.
	if totals.reaching != 6 {
		t.Errorf("reaching targets = %d, want the 6 the routes still reach", totals.reaching)
	}
}

func TestRoutingBlockReportsDischargedTargetsAndRoutes(t *testing.T) {
	t.Parallel()
	lines := strings.Join(routingBlock([]trace.Event{
		dischargedRouteEvent("mutant-a", 2, "TestSkipped", "TestOther"),
		dischargedRouteEvent("mutant-b", 1, "TestSkipped"),
	}), "\n")
	if want := "discharged: 3 targets across 2 routes (branch-never-taken 3)"; !strings.Contains(lines, want) {
		t.Errorf("the block does not carry %q:\n%s", want, lines)
	}
}

func TestRoutingBlockOmitsTheDischargeLineWhenNoneWereRecorded(t *testing.T) {
	t.Parallel()
	// A recording made before any proof discharged a target renders as it did
	// then, so the line is absent rather than a tally of zeroes.
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
	// The mutant a repair round revisits is disposed of by what became of it
	// last, and the executions that reached the earlier outcome were still
	// paid for, so they are charged to the disposition rather than dropped.
	events := []trace.Event{
		mutantExecEvent("mutant-a", 10, "killed"),
		mutantExecEvent("mutant-a", 30, "survived"),
		mutantExecEvent("mutant-b", 5, "killed"),
		{Type: trace.TypeMutantExec, Mutant: nil},
		{Type: trace.TypeExec, Exec: &trace.ExecRecord{}},
	}
	totals := dispositionTotals(events)
	if len(totals) != 2 {
		t.Fatalf("dispositions = %+v, want survived and killed", totals)
	}
	if totals[0].disposition != "survived" || totals[0].mutants != 1 || totals[0].executions != 2 || totals[0].duration != 40 {
		t.Errorf("first disposition = %+v, want survived with 1 mutant, 2 executions and 40ms", totals[0])
	}
	if totals[1].disposition != "killed" || totals[1].mutants != 1 || totals[1].executions != 1 || totals[1].duration != 5 {
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
	if len(totals) != 3 {
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
