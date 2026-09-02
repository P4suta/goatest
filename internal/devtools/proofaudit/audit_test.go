// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/P4suta/goatest/internal/trace"
)

// The fixtures below are the two halves of a recorded run: a trace written
// with the trace types themselves, so a change to the contract reaches these
// tests through the compiler, and coverage profiles written in the format
// cmd/cover emits, so the evidence is always the bytes a run would have left.
const (
	// fixtureModule is the module the fixture profiles name their files under.
	fixtureModule = "example.com/audited"
	// subjectPath is the module-relative file every fixture mutant lives in.
	subjectPath = "pkg/subject.go"
	// fixtureTime is the moment every fixture event carries. The audit never
	// reads a clock, so one moment is enough for a whole recording.
	fixtureTime = "2026-01-01T00:00:00Z"
)

// The fixture identities. A target is named by the 16 hexadecimal characters a
// run derives, and a mutant by the 64 of its content address with the first 20
// of them as the display identity, so the widths a real recording prints are
// the widths these tests assert on.
const (
	killerTarget = "a1b2c3d4e5f60718"
	secondTarget = "b1c2d3e4f5061728"
	thirdTarget  = "d1e2f30415162738"
	absentTarget = "c1d2e3f405162738"

	firstMutant   = "a1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f90"
	firstDisplay  = "a1b2c3d4e5f60718293a"
	secondMutant  = "b1c2d3e4f5061728394a5b6c7d8e9f01b1c2d3e4f5061728394a5b6c7d8e9f01"
	secondDisplay = "b1c2d3e4f5061728394a"
	thirdMutant   = "c1d2e3f405162738495a6b7c8d9e0f12c1d2e3f405162738495a6b7c8d9e0f12"
	thirdDisplay  = "c1d2e3f405162738495a"
	fourthMutant  = "d1e2f30415162738495a6b7c8d9e0f12d1e2f30415162738495a6b7c8d9e0f12"
	fourthDisplay = "d1e2f30415162738495a"
	fifthMutant   = "e1f203142536475869708a9bacbdcedfe1f203142536475869708a9bacbdcedf"
	fifthDisplay  = "e1f20314253647586970"
)

// ran renders one profile line for a block of the subject file the profiled
// execution executed.
func ran(startLine, startColumn, endLine, endColumn int) string {
	return profileLine(startLine, startColumn, endLine, endColumn, 1)
}

// linked renders one profile line for a block the profile instrumented and the
// profiled execution never executed. It is what puts a position inside the
// coverage the toolchain measured without any target having reached it.
func linked(startLine, startColumn, endLine, endColumn int) string {
	return profileLine(startLine, startColumn, endLine, endColumn, 0)
}

func profileLine(startLine, startColumn, endLine, endColumn, count int) string {
	return fmt.Sprintf("%s/%s:%d.%d,%d.%d 1 %d",
		fixtureModule, subjectPath, startLine, startColumn, endLine, endColumn, count)
}

// writeProfiles writes one "mode: set" profile per target into a fresh
// directory, named the way a run names them, and returns the directory.
func writeProfiles(t *testing.T, profiles map[string][]string) string {
	t.Helper()
	directory := t.TempDir()
	for target, lines := range profiles {
		body := "mode: set\n" + strings.Join(lines, "\n") + "\n"
		if err := os.WriteFile(filepath.Join(directory, target+".cover"), []byte(body), 0o644); err != nil {
			t.Fatalf("write the profile of %s: %v", target, err)
		}
	}
	return directory
}

// recordedEvidence writes the fixture profiles and reads them back through the
// tool, so a fixture proves the reading as well as the rule.
func recordedEvidence(t *testing.T, profiles map[string][]string) evidence {
	t.Helper()
	recorded, err := readEvidence(writeProfiles(t, profiles), fixtureModule)
	if err != nil {
		t.Fatalf("read the recorded evidence: %v", err)
	}
	return recorded
}

