// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/P4suta/goatest/internal/filemode"
	"github.com/P4suta/goatest/internal/trace"
)

const (
	fixtureModule       = "example.com/audited"
	fixtureBinaryPrefix = "/tmp/goatest-baseline/fixture-"

	subjectPath = "pkg/subject.go"

	fixtureTime = "2026-01-01T00:00:00Z"
)

const (
	killerTarget = "a1b2c3d4e5f60718"
	secondTarget = "b1c2d3e4f5061728"
	thirdTarget  = "d1e2f30415162738"
	absentTarget = "c1d2e3f405162738"

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

func ran(startLine, startColumn, endLine, endColumn int) string {
	return profileLine(startLine, startColumn, endLine, endColumn, 1)
}

func linked(startLine, startColumn, endLine, endColumn int) string {
	return profileLine(startLine, startColumn, endLine, endColumn, 0)
}

func profileLine(startLine, startColumn, endLine, endColumn, count int) string {
	return fmt.Sprintf("%s/%s:%d.%d,%d.%d 1 %d",
		fixtureModule, subjectPath, startLine, startColumn, endLine, endColumn, count)
}

func writeProfiles(t *testing.T, profiles map[string][]string) string {
	t.Helper()
	directory := t.TempDir()
	for target, lines := range profiles {
		body := "mode: set\n" + strings.Join(lines, "\n") + "\n"
		if err := os.WriteFile(filepath.Join(directory, target+".cover"), []byte(body), filemode.ReadableFile); err != nil {
			t.Fatalf("write the profile of %s: %v", target, err)
		}
	}
	return directory
}

func recordedEvidence(t *testing.T, profiles map[string][]string) evidence {
	t.Helper()
	recorded, err := readEvidence(writeProfiles(t, profiles), fixtureModule)
	if err != nil {
		t.Fatalf("read the recorded evidence: %v", err)
	}
	return recorded
}

func routeEvent(seq int64, record trace.RouteRecord) trace.Event {
	return trace.Event{Seq: seq, Type: trace.TypeRoute, Timestamp: fixtureTime, Route: &record}
}

func mutantEvent(seq int64, record trace.MutantRecord) trace.Event {
	return trace.Event{Seq: seq, Type: trace.TypeMutantExec, Timestamp: fixtureTime, Mutant: &record}
}

func blockRoute(seq int64, mutant string, line, column int, targets ...string) trace.Event {
	plan := make([]string, 0, len(targets))
	for _, target := range targets {
		plan = append(plan, individualPlan+testNameOf(target))
	}
	return batchedRoute(seq, mutant, line, column, targets, plan)
}

func batchedRoute(seq int64, mutant string, line, column int, targets, plan []string) trace.Event {
	return routeEvent(seq, trace.RouteRecord{
		MutantID: mutant, Rule: "eq-to-neq", Path: subjectPath, Line: line, Column: column,
		ReachingTargets: targets, Plan: plan, Reason: trace.ReasonCoverageReaching,
		Granularity: trace.GranularityBlock, FileCandidates: len(targets),
	})
}

func testNameOf(target string) string {
	if target == fuzzTarget {
		return "FuzzTarget" + strings.ToUpper(target[:2])
	}
	return "TestTarget" + strings.ToUpper(target[:2])
}

const (
	individualPlan   = "individual:"
	packageSuitePlan = "package-suite"
)

func measured(seq int64, target string) trace.Event {
	return measuredIn(seq, target, fixtureModule+"/pkg", testNameOf(target))
}

func measuredIn(seq int64, target, packagePath, test string) trace.Event {
	return trace.Event{Seq: seq, Type: trace.TypeExec, Timestamp: fixtureTime, Exec: &trace.ExecRecord{
		Argv: []string{
			fixtureBinary(packagePath), "-test.v=test2json",
			"-test.run=^" + test + "$",
			"-test.coverprofile=/tmp/goatest-baseline/" + target + profileSuffix,
			"-test.count=1",
		},
	}}
}

func compiledBaseline(seq int64, binary, packagePath string) trace.Event {
	return trace.Event{Seq: seq, Type: trace.TypeExec, Timestamp: fixtureTime, Exec: &trace.ExecRecord{
		Argv: []string{"go", "test", "-c", "-coverpkg=" + fixtureModule + "/...", "-o", binary, packagePath},
	}}
}

func directlyMeasuredIn(seq int64, target, binary, test string) trace.Event {
	return trace.Event{Seq: seq, Type: trace.TypeExec, Timestamp: fixtureTime, Exec: &trace.ExecRecord{
		Argv: []string{
			binary, "-test.v=test2json", "-test.run=^" + test + "$",
			"-test.coverprofile=/tmp/goatest-baseline/" + target + profileSuffix,
			"-test.count=1",
		},
	}}
}

func killedBy(seq int64, mutant, display, target string) trace.Event {
	return killedIn(seq, mutant, display, fixtureModule+"/pkg", testNameOf(target))
}

func killedIn(seq int64, mutant, display, packagePath, test string) trace.Event {
	return mutantEvent(seq, trace.MutantRecord{
		ID: mutant, DisplayID: display, Package: packagePath,
		Args: []string{"-test.run=^" + test + "$"}, Outcome: outcomeKilled, DurationMS: 5,
	})
}

func executedBy(seq int64, mutant, display, target, outcome string) trace.Event {
	return mutantEvent(seq, trace.MutantRecord{
		ID: mutant, DisplayID: display, Package: fixtureModule + "/pkg",
		Args: []string{"-test.run=^" + testNameOf(target) + "$"}, Outcome: outcome, DurationMS: 7,
	})
}

func gatedBody(startLine, startColumn, endLine, endColumn int) *branchProof {
	return &branchProof{
		BodyStartLine: startLine, BodyStartColumn: startColumn,
		BodyEndLine: endLine, BodyEndColumn: endColumn,
	}
}

func cataloguedMutant(id string, line, column int, proof *branchProof) catalogMutant {
	return catalogMutant{ID: id, Path: subjectPath, Line: line, Column: column, Branch: proof}
}

func fixtureCatalog(mutants ...catalogMutant) *mutantCatalog {
	catalog := &mutantCatalog{mutants: make(map[string]catalogMutant, len(mutants))}
	for _, mutant := range mutants {
		catalog.mutants[mutant.ID] = mutant
	}
	return catalog
}

func recordedRun(t *testing.T, measurements []string, events ...trace.Event) string {
	t.Helper()
	recording := make([]trace.Event, 0, len(measurements)+len(events))
	for index, target := range measurements {
		recording = append(recording, measured(int64(index+1), target))
	}
	return recordedTrace(t, append(recording, events...)...)
}

func recordedTrace(t *testing.T, events ...trace.Event) string {
	t.Helper()
	binaries := make(map[string]string)
	for _, event := range events {
		if event.Exec == nil || len(event.Exec.Argv) == 0 {
			continue
		}
		if packagePath, ok := fixtureBinaryPackage(event.Exec.Argv[0]); ok {
			binaries[event.Exec.Argv[0]] = packagePath
		}
	}
	names := make([]string, 0, len(binaries))
	for binary := range binaries {
		names = append(names, binary)
	}
	slices.Sort(names)
	prefix := make([]trace.Event, 0, len(names))
	for _, binary := range names {
		prefix = append(prefix, compiledBaseline(0, binary, binaries[binary]))
	}
	events = append(prefix, events...)
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

func fixtureBinary(packagePath string) string {
	return fixtureBinaryPrefix + base64.RawURLEncoding.EncodeToString([]byte(packagePath)) + ".test"
}

func fixtureBinaryPackage(binary string) (string, bool) {
	encoded, found := strings.CutPrefix(binary, fixtureBinaryPrefix)
	if !found {
		return "", false
	}
	encoded, found = strings.CutSuffix(encoded, ".test")
	if !found {
		return "", false
	}
	decoded, err := base64.RawURLEncoding.DecodeString(encoded)
	return string(decoded), err == nil && len(decoded) != 0
}

func auditFixture(t *testing.T, stream string, recorded evidence) auditResult {
	t.Helper()
	return auditWithCatalog(t, stream, recorded, nil)
}

func auditWithCatalog(t *testing.T, stream string, recorded evidence, catalog *mutantCatalog) auditResult {
	t.Helper()
	result, err := auditTrace(&terminalAuditReader{reader: strings.NewReader(stream)}, recorded, catalog, auditLayers(catalog))
	if err != nil {
		t.Fatalf("audit the recording: %v", err)
	}
	return result
}

type terminalAuditReader struct {
	reader   io.Reader
	terminal bool
}

func (reader *terminalAuditReader) Read(destination []byte) (int, error) {
	if reader.terminal {
		panic("read after terminal trace result")
	}
	read, err := reader.reader.Read(destination)
	reader.terminal = read == 0 && err != nil
	return read, err
}

func TestAuditFailsWhenAKillerLiesOutsideItsBlocks(t *testing.T) {
	t.Parallel()

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
	const soundRoutingPairs = 2

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
	if result.pairs != soundRoutingPairs {
		t.Fatalf("audited %d kill pairs, want 2", result.pairs)
	}
	if len(result.violations) != 0 {
		t.Fatalf("reported %d violations of a sound routing, want none: %+v", len(result.violations), result.violations)
	}
	if len(result.unverifiable) != 0 {
		t.Fatalf("reported %d unverifiable pairs, want none: %+v", len(result.unverifiable), result.unverifiable)
	}
	if got := result.layers[0]; got.kept != soundRoutingPairs || got.audited != soundRoutingPairs {
		t.Errorf("the reach layer audited %+v, want two audited pairs it keeps", got)
	}
	if result.targets != len(recorded.targets) {
		t.Errorf("the audit counted %d targets with profiles, want 2", result.targets)
	}
	if result.routes != 1 {
		t.Errorf("the audit counted %d routes, want 1", result.routes)
	}
}

func TestAuditKeepsAKillerWhenThePositionIsUnknown(t *testing.T) {
	t.Parallel()

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
					Args: []string{"-test.run=^$"}, Outcome: outcomeKilled,
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

type failingReader struct{ err error }

func (reader failingReader) Read([]byte) (int, error) { return 0, reader.err }

func TestAuditReportsAStreamItCannotRead(t *testing.T) {
	t.Parallel()

	broken := errors.New("the stream broke")

	_, err := auditTrace(failingReader{err: broken}, evidence{}, nil, auditLayers(nil))
	if !errors.Is(err, broken) {
		t.Fatalf("auditing a broken stream returned %v, want the read failure", err)
	}
}

func TestAuditCountsOneKillPairPerMutantAndTarget(t *testing.T) {
	t.Parallel()
	const repeatedKillExecutions = 2

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
	if result.killedExecutions != repeatedKillExecutions {
		t.Errorf("counted %d killed executions, want 2", result.killedExecutions)
	}
}

func TestAuditIgnoresAnExecutionThatDidNotKill(t *testing.T) {
	t.Parallel()

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

func TestAuditRejectsUnknownFieldsAndTrailingDocuments(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		stream string
		want   string
	}{
		{
			name:   "unknown field",
			stream: `{"seq":1,"type":"run-start","timestamp":"2026-01-01T00:00:00Z","unknown":true}`,
			want:   `unknown field "unknown"`,
		},
		{
			name:   "trailing document",
			stream: `{"seq":1,"type":"run-start","timestamp":"2026-01-01T00:00:00Z"}{}`,
			want:   "trailing data",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := auditTrace(strings.NewReader(test.stream), evidence{}, nil, auditLayers(nil))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("audit error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestAuditOrdersViolationsByPosition(t *testing.T) {
	t.Parallel()

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
	const recordedProfileCount = 2

	directory := writeProfiles(t, map[string][]string{
		killerTarget: {ran(10, 2, 12, 16)},
		secondTarget: {ran(20, 2, 24, 3)},
	})
	if err := os.WriteFile(filepath.Join(directory, "targets.json"), []byte("{}\n"), filemode.ReadableFile); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(directory, "nested.cover"), filemode.ReadableDirectory); err != nil {
		t.Fatal(err)
	}

	recorded, err := readEvidence(directory, fixtureModule)
	if err != nil {
		t.Fatalf("read the recorded evidence: %v", err)
	}
	if len(recorded.targets) != recordedProfileCount {
		t.Errorf("read %d targets, want the 2 profiles of the directory", len(recorded.targets))
	}

	for _, position := range []struct{ line, column int }{{11, 4}, {21, 4}} {
		if !recorded.instrumentedAt(subjectPath, position.line, position.column) {
			t.Errorf("the instrumented union does not contain %d.%d, which a profile named",
				position.line, position.column)
		}
	}
}

func layerNames(layers []layer) []string {
	names := make([]string, 0, len(layers))
	for _, audited := range layers {
		names = append(names, audited.name)
	}
	return names
}

func TestAuditLayersAddsTheBranchLayerOnlyWithACatalog(t *testing.T) {
	t.Parallel()

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
	const auditedLayerCount = 2

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
	if len(result.layers) != auditedLayerCount {
		t.Fatalf("audited %d layers, want the reach layer and the branch layer", len(result.layers))
	}
	branch := result.layers[1]
	if branch.name != branchLayerName {
		t.Fatalf("the second layer is %q, want %q", branch.name, branchLayerName)
	}
	if branch.audited != auditedLayerCount || branch.kept != 1 || branch.inapplicable != 1 {
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
	want := dischargeSavings{routes: 2, reaching: 4, discharged: 4, emptied: 2, executions: 3}
	if result.branch != want {
		t.Errorf("the audit measured %+v, want %+v", result.branch, want)
	}
}

func TestAuditMeasuresNoSavingWithoutACatalog(t *testing.T) {
	t.Parallel()

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

	if violation.pair.path != subjectPath || violation.pair.line != 21 || violation.pair.column != 4 {
		t.Errorf("the pair places the mutant at %s:%d:%d, want the position the route recorded",
			violation.pair.path, violation.pair.line, violation.pair.column)
	}
}

func TestAuditAttributesADirectBaselineExecutionThroughItsCompileRecord(t *testing.T) {
	t.Parallel()
	binary := "/tmp/goatest-baseline/direct.test"
	packagePath := fixtureModule + "/pkg"
	recorded := recordedEvidence(t, map[string][]string{killerTarget: {ran(20, 2, 24, 3)}})
	stream := recordedTrace(t,
		compiledBaseline(1, binary, packagePath),
		directlyMeasuredIn(2, killerTarget, binary, testNameOf(killerTarget)),
		blockRoute(3, firstMutant, 20, 4, killerTarget),
		killedIn(4, firstMutant, firstDisplay, packagePath, testNameOf(killerTarget)),
	)

	result := auditFixture(t, stream, recorded)
	if result.pairs != 1 || result.unattributedKills != 0 || len(result.violations) != 0 {
		t.Fatalf("direct measurement audit = pairs %d unattributed %d violations %+v",
			result.pairs, result.unattributedKills, result.violations)
	}
}

func TestCompiledTestBinaryRejectsRunsAndReadsPortableGoNames(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name         string
		argv         []string
		wantBinary   string
		wantPackage  string
		wantCompiled bool
	}{
		{name: "ordinary", argv: []string{"go", "test", "-c", "-o", "pkg.test", fixtureModule + "/pkg"}, wantBinary: "pkg.test", wantPackage: fixtureModule + "/pkg", wantCompiled: true},
		{name: "windows executable", argv: []string{`C:\\Go\\bin\\go.exe`, "test", "-c", "-o=package.test.exe", fixtureModule + "/pkg"}, wantBinary: "package.test.exe", wantPackage: fixtureModule + "/pkg", wantCompiled: true},
		{name: "run", argv: []string{"go", "test", "-o", "pkg.test", fixtureModule + "/pkg"}},
		{name: "no output", argv: []string{"go", "test", "-c", fixtureModule + "/pkg"}},
		{name: "other tool", argv: []string{"cargo", "test", "-c", "-o", "pkg.test", fixtureModule + "/pkg"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			binary, packagePath, compiled := compiledTestBinary(test.argv)
			if binary != test.wantBinary || packagePath != test.wantPackage || compiled != test.wantCompiled {
				t.Fatalf("compiledTestBinary(%q) = (%q, %q, %t)", test.argv, binary, packagePath, compiled)
			}
		})
	}
}

func TestAuditAttributesAKillByThePackageItRanIn(t *testing.T) {
	t.Parallel()

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

func TestAuditCountsAReusedRouteAsAClassOfItsOwn(t *testing.T) {
	t.Parallel()

	recorded := recordedEvidence(t, map[string][]string{
		killerTarget: {ran(10, 2, 12, 16), linked(20, 2, 24, 3)},
	})
	reused := blockRoute(2, firstMutant, 11, 4, killerTarget)
	reused.Route.Plan, reused.Route.Reused = []string{"reused"}, true
	stream := recordedRun(t, []string{killerTarget},
		reused,
		blockRoute(3, secondMutant, 11, 4, killerTarget),
		mutantEvent(4, trace.MutantRecord{
			ID: secondMutant, DisplayID: secondDisplay, Package: fixtureModule + "/pkg",
			Args: []string{"-test.run=^" + testNameOf(killerTarget) + "$"}, Outcome: outcomeKilled,
		}),
	)

	result := auditFixture(t, stream, recorded)
	if result.reusedRoutes != 1 {
		t.Errorf("counted %d reused routes, want 1", result.reusedRoutes)
	}
	if result.routes != 2 || result.killedExecutions != 1 || result.pairs != 1 {
		t.Errorf("a reused route changed what the audit measured: %d routes, %d kills, %d pairs",
			result.routes, result.killedExecutions, result.pairs)
	}
	if len(result.violations) != 0 || len(result.unverifiable) != 0 {
		t.Errorf("a reused route was audited: %d violations, %d unverifiable",
			len(result.violations), len(result.unverifiable))
	}
}
