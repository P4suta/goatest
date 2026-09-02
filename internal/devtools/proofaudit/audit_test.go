// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
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
	// fuzzTarget is the fixture target whose test is a fuzz target. Go names
	// one by its Fuzz prefix and nothing else, so the fixture says it in the
	// name and the rule reads it from there.
	fuzzTarget = "e1f203142536475a"

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
	sixthMutant   = "f1a2b3c4d5e60718293a4b5c6d7e8f90f1a2b3c4d5e60718293a4b5c6d7e8f90"
	sixthDisplay  = "f1a2b3c4d5e60718293a"
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
	return batchedRoute(seq, mutant, line, column, targets, plan)
}

// batchedRoute is the route shape a run records once it reaches more targets
// than it executes one at a time: the plan holds one entry per execution, so it
// is shorter than the reaching set, and a batch of a single target is rendered
// exactly like an individual run. Past the individual prefix the two lists no
// longer line up, which is why the position of a plan entry says nothing about
// which target an execution belonged to.
func batchedRoute(seq int64, mutant string, line, column int, targets, plan []string) trace.Event {
	return routeEvent(seq, trace.RouteRecord{
		MutantID: mutant, Rule: "eq-to-neq", Path: subjectPath, Line: line, Column: column,
		ReachingTargets: targets, Plan: plan, Reason: trace.ReasonCoverageReaching,
		Granularity: trace.GranularityBlock, FileCandidates: len(targets),
	})
}

// testNameOf is the test a fixture target runs. A kill is attributed through
// the baseline measurements, so the name only has to be the same in the
// measurement of a target and in the execution that names it.
func testNameOf(target string) string {
	if target == fuzzTarget {
		return "FuzzTarget" + strings.ToUpper(target[:2])
	}
	return "TestTarget" + strings.ToUpper(target[:2])
}

// The plan entries a run records. A plan holds one entry per execution rather
// than one per reaching target, and a batch of a single target is rendered
// exactly like an individual run, so it is a recording shape the fixtures
// reproduce and never an identity the audit reads.
const (
	individualPlan   = "individual:"
	packageSuitePlan = "package-suite"
)

// measured is the baseline measurement of one target, in the package and under
// the test name the fixtures use by default.
func measured(seq int64, target string) trace.Event {
	return measuredIn(seq, target, fixtureModule+"/pkg", testNameOf(target))
}

// measuredIn is the baseline measurement of one target: the command a run
// executes to record what that target covers. Its arguments are the only place
// in a recording where a target's identity, the test it runs and the package it
// runs in meet, which is what a kill is attributed through.
func measuredIn(seq int64, target, packagePath, test string) trace.Event {
	return trace.Event{Seq: seq, Type: trace.TypeExec, Timestamp: fixtureTime, Exec: &trace.ExecRecord{
		Argv: []string{
			"go", "tool", "test2json", "-t", "-p", packagePath,
			"/tmp/goatest-baseline/" + target + ".test", "-test.v=test2json",
			"-test.run=^" + test + "$",
			"-test.coverprofile=/tmp/goatest-baseline/" + target + profileSuffix,
			"-test.count=1",
		},
	}}
}

// killedBy is one execution of a mutant that its target killed.
func killedBy(seq int64, mutant, display, target string) trace.Event {
	return killedIn(seq, mutant, display, fixtureModule+"/pkg", testNameOf(target))
}

// killedIn is one execution of a mutant that a named test of a named package
// killed. A recording says which package an execution ran in, and a test name
// is only unique within one.
func killedIn(seq int64, mutant, display, packagePath, test string) trace.Event {
	return mutantEvent(seq, trace.MutantRecord{
		ID: mutant, DisplayID: display, Package: packagePath,
		Args: []string{"-test.run=^" + test + "$"}, Outcome: outcomeKilled, DurationMS: 5,
	})
}

// executedBy is one execution of a mutant against a single target's test,
// whatever became of it. The executions a proof would remove are the ones no
// kill has to be preserved from, so the savings measurement counts these and
// not only the kills.
func executedBy(seq int64, mutant, display, target, outcome string) trace.Event {
	return mutantEvent(seq, trace.MutantRecord{
		ID: mutant, DisplayID: display, Package: fixtureModule + "/pkg",
		Args: []string{"-test.run=^" + testNameOf(target) + "$"}, Outcome: outcome, DurationMS: 7,
	})
}