// routeEvent is one recorded routing decision.
func routeEvent(seq int64, record trace.RouteRecord) trace.Event {
	return trace.Event{Seq: seq, Type: trace.TypeRoute, Timestamp: fixtureTime, Route: &record}
}

// mutantEvent is one recorded mutant execution.
func mutantEvent(seq int64, record trace.MutantRecord) trace.Event {
	return trace.Event{Seq: seq, Type: trace.TypeMutantExec, Timestamp: fixtureTime, Mutant: &record}
}

// blockRoute is the route of a mutant coverage blocks decided: the targets
// that reach the position, and the individual run planned for each of them.
func blockRoute(seq int64, mutant string, line, column int, targets ...string) trace.Event {
	plan := make([]string, 0, len(targets))
	for _, target := range targets {
		plan = append(plan, individualPlan+testNameOf(target))
	}
	return routeEvent(seq, trace.RouteRecord{
		MutantID: mutant, Rule: "eq-to-neq", Path: subjectPath, Line: line, Column: column,
		ReachingTargets: targets, Plan: plan, Reason: trace.ReasonCoverageReaching,
		Granularity: trace.GranularityBlock, FileCandidates: len(targets),
	})
}

// testNameOf is the test a fixture target runs. The audit maps a killer test
// back to its target through the plan alone, so the name only has to be the
// same on both sides of a fixture.
func testNameOf(target string) string { return "TestTarget" + strings.ToUpper(target[:2]) }

// killedBy is one execution of a mutant that its target killed.
func killedBy(seq int64, mutant, display, target string) trace.Event {
	return mutantEvent(seq, trace.MutantRecord{
		ID: mutant, DisplayID: display, Package: fixtureModule + "/pkg",
		Args: []string{"-test.run=^" + testNameOf(target) + "$"}, Outcome: outcomeKilled, DurationMS: 5,
	})
}

// recordedTrace renders events as the JSONL a recording is made of.
func recordedTrace(t *testing.T, events ...trace.Event) string {
	t.Helper()
	var builder strings.Builder
	for _, event := range events {
		line, err := json.Marshal(event)
		if err != nil {
			t.Fatalf("marshal event %d: %v", event.Seq, err)
		}
		builder.Write(line)
		builder.WriteByte('\n')
	}
	return builder.String()
}

// auditFixture runs the reach layer over a fixture recording.
func auditFixture(t *testing.T, stream string, recorded evidence) auditResult {
	t.Helper()
	result, err := auditTrace(strings.NewReader(stream), recorded, auditLayers())
	if err != nil {
		t.Fatalf("audit the recording: %v", err)
	}
	return result
}

func TestAuditFailsWhenAKillerLiesOutsideItsBlocks(t *testing.T) {
	t.Parallel()
	// The killer ran the top of the file and the mutation is at the bottom of
	// it, inside a block the profile instrumented. Block routing would leave
	// the killer out, and leaving a proven killer out is the one thing a
	// routing layer may never do.
	recorded := recordedEvidence(t, map[string][]string{
		killerTarget: {ran(10, 2, 12, 16), linked(20, 2, 24, 3)},
	})
	stream := recordedTrace(t,
		blockRoute(1, firstMutant, 21, 4, killerTarget),
		killedBy(2, firstMutant, firstDisplay, killerTarget),
	)

	result := auditFixture(t, stream, recorded)
	if result.pairs != 1 {
		t.Fatalf("audited %d kill pairs, want 1", result.pairs)
	}
	if len(result.violations) != 1 {
		t.Fatalf("reported %d violations, want exactly 1: %+v", len(result.violations), result.violations)
	}
	violation := result.violations[0]
	if violation.pair.mutant != firstMutant {
		t.Errorf("the violation names mutant %q, want %q", violation.pair.mutant, firstMutant)
	}
	if violation.pair.target != killerTarget {
		t.Errorf("the violation names killer target %q, want %q", violation.pair.target, killerTarget)
	}
	if violation.layer != reachLayerName {
		t.Errorf("the violation names layer %q, want %q", violation.layer, reachLayerName)
	}
	if violation.why != whyOutsideCoveredBlocks {
		t.Errorf("the violation explains itself as %q, want %q", violation.why, whyOutsideCoveredBlocks)
	}
	if got := result.layers[0]; got.violations != 1 || got.kept != 0 || got.audited != 1 {
		t.Errorf("the reach layer audited %+v, want one audited pair that it drops", got)
	}
}

