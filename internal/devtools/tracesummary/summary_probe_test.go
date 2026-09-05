// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"slices"
	"strings"
	"testing"

	"github.com/P4suta/goatest/internal/trace"
)

// probeEvent is one probe execution of a synthetic recording. Only a measured
// execution carries the mutants it infected, so the outcome and the infections
// are given together.
func probeEvent(target, outcome string, infected ...string) trace.Event {
	return trace.Event{Type: trace.TypeProbeExec, Probe: &trace.ProbeRecord{
		Target:   target,
		Outcome:  outcome,
		Infected: infected,
	}}
}

// erroredProbeEvent is a probe execution that failed before it reached an
// outcome, which is the one execution that carries an error instead of one.
func erroredProbeEvent(target, failure string) trace.Event {
	return trace.Event{Type: trace.TypeProbeExec, Probe: &trace.ProbeRecord{
		Target:   target,
		ExitCode: -1,
		Error:    failure,
	}}
}

func TestProbeBlockCountsExecutionsOutcomesAndInfections(t *testing.T) {
	t.Parallel()
	lines := strings.Join(probeBlock([]trace.Event{
		probeEvent("target-a", trace.ProbeOutcomeMeasured, "m-0001", "m-0002"),
		probeEvent("target-b", trace.ProbeOutcomeMeasured, "m-0002"),
		probeEvent("target-c", trace.ProbeOutcomeMeasured),
		probeEvent("target-d", trace.ProbeOutcomeTestFailed),
		erroredProbeEvent("target-e", "goatest: probe tree unavailable"),
		{Type: trace.TypeProbeExec, Probe: nil},
		{Type: trace.TypeMutantExec, Mutant: &trace.MutantRecord{ID: "m-0001"}},
	}), "\n")
	for _, want := range []string{
		"probe: 5 executions across 5 targets",
		"outcomes: measured 3, test-failed 1, timed-out 0, unavailable 0, error 1",
		// A mutant two targets infected is one mutant and two pairs, and a
		// measured target that infected nothing is the reduction the pass buys.
		"infections: 3 (target, mutant) pairs across 2 mutants; 1 measured target infected nothing",
	} {
		if !strings.Contains(lines, want) {
			t.Errorf("the probe block does not carry %q:\n%s", want, lines)
		}
	}
}

func TestProbeBlockCountsPackageSuitesApartFromTargets(t *testing.T) {
	t.Parallel()
	suite := probeEvent("package-suite:example.com/app", trace.ProbeOutcomeMeasured, "m-0001")
	suite.Probe.Package, suite.Probe.Suite = "example.com/app", true
	barrenSuite := probeEvent("package-suite:example.com/lib", trace.ProbeOutcomeMeasured)
	barrenSuite.Probe.Package, barrenSuite.Probe.Suite = "example.com/lib", true
	lines := strings.Join(probeBlock([]trace.Event{
		probeEvent("target-a", trace.ProbeOutcomeMeasured, "m-0001"), suite, barrenSuite,
	}), "\n")
	for _, want := range []string{
		"probe: 3 executions across 1 target and 2 package suites",
		"infections: 2 (probe, mutant) pairs across 1 mutant; 0 measured targets infected nothing; 1 measured package suite infected nothing",
	} {
		if !strings.Contains(lines, want) {
			t.Errorf("the probe block does not carry %q:\n%s", want, lines)
		}
	}
}

func TestProbeBlockNamesARecordingWithoutProbes(t *testing.T) {
	t.Parallel()
	// A recording made before the probe pass existed carries no probe event,
	// and the block says the pass was not recorded rather than reading the
	// absence as a pass that infected nothing.
	lines := probeBlock([]trace.Event{
		{Type: trace.TypeRunStart},
		{Type: trace.TypeProbeExec, Probe: nil},
	})
	if !slices.Equal(lines, []string{"probe: not recorded"}) {
		t.Fatalf("block without probes = %q", lines)
	}
}

