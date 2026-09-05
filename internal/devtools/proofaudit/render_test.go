// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"strings"
	"testing"

	"github.com/P4suta/goatest/internal/testkit"
	"github.com/P4suta/goatest/internal/trace"
)

// sampleAudit is the recording the golden report is rendered from: a run with
// every outcome the audit distinguishes. One killer that block routing keeps,
// one it would drop, one whose target left no profile, a mutant its package
// suite settled, a batch execution that names no single killer, and a last
// line the run was interrupted in the middle of, behind a blank one.
func sampleAudit(t *testing.T) auditResult {
	t.Helper()
	recorded := recordedEvidence(t, map[string][]string{
		killerTarget: {ran(10, 2, 12, 16), linked(20, 2, 24, 3), linked(30, 2, 34, 3)},
		secondTarget: {ran(10, 2, 12, 16), linked(20, 2, 24, 3)},
		thirdTarget:  {ran(10, 2, 12, 16)},
	})
	stream := recordedTrace(t,
		trace.Event{Seq: 1, Type: trace.TypeRunStart, Schema: trace.SchemaV1, Timestamp: fixtureTime},
		measured(2, killerTarget), measured(3, secondTarget),
		measured(4, thirdTarget), measured(5, absentTarget),
		blockRoute(6, firstMutant, 11, 4, killerTarget),
		killedBy(7, firstMutant, firstDisplay, killerTarget),
		killedBy(8, firstMutant, firstDisplay, killerTarget),
		blockRoute(9, secondMutant, 21, 4, secondTarget),
		killedBy(10, secondMutant, secondDisplay, secondTarget),
		blockRoute(11, thirdMutant, 31, 5, absentTarget),
		killedBy(12, thirdMutant, thirdDisplay, absentTarget),
		routeEvent(13, trace.RouteRecord{
			MutantID: fourthMutant, Rule: "cond-true", Path: subjectPath, Line: 41, Column: 2,
			Plan: []string{packageSuitePlan}, Reason: trace.ReasonUnreached, Granularity: trace.GranularityBlock,
		}),
		mutantEvent(14, trace.MutantRecord{
			ID: fourthMutant, DisplayID: fourthDisplay, Package: fixtureModule + "/pkg",
			Outcome: outcomeKilled, DurationMS: 12,
		}),
		blockRoute(15, fifthMutant, 11, 6, killerTarget, secondTarget),
		mutantEvent(16, trace.MutantRecord{
			ID: fifthMutant, DisplayID: fifthDisplay, Package: fixtureModule + "/pkg",
			Args:    []string{"-test.run=^(" + testNameOf(killerTarget) + "|" + testNameOf(secondTarget) + ")$"},
			Outcome: outcomeKilled, DurationMS: 20,
		}),
	) + "\n" + `{"seq":17,"type":"mutant-exec","timestamp":"2026-01-0`

	return auditFixture(t, stream, recorded)
}

// sampleCatalog is the fixture catalog the branch golden is audited against: a
// hand-written `go-mutants list --json` document over the same subject file the
// fixture profiles measure. It carries fields this audit does not read, because
// the audit is run against catalogs older and newer than itself.
const sampleCatalog = "sample-catalog.json"