func TestAuditFailsWhenAKillerCoversNoneOfTheFile(t *testing.T) {
	t.Parallel()
	// The killer's profile never names the file the mutation lives in, so
	// neither the block rule nor the file rule behind it would keep the
	// target. The recording says it killed the mutant there all the same,
	// which is a contradiction worth a violation rather than a shrug.
	recorded := recordedEvidence(t, map[string][]string{
		killerTarget: {profileLine(10, 2, 12, 16, 0)},
		secondTarget: {ran(10, 2, 12, 16)},
	})
	stream := recordedTrace(t,
		blockRoute(1, firstMutant, 11, 4, killerTarget),
		killedBy(2, firstMutant, firstDisplay, killerTarget),
	)

	result := auditFixture(t, stream, recorded)
	if len(result.violations) != 1 {
		t.Fatalf("reported %d violations, want exactly 1: %+v", len(result.violations), result.violations)
	}
	if got := result.violations[0].why; got != whyCoversNoneOfTheFile {
		t.Errorf("the violation explains itself as %q, want %q", got, whyCoversNoneOfTheFile)
	}
}

func TestAuditPassesWhenEveryKillerReachesByBlock(t *testing.T) {
	t.Parallel()
	// Both killers ran the block the mutation sits in, which is exactly what
	// block routing keeps.
	recorded := recordedEvidence(t, map[string][]string{
		killerTarget: {ran(10, 2, 12, 16), linked(20, 2, 24, 3)},
		secondTarget: {ran(10, 2, 12, 16), ran(20, 2, 24, 3)},
	})
	stream := recordedTrace(t,
		blockRoute(1, firstMutant, 11, 4, killerTarget, secondTarget),
		killedBy(2, firstMutant, firstDisplay, killerTarget),
		killedBy(3, firstMutant, firstDisplay, secondTarget),
	)

	result := auditFixture(t, stream, recorded)
	if result.pairs != 2 {
		t.Fatalf("audited %d kill pairs, want 2", result.pairs)
	}
	if len(result.violations) != 0 {
		t.Fatalf("reported %d violations of a sound routing, want none: %+v", len(result.violations), result.violations)
	}
	if len(result.unverifiable) != 0 {
		t.Fatalf("reported %d unverifiable pairs, want none: %+v", len(result.unverifiable), result.unverifiable)
	}
	if got := result.layers[0]; got.kept != 2 || got.audited != 2 {
		t.Errorf("the reach layer audited %+v, want two audited pairs it keeps", got)
	}
	if result.targets != 2 {
		t.Errorf("the audit counted %d targets with profiles, want 2", result.targets)
	}
	if result.routes != 1 {
		t.Errorf("the audit counted %d routes, want 1", result.routes)
	}
}

func TestAuditKeepsAKillerWhenThePositionIsUnknown(t *testing.T) {
	t.Parallel()
	// A mutant whose position the engine did not report cannot be placed in a
	// block, so the file decides and every target that ran the file is kept.
	// Dropping such a killer would be the audit inventing a rule of its own.
	recorded := recordedEvidence(t, map[string][]string{
		killerTarget: {ran(10, 2, 12, 16)},
	})
	for _, position := range []struct {
		name         string
		line, column int
	}{
		{name: "no position at all", line: 0, column: 0},
		{name: "a line without a column", line: 21, column: 0},
	} {
		t.Run(position.name, func(t *testing.T) {
			t.Parallel()
			stream := recordedTrace(t,
				blockRoute(1, firstMutant, position.line, position.column, killerTarget),
				killedBy(2, firstMutant, firstDisplay, killerTarget),
			)
			result := auditFixture(t, stream, recorded)
			if result.pairs != 1 {
				t.Fatalf("audited %d kill pairs, want 1", result.pairs)
			}
			if len(result.violations) != 0 {
				t.Fatalf("reported %d violations, want none: %+v", len(result.violations), result.violations)
			}
			if got := result.layers[0].kept; got != 1 {
				t.Errorf("the reach layer kept %d killers, want 1", got)
			}
		})
	}
}