func TestProbeBlockCountsAnErroredExecutionAsError(t *testing.T) {
	t.Parallel()
	// An execution that failed says nothing about any mutant, and neither does
	// one whose tests failed, so both are counted and neither is measured.
	lines := strings.Join(probeBlock([]trace.Event{
		erroredProbeEvent("target-a", "goatest: probe tree unavailable"),
		probeEvent("target-b", trace.ProbeOutcomeTimedOut),
		probeEvent("target-c", trace.ProbeOutcomeUnavailable),
	}), "\n")
	for _, want := range []string{
		"probe: 3 executions across 3 targets",
		"outcomes: measured 0, test-failed 0, timed-out 1, unavailable 1, error 1",
		"infections: 0 (target, mutant) pairs across 0 mutants; 0 measured targets infected nothing",
	} {
		if !strings.Contains(lines, want) {
			t.Errorf("the probe block does not carry %q:\n%s", want, lines)
		}
	}
}

func TestRoutingBlockCountsProbedRoutesOnlyWhenRecorded(t *testing.T) {
	t.Parallel()
	probed := routeEvent("mutant-a", 2, trace.ReasonCoverageReaching, trace.GranularityBlock, "", 4)
	probed.Route.Probed = true
	unprobed := routeEvent("mutant-b", 1, trace.ReasonCoverageReaching, trace.GranularityBlock, "", 2)
	lines := strings.Join(routingBlock([]trace.Event{probed, unprobed}), "\n")
	if want := "probed: 1 route"; !strings.Contains(lines, want) {
		t.Errorf("the block does not carry %q:\n%s", want, lines)
	}
	// A recording made before the probe pass carries no probed route, and
	// renders the block it rendered then rather than a count of zero.
	before := strings.Join(routingBlock([]trace.Event{unprobed}), "\n")
	if strings.Contains(before, "probed") {
		t.Errorf("the block counts probes no route recorded:\n%s", before)
	}
	// The line has one slot, whether or not a proof discharged anything: after
	// the discharged line when there is one, and before the reduction either
	// way, so that two recordings read in the same order.
	discharging := routeEvent("mutant-c", 3, trace.ReasonCoverageReaching, trace.GranularityBlock, "", 5)
	discharging.Route.Probed = true
	discharging.Route.Discharged = []trace.Discharge{{Target: "target-z", Reason: trace.DischargeBranchNeverTaken}}
	for name, events := range map[string][]trace.Event{
		"without a discharge": {probed, unprobed},
		"with a discharge":    {probed, unprobed, discharging},
	} {
		block := routingBlock(events)
		probedAt := slices.IndexFunc(block, func(line string) bool { return strings.HasPrefix(line, "probed: ") })
		reductionAt := slices.IndexFunc(block, func(line string) bool { return strings.HasPrefix(line, "reduction: ") })
		dischargedAt := slices.IndexFunc(block, func(line string) bool { return strings.HasPrefix(line, "discharged: ") })
		if probedAt < 0 || reductionAt < 0 || probedAt != reductionAt-1 {
			t.Errorf("%s: the probed line is not the line before the reduction:\n%s", name, strings.Join(block, "\n"))
		}
		if dischargedAt >= 0 && dischargedAt != probedAt-1 {
			t.Errorf("%s: the probed line does not follow the discharged line:\n%s", name, strings.Join(block, "\n"))
		}
	}
}

func TestProbeBlockIsDeterministic(t *testing.T) {
	t.Parallel()
	// The block reads the events alone, so the same executions render the same
	// bytes whichever order the recorder serialised them in.
	first := []trace.Event{
		probeEvent("target-a", trace.ProbeOutcomeMeasured, "m-0001", "m-0002"),
		probeEvent("target-b", trace.ProbeOutcomeTestFailed),
		probeEvent("target-c", trace.ProbeOutcomeMeasured, "m-0002"),
	}
	second := []trace.Event{first[2], first[0], first[1]}
	if !slices.Equal(probeBlock(first), probeBlock(second)) {
		t.Fatalf("two orders of the same executions render differently:\n%s\n%s",
			strings.Join(probeBlock(first), "\n"), strings.Join(probeBlock(second), "\n"))
	}
}