// branchSampleAudit is the recording the branch golden is rendered from: a run
// whose catalog reaches every conclusion the layer has. One killer that took
// the body its mutation gates, one that never took it, one mutant the catalog
// proved nothing about, one the catalog does not list at all, one whose proof
// does not survive the sanity check, and one whose body no profile
// instrumented.
//
// The subject is one file: a head at 10-12 every target runs, a condition at 20
// gating a body from 20:15 to 22:3, a second condition at 30 gating a body from
// 30:15 to 32:3, and a third at 38-40.
func branchSampleAudit(t *testing.T) auditResult {
	t.Helper()
	recorded := recordedEvidence(t, map[string][]string{
		killerTarget: {
			ran(10, 2, 12, 16), ran(18, 2, 20, 15), ran(20, 15, 22, 3),
			ran(28, 2, 30, 15), linked(30, 15, 32, 3), ran(38, 2, 40, 25),
		},
		secondTarget: {
			ran(10, 2, 12, 16), ran(18, 2, 20, 15), linked(20, 15, 22, 3),
			ran(28, 2, 30, 15), linked(30, 15, 32, 3),
		},
		thirdTarget: {ran(10, 2, 12, 16), linked(28, 2, 30, 15), linked(30, 15, 32, 3)},
	})
	catalog, err := readCatalog(testkit.GoldenPath(sampleCatalog))
	if err != nil {
		t.Fatalf("read the fixture catalog: %v", err)
	}
	stream := recordedTrace(t,
		trace.Event{Seq: 1, Type: trace.TypeRunStart, Schema: trace.SchemaV1, Timestamp: fixtureTime},
		measured(2, killerTarget), measured(3, secondTarget), measured(4, thirdTarget),
		blockRoute(5, firstMutant, 20, 4, killerTarget, secondTarget),
		killedBy(6, firstMutant, firstDisplay, killerTarget),
		executedBy(7, firstMutant, firstDisplay, secondTarget, "survived"),
		blockRoute(8, secondMutant, 30, 4, secondTarget, thirdTarget),
		killedBy(9, secondMutant, secondDisplay, secondTarget),
		executedBy(10, secondMutant, secondDisplay, thirdTarget, "survived"),
		blockRoute(11, thirdMutant, 11, 4, killerTarget),
		killedBy(12, thirdMutant, thirdDisplay, killerTarget),
		blockRoute(13, fourthMutant, 11, 6, killerTarget),
		killedBy(14, fourthMutant, fourthDisplay, killerTarget),
		blockRoute(15, fifthMutant, 40, 20, killerTarget),
		killedBy(16, fifthMutant, fifthDisplay, killerTarget),
		blockRoute(17, sixthMutant, 11, 8, killerTarget),
		killedBy(18, sixthMutant, sixthDisplay, killerTarget),
	)

	return auditWithCatalog(t, stream, recorded, catalog)
}

// infectionSampleAudit is the recording the infection golden is rendered from:
// a run whose probe pass reaches every conclusion the layer has. One killer the
// pass saw the mutant infect, one it measured and never saw infect, one mutant
// it carried no site for, and one killer it recorded twice. Every count around
// them is non-zero as well, so a rendering regression cannot hide behind a
// column of zeroes.
//
// The subject is one file whose head at 10-12 every target ran, so block
// routing keeps every pair and the only violation the report names is the
// infection layer's.
func infectionSampleAudit(t *testing.T) auditResult {
	t.Helper()
	recorded := recordedEvidence(t, map[string][]string{
		killerTarget: {ran(10, 2, 12, 16)},
		secondTarget: {ran(10, 2, 12, 16)},
		thirdTarget:  {ran(10, 2, 12, 16)},
	})
	stream := recordedTrace(t,
		trace.Event{Seq: 1, Type: trace.TypeRunStart, Schema: trace.SchemaV1, Timestamp: fixtureTime},
		measured(2, killerTarget), measured(3, secondTarget), measured(4, thirdTarget),
		probeMeasured(5, killerTarget, firstMutant),
		probeMeasured(6, secondTarget),
		probeMeasured(7, thirdTarget, fourthMutant),
		probeOutcome(8, thirdTarget, trace.ProbeOutcomeTimedOut),
		probedRoute(9, firstMutant, 11, 4, killerTarget, secondTarget),
		killedBy(10, firstMutant, firstDisplay, killerTarget),
		executedBy(11, firstMutant, firstDisplay, secondTarget, "survived"),
		probedRoute(12, secondMutant, 11, 6, secondTarget, thirdTarget),
		killedBy(13, secondMutant, secondDisplay, secondTarget),
		blockRoute(14, thirdMutant, 11, 8, killerTarget),
		killedBy(15, thirdMutant, thirdDisplay, killerTarget),
		killedIn(16, thirdMutant, thirdDisplay, fixtureModule+"/other", testNameOf(killerTarget)),
		probedRoute(17, fourthMutant, 11, 10, thirdTarget, secondTarget),
		killedBy(18, fourthMutant, fourthDisplay, thirdTarget),
		routeEvent(19, trace.RouteRecord{
			MutantID: fifthMutant, Rule: "cond-true", Path: subjectPath, Line: 11, Column: 12,
			Plan: []string{packageSuitePlan}, Reason: trace.ReasonUnreached, Granularity: trace.GranularityBlock,
		}),
		mutantEvent(20, trace.MutantRecord{
			ID: fifthMutant, DisplayID: fifthDisplay, Package: fixtureModule + "/pkg",
			Outcome: outcomeKilled, DurationMS: 12,
		}),
		probedRoute(21, sixthMutant, 11, 14, secondTarget),
		mutantEvent(22, trace.MutantRecord{
			ID: sixthMutant, DisplayID: sixthDisplay, Package: fixtureModule + "/pkg",
			Args:    []string{"-test.run=^(" + testNameOf(killerTarget) + "|" + testNameOf(secondTarget) + ")$"},
			Outcome: outcomeKilled, DurationMS: 20,
		}),
	) + "\n" + `{"seq":23,"type":"mutant-exec","timestamp":"2026-01-0`

	return auditFixture(t, stream, recorded)
}

