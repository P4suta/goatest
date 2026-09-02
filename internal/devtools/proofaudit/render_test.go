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
// line the run was interrupted in the middle of.
func sampleAudit(t *testing.T) auditResult {
	t.Helper()
	recorded := recordedEvidence(t, map[string][]string{
		killerTarget: {ran(10, 2, 12, 16), linked(20, 2, 24, 3), linked(30, 2, 34, 3)},
		secondTarget: {ran(10, 2, 12, 16), linked(20, 2, 24, 3)},
		thirdTarget:  {ran(10, 2, 12, 16)},
	})
	stream := recordedTrace(t,
		trace.Event{Seq: 1, Type: trace.TypeRunStart, Schema: trace.SchemaV1, Timestamp: fixtureTime},
		blockRoute(2, firstMutant, 11, 4, killerTarget),
		killedBy(3, firstMutant, firstDisplay, killerTarget),
		killedBy(4, firstMutant, firstDisplay, killerTarget),
		blockRoute(5, secondMutant, 21, 4, secondTarget),
		killedBy(6, secondMutant, secondDisplay, secondTarget),
		blockRoute(7, thirdMutant, 31, 5, absentTarget),
		killedBy(8, thirdMutant, thirdDisplay, absentTarget),
		routeEvent(9, trace.RouteRecord{
			MutantID: fourthMutant, Rule: "cond-true", Path: subjectPath, Line: 41, Column: 2,
			Plan: []string{packageSuitePlan}, Reason: trace.ReasonUnreached, Granularity: trace.GranularityBlock,
		}),
		mutantEvent(10, trace.MutantRecord{
			ID: fourthMutant, DisplayID: fourthDisplay, Package: fixtureModule + "/pkg",
			Outcome: outcomeKilled, DurationMS: 12,
		}),
		blockRoute(11, fifthMutant, 11, 6, killerTarget, secondTarget),
		mutantEvent(12, trace.MutantRecord{
			ID: fifthMutant, DisplayID: fifthDisplay, Package: fixtureModule + "/pkg",
			Args:    []string{"-test.run=^(" + testNameOf(killerTarget) + "|" + testNameOf(secondTarget) + ")$"},
			Outcome: outcomeKilled, DurationMS: 20,
		}),
	) + `{"seq":13,"type":"mutant-exec","timestamp":"2026-01-0`

	return auditFixture(t, stream, recorded)
}

// renderSample renders the sample audit under fixed paths, so the golden
// records the report rather than the temporary directory it was read from.
func renderSample(t *testing.T) string {
	t.Helper()
	return renderAudit("testdata/sample-trace.jsonl", "testdata/sample-profiles", fixtureModule, sampleAudit(t))
}

func TestRenderAuditBreaksDownOneRecording(t *testing.T) {
	t.Parallel()
	testkit.Golden(t, "sample-audit.txt", []byte(renderSample(t)))
}

func TestRenderAuditDependsOnTheRecordingAlone(t *testing.T) {
	t.Parallel()
	first, second := renderSample(t), renderSample(t)
	if first != second {
		t.Error("two renderings of one recording differ; the audit is not deterministic")
	}
}

func TestRenderAuditReportsAnAuditWithNothingToSay(t *testing.T) {
	t.Parallel()
	// The clean report is the one a gate sees most, so what it says when
	// there is nothing to report is part of the contract.
	recorded := recordedEvidence(t, map[string][]string{
		killerTarget: {ran(10, 2, 12, 16)},
	})
	stream := recordedTrace(t,
		blockRoute(1, firstMutant, 11, 4, killerTarget),
		killedBy(2, firstMutant, firstDisplay, killerTarget),
	)
	got := renderAudit("trace.jsonl", "profiles", fixtureModule, auditFixture(t, stream, recorded))

	for _, want := range []string{
		"trace: trace.jsonl",
		"profiles: profiles",
		"module: " + fixtureModule,
		"kill pairs audited       1",
		"reach          1     1             0           0",
		"every killer of a recorded kill left a coverage profile",
		"no layer drops a killer a recorded run proved",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the audit does not say %q:\n%s", want, got)
		}
	}
	if strings.HasSuffix(got, "\n\n") {
		t.Errorf("the audit ends with a blank line:\n%q", got)
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
