// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package app_test

import (
	"slices"
	"testing"

	"github.com/P4suta/goatest/internal/trace"
)

func routeHasPlanOrProof(record trace.RouteRecord, measuredProbes map[string][]string) bool {
	if len(record.Plan) != 0 {
		return true
	}
	settled := record.SuiteCoverage != "" && !record.SuiteReached
	requiresPlan := record.SuiteCoverage != "" && record.SuiteReached
	if record.SuiteProbe != "" {
		infected, measured := measuredProbes[record.SuiteProbe]
		if measured && slices.Contains(infected, record.MutantID) {
			requiresPlan = true
		} else if measured {
			settled = true
		}
	}
	if requiresPlan {
		return false
	}
	return len(record.Discharged) != 0 || settled
}

func TestRouteHasPlanOrProofRequiresExecutionForPositiveSuiteEvidence(t *testing.T) {
	t.Parallel()
	mutant := "mutant-a"
	probe := "package-suite:example.com/app"
	measured := map[string][]string{probe: {"mutant-b"}}
	tests := []struct {
		name   string
		route  trace.RouteRecord
		probes map[string][]string
		want   bool
	}{
		{name: "an explicit plan", route: trace.RouteRecord{Plan: []string{"package-suite"}}, want: true},
		{name: "an explicit discharge", route: trace.RouteRecord{Discharged: []trace.Discharge{{Target: "target-a"}}}, want: true},
		{name: "no disposition", route: trace.RouteRecord{MutantID: mutant}},
		{name: "unreached suite coverage", route: trace.RouteRecord{MutantID: mutant, SuiteCoverage: "coverage"}, want: true},
		{name: "reached suite coverage", route: trace.RouteRecord{MutantID: mutant, SuiteCoverage: "coverage", SuiteReached: true}},
		{name: "non-infecting suite probe", route: trace.RouteRecord{MutantID: mutant, SuiteProbe: probe}, probes: measured, want: true},
		{name: "missing suite probe", route: trace.RouteRecord{MutantID: mutant, SuiteProbe: probe}},
		{name: "infecting suite probe", route: trace.RouteRecord{MutantID: mutant, SuiteProbe: probe}, probes: map[string][]string{probe: {mutant}}},
		{
			name:   "positive infection overrides silent coverage",
			route:  trace.RouteRecord{MutantID: mutant, SuiteCoverage: "coverage", SuiteProbe: probe},
			probes: map[string][]string{probe: {mutant}},
		},
		{
			name: "positive suite evidence overrides target discharges",
			route: trace.RouteRecord{
				MutantID: mutant, SuiteCoverage: "coverage", SuiteReached: true,
				Discharged: []trace.Discharge{{Target: "target-a"}},
			},
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			if got := routeHasPlanOrProof(testCase.route, testCase.probes); got != testCase.want {
				t.Fatalf("routeHasPlanOrProof(%+v) = %t, want %t", testCase.route, got, testCase.want)
			}
		})
	}
}