// renderSample renders the sample audit under fixed paths, so the golden
// records the report rather than the temporary directory it was read from.
func renderSample(t *testing.T) string {
	t.Helper()
	return renderAudit("testdata/sample-trace.jsonl", "testdata/sample-profiles", fixtureModule, sampleAudit(t))
}

// renderBranchSample renders the branch sample under the same fixed paths.
func renderBranchSample(t *testing.T) string {
	t.Helper()
	return renderAudit("testdata/sample-trace.jsonl", "testdata/sample-profiles", fixtureModule, branchSampleAudit(t))
}

// renderInfectionSample renders the infection sample under the same fixed paths.
func renderInfectionSample(t *testing.T) string {
	t.Helper()
	return renderAudit("testdata/sample-trace.jsonl", "testdata/sample-profiles", fixtureModule, infectionSampleAudit(t))
}

func TestRenderAuditBreaksDownOneRecording(t *testing.T) {
	t.Parallel()
	testkit.Golden(t, "sample-audit.txt", []byte(renderSample(t)))
}

func TestRenderAuditBreaksDownOneRecordingAgainstACatalog(t *testing.T) {
	t.Parallel()
	testkit.Golden(t, "sample-branch-audit.txt", []byte(renderBranchSample(t)))
}

func TestRenderAuditSaysWhatTheInfectionLayerWouldHaveSaved(t *testing.T) {
	t.Parallel()
	testkit.Golden(t, "sample-infection-audit.txt", []byte(renderInfectionSample(t)))
}

func TestRenderAuditDependsOnTheRecordingAlone(t *testing.T) {
	t.Parallel()
	for name, render := range map[string]func(*testing.T) string{
		"without a catalog": renderSample,
		"with a catalog":    renderBranchSample,
		"with a probe pass": renderInfectionSample,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if first, second := render(t), render(t); first != second {
				t.Error("two renderings of one recording differ; the audit is not deterministic")
			}
		})
	}
}

func TestRenderAuditReportsAnAuditWithNothingToSay(t *testing.T) {
	t.Parallel()
	// The clean report is the one a gate sees most, so what it says when
	// there is nothing to report is part of the contract.
	recorded := recordedEvidence(t, map[string][]string{
		killerTarget: {ran(10, 2, 12, 16)},
	})
	stream := recordedRun(t, []string{killerTarget},
		blockRoute(2, firstMutant, 11, 4, killerTarget),
		killedBy(3, firstMutant, firstDisplay, killerTarget),
	)
	got := renderAudit("trace.jsonl", "profiles", fixtureModule, auditFixture(t, stream, recorded))

	for _, want := range []string{
		"trace: trace.jsonl",
		"profiles: profiles",
		"module: " + fixtureModule,
		"target kill pairs audited                  1",
		"probe executions                           0",
		"targets the probe measured                 0",
		"reach        1     1             0             0           0",
		whyBranchNotAudited,
		whyInfectionNotAudited,
		whySuiteReachNotAudited,
		"every layer could decide every kill pair",
		"no layer drops a killer a recorded run proved",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the audit does not say %q:\n%s", want, got)
		}
	}
	for _, heading := range []string{branchDischargeHeading, infectionDischargeHeading} {
		if strings.Contains(got, heading) {
			t.Errorf("an unaudited layer reports what it would have saved under %q:\n%s", heading, got)
		}
	}
	if strings.HasSuffix(got, "\n\n") {
		t.Errorf("the audit ends with a blank line:\n%q", got)
	}
}

func TestRenderAuditSaysWhatTheBranchLayerWouldHaveSaved(t *testing.T) {
	t.Parallel()
	// The savings are the reason to have the layer at all, and they are read
	// off the recording of a run that discharged nothing, so the block says
	// what would not have happened rather than what did.
	got := renderAudit("trace.jsonl", "profiles", fixtureModule, auditResult{
		branchAudited: true,
		branch:        dischargeSavings{routes: 635, reaching: 8535, discharged: 7318, emptied: 182, executions: 3102},
	})

	for _, want := range []string{
		branchDischargeHeading,
		"routes with a branch proof",
		"reaching targets the proof discharges",
		"7318 of 8535",
		"routes left with no reaching target",
		"recorded executions it would have saved",
		"3102",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the audit does not say %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, whyBranchNotAudited) {
		t.Errorf("an audited branch layer is reported as not audited:\n%s", got)
	}
}