func TestAuditKeepsAKillerOutsideEveryInstrumentedBlock(t *testing.T) {
	t.Parallel()
	// A position no instrumented block contains is a gap between the blocks
	// cmd/cover cut, not proof that nothing runs it, so routing falls back to
	// the file and the audit has to fall back with it.
	recorded := recordedEvidence(t, map[string][]string{
		killerTarget: {ran(10, 2, 12, 16)},
		secondTarget: {ran(10, 2, 12, 16)},
	})
	stream := recordedTrace(t,
		blockRoute(1, firstMutant, 40, 1, killerTarget),
		killedBy(2, firstMutant, firstDisplay, killerTarget),
	)

	result := auditFixture(t, stream, recorded)
	if len(result.violations) != 0 {
		t.Fatalf("reported %d violations outside the instrumented blocks, want none: %+v",
			len(result.violations), result.violations)
	}
	if got := result.layers[0].kept; got != 1 {
		t.Errorf("the reach layer kept %d killers, want 1", got)
	}
}

func TestAuditReportsAMissingProfileAsUnverifiable(t *testing.T) {
	t.Parallel()
	// A target whose baseline failed leaves no profile. goatest keeps such a
	// target for the whole file, so the audit has nothing to check and says
	// so instead of counting a violation it cannot prove.
	recorded := recordedEvidence(t, map[string][]string{
		killerTarget: {ran(10, 2, 12, 16), linked(20, 2, 24, 3)},
	})
	stream := recordedTrace(t,
		blockRoute(1, firstMutant, 21, 4, absentTarget),
		killedBy(2, firstMutant, firstDisplay, absentTarget),
	)

	result := auditFixture(t, stream, recorded)
	if result.pairs != 1 {
		t.Fatalf("audited %d kill pairs, want 1", result.pairs)
	}
	if len(result.violations) != 0 {
		t.Fatalf("reported %d violations for an unmeasured target, want none: %+v",
			len(result.violations), result.violations)
	}
	if len(result.unverifiable) != 1 {
		t.Fatalf("reported %d unverifiable pairs, want exactly 1: %+v", len(result.unverifiable), result.unverifiable)
	}
	row := result.unverifiable[0]
	if row.pair.target != absentTarget {
		t.Errorf("the unverifiable pair names target %q, want %q", row.pair.target, absentTarget)
	}
	if row.why != whyNoProfile {
		t.Errorf("the unverifiable pair explains itself as %q, want %q", row.why, whyNoProfile)
	}
	if got := result.layers[0]; got.unverifiable != 1 || got.kept != 0 {
		t.Errorf("the reach layer audited %+v, want one unverifiable pair", got)
	}
}

func TestAuditCountsAPackageSuiteKillWithoutAuditingIt(t *testing.T) {
	t.Parallel()
	// A mutant no target reaches is settled by its package suite, which runs
	// without a -test.run argument. No single test is named, so no pair can be
	// attributed, and the kill is counted rather than audited or lost.
	recorded := recordedEvidence(t, map[string][]string{
		killerTarget: {ran(10, 2, 12, 16), linked(20, 2, 24, 3)},
	})
	stream := recordedTrace(t,
		routeEvent(1, trace.RouteRecord{
			MutantID: firstMutant, Rule: "eq-to-neq", Path: subjectPath, Line: 21, Column: 4,
			Plan: []string{packageSuitePlan}, Reason: trace.ReasonUnreached,
			Granularity: trace.GranularityBlock,
		}),
		mutantEvent(2, trace.MutantRecord{
			ID: firstMutant, DisplayID: firstDisplay, Package: fixtureModule + "/pkg",
			Outcome: outcomeKilled, DurationMS: 9,
		}),
	)

	result := auditFixture(t, stream, recorded)
	if result.packageSuiteKills != 1 {
		t.Errorf("counted %d package-suite kills, want 1", result.packageSuiteKills)
	}
	if result.pairs != 0 || len(result.violations) != 0 || len(result.unverifiable) != 0 {
		t.Errorf("a package-suite kill was audited: %d pairs, %d violations, %d unverifiable",
			result.pairs, len(result.violations), len(result.unverifiable))
	}
	if result.killedExecutions != 1 {
		t.Errorf("counted %d killed executions, want 1", result.killedExecutions)
	}
}

