// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"slices"
	"testing"

	"github.com/P4suta/goatest/internal/trace"
)

// The fixtures below are the half of a recording the infection layer reads:
// what the probe pass measured of each target, written with the trace types
// themselves so a change to the contract reaches these tests through the
// compiler.

// probeEvent is one recorded probe execution.
func probeEvent(seq int64, record trace.ProbeRecord) trace.Event {
	return trace.Event{Seq: seq, Type: trace.TypeProbeExec, Timestamp: fixtureTime, Probe: &record}
}

// probeMeasured is the probe run of a target the pass measured, naming the
// mutants whose site it saw the target make differ. A measured run is the only
// one carrying facts, and the mutants it leaves out are the ones it proves that
// target can never observe.
func probeMeasured(seq int64, target string, infected ...string) trace.Event {
	return probeEvent(seq, trace.ProbeRecord{
		Target: target, Package: fixtureModule + "/pkg",
		Args:     []string{"-test.run=^" + testNameOf(target) + "$"},
		Outcome:  trace.ProbeOutcomeMeasured,
		Infected: infected, DurationMS: 3,
	})
}

// probeOutcome is the probe run of a target the pass did not measure. Every
// outcome but measured says nothing about any mutant, so the record exists and
// carries no infections to read.
func probeOutcome(seq int64, target, outcome string) trace.Event {
	return probeEvent(seq, trace.ProbeRecord{
		Target: target, Package: fixtureModule + "/pkg",
		Args:    []string{"-test.run=^" + testNameOf(target) + "$"},
		Outcome: outcome, ExitCode: 1, DurationMS: 3,
	})
}

// probeErrored is the probe run that never reached an outcome at all: the
// record carries the error that stopped it and no outcome to read.
func probeErrored(seq int64, target string) trace.Event {
	return probeEvent(seq, trace.ProbeRecord{
		Target: target, Package: fixtureModule + "/pkg",
		Args:  []string{"-test.run=^" + testNameOf(target) + "$"},
		Error: "the probe binary could not be built",
	})
}

// probedRoute is the route of a mutant the probe pass carried a site for, which
// is what says the pass could have measured it. A route without the flag is a
// mutant nobody measured, and the layer has no facts about one.
func probedRoute(seq int64, mutant string, line, column int, targets ...string) trace.Event {
	route := blockRoute(seq, mutant, line, column, targets...)
	route.Route.Probed = true
	return route
}

// layerRow is what one named layer concluded. The position of a row moves with
// the catalog and the name does not, so the infection assertions read it by
// name.
func layerRow(t *testing.T, result auditResult, name string) layerResult {
	t.Helper()
	for _, audited := range result.layers {
		if audited.name == name {
			return audited
		}
	}
	t.Fatalf("the audit reports no %q layer among %+v", name, result.layers)
	return layerResult{}
}

// hasLayer reports whether the audit reported a named layer at all.
func hasLayer(result auditResult, name string) bool {
	return slices.ContainsFunc(result.layers, func(audited layerResult) bool { return audited.name == name })
}

func TestAuditLayersAlwaysAuditsInfectionLast(t *testing.T) {
	t.Parallel()
	// The infection layer decides by facts the recording itself carries, so
	// unlike the branch layer it needs no third input and is always in the
	// audit. The order is the order the engine applies the layers in, so the
	// report reads as the ladder a run walks.
	if got := layerNames(auditLayers(nil)); !slices.Equal(got, []string{reachLayerName, infectionLayerName}) {
		t.Errorf("without a catalog the audit runs %v, want %q after %q", got, infectionLayerName, reachLayerName)
	}
	got := layerNames(auditLayers(fixtureCatalog()))
	if !slices.Equal(got, []string{reachLayerName, branchLayerName, infectionLayerName}) {
		t.Errorf("with a catalog the audit runs %v, want the infection layer last", got)
	}
}

