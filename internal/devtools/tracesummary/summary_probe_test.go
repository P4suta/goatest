// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"slices"
	"strings"
	"testing"

	"github.com/P4suta/goatest/internal/trace"
)

func probeEvent(target, outcome string, infected ...string) trace.Event {
	return trace.Event{Type: trace.TypeProbeExec, Probe: &trace.ProbeRecord{
		Target:   target,
		Outcome:  outcome,
		Infected: infected,
	}}
}

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

func TestProbeBlockExcludesMutationControlsAndReportsExactOriginalPreflightsApart(t *testing.T) {
	t.Parallel()
	control := probeEvent(trace.MutationControlProbePrefix+"example.com/app", trace.ProbeOutcomeMeasured)
	control.Probe.Package, control.Probe.Control, control.Probe.DurationMS = "example.com/app", true, mutationControlDurationMS
	timedOut := probeEvent(trace.MutationControlProbePrefix+"example.com/lib", trace.ProbeOutcomeTimedOut)
	timedOut.Probe.Package, timedOut.Probe.Control, timedOut.Probe.DurationMS = "example.com/lib", true, timedOutControlDurationMS
	probe := probeEvent("target-a", trace.ProbeOutcomeMeasured, "m-0001")
	events := []trace.Event{control, probe, timedOut}

	if lines := strings.Join(probeBlock(events), "\n"); !strings.Contains(lines, "probe: 1 execution across 1 target") || strings.Contains(lines, trace.MutationControlProbePrefix) {
		t.Fatalf("infection probe block mixed in exact original preflights:\n%s", lines)
	}
	want := []string{
		"exact original preflights: 2 executions in 3.5s",
		"outcomes: measured 1, test-failed 0, timed-out 1, unavailable 0, error 0",
	}
	if got := controlBlock(events); !slices.Equal(got, want) {
		t.Fatalf("control block = %q, want %q", got, want)
	}
}

func TestProbeBlockNamesARecordingWithoutProbes(t *testing.T) {
	t.Parallel()

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

	before := strings.Join(routingBlock([]trace.Event{unprobed}), "\n")
	if strings.Contains(before, "probed") {
		t.Errorf("the block counts probes no route recorded:\n%s", before)
	}

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

func wholeTreeSuiteEvent(pkg, reason string) trace.Event {
	event := probeEvent("package-suite:"+pkg, trace.ProbeOutcomeMeasured)
	event.Probe.Package, event.Probe.Suite = pkg, true
	event.Probe.WholeTree, event.Probe.WholeTreeReason = reason != "", reason
	return event
}

func TestProbeBlockNamesWhyEachPackageSuiteWidenedItsKey(t *testing.T) {
	t.Parallel()
	lines := strings.Join(probeBlock([]trace.Event{
		wholeTreeSuiteEvent("example.com/app", trace.WholeTreeStaticUnobservable),
		wholeTreeSuiteEvent("example.com/lib", trace.WholeTreeOutsideInput),
		wholeTreeSuiteEvent("example.com/tool", trace.WholeTreeStaticUnobservable),
		wholeTreeSuiteEvent("example.com/quiet", ""),
	}), "\n")
	want := "whole-tree keys: 0 of 0 targets, 3 of 4 package suites; " +
		"static-unobservable 2, log-unavailable 0, log-ambiguous 0, directory-access 0, outside-input 1, unstated 0"
	if !strings.Contains(lines, want) {
		t.Fatalf("the probe block does not carry %q:\n%s", want, lines)
	}
}

func TestProbeBlockCountsWidenedTargetsBesideWidenedSuites(t *testing.T) {
	t.Parallel()
	widened := probeEvent("target-a", trace.ProbeOutcomeMeasured)
	widened.Probe.WholeTree, widened.Probe.WholeTreeReason = true, trace.WholeTreeDirectoryAccess
	lines := strings.Join(probeBlock([]trace.Event{
		widened,
		probeEvent("target-b", trace.ProbeOutcomeMeasured),
		wholeTreeSuiteEvent("example.com/app", trace.WholeTreeStaticUnobservable),
	}), "\n")
	want := "whole-tree keys: 1 of 2 targets, 1 of 1 package suites; " +
		"static-unobservable 1, log-unavailable 0, log-ambiguous 0, directory-access 1, outside-input 0, unstated 0"
	if !strings.Contains(lines, want) {
		t.Fatalf("the probe block does not carry %q:\n%s", want, lines)
	}
}

func TestProbeBlockOmitsWholeTreeKeysWhenEveryObservationStayedNarrow(t *testing.T) {
	t.Parallel()
	lines := strings.Join(probeBlock([]trace.Event{
		wholeTreeSuiteEvent("example.com/app", ""),
		probeEvent("target-a", trace.ProbeOutcomeMeasured),
	}), "\n")
	if strings.Contains(lines, "whole-tree keys") {
		t.Fatalf("the probe block carries an empty whole-tree line:\n%s", lines)
	}
}

func TestProbeBlockCountsAWideningThatNamedNoReason(t *testing.T) {
	t.Parallel()
	silent := probeEvent("package-suite:example.com/app", trace.ProbeOutcomeMeasured)
	silent.Probe.Package, silent.Probe.Suite = "example.com/app", true
	silent.Probe.WholeTree = true
	lines := strings.Join(probeBlock([]trace.Event{
		silent,
		wholeTreeSuiteEvent("example.com/lib", trace.WholeTreeOutsideInput),
	}), "\n")
	want := "whole-tree keys: 0 of 0 targets, 2 of 2 package suites; " +
		"static-unobservable 0, log-unavailable 0, log-ambiguous 0, directory-access 0, outside-input 1, unstated 1"
	if !strings.Contains(lines, want) {
		t.Fatalf("the probe block does not carry %q:\n%s", want, lines)
	}
}