func TestAuditCountsABatchKillWithoutAttributingIt(t *testing.T) {
	t.Parallel()
	// A batch runs several targets under one -test.run pattern, so a kill
	// proves one of them killed the mutant without saying which. There is no
	// pair to preserve, so the kill is counted and left alone.
	recorded := recordedEvidence(t, map[string][]string{
		killerTarget: {ran(10, 2, 12, 16), linked(20, 2, 24, 3)},
		secondTarget: {ran(10, 2, 12, 16), linked(20, 2, 24, 3)},
	})
	stream := recordedTrace(t,
		blockRoute(1, firstMutant, 21, 4, killerTarget, secondTarget),
		mutantEvent(2, trace.MutantRecord{
			ID: firstMutant, DisplayID: firstDisplay, Package: fixtureModule + "/pkg",
			Args:    []string{"-test.run=^(" + testNameOf(killerTarget) + "|" + testNameOf(secondTarget) + ")$"},
			Outcome: outcomeKilled,
		}),
	)

	result := auditFixture(t, stream, recorded)
	if result.batchKills != 1 {
		t.Errorf("counted %d batch kills, want 1", result.batchKills)
	}
	if result.pairs != 0 {
		t.Errorf("audited %d kill pairs of a batch execution, want none", result.pairs)
	}
}

func TestAuditCountsAKillItCannotAttributeToATarget(t *testing.T) {
	t.Parallel()
	// A killer no route names, a kill with no route at all, and an execution
	// that selected no test — the fuzzing of a target asks for "^$" — are
	// recordings the audit cannot read as a pair: there is no target to check
	// the rule against. They are counted so that a trace whose routes and
	// executions disagree is visible rather than silently narrowing what was
	// audited.
	recorded := recordedEvidence(t, map[string][]string{
		killerTarget: {ran(10, 2, 12, 16)},
	})
	cases := []struct {
		name   string
		events []trace.Event
	}{
		{
			name: "a killer the route does not name",
			events: []trace.Event{
				blockRoute(1, firstMutant, 11, 4, killerTarget),
				mutantEvent(2, trace.MutantRecord{
					ID: firstMutant, DisplayID: firstDisplay,
					Args: []string{"-test.run=^TestNobodyPlanned$"}, Outcome: outcomeKilled,
				}),
			},
		},
		{
			name:   "a kill with no route recorded before it",
			events: []trace.Event{killedBy(1, firstMutant, firstDisplay, killerTarget)},
		},
		{
			name: "an execution that selected no test",
			events: []trace.Event{
				blockRoute(1, firstMutant, 11, 4, killerTarget),
				mutantEvent(2, trace.MutantRecord{
					ID: firstMutant, DisplayID: firstDisplay,
					Args: []string{"-test.run=^$", "-test.fuzz=^FuzzTarget$"}, Outcome: outcomeKilled,
				}),
			},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			result := auditFixture(t, recordedTrace(t, testCase.events...), recorded)
			if result.unattributedKills != 1 {
				t.Errorf("counted %d unattributed kills, want 1", result.unattributedKills)
			}
			if result.pairs != 0 {
				t.Errorf("audited %d kill pairs, want none", result.pairs)
			}
		})
	}
}

// failingReader is a stream that breaks partway, which is what a trace on a
// failing disk or an interrupted pipe reads like.
type failingReader struct{ err error }

func (reader failingReader) Read([]byte) (int, error) { return 0, reader.err }