func TestDecideInfectionKeepsEveryKillerItHasNoFactsAgainst(t *testing.T) {
	t.Parallel()
	// The probe pass ran every target once against a tree carrying a site per
	// eligible mutant, and recorded which sites the target made differ. Exactly
	// one state of that evidence discharges a killer — the mutant was probed,
	// the killer's run was measured, and the run did not name the mutant — and
	// every other state keeps it.
	measuredWith := func(infected ...string) *probeFacts {
		facts := &probeFacts{outcome: trace.ProbeOutcomeMeasured, infected: make(map[string]struct{}, len(infected))}
		for _, mutant := range infected {
			facts.infected[mutant] = struct{}{}
		}
		return facts
	}
	cases := []struct {
		name   string
		probed bool
		probe  *probeFacts
		want   conclusion
		why    string
	}{
		{
			name:  "a mutant the probe pass carried no site for",
			probe: measuredWith(secondMutant), want: inapplicable,
		},
		{
			name:   "a killer the probe pass recorded nothing for",
			probed: true, probe: nil, want: kept,
		},
		{
			name:   "a killer whose probe run failed its test",
			probed: true, probe: &probeFacts{outcome: trace.ProbeOutcomeTestFailed}, want: kept,
		},
		{
			name:   "a killer whose probe run timed out",
			probed: true, probe: &probeFacts{outcome: trace.ProbeOutcomeTimedOut}, want: kept,
		},
		{
			name:   "a killer whose probe run was unavailable",
			probed: true, probe: &probeFacts{outcome: trace.ProbeOutcomeUnavailable}, want: kept,
		},
		{
			name:   "a killer whose probe run errored before any outcome",
			probed: true, probe: &probeFacts{}, want: kept,
		},
		{
			name:   "a killer whose probe saw the mutant infect",
			probed: true, probe: measuredWith(secondMutant, firstMutant), want: kept,
		},
		{
			name:   "a killer recorded by more than one probe run",
			probed: true, probe: &probeFacts{conflicting: true}, want: unverifiable, why: whyProbeRecordedTwice,
		},
		{
			name:   "a killer whose probe measured no infection by the mutant",
			probed: true, probe: measuredWith(secondMutant), want: discharged, why: whyNeverInfected,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			pair := killPair{
				mutant: firstMutant, path: subjectPath, target: killerTarget,
				probed: testCase.probed, probe: testCase.probe,
			}

			// The evidence is the coverage half of the run, and this layer reads
			// none of it: the pair carries every fact the rule decides by.
			got := decideInfection(pair, evidence{})
			if got.conclusion != testCase.want {
				t.Errorf("decideInfection concluded %v, want %v", got.conclusion, testCase.want)
			}
			if got.why != testCase.why {
				t.Errorf("decideInfection explained itself as %q, want %q", got.why, testCase.why)
			}
		})
	}
}

func TestAuditFailsWhenAMeasuredKillerNeverSawTheMutantInfect(t *testing.T) {
	t.Parallel()
	// The probe pass measured the killer against a tree carrying the mutant's
	// site and saw that site never differ. The rule says such a target cannot
	// kill the mutant, and the recording says it did, so one of the two is
	// wrong and the audit prints which pair to go and look at.
	recorded := recordedEvidence(t, map[string][]string{
		killerTarget: {ran(10, 2, 12, 16)},
	})
	stream := recordedRun(t, []string{killerTarget},
		probeMeasured(2, killerTarget, secondMutant),
		probedRoute(3, firstMutant, 11, 4, killerTarget),
		killedBy(4, firstMutant, firstDisplay, killerTarget),
	)

	result := auditFixture(t, stream, recorded)
	if result.pairs != 1 {
		t.Fatalf("audited %d kill pairs, want 1", result.pairs)
	}
	if len(result.violations) != 1 {
		t.Fatalf("reported %d violations, want exactly 1: %+v", len(result.violations), result.violations)
	}
	violation := result.violations[0]
	if violation.layer != infectionLayerName {
		t.Errorf("the violation names layer %q, want %q", violation.layer, infectionLayerName)
	}
	if violation.why != whyNeverInfected {
		t.Errorf("the violation explains itself as %q, want %q", violation.why, whyNeverInfected)
	}
	if violation.pair.mutant != firstMutant || violation.pair.target != killerTarget {
		t.Errorf("the violation names %s killed by %q, want %s killed by %q",
			violation.pair.mutant, violation.pair.target, firstMutant, killerTarget)
	}
	// The route is still what says where the mutant is.
	if violation.pair.path != subjectPath || violation.pair.line != 11 || violation.pair.column != 4 {
		t.Errorf("the pair places the mutant at %s:%d:%d, want the position the route recorded",
			violation.pair.path, violation.pair.line, violation.pair.column)
	}
	row := layerRow(t, result, infectionLayerName)
	if row.audited != 1 || row.violations != 1 || row.kept != 0 {
		t.Errorf("the infection layer audited %+v, want one audited pair that it drops", row)
	}
}

