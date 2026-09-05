// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"slices"
	"testing"

	"github.com/P4suta/goatest/internal/trace"
)

const fixtureSuiteProfile = "0123456789abcdef.test.suite"

// measuredSuite is the baseline package-suite coverage control. It deliberately
// carries no -test.run selector: the absence is what distinguishes this command
// from an ordinary target measurement, while -p independently attributes the
// otherwise hashed profile name to its package.
func measuredSuite(seq int64, profile, packagePath string) trace.Event {
	return trace.Event{Seq: seq, Type: trace.TypeExec, Timestamp: fixtureTime, Exec: &trace.ExecRecord{
		Argv: []string{
			"go", "tool", "test2json", "-t", "-p", packagePath,
			"/tmp/goatest-baseline/" + profile + ".test", "-test.v=test2json",
			"-test.coverprofile=/tmp/goatest-baseline/" + profile + profileSuffix,
			"-test.count=1",
		},
	}}
}

func packageSuiteKilled(seq int64, mutant, display, packagePath string) trace.Event {
	return mutantEvent(seq, trace.MutantRecord{
		ID: mutant, DisplayID: display, Package: packagePath,
		Outcome: outcomeKilled, DurationMS: 9,
	})
}

func TestAuditHoldsSuiteReachToCoveredAndUncoveredPackageSuiteKills(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name       string
		suiteBlock string
		want       conclusion
		why        string
	}{
		{name: "the suite reached the mutant", suiteBlock: ran(20, 2, 24, 3), want: kept},
		{name: "the suite killed outside its covered blocks", suiteBlock: linked(20, 2, 24, 3), want: discharged, why: whyOutsideCoveredBlocks},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			recorded := recordedEvidence(t, map[string][]string{
				killerTarget:        {ran(10, 2, 12, 16)},
				fixtureSuiteProfile: {ran(10, 2, 12, 16), testCase.suiteBlock},
			})
			stream := recordedTrace(t,
				measured(1, killerTarget),
				measuredSuite(2, fixtureSuiteProfile, fixtureModule+"/pkg"),
				routeEvent(3, trace.RouteRecord{
					MutantID: firstMutant, Rule: "eq-to-neq", Path: subjectPath, Line: 21, Column: 4,
					Plan: []string{packageSuitePlan}, Reason: trace.ReasonUnreached,
					Granularity: trace.GranularityBlock,
				}),
				packageSuiteKilled(4, firstMutant, firstDisplay, fixtureModule+"/pkg"),
				// Kill confirmation repeats the same pair and must not inflate the
				// soundness sample.
				packageSuiteKilled(5, firstMutant, firstDisplay, fixtureModule+"/pkg"),
			)

			result := auditFixture(t, stream, recorded)
			if result.targets != 1 || result.suiteCoverageProfiles != 1 {
				t.Fatalf("profile counts = targets %d suites %d, want 1 and 1", result.targets, result.suiteCoverageProfiles)
			}
			if result.packageSuiteKills != 2 || result.suitePairs != 1 {
				t.Fatalf("suite counts = executions %d pairs %d, want 2 and 1", result.packageSuiteKills, result.suitePairs)
			}
			row := layerRow(t, result, suiteReachLayerName)
			if row.audited != 1 {
				t.Fatalf("suite reach audited %+v, want one unique pair", row)
			}
			switch testCase.want {
			case kept:
				if row.kept != 1 || len(result.violations) != 0 {
					t.Fatalf("suite reach audited %+v with violations %+v, want a kept pair", row, result.violations)
				}
			case discharged:
				if row.violations != 1 || len(result.violations) != 1 {
					t.Fatalf("suite reach audited %+v with violations %+v, want one violation", row, result.violations)
				}
				violation := result.violations[0]
				if violation.layer != suiteReachLayerName || violation.why != testCase.why ||
					violation.pair.target != fixtureModule+"/pkg package suite" {
					t.Errorf("suite violation = %+v, want the package suite and %q", violation, testCase.why)
				}
			}
		})
	}
}

func TestSuiteReachUsesOnlyTheSuiteProfilesInstrumentation(t *testing.T) {
	t.Parallel()
	// Another profile describes the mutant position, but the suite profile does
	// not. Production cannot derive a negative suite fact from that gap, so the
	// independent audit must keep the kill rather than borrowing instrumentation
	// from the other target.
	recorded := recordedEvidence(t, map[string][]string{
		killerTarget:        {linked(20, 2, 24, 3)},
		fixtureSuiteProfile: {ran(10, 2, 12, 16)},
	})
	stream := recordedTrace(t,
		measured(1, killerTarget),
		measuredSuite(2, fixtureSuiteProfile, fixtureModule+"/pkg"),
		blockRoute(3, firstMutant, 21, 4),
		packageSuiteKilled(4, firstMutant, firstDisplay, fixtureModule+"/pkg"),
	)

	result := auditFixture(t, stream, recorded)
	row := layerRow(t, result, suiteReachLayerName)
	if row.audited != 1 || row.kept != 1 || row.violations != 0 || len(result.violations) != 0 {
		t.Fatalf("suite reach audited %+v with violations %+v, want one kept pair", row, result.violations)
	}
}

func TestAuditFailsClosedWhenASuiteProfileCannotBeAttributed(t *testing.T) {
	t.Parallel()
	secondSuiteProfile := "fedcba9876543210.test.suite"
	for _, testCase := range []struct {
		name         string
		measurements []trace.Event
		why          string
	}{
		{name: "no command attributes the profile", why: whyNoSuiteProfile},
		{
			name: "two commands claim the package",
			measurements: []trace.Event{
				measuredSuite(1, fixtureSuiteProfile, fixtureModule+"/pkg"),
				measuredSuite(2, secondSuiteProfile, fixtureModule+"/pkg"),
			},
			why: whyConflictingSuiteProfile,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			profiles := map[string][]string{
				fixtureSuiteProfile: {ran(20, 2, 24, 3)},
			}
			if len(testCase.measurements) > 1 {
				profiles[secondSuiteProfile] = []string{ran(20, 2, 24, 3)}
			}
			recorded := recordedEvidence(t, profiles)
			events := slices.Clone(testCase.measurements)
			events = append(events,
				blockRoute(3, firstMutant, 21, 4),
				packageSuiteKilled(4, firstMutant, firstDisplay, fixtureModule+"/pkg"),
			)
			result := auditFixture(t, recordedTrace(t, events...), recorded)
			row := layerRow(t, result, suiteReachLayerName)
			if row.audited != 1 || row.unverifiable != 1 || len(result.unverifiable) != 1 {
				t.Fatalf("suite reach audited %+v with rows %+v, want one unverifiable pair", row, result.unverifiable)
			}
			if got := result.unverifiable[0].why; got != testCase.why {
				t.Errorf("unverifiable reason = %q, want %q", got, testCase.why)
			}
		})
	}
}

func TestAuditReportsASuiteKillWithoutARouteAsUnverifiable(t *testing.T) {
	t.Parallel()
	recorded := recordedEvidence(t, map[string][]string{
		fixtureSuiteProfile: {ran(20, 2, 24, 3)},
	})
	result := auditFixture(t, recordedTrace(t,
		measuredSuite(1, fixtureSuiteProfile, fixtureModule+"/pkg"),
		packageSuiteKilled(2, firstMutant, firstDisplay, fixtureModule+"/pkg"),
	), recorded)
	if len(result.unverifiable) != 1 || result.unverifiable[0].why != whyNoSuiteRoute {
		t.Fatalf("unverifiable suite kill = %+v, want %q", result.unverifiable, whyNoSuiteRoute)
	}
}