func TestAuditReportsAStreamItCannotRead(t *testing.T) {
	t.Parallel()
	// A stream that breaks is not an interrupted recording: nothing says how
	// much of it was read, so the audit refuses rather than reporting a total
	// over the part that arrived.
	broken := errors.New("the stream broke")

	_, err := auditTrace(failingReader{err: broken}, evidence{}, auditLayers())
	if !errors.Is(err, broken) {
		t.Fatalf("auditing a broken stream returned %v, want the read failure", err)
	}
}

func TestAuditCountsOneKillPairPerMutantAndTarget(t *testing.T) {
	t.Parallel()
	// goatest confirms a kill by running it again, so one pair is recorded
	// twice. The pair is audited once and both executions are counted, so the
	// two numbers say what they mean.
	recorded := recordedEvidence(t, map[string][]string{
		killerTarget: {ran(10, 2, 12, 16)},
	})
	stream := recordedTrace(t,
		blockRoute(1, firstMutant, 11, 4, killerTarget),
		killedBy(2, firstMutant, firstDisplay, killerTarget),
		killedBy(3, firstMutant, firstDisplay, killerTarget),
	)

	result := auditFixture(t, stream, recorded)
	if result.pairs != 1 {
		t.Errorf("audited %d kill pairs, want the one pair the two executions prove", result.pairs)
	}
	if result.killedExecutions != 2 {
		t.Errorf("counted %d killed executions, want 2", result.killedExecutions)
	}
}

func TestAuditIgnoresAnExecutionThatDidNotKill(t *testing.T) {
	t.Parallel()
	// A survived execution proves nothing about reach: the target ran the
	// mutant and it lived. Only a kill is a pair the rule must preserve.
	recorded := recordedEvidence(t, map[string][]string{
		killerTarget: {ran(10, 2, 12, 16), linked(20, 2, 24, 3)},
	})
	stream := recordedTrace(t,
		blockRoute(1, firstMutant, 21, 4, killerTarget),
		mutantEvent(2, trace.MutantRecord{
			ID: firstMutant, DisplayID: firstDisplay,
			Args: []string{"-test.run=^" + testNameOf(killerTarget) + "$"}, Outcome: "survived",
		}),
	)

	result := auditFixture(t, stream, recorded)
	if result.killedExecutions != 0 || result.pairs != 0 || len(result.violations) != 0 {
		t.Errorf("a surviving execution was audited: %d killed executions, %d pairs, %d violations",
			result.killedExecutions, result.pairs, len(result.violations))
	}
}

func TestAuditToleratesATruncatedTrailingLine(t *testing.T) {
	t.Parallel()
	// A run that was interrupted leaves its last line half written. That is
	// what an interrupted recording looks like rather than a deviation, so the
	// audit reports the whole recording it did read and counts the fragment.
	recorded := recordedEvidence(t, map[string][]string{
		killerTarget: {ran(10, 2, 12, 16)},
	})
	stream := recordedTrace(t,
		blockRoute(1, firstMutant, 11, 4, killerTarget),
		killedBy(2, firstMutant, firstDisplay, killerTarget),
	) + `{"seq":3,"type":"mutant-exec","timestamp":"2026-01-01T00:0`

	result := auditFixture(t, stream, recorded)
	if result.truncatedLines != 1 {
		t.Errorf("counted %d truncated trailing lines, want 1", result.truncatedLines)
	}
	if result.pairs != 1 {
		t.Errorf("audited %d kill pairs before the fragment, want 1", result.pairs)
	}
	if len(result.violations) != 0 {
		t.Errorf("reported %d violations, want none: %+v", len(result.violations), result.violations)
	}
}