func TestRenderAuditSaysWhenTheInfectionLayerWasNotAudited(t *testing.T) {
	t.Parallel()
	// A recording that holds no probe pass is a recording the layer was never
	// held to, and a missing row reads exactly like a clean one to anyone
	// skimming, so the report says which of the two it is — and says it only
	// then, because a report that always said it would say nothing.
	saved := dischargeSavings{routes: 412, reaching: 5104, discharged: 3990, emptied: 96, executions: 1877}
	cases := []struct {
		name    string
		audited bool
	}{
		{name: "a recording that holds a probe pass", audited: true},
		{name: "a recording that holds none", audited: false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			got := renderAudit("trace.jsonl", "profiles", fixtureModule, auditResult{
				layers:           []layerResult{{name: reachLayerName}},
				infectionAudited: testCase.audited,
				infection:        saved,
			})

			if said := strings.Contains(got, whyInfectionNotAudited); said == testCase.audited {
				t.Errorf("the report says %q for an audited=%t recording:\n%s",
					whyInfectionNotAudited, testCase.audited, got)
			}
			if !testCase.audited {
				if strings.Contains(got, infectionDischargeHeading) {
					t.Errorf("an unaudited infection layer reports what it would have saved:\n%s", got)
				}
				return
			}
			for _, want := range []string{
				infectionDischargeHeading,
				"routes of a probed mutant",
				"reaching targets the probe discharges",
				"3990 of 5104",
				"routes left with no reaching target",
				"recorded executions it would have saved",
				"1877",
			} {
				if !strings.Contains(got, want) {
					t.Errorf("the audit does not say %q:\n%s", want, got)
				}
			}
		})
	}
}

func TestRenderAuditSaysWhenNoLayerWasAudited(t *testing.T) {
	t.Parallel()
	// An audit of no layers is not a clean audit, and a table with no rows
	// under a "layers" heading would read as one.
	got := renderAudit("trace.jsonl", "profiles", fixtureModule, auditResult{})
	if !strings.Contains(got, "no layer was audited") {
		t.Errorf("an audit of no layers does not say so:\n%s", got)
	}
}

func TestOrNoValueMarksAFieldTheRecordingLacked(t *testing.T) {
	t.Parallel()
	// A recording from before a field existed carries none of it, and an
	// empty cell in a table reads as a rendering bug rather than as an
	// absence.
	if got := orNoValue(""); got != noValue {
		t.Errorf("orNoValue rendered an absent value as %q, want %q", got, noValue)
	}
	if got := orNoValue("eq-to-neq"); got != "eq-to-neq" {
		t.Errorf("orNoValue rewrote a recorded value as %q", got)
	}
}

func TestPositionRendersOnlyWhatTheRecordingCarried(t *testing.T) {
	t.Parallel()
	// A route records a position or nothing, and a mutant the engine could
	// not place is exactly the mutant a reader wants to see as unplaced
	// rather than as sitting at line zero.
	cases := []struct {
		name         string
		line, column int
		want         string
	}{
		{name: "a whole position", line: 21, column: 4, want: subjectPath + ":21:4"},
		{name: "a line the column is missing from", line: 21, column: 0, want: subjectPath + ":21"},
		{name: "no position at all", line: 0, column: 0, want: subjectPath},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			got := position(killPair{path: subjectPath, line: testCase.line, column: testCase.column})
			if got != testCase.want {
				t.Errorf("position rendered %q, want %q", got, testCase.want)
			}
		})
	}
}

func TestMutantNameFallsBackToTheRecordedIdentity(t *testing.T) {
	t.Parallel()
	// The engine names a mutant by the first twenty characters of its content
	// address. A recording that carries none is named by the same twenty, so
	// one column stays one width whatever wrote the trace.
	cases := []struct {
		name string
		pair killPair
		want string
	}{
		{name: "the display identity a run recorded", pair: killPair{mutant: firstMutant, display: firstDisplay}, want: firstDisplay},
		{name: "the identity cut to the same width", pair: killPair{mutant: firstMutant}, want: firstMutant[:displayWidth]},
		{name: "an identity shorter than the width", pair: killPair{mutant: "abc"}, want: "abc"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			if got := mutantName(testCase.pair); got != testCase.want {
				t.Errorf("mutantName rendered %q, want %q", got, testCase.want)
			}
		})
	}
}