func TestAuditKeepsAKillerWhoseProbeSawTheMutantInfect(t *testing.T) {
	t.Parallel()
	// The pass measured the killer and saw the mutant's site differ there, so
	// the target can observe the mutation and the rule never drops it.
	recorded := recordedEvidence(t, map[string][]string{
		killerTarget: {ran(10, 2, 12, 16)},
	})
	stream := recordedRun(t, []string{killerTarget},
		probeMeasured(2, killerTarget, firstMutant, secondMutant),
		probedRoute(3, firstMutant, 11, 4, killerTarget),
		killedBy(4, firstMutant, firstDisplay, killerTarget),
	)

	result := auditFixture(t, stream, recorded)
	if len(result.violations) != 0 {
		t.Fatalf("reported %d violations of a sound layer, want none: %+v", len(result.violations), result.violations)
	}
	if len(result.unverifiable) != 0 {
		t.Fatalf("reported %d unverifiable pairs, want none: %+v", len(result.unverifiable), result.unverifiable)
	}
	row := layerRow(t, result, infectionLayerName)
	if row.audited != 1 || row.kept != 1 {
		t.Errorf("the infection layer audited %+v, want the one pair it keeps", row)
	}
}

func TestAuditKeepsAKillerTheProbePassNeverMeasured(t *testing.T) {
	t.Parallel()
	// Three ways the pass measured nothing about a killer: a probe run whose
	// test failed, one that never reached an outcome, and a fuzz target the
	// pass records nothing for at all, because fuzzing explores past the seed
	// corpus its coverage was measured from. None of the three is a fact, so
	// all three killers stay. The fourth is the killer the pass did measure and
	// never saw infect.
	recorded := recordedEvidence(t, map[string][]string{
		killerTarget: {ran(10, 2, 12, 16)},
		secondTarget: {ran(10, 2, 12, 16)},
		thirdTarget:  {ran(10, 2, 12, 16)},
		fuzzTarget:   {ran(10, 2, 12, 16)},
	})
	stream := recordedRun(t, []string{killerTarget, secondTarget, thirdTarget, fuzzTarget},
		probeOutcome(5, killerTarget, trace.ProbeOutcomeTestFailed),
		probeErrored(6, secondTarget),
		probeMeasured(7, thirdTarget),
		probedRoute(8, firstMutant, 11, 4, killerTarget),
		killedBy(9, firstMutant, firstDisplay, killerTarget),
		probedRoute(10, secondMutant, 11, 6, secondTarget),
		killedBy(11, secondMutant, secondDisplay, secondTarget),
		probedRoute(12, thirdMutant, 11, 8, fuzzTarget),
		killedBy(13, thirdMutant, thirdDisplay, fuzzTarget),
		probedRoute(14, fourthMutant, 11, 10, thirdTarget),
		killedBy(15, fourthMutant, fourthDisplay, thirdTarget),
	)

	result := auditFixture(t, stream, recorded)
	if len(result.violations) != 1 {
		t.Fatalf("reported %d violations, want the one measured killer: %+v",
			len(result.violations), result.violations)
	}
	if got := result.violations[0].pair.mutant; got != fourthMutant {
		t.Errorf("the violation names mutant %s, want %s, the one a measured probe never saw infect",
			got, fourthMutant)
	}
	row := layerRow(t, result, infectionLayerName)
	if row.audited != 4 || row.kept != 3 || row.violations != 1 {
		t.Errorf("the infection layer audited %+v, want three killers it keeps and one it drops", row)
	}
}