func TestAuditRejectsAMalformedLineBeforeTheEnd(t *testing.T) {
	t.Parallel()
	// A broken line with a whole recording behind it is not an interrupted
	// run: something wrote a line no reader can trust, and an audit that
	// skipped it would be auditing less than it claims.
	recorded := recordedEvidence(t, map[string][]string{
		killerTarget: {ran(10, 2, 12, 16)},
	})
	stream := "{\"seq\":1,\"type\":\"route\"\n" + recordedTrace(t, killedBy(2, firstMutant, firstDisplay, killerTarget))

	if _, err := auditTrace(strings.NewReader(stream), recorded, auditLayers()); err == nil {
		t.Fatal("a malformed line in the middle of a recording was accepted")
	} else if !strings.Contains(err.Error(), "line 1") {
		t.Errorf("the error is %q, want it to name the line it refused", err)
	}
}

func TestAuditOrdersViolationsByPosition(t *testing.T) {
	t.Parallel()
	// The report is read as a list of places to look at, so it is ordered by
	// where the mutants are and not by the order the run happened to execute
	// them in.
	recorded := recordedEvidence(t, map[string][]string{
		killerTarget: {ran(10, 2, 12, 16), linked(20, 2, 24, 3), linked(30, 2, 34, 3)},
	})
	stream := recordedTrace(t,
		blockRoute(1, firstMutant, 31, 4, killerTarget),
		killedBy(2, firstMutant, firstDisplay, killerTarget),
		blockRoute(3, secondMutant, 21, 4, killerTarget),
		killedBy(4, secondMutant, secondDisplay, killerTarget),
		blockRoute(5, thirdMutant, 21, 2, killerTarget),
		killedBy(6, thirdMutant, thirdDisplay, killerTarget),
	)

	result := auditFixture(t, stream, recorded)
	got := make([]string, 0, len(result.violations))
	for _, violation := range result.violations {
		got = append(got, fmt.Sprintf("%d.%d", violation.pair.line, violation.pair.column))
	}
	want := []string{"21.2", "21.4", "31.4"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("the violations are ordered %v, want %v", got, want)
	}
}

func TestReadEvidenceReportsAProfileItCannotParse(t *testing.T) {
	t.Parallel()
	directory := writeProfiles(t, map[string][]string{killerTarget: {"not a coverage line"}})

	_, err := readEvidence(directory, fixtureModule)
	if err == nil {
		t.Fatal("a malformed coverage profile was accepted")
	}
	if !strings.Contains(err.Error(), killerTarget+".cover") {
		t.Errorf("the error is %q, want it to name the profile it could not read", err)
	}
}

func TestReadEvidenceReportsADirectoryItCannotRead(t *testing.T) {
	t.Parallel()
	missing := filepath.Join(t.TempDir(), "absent")

	_, err := readEvidence(missing, fixtureModule)
	if err == nil {
		t.Fatal("a missing profile directory was accepted")
	}
	if !strings.Contains(err.Error(), missing) {
		t.Errorf("the error is %q, want it to name the directory it could not read", err)
	}
}

func TestReadEvidenceReadsTheProfilesAndNothingElse(t *testing.T) {
	t.Parallel()
	// A temporary directory of a run holds more than profiles. Only the
	// profiles are evidence, and a file that is not one is left alone rather
	// than refused, so an audit runs against the directory a run really left.
	directory := writeProfiles(t, map[string][]string{
		killerTarget: {ran(10, 2, 12, 16)},
		secondTarget: {ran(20, 2, 24, 3)},
	})
	if err := os.WriteFile(filepath.Join(directory, "targets.json"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(directory, "nested.cover"), 0o755); err != nil {
		t.Fatal(err)
	}

	recorded, err := readEvidence(directory, fixtureModule)
	if err != nil {
		t.Fatalf("read the recorded evidence: %v", err)
	}
	if len(recorded.targets) != 2 {
		t.Errorf("read %d targets, want the 2 profiles of the directory", len(recorded.targets))
	}
	// The instrumented union is what the outside-blocks fallback is decided
	// on, so it has to hold the blocks of every profile rather than of one.
	for _, position := range []struct{ line, column int }{{11, 4}, {21, 4}} {
		if !recorded.instrumentedAt(subjectPath, position.line, position.column) {
			t.Errorf("the instrumented union does not contain %d.%d, which a profile named",
				position.line, position.column)
		}
	}
}