// gatedBody is the span of a branch body a proof names.
func gatedBody(startLine, startColumn, endLine, endColumn int) *branchProof {
	return &branchProof{
		BodyStartLine: startLine, BodyStartColumn: startColumn,
		BodyEndLine: endLine, BodyEndColumn: endColumn,
	}
}

// cataloguedMutant is one entry of a fixture catalog: where the engine placed
// the mutant, and the body its condition gates when it proved one.
func cataloguedMutant(id string, line, column int, proof *branchProof) catalogMutant {
	return catalogMutant{ID: id, Path: subjectPath, Line: line, Column: column, Branch: proof}
}

// fixtureCatalog is a catalog listing exactly the given mutants.
func fixtureCatalog(mutants ...catalogMutant) *mutantCatalog {
	catalog := &mutantCatalog{mutants: make(map[string]catalogMutant, len(mutants))}
	for _, mutant := range mutants {
		catalog.mutants[mutant.ID] = mutant
	}
	return catalog
}

// recordedRun renders the recording of a run that measured the given targets
// before it did the given work. Every real recording opens this way — a run
// measures each target's coverage before it mutates anything — and a kill is
// attributed through those measurements, so a fixture without them would be a
// recording no run ever wrote.
func recordedRun(t *testing.T, measurements []string, events ...trace.Event) string {
	t.Helper()
	recording := make([]trace.Event, 0, len(measurements)+len(events))
	for index, target := range measurements {
		recording = append(recording, measured(int64(index+1), target))
	}
	return recordedTrace(t, append(recording, events...)...)
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

// auditFixture runs the layers of a run audited without a catalog — the reach
// layer alone — over a fixture recording.
func auditFixture(t *testing.T, stream string, recorded evidence) auditResult {
	t.Helper()
	return auditWithCatalog(t, stream, recorded, nil)
}

// auditWithCatalog runs every layer a catalog enables over a fixture recording.
func auditWithCatalog(t *testing.T, stream string, recorded evidence, catalog *mutantCatalog) auditResult {
	t.Helper()
	result, err := auditTrace(strings.NewReader(stream), recorded, catalog, auditLayers(catalog))
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
	stream := recordedRun(t, []string{killerTarget},
		blockRoute(2, firstMutant, 21, 4, killerTarget),
		killedBy(3, firstMutant, firstDisplay, killerTarget),
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
	stream := recordedRun(t, []string{killerTarget},
		blockRoute(2, firstMutant, 11, 4, killerTarget),
		killedBy(3, firstMutant, firstDisplay, killerTarget),
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
	stream := recordedRun(t, []string{killerTarget, secondTarget},
		blockRoute(3, firstMutant, 11, 4, killerTarget, secondTarget),
		killedBy(4, firstMutant, firstDisplay, killerTarget),
		killedBy(5, firstMutant, firstDisplay, secondTarget),
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
			stream := recordedRun(t, []string{killerTarget},
				blockRoute(2, firstMutant, position.line, position.column, killerTarget),
				killedBy(3, firstMutant, firstDisplay, killerTarget),
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
	stream := recordedRun(t, []string{killerTarget, secondTarget},
		blockRoute(3, firstMutant, 40, 1, killerTarget),
		killedBy(4, firstMutant, firstDisplay, killerTarget),
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
	stream := recordedRun(t, []string{killerTarget, absentTarget},
		blockRoute(3, firstMutant, 21, 4, absentTarget),
		killedBy(4, firstMutant, firstDisplay, absentTarget),
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
	stream := recordedRun(t, []string{killerTarget},
		routeEvent(2, trace.RouteRecord{
			MutantID: firstMutant, Rule: "eq-to-neq", Path: subjectPath, Line: 21, Column: 4,
			Plan: []string{packageSuitePlan}, Reason: trace.ReasonUnreached,
			Granularity: trace.GranularityBlock,
		}),
		mutantEvent(3, trace.MutantRecord{
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
	stream := recordedRun(t, []string{killerTarget, secondTarget},
		blockRoute(3, firstMutant, 21, 4, killerTarget, secondTarget),
		mutantEvent(4, trace.MutantRecord{
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
	// A kill with no route at all and an execution that selected no test — the
	// fuzzing of a target asks for "^$" — are recordings the audit cannot read
	// as a pair: there is no target to check the rule against. They are counted
	// so that a trace whose halves disagree is visible rather than silently
	// narrowing what was audited.
	recorded := recordedEvidence(t, map[string][]string{
		killerTarget: {ran(10, 2, 12, 16)},
	})
	cases := []struct {
		name   string
		events []trace.Event
	}{
		{
			name:   "a kill with no route recorded before it",
			events: []trace.Event{killedBy(2, firstMutant, firstDisplay, killerTarget)},
		},
		{
			name: "an execution that selected no test",
			events: []trace.Event{
				blockRoute(2, firstMutant, 11, 4, killerTarget),
				mutantEvent(3, trace.MutantRecord{
					ID: firstMutant, DisplayID: firstDisplay,
					Args: []string{"-test.run=^$", "-test.fuzz=^FuzzTarget$"}, Outcome: outcomeKilled,
				}),
			},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			result := auditFixture(t, recordedRun(t, []string{killerTarget}, testCase.events...), recorded)
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

	_, err := auditTrace(failingReader{err: broken}, evidence{}, nil, auditLayers(nil))
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
	stream := recordedRun(t, []string{killerTarget},
		blockRoute(2, firstMutant, 11, 4, killerTarget),
		killedBy(3, firstMutant, firstDisplay, killerTarget),
		killedBy(4, firstMutant, firstDisplay, killerTarget),
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
	stream := recordedRun(t, []string{killerTarget},
		blockRoute(2, firstMutant, 21, 4, killerTarget),
		mutantEvent(3, trace.MutantRecord{
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
	stream := recordedRun(t, []string{killerTarget},
		blockRoute(2, firstMutant, 11, 4, killerTarget),
		killedBy(3, firstMutant, firstDisplay, killerTarget),
	) + `{"seq":4,"type":"mutant-exec","timestamp":"2026-01-01T00:0`

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

	if _, err := auditTrace(strings.NewReader(stream), recorded, nil, auditLayers(nil)); err == nil {
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
	stream := recordedRun(t, []string{killerTarget},
		blockRoute(2, firstMutant, 31, 4, killerTarget),
		killedBy(3, firstMutant, firstDisplay, killerTarget),
		blockRoute(4, secondMutant, 21, 4, killerTarget),
		killedBy(5, secondMutant, secondDisplay, killerTarget),
		blockRoute(6, thirdMutant, 21, 2, killerTarget),
		killedBy(7, thirdMutant, thirdDisplay, killerTarget),
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

// layerNames is the audit's layer list, which is what a catalog changes.
func layerNames(layers []layer) []string {
	names := make([]string, 0, len(layers))
	for _, audited := range layers {
		names = append(names, audited.name)
	}
	return names
}

func TestAuditLayersAddsTheBranchLayerOnlyWithACatalog(t *testing.T) {
	t.Parallel()
	// The branch layer decides by a proof only a catalog carries, so a run
	// audited without one is a run the layer was not held to. Adding it
	// silently would be worse than not adding it: a reader would take the
	// missing row for a clean one.
	if got := layerNames(auditLayers(nil)); slices.Contains(got, branchLayerName) {
		t.Errorf("without a catalog the audit runs %v, want no %q layer", got, branchLayerName)
	}
	got := layerNames(auditLayers(fixtureCatalog()))
	if !slices.Equal(got, []string{reachLayerName, branchLayerName, infectionLayerName}) {
		t.Errorf("with a catalog the audit runs %v, want %q after %q", got, branchLayerName, reachLayerName)
	}
}

func TestDecideBranchKeepsEveryKillerItCannotProveInert(t *testing.T) {
	t.Parallel()
	// The subject of every case below: a condition at 20:4 gating a body that
	// runs from its opening brace at 20:15 to its closing one at 22:3. The
	// mutated condition implies the original, so a test during which no
	// statement of that body ran cannot observe the mutation — and every other
	// state of the evidence keeps the killer.
	cases := []struct {
		name     string
		catalog  *mutantCatalog
		profiles map[string][]string
		target   string
		want     conclusion
		why      string
	}{
		{
			name:     "a mutant the catalog does not list",
			catalog:  fixtureCatalog(),
			profiles: map[string][]string{killerTarget: {ran(20, 15, 22, 3)}},
			target:   killerTarget,
			want:     unverifiable,
			why:      whyNotInCatalog,
		},
		{
			name:     "a mutant the catalog lists without a proof",
			catalog:  fixtureCatalog(cataloguedMutant(firstMutant, 20, 4, nil)),
			profiles: map[string][]string{killerTarget: {ran(20, 15, 22, 3)}},
			target:   killerTarget,
			want:     inapplicable,
		},
		{
			name:     "a body that ends before it starts",
			catalog:  fixtureCatalog(cataloguedMutant(firstMutant, 20, 4, gatedBody(22, 3, 20, 15))),
			profiles: map[string][]string{killerTarget: {ran(20, 15, 22, 3)}},
			target:   killerTarget,
			want:     kept,
		},
		{
			name:     "a body coordinate below one",
			catalog:  fixtureCatalog(cataloguedMutant(firstMutant, 20, 4, gatedBody(20, 15, 22, 0))),
			profiles: map[string][]string{killerTarget: {ran(20, 15, 22, 3)}},
			target:   killerTarget,
			want:     kept,
		},
		{
			name:     "a mutation at the start of the body it gates",
			catalog:  fixtureCatalog(cataloguedMutant(firstMutant, 20, 15, gatedBody(20, 15, 22, 3))),
			profiles: map[string][]string{killerTarget: {ran(20, 15, 22, 3)}},
			target:   killerTarget,
			want:     kept,
		},
		{
			name:     "a mutation inside the body it gates",
			catalog:  fixtureCatalog(cataloguedMutant(firstMutant, 21, 3, gatedBody(20, 15, 22, 3))),
			profiles: map[string][]string{killerTarget: {ran(20, 15, 22, 3)}},
			target:   killerTarget,
			want:     kept,
		},
		{
			name:     "a body no profile instrumented",
			catalog:  fixtureCatalog(cataloguedMutant(firstMutant, 20, 4, gatedBody(20, 15, 22, 3))),
			profiles: map[string][]string{killerTarget: {ran(10, 2, 12, 16)}},
			target:   killerTarget,
			want:     kept,
		},
		{
			name:     "a body whose first block starts at its brace",
			catalog:  fixtureCatalog(cataloguedMutant(firstMutant, 20, 4, gatedBody(20, 15, 22, 3))),
			profiles: map[string][]string{killerTarget: {ran(10, 2, 12, 16), ran(20, 15, 22, 3)}},
			target:   killerTarget,
			want:     kept,
		},
		{
			name:     "a body whose first block starts at its first statement",
			catalog:  fixtureCatalog(cataloguedMutant(firstMutant, 20, 4, gatedBody(20, 15, 22, 3))),
			profiles: map[string][]string{killerTarget: {ran(10, 2, 12, 16), ran(21, 3, 22, 3)}},
			target:   killerTarget,
			want:     kept,
		},
		{
			name:    "a block that starts before the body and ends inside it",
			catalog: fixtureCatalog(cataloguedMutant(firstMutant, 20, 4, gatedBody(20, 15, 22, 3))),
			profiles: map[string][]string{
				killerTarget: {ran(18, 2, 21, 10)},
				secondTarget: {linked(20, 15, 22, 3)},
			},
			target: killerTarget,
			want:   discharged,
			why:    whyBodyNeverTaken,
		},
		{
			name:     "a block that starts where the body ends",
			catalog:  fixtureCatalog(cataloguedMutant(firstMutant, 20, 4, gatedBody(20, 15, 22, 3))),
			profiles: map[string][]string{killerTarget: {linked(20, 15, 22, 3), ran(22, 3, 24, 2)}},
			target:   killerTarget,
			want:     kept,
		},
		{
			name:     "a block that starts one column past the body",
			catalog:  fixtureCatalog(cataloguedMutant(firstMutant, 20, 4, gatedBody(20, 15, 22, 3))),
			profiles: map[string][]string{killerTarget: {linked(20, 15, 22, 3), ran(22, 4, 24, 2)}},
			target:   killerTarget,
			want:     discharged,
			why:      whyBodyNeverTaken,
		},
		{
			name:     "a body and its mutation on one line",
			catalog:  fixtureCatalog(cataloguedMutant(firstMutant, 20, 4, gatedBody(20, 15, 20, 40))),
			profiles: map[string][]string{killerTarget: {ran(20, 20, 20, 39)}},
			target:   killerTarget,
			want:     kept,
		},
		{
			name:     "a block past a body that shares the mutation's line",
			catalog:  fixtureCatalog(cataloguedMutant(firstMutant, 20, 4, gatedBody(20, 15, 20, 40))),
			profiles: map[string][]string{killerTarget: {linked(20, 20, 20, 39), ran(20, 41, 20, 60)}},
			target:   killerTarget,
			want:     discharged,
			why:      whyBodyNeverTaken,
		},
		{
			name:     "a killer that left no profile",
			catalog:  fixtureCatalog(cataloguedMutant(firstMutant, 20, 4, gatedBody(20, 15, 22, 3))),
			profiles: map[string][]string{killerTarget: {ran(20, 15, 22, 3)}},
			target:   absentTarget,
			want:     unverifiable,
			why:      whyNoProfile,
		},
		{
			name:     "a killer that covers no block of the file",
			catalog:  fixtureCatalog(cataloguedMutant(firstMutant, 20, 4, gatedBody(20, 15, 22, 3))),
			profiles: map[string][]string{killerTarget: {linked(20, 15, 22, 3)}},
			target:   killerTarget,
			want:     discharged,
			why:      whyBodyNeverTaken,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			recorded := recordedEvidence(t, testCase.profiles)
			pair := killPair{mutant: firstMutant, path: subjectPath, target: testCase.target}

			got := decideBranch(testCase.catalog, pair, recorded)
			if got.conclusion != testCase.want {
				t.Errorf("decideBranch concluded %v, want %v", got.conclusion, testCase.want)
			}
			if got.why != testCase.why {
				t.Errorf("decideBranch explained itself as %q, want %q", got.why, testCase.why)
			}
		})
	}
}

func TestAuditCountsWhatTheBranchLayerHasNoProofFor(t *testing.T) {
	t.Parallel()
	// A mutant the layer has no proof for is not a mutant it keeps: it is one
	// the layer changes nothing about. Counting it as kept would let a layer
	// that proves almost nothing report almost every pair as its own.
	recorded := recordedEvidence(t, map[string][]string{
		killerTarget: {ran(10, 2, 12, 16), ran(20, 15, 22, 3)},
	})
	catalog := fixtureCatalog(
		cataloguedMutant(firstMutant, 20, 4, gatedBody(20, 15, 22, 3)),
		cataloguedMutant(secondMutant, 11, 4, nil),
	)
	stream := recordedRun(t, []string{killerTarget},
		blockRoute(2, firstMutant, 20, 4, killerTarget),
		killedBy(3, firstMutant, firstDisplay, killerTarget),
		blockRoute(4, secondMutant, 11, 4, killerTarget),
		killedBy(5, secondMutant, secondDisplay, killerTarget),
	)

	result := auditWithCatalog(t, stream, recorded, catalog)
	if len(result.layers) != 2 {
		t.Fatalf("audited %d layers, want the reach layer and the branch layer", len(result.layers))
	}
	branch := result.layers[1]
	if branch.name != branchLayerName {
		t.Fatalf("the second layer is %q, want %q", branch.name, branchLayerName)
	}
	if branch.audited != 2 || branch.kept != 1 || branch.inapplicable != 1 {
		t.Errorf("the branch layer audited %+v, want one pair it keeps and one it has no proof for", branch)
	}
	if result.layers[0].inapplicable != 0 {
		t.Errorf("the reach layer reported %d pairs it does not apply to, want none", result.layers[0].inapplicable)
	}
	if len(result.violations) != 0 || len(result.unverifiable) != 0 {
		t.Errorf("a sound recording reported %d violations and %d unverifiable pairs, want none",
			len(result.violations), len(result.unverifiable))
	}
}

func TestAuditFailsWhenAKillerNeverTookTheBodyItsMutationGates(t *testing.T) {
	t.Parallel()
	// The killer ran the condition and never a statement of the body behind
	// it. The proof says such a target cannot observe the mutation, and the
	// recording says it killed the mutant, so one of the two is wrong and the
	// audit prints which pair to look at.
	recorded := recordedEvidence(t, map[string][]string{
		killerTarget: {ran(18, 2, 20, 15), linked(20, 15, 22, 3)},
	})
	catalog := fixtureCatalog(cataloguedMutant(firstMutant, 20, 4, gatedBody(20, 15, 22, 3)))
	stream := recordedRun(t, []string{killerTarget},
		blockRoute(2, firstMutant, 20, 4, killerTarget),
		killedBy(3, firstMutant, firstDisplay, killerTarget),
	)

	result := auditWithCatalog(t, stream, recorded, catalog)
	if len(result.violations) != 1 {
		t.Fatalf("reported %d violations, want exactly 1: %+v", len(result.violations), result.violations)
	}
	violation := result.violations[0]
	if violation.layer != branchLayerName {
		t.Errorf("the violation names layer %q, want %q", violation.layer, branchLayerName)
	}
	if violation.why != whyBodyNeverTaken {
		t.Errorf("the violation explains itself as %q, want %q", violation.why, whyBodyNeverTaken)
	}
	if result.layers[1].violations != 1 {
		t.Errorf("the branch layer counted %d violations, want 1", result.layers[1].violations)
	}
}

func TestAuditMeasuresWhatTheBranchLayerWouldHaveSaved(t *testing.T) {
	t.Parallel()
	// Soundness is the invariant; this is the value. The recording was made by
	// a run that discharged nothing, so what the layer would have bought is
	// read off the routes and the profiles rather than off a field the trace
	// does not carry.
	recorded := recordedEvidence(t, map[string][]string{
		killerTarget: {ran(10, 2, 12, 16), ran(20, 15, 22, 3), linked(30, 15, 32, 3)},
		secondTarget: {ran(10, 2, 12, 16), linked(20, 15, 22, 3), linked(30, 15, 32, 3)},
		thirdTarget:  {ran(10, 2, 12, 16)},
		fuzzTarget:   {ran(10, 2, 12, 16)},
	})
	catalog := fixtureCatalog(
		cataloguedMutant(firstMutant, 20, 4, gatedBody(20, 15, 22, 3)),
		cataloguedMutant(secondMutant, 30, 4, gatedBody(30, 15, 32, 3)),
		cataloguedMutant(thirdMutant, 11, 4, nil),
	)
	stream := recordedTrace(t,
		measured(1, killerTarget), measured(2, secondTarget), measured(3, thirdTarget), measured(4, fuzzTarget),
		blockRoute(5, firstMutant, 20, 4, secondTarget, thirdTarget, fuzzTarget),
		executedBy(6, firstMutant, firstDisplay, secondTarget, "survived"),
		executedBy(7, firstMutant, firstDisplay, fuzzTarget, "survived"),
		mutantEvent(8, trace.MutantRecord{
			ID: firstMutant, DisplayID: firstDisplay, Package: fixtureModule + "/pkg",
			Args:    []string{"-test.run=^(" + testNameOf(secondTarget) + "|" + testNameOf(thirdTarget) + ")$"},
			Outcome: "survived",
		}),
		mutantEvent(9, trace.MutantRecord{
			ID: firstMutant, DisplayID: firstDisplay, Package: fixtureModule + "/other",
			Args:    []string{"-test.run=^" + testNameOf(thirdTarget) + "$"},
			Outcome: "survived",
		}),
		blockRoute(10, secondMutant, 30, 4, secondTarget),
		executedBy(11, secondMutant, secondDisplay, secondTarget, "survived"),
		blockRoute(12, thirdMutant, 11, 4, killerTarget),
		executedBy(13, thirdMutant, thirdDisplay, killerTarget, "survived"),
	)

	result := auditWithCatalog(t, stream, recorded, catalog)
	want := dischargeSavings{routes: 2, reaching: 4, discharged: 3, emptied: 1, executions: 2}
	if result.branch != want {
		t.Errorf("the audit measured %+v, want %+v", result.branch, want)
	}
}

func TestAuditMeasuresNoSavingWithoutACatalog(t *testing.T) {
	t.Parallel()
	// Without a catalog the layer is not audited at all, and a savings block
	// of zeroes would read as a layer that buys nothing rather than as one
	// nobody measured.
	recorded := recordedEvidence(t, map[string][]string{killerTarget: {ran(20, 15, 22, 3)}})
	stream := recordedTrace(t,
		measured(1, killerTarget),
		blockRoute(2, firstMutant, 20, 4, killerTarget),
		killedBy(3, firstMutant, firstDisplay, killerTarget),
	)

	result := auditFixture(t, stream, recorded)
	if result.branchAudited {
		t.Error("a run audited without a catalog reports the branch layer as audited")
	}
	if (result.branch != dischargeSavings{}) {
		t.Errorf("a run audited without a catalog measured %+v, want nothing", result.branch)
	}
}

func TestAuditRefusesToDischargeAnUninstrumentedBody(t *testing.T) {
	t.Parallel()
	// A body no profile instrumented is a gap in the measurement, not proof
	// that nothing ran it: cmd/cover may simply not have cut a block there.
	// Nothing is discharged, so nothing is saved either.
	recorded := recordedEvidence(t, map[string][]string{
		killerTarget: {ran(10, 2, 12, 16)},
		secondTarget: {ran(10, 2, 12, 16)},
	})
	catalog := fixtureCatalog(cataloguedMutant(firstMutant, 20, 4, gatedBody(20, 15, 22, 3)))
	stream := recordedTrace(t,
		measured(1, killerTarget), measured(2, secondTarget),
		blockRoute(3, firstMutant, 20, 4, killerTarget, secondTarget),
		killedBy(4, firstMutant, firstDisplay, killerTarget),
	)

	result := auditWithCatalog(t, stream, recorded, catalog)
	if len(result.violations) != 0 {
		t.Fatalf("reported %d violations over an uninstrumented body, want none: %+v",
			len(result.violations), result.violations)
	}
	if (result.branch != dischargeSavings{}) {
		t.Errorf("an uninstrumented body measured %+v, want nothing", result.branch)
	}
}

func TestAuditAttributesAKillToTheTargetThatMeasuredItsTest(t *testing.T) {
	t.Parallel()
	// A route's plan holds one entry per execution and not one per reaching
	// target: past the targets a run executes one at a time the rest are
	// batched, and a batch of a single target is rendered exactly like an
	// individual run. The two lists stop lining up there, so the position of a
	// plan entry is not the identity of a target. The identity comes from the
	// baseline measurements, which say which target ran which test.
	//
	// Here the plan names the first and the third target while the second sits
	// between them in the reaching set. The kill belongs to the third target,
	// whose profile never ran the mutated block; reading it off the plan
	// position would credit the second target, whose profile did, and the audit
	// would report a clean run over a killer a layer drops.
	recorded := recordedEvidence(t, map[string][]string{
		killerTarget: {ran(10, 2, 12, 16), linked(20, 2, 24, 3)},
		secondTarget: {ran(10, 2, 12, 16), ran(20, 2, 24, 3)},
		thirdTarget:  {ran(10, 2, 12, 16), linked(20, 2, 24, 3)},
	})
	stream := recordedRun(t, []string{killerTarget, secondTarget, thirdTarget},
		batchedRoute(4, firstMutant, 21, 4,
			[]string{killerTarget, secondTarget, thirdTarget},
			[]string{individualPlan + testNameOf(killerTarget), individualPlan + testNameOf(thirdTarget)}),
		killedBy(5, firstMutant, firstDisplay, thirdTarget),
	)

	// Attribution is not one layer's business: it decides the pair every layer
	// is held to, so it has to hold with the reach layer alone.
	result := auditFixture(t, stream, recorded)
	if len(result.layers) != 1 || result.layers[0].name != reachLayerName {
		t.Fatalf("the audit ran %+v, want the reach layer alone", result.layers)
	}
	if result.pairs != 1 || result.unattributedKills != 0 {
		t.Fatalf("audited %d kill pairs and counted %d unattributed kills, want 1 and 0",
			result.pairs, result.unattributedKills)
	}
	if len(result.violations) != 1 {
		t.Fatalf("reported %d violations, want the killer the layer would drop: %+v",
			len(result.violations), result.violations)
	}
	violation := result.violations[0]
	if violation.pair.target != thirdTarget {
		t.Errorf("the kill was attributed to %q, want %q, the target whose measurement ran %q",
			violation.pair.target, thirdTarget, testNameOf(thirdTarget))
	}
	if violation.pair.killer != testNameOf(thirdTarget) {
		t.Errorf("the pair names killer test %q, want %q", violation.pair.killer, testNameOf(thirdTarget))
	}
	if violation.why != whyOutsideCoveredBlocks {
		t.Errorf("the violation explains itself as %q, want the decision read off the attributed target's profile",
			violation.why)
	}
	// The route is still what says where the mutant is.
	if violation.pair.path != subjectPath || violation.pair.line != 21 || violation.pair.column != 4 {
		t.Errorf("the pair places the mutant at %s:%d:%d, want the position the route recorded",
			violation.pair.path, violation.pair.line, violation.pair.column)
	}
}

func TestAuditAttributesAKillByThePackageItRanIn(t *testing.T) {
	t.Parallel()
	// A test name is only unique within a package, and two packages of one
	// repository routinely define a test of the same name. An execution records
	// the package it ran in, and that is the half of the identity telling the
	// two apart — without it the audit would decide one target's kill against
	// another target's coverage.
	const shared = "TestTheSameNameInTwoPackages"
	recorded := recordedEvidence(t, map[string][]string{
		killerTarget: {ran(10, 2, 12, 16), ran(20, 2, 24, 3)},
		secondTarget: {ran(10, 2, 12, 16), linked(20, 2, 24, 3)},
	})
	cases := []struct {
		name        string
		packagePath string
		target      string
		violations  int
	}{
		{
			name: "the package whose target ran the mutated block", packagePath: fixtureModule + "/pkg",
			target: killerTarget, violations: 0,
		},
		{
			name: "the package whose target did not", packagePath: fixtureModule + "/other",
			target: secondTarget, violations: 1,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			stream := recordedTrace(t,
				measuredIn(1, killerTarget, fixtureModule+"/pkg", shared),
				measuredIn(2, secondTarget, fixtureModule+"/other", shared),
				batchedRoute(3, firstMutant, 21, 4,
					[]string{killerTarget, secondTarget},
					[]string{individualPlan + shared, individualPlan + shared}),
				killedIn(4, firstMutant, firstDisplay, testCase.packagePath, shared),
			)

			result := auditFixture(t, stream, recorded)
			if result.pairs != 1 || result.unattributedKills != 0 {
				t.Fatalf("audited %d kill pairs and counted %d unattributed kills, want 1 and 0",
					result.pairs, result.unattributedKills)
			}
			if len(result.violations) != testCase.violations {
				t.Fatalf("reported %d violations, want %d; the kill belongs to %q: %+v",
					len(result.violations), testCase.violations, testCase.target, result.violations)
			}
			if testCase.violations == 0 {
				if result.layers[0].kept != 1 {
					t.Errorf("the reach layer kept %d killers, want the one it reaches", result.layers[0].kept)
				}
				return
			}
			if got := result.violations[0].pair.target; got != testCase.target {
				t.Errorf("the kill was attributed to %q, want %q, the target measured in %q",
					got, testCase.target, testCase.packagePath)
			}
		})
	}
}

func TestAuditCountsAKillNoMeasurementNames(t *testing.T) {
	t.Parallel()
	// A killer the baseline never measured is a killer the audit cannot place:
	// no target's coverage is the one the rule would be checked against.
	// Guessing a target would be inventing the evidence the audit exists to
	// check, so the kill is counted and no layer decides it.
	recorded := recordedEvidence(t, map[string][]string{
		killerTarget: {ran(10, 2, 12, 16), linked(20, 2, 24, 3)},
	})
	cases := []struct{ name, packagePath, test string }{
		{name: "a test no measurement ran", packagePath: fixtureModule + "/pkg", test: "TestNobodyMeasured"},
		{name: "a measured test in another package", packagePath: fixtureModule + "/other", test: testNameOf(killerTarget)},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			stream := recordedRun(t, []string{killerTarget},
				batchedRoute(2, firstMutant, 21, 4,
					[]string{killerTarget}, []string{individualPlan + testCase.test}),
				killedIn(3, firstMutant, firstDisplay, testCase.packagePath, testCase.test),
			)

			result := auditFixture(t, stream, recorded)
			if result.unattributedKills != 1 {
				t.Errorf("counted %d unattributed kills, want 1", result.unattributedKills)
			}
			if result.pairs != 0 {
				t.Errorf("audited %d kill pairs, want none", result.pairs)
			}
			if result.layers[0].audited != 0 {
				t.Errorf("the reach layer decided %d pairs, want none", result.layers[0].audited)
			}
			if len(result.violations) != 0 || len(result.unverifiable) != 0 {
				t.Errorf("a kill no measurement names was decided: %d violations, %d unverifiable",
					len(result.violations), len(result.unverifiable))
			}
		})
	}
}