func TestAuditLeavesTheInfectionLayerOutWithoutAProbePass(t *testing.T) {
	t.Parallel()
	// A row of zeroes for a layer nobody checked reads exactly like a layer
	// that came out clean. Whether anybody checked is only known once the whole
	// recording was read: a run that made no probe pass records no probe-exec
	// event, and a route saying the mutant carries a site is not a pass that
	// measured one.
	recorded := recordedEvidence(t, map[string][]string{killerTarget: {ran(10, 2, 12, 16)}})
	routed := []trace.Event{
		probedRoute(3, firstMutant, 11, 4, killerTarget),
		killedBy(4, firstMutant, firstDisplay, killerTarget),
	}

	without := auditFixture(t, recordedRun(t, []string{killerTarget}, routed...), recorded)
	if without.infectionAudited {
		t.Error("a recording with no probe pass reports the infection layer as audited")
	}
	if hasLayer(without, infectionLayerName) {
		t.Errorf("the report carries an %q row for a recording with no probe pass: %+v",
			infectionLayerName, without.layers)
	}
	if (without.infection != dischargeSavings{}) {
		t.Errorf("a recording with no probe pass measured %+v, want nothing", without.infection)
	}
	// Removing the row loses nothing: every pair of such a recording is one the
	// layer keeps or does not apply to, so neither list it appends to has a row
	// of its own.
	if len(without.violations) != 0 || len(without.unverifiable) != 0 {
		t.Errorf("dropping the row lost %d violations and %d unverifiable pairs",
			len(without.violations), len(without.unverifiable))
	}

	with := auditFixture(t, recordedRun(t, []string{killerTarget},
		append([]trace.Event{probeMeasured(2, killerTarget, firstMutant)}, routed...)...), recorded)
	if !with.infectionAudited {
		t.Error("a recording holding a probe pass reports the infection layer as not audited")
	}
	row := layerRow(t, with, infectionLayerName)
	if row.audited != 1 || row.kept != 1 {
		t.Errorf("the infection layer audited %+v, want the one pair it keeps", row)
	}
}

func TestAuditReportsAProbeRecordedTwiceAsUnverifiable(t *testing.T) {
	t.Parallel()
	// One target with two probe records is a recording whose meaning the audit
	// does not know: the two runs disagree about what that target infected, and
	// believing whichever came first would decide a pair on a coin toss.
	recorded := recordedEvidence(t, map[string][]string{killerTarget: {ran(10, 2, 12, 16)}})
	stream := recordedRun(t, []string{killerTarget},
		probeMeasured(2, killerTarget, firstMutant),
		probeMeasured(3, killerTarget),
		probedRoute(4, firstMutant, 11, 4, killerTarget),
		killedBy(5, firstMutant, firstDisplay, killerTarget),
	)

	result := auditFixture(t, stream, recorded)
	if len(result.violations) != 0 {
		t.Fatalf("reported %d violations over a target it cannot read, want none: %+v",
			len(result.violations), result.violations)
	}
	if len(result.unverifiable) != 1 {
		t.Fatalf("reported %d unverifiable pairs, want exactly 1: %+v", len(result.unverifiable), result.unverifiable)
	}
	row := result.unverifiable[0]
	if row.layer != infectionLayerName {
		t.Errorf("the unverifiable pair names layer %q, want %q", row.layer, infectionLayerName)
	}
	if row.why != whyProbeRecordedTwice {
		t.Errorf("the unverifiable pair explains itself as %q, want %q", row.why, whyProbeRecordedTwice)
	}
	audited := layerRow(t, result, infectionLayerName)
	if audited.unverifiable != 1 || audited.kept != 0 {
		t.Errorf("the infection layer audited %+v, want one pair it could not decide", audited)
	}
	// Both records are counted as executions, and neither of them is a
	// measurement the audit reads.
	if result.probeExecutions != 2 || result.probeMeasured != 0 {
		t.Errorf("counted %d probe executions and %d measured targets, want 2 and 0",
			result.probeExecutions, result.probeMeasured)
	}
}

func TestAuditMeasuresWhatTheInfectionLayerWouldHaveSaved(t *testing.T) {
	t.Parallel()
	// Soundness is the invariant; this is the value. The recording was made by
	// a run that discharged nothing, so what the layer would have bought is
	// read off the routes and the probe records rather than off a field the
	// trace does not carry. Every fact it reads is in the trace, so it is
	// measured with no catalog at all.
	recorded := recordedEvidence(t, map[string][]string{
		killerTarget: {ran(10, 2, 12, 16)},
		secondTarget: {ran(10, 2, 12, 16)},
		thirdTarget:  {ran(10, 2, 12, 16)},
	})
	stream := recordedTrace(t,
		measured(1, killerTarget), measured(2, secondTarget), measured(3, thirdTarget),
		probeMeasured(4, killerTarget, firstMutant),
		probeMeasured(5, secondTarget),
		probeOutcome(6, thirdTarget, trace.ProbeOutcomeTimedOut),
		probedRoute(7, firstMutant, 11, 4, killerTarget, secondTarget, thirdTarget),
		executedBy(8, firstMutant, firstDisplay, secondTarget, "survived"),
		mutantEvent(9, trace.MutantRecord{
			ID: firstMutant, DisplayID: firstDisplay, Package: fixtureModule + "/pkg",
			Args:    []string{"-test.run=^(" + testNameOf(killerTarget) + "|" + testNameOf(secondTarget) + ")$"},
			Outcome: "survived",
		}),
		probedRoute(10, secondMutant, 11, 6, secondTarget),
		executedBy(11, secondMutant, secondDisplay, secondTarget, "survived"),
		blockRoute(12, thirdMutant, 11, 8, secondTarget),
		executedBy(13, thirdMutant, thirdDisplay, secondTarget, "survived"),
	)

	result := auditFixture(t, stream, recorded)
	want := dischargeSavings{routes: 2, reaching: 4, discharged: 2, emptied: 1, executions: 2}
	if result.infection != want {
		t.Errorf("the audit measured %+v, want %+v", result.infection, want)
	}
	// The branch layer was not audited here, and one measurement never fills
	// the other's numbers in.
	if result.branchAudited {
		t.Error("a run audited without a catalog reports the branch layer as audited")
	}
	if (result.branch != dischargeSavings{}) {
		t.Errorf("a run audited without a catalog measured %+v for the branch layer, want nothing", result.branch)
	}
}

func TestAuditKeepsEveryKillerOfARunThatAlreadyDischargedByInfection(t *testing.T) {
	t.Parallel()
	// A run that applies the layer records the discharge and never executes the
	// target it removed, so every kill that reaches the audit comes from a
	// target the rule kept and the layer holds. The savings measurement then
	// reads nothing: it counts the reaching targets the rule would still drop,
	// and a discharged target is one the route no longer reaches.
	recorded := recordedEvidence(t, map[string][]string{
		killerTarget: {ran(10, 2, 12, 16)},
		secondTarget: {ran(10, 2, 12, 16)},
	})
	route := probedRoute(5, firstMutant, 11, 4, killerTarget)
	route.Route.FileCandidates = 2
	route.Route.Discharged = []trace.Discharge{{Target: secondTarget, Reason: trace.DischargeNeverInfected}}
	stream := recordedRun(t, []string{killerTarget, secondTarget},
		probeMeasured(3, killerTarget, firstMutant),
		probeMeasured(4, secondTarget),
		route,
		killedBy(6, firstMutant, firstDisplay, killerTarget),
	)

	result := auditFixture(t, stream, recorded)
	if len(result.violations) != 0 || len(result.unverifiable) != 0 {
		t.Fatalf("audited a run that applied the layer as %d violations and %d unverifiable pairs, want none: %+v %+v",
			len(result.violations), len(result.unverifiable), result.violations, result.unverifiable)
	}
	row := layerRow(t, result, infectionLayerName)
	if row.audited != 1 || row.kept != 1 {
		t.Errorf("the infection layer audited %+v, want the one killer the rule kept", row)
	}
	want := dischargeSavings{routes: 1, reaching: 1}
	if result.infection != want {
		t.Errorf("the audit measured %+v, want %+v: the discharged target is no longer a reaching one",
			result.infection, want)
	}
}

func TestAuditCountsProbeExecutionsAndMeasuredTargets(t *testing.T) {
	t.Parallel()
	// The two counts say how far the probe pass got: how many targets it
	// executed, and how many of those it got facts out of. A pass that ran every
	// target and measured half of them is a pass whose facts cover half the run,
	// which is what the layer's numbers have to be read against.
	recorded := recordedEvidence(t, map[string][]string{killerTarget: {ran(10, 2, 12, 16)}})
	stream := recordedTrace(t,
		measured(1, killerTarget),
		probeMeasured(2, killerTarget, firstMutant),
		probeMeasured(3, secondTarget),
		probeOutcome(4, thirdTarget, trace.ProbeOutcomeUnavailable),
	)

	result := auditFixture(t, stream, recorded)
	if result.probeExecutions != 3 {
		t.Errorf("counted %d probe executions, want 3", result.probeExecutions)
	}
	if result.probeMeasured != 2 {
		t.Errorf("counted %d targets the probe measured, want 2", result.probeMeasured)
	}
}
