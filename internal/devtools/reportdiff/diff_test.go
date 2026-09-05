// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"fmt"
	"slices"
	"testing"

	"github.com/P4suta/goatest/internal/report"
)

// mutantAt is one entry of a fixture inventory. Every fixture mutant carries a
// location and a rule, because a regression is reported by where it is rather
// than by the content address that identifies it.
func mutantAt(id string, status report.MutantStatus, path string, line int) report.MutantDisposition {
	return report.MutantDisposition{ID: id, Status: status, Path: path, Line: line, Rule: "conditional"}
}

// findingOf is one finding of a fixture report, attributed to a mutant.
func findingOf(kind, mutantID string) report.Finding {
	return report.Finding{ID: kind + ":" + mutantID, Kind: kind, MutantID: mutantID, Summary: kind}
}

// transitionLines renders a status transition matrix so a test can pin both
// the counts and the order in one comparison.
func transitionLines(transitions []statusTransition) []string {
	lines := make([]string, 0, len(transitions))
	for _, transition := range transitions {
		lines = append(lines, fmt.Sprintf("%s -> %s %d", transition.before, transition.after, transition.mutants))
	}
	return lines
}

// kindLines renders a finding-kind transition matrix the same way.
func kindLines(transitions []kindTransition) []string {
	lines := make([]string, 0, len(transitions))
	for _, transition := range transitions {
		lines = append(lines, fmt.Sprintf("%s -> %s %d", transition.before, transition.after, transition.mutants))
	}
	return lines
}

func TestTransitionComparatorsUseCountsThenBothLabels(t *testing.T) {
	t.Parallel()
	statusCases := []struct {
		name          string
		first, second statusTransition
	}{
		{
			name: "count before labels",
			first: statusTransition{
				before: report.MutantSurvived, after: report.MutantSurvived, mutants: 2,
			},
			second: statusTransition{
				before: report.MutantKilled, after: report.MutantKilled, mutants: 1,
			},
		},
		{
			name:   "before label",
			first:  statusTransition{before: report.MutantKilled, after: report.MutantSurvived, mutants: 1},
			second: statusTransition{before: report.MutantSurvived, after: report.MutantKilled, mutants: 1},
		},
		{
			name:   "after label",
			first:  statusTransition{before: report.MutantKilled, after: report.MutantKilled, mutants: 1},
			second: statusTransition{before: report.MutantKilled, after: report.MutantSurvived, mutants: 1},
		},
	}
	for _, test := range statusCases {
		t.Run("status "+test.name, func(t *testing.T) {
			t.Parallel()
			if got := compareStatusTransitions(test.first, test.second); got >= 0 {
				t.Fatalf("compareStatusTransitions(first, second) = %d, want negative", got)
			}
			if got := compareStatusTransitions(test.second, test.first); got <= 0 {
				t.Fatalf("compareStatusTransitions(second, first) = %d, want positive", got)
			}
		})
	}

	kindCases := []struct {
		name          string
		first, second kindTransition
	}{
		{
			name:   "count before labels",
			first:  kindTransition{before: "z", after: "z", mutants: 2},
			second: kindTransition{before: "a", after: "a", mutants: 1},
		},
		{
			name:   "before label",
			first:  kindTransition{before: "a", after: "z", mutants: 1},
			second: kindTransition{before: "b", after: "a", mutants: 1},
		},
		{
			name:   "after label",
			first:  kindTransition{before: "a", after: "a", mutants: 1},
			second: kindTransition{before: "a", after: "b", mutants: 1},
		},
	}
	for _, test := range kindCases {
		t.Run("kind "+test.name, func(t *testing.T) {
			t.Parallel()
			if got := compareKindTransitions(test.first, test.second); got >= 0 {
				t.Fatalf("compareKindTransitions(first, second) = %d, want negative", got)
			}
			if got := compareKindTransitions(test.second, test.first); got <= 0 {
				t.Fatalf("compareKindTransitions(second, first) = %d, want positive", got)
			}
		})
	}

	equalStatus := statusTransition{before: report.MutantKilled, after: report.MutantSurvived, mutants: 1}
	if got := compareStatusTransitions(equalStatus, equalStatus); got != 0 {
		t.Errorf("equal status transitions compare as %d", got)
	}
	equalKind := kindTransition{before: "a", after: "b", mutants: 1}
	if got := compareKindTransitions(equalKind, equalKind); got != 0 {
		t.Errorf("equal kind transitions compare as %d", got)
	}
}

func TestCompareCountsStatusTransitionsOverCommonMutantsOnly(t *testing.T) {
	t.Parallel()
	before := report.Report{Mutants: []report.MutantDisposition{
		mutantAt("m-a", report.MutantKilled, "a.go", 1),
		mutantAt("m-b", report.MutantKilled, "b.go", 2),
		mutantAt("m-c", report.MutantSurvived, "c.go", 3),
		mutantAt("m-d", report.MutantSurvived, "d.go", 4),
		mutantAt("m-e", report.MutantSurvived, "e.go", 5),
		mutantAt("m-f", report.MutantInconclusive, "f.go", 6),
		mutantAt("m-g", report.MutantKilled, "g.go", 7),
		mutantAt("m-h", report.MutantKilled, "h.go", 8),
		mutantAt("m-i", report.MutantKilled, "i.go", 9),
	}}
	after := report.Report{Mutants: []report.MutantDisposition{
		mutantAt("m-a", report.MutantKilled, "a.go", 1),
		mutantAt("m-b", report.MutantKilled, "b.go", 2),
		mutantAt("m-c", report.MutantKilled, "c.go", 3),
		mutantAt("m-d", report.MutantKilled, "d.go", 4),
		mutantAt("m-e", report.MutantKilled, "e.go", 5),
		mutantAt("m-f", report.MutantSurvived, "f.go", 6),
		mutantAt("m-g", report.MutantInconclusive, "g.go", 7),
		mutantAt("m-h", report.MutantSurvived, "h.go", 8),
		mutantAt("m-j", report.MutantSurvived, "j.go", 10),
	}}
	result := compare(before, after)
	// Most common transition first, then the pair itself, so two runs of the
	// same comparison print the same matrix. The counts descend against the
	// name order of the pairs, and the tie at one row is broken by a before
	// name whose order contradicts the after names, so nothing but the count
	// comparison followed by each tie-break in turn produces this order: a
	// comparator that read the count last, or that fell from the before name
	// through to the after one, sorts this fixture some other way whatever
	// order the map hands the rows over in.
	want := []string{
		"survived -> killed 3",
		"killed -> killed 2",
		"inconclusive -> survived 1",
		"killed -> inconclusive 1",
		"killed -> survived 1",
	}
	if got := transitionLines(result.statuses); !slices.Equal(got, want) {
		t.Fatalf("transitions = %q, want %q", got, want)
	}
	// m-i and m-j are in one report alone: a mutant the other report never
	// discovered has no transition to count, so the matrix totals the eight
	// common mutants and nothing besides.
	counted := 0
	for _, transition := range result.statuses {
		counted += transition.mutants
	}
	if counted != 8 || result.commonMutants != 8 {
		t.Errorf("transitions count %d mutants over %d common ones, want 8 over 8", counted, result.commonMutants)
	}
}

func TestCompareListsKilledMutantsThatStoppedBeingKilledByLocation(t *testing.T) {
	t.Parallel()
	before := report.Report{Mutants: []report.MutantDisposition{
		mutantAt("m-1", report.MutantKilled, "z.go", 5),
		mutantAt("m-2", report.MutantKilled, "a.go", 9),
		mutantAt("m-3", report.MutantKilled, "a.go", 2),
		mutantAt("m-4", report.MutantSurvived, "a.go", 1),
		mutantAt("m-5", report.MutantKilled, "a.go", 9),
	}}
	after := report.Report{Mutants: []report.MutantDisposition{
		mutantAt("m-1", report.MutantSurvived, "z.go", 5),
		mutantAt("m-2", report.MutantInconclusive, "a.go", 9),
		mutantAt("m-3", report.MutantKilled, "a.go", 2),
		mutantAt("m-4", report.MutantSurvived, "a.go", 1),
		mutantAt("m-5", report.MutantSurvived, "a.go", 9),
	}}
	result := compare(before, after)
	var got []string
	for _, regression := range result.regressions {
		got = append(got, fmt.Sprintf("%s:%d %s %s", regression.path, regression.line, regression.id, regression.status))
	}
	want := []string{
		"a.go:9 m-2 inconclusive",
		"a.go:9 m-5 survived",
		"z.go:5 m-1 survived",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("regressions = %q, want %q ordered by path, line and identity", got, want)
	}
}

func TestCompareAccountsForMutantsPresentInOneReport(t *testing.T) {
	t.Parallel()
	before := report.Report{Mutants: []report.MutantDisposition{
		mutantAt("m-a", report.MutantKilled, "a.go", 1),
		mutantAt("m-b", report.MutantKilled, "b.go", 2),
		mutantAt("m-c", report.MutantKilled, "c.go", 3),
	}}
	after := report.Report{Mutants: []report.MutantDisposition{
		mutantAt("m-b", report.MutantKilled, "b.go", 2),
		mutantAt("m-c", report.MutantKilled, "c.go", 3),
		mutantAt("m-d", report.MutantKilled, "d.go", 4),
		mutantAt("m-e", report.MutantKilled, "e.go", 5),
	}}
	result := compare(before, after)
	if result.commonMutants != 2 || result.onlyBefore != 1 || result.onlyAfter != 2 {
		t.Fatalf("inventory = %d common, %d only before, %d only after; want 2, 1 and 2",
			result.commonMutants, result.onlyBefore, result.onlyAfter)
	}
}

func TestCompareGroupsFindingKindsPerMutant(t *testing.T) {
	t.Parallel()
	before := report.Report{
		Mutants: []report.MutantDisposition{
			mutantAt("m-a", report.MutantSurvived, "a.go", 1),
			mutantAt("m-b", report.MutantSurvived, "b.go", 2),
			mutantAt("m-c", report.MutantSurvived, "c.go", 3),
			mutantAt("m-d", report.MutantSurvived, "d.go", 4),
			mutantAt("m-e", report.MutantSurvived, "e.go", 5),
			mutantAt("m-f", report.MutantKilled, "f.go", 6),
			mutantAt("m-g", report.MutantSurvived, "g.go", 7),
			mutantAt("m-h", report.MutantSurvived, "h.go", 8),
		},
		Findings: []report.Finding{
			findingOf("unreached-mutant", "m-a"),
			findingOf("unreached-mutant", "m-b"),
			findingOf("unreached-mutant", "m-c"),
			findingOf("surviving-mutant", "m-d"),
			findingOf("surviving-mutant", "m-e"),
			findingOf("surviving-mutant", "m-g"),
			findingOf("surviving-mutant", "m-h"),
			findingOf("race", ""),
		},
	}
	after := report.Report{
		Mutants: []report.MutantDisposition{
			mutantAt("m-a", report.MutantSurvived, "a.go", 1),
			mutantAt("m-b", report.MutantSurvived, "b.go", 2),
			mutantAt("m-c", report.MutantSurvived, "c.go", 3),
			mutantAt("m-d", report.MutantSurvived, "d.go", 4),
			mutantAt("m-e", report.MutantSurvived, "e.go", 5),
			mutantAt("m-f", report.MutantSurvived, "f.go", 6),
			mutantAt("m-g", report.MutantKilled, "g.go", 7),
			mutantAt("m-h", report.MutantSurvived, "h.go", 8),
		},
		Findings: []report.Finding{
			findingOf("unreached-mutant", "m-a"),
			findingOf("unreached-mutant", "m-b"),
			findingOf("unreached-mutant", "m-c"),
			findingOf("unreached-mutant", "m-d"),
			findingOf("unreached-mutant", "m-e"),
			findingOf("unreached-mutant", "m-f"),
			findingOf("surviving-mutant", "m-h"),
			findingOf("unreached-mutant", "m-h"),
		},
	}
	result := compare(before, after)
	// A mutant with several findings is one row named by the whole set it
	// carried, and a mutant no finding names is the absence rather than a
	// blank cell. The counts descend against the name order and the tie at one
	// row is broken by a before name whose order contradicts the after names,
	// so this order comes from the count comparison and each tie-break in
	// turn rather than from the order the map handed the rows over in.
	want := []string{
		"unreached-mutant -> unreached-mutant 3",
		"surviving-mutant -> unreached-mutant 2",
		"(none) -> unreached-mutant 1",
		"surviving-mutant -> (none) 1",
		"surviving-mutant -> surviving-mutant+unreached-mutant 1",
	}
	if got := kindLines(result.kinds); !slices.Equal(got, want) {
		t.Fatalf("kind transitions = %q, want %q", got, want)
	}
	// The kind totals count every finding of each report, including the ones
	// no mutant is attributed to.
	var totals []string
	for _, total := range result.kindTotals {
		totals = append(totals, fmt.Sprintf("%s %d %d", total.name, total.before, total.after))
	}
	wantTotals := []string{"race 1 0", "surviving-mutant 4 1", "unreached-mutant 3 7"}
	if !slices.Equal(totals, wantTotals) {
		t.Fatalf("kind totals = %q, want %q in name order", totals, wantTotals)
	}
}

func TestCompareReportsAccountingDeltasInAFixedOrder(t *testing.T) {
	t.Parallel()
	before := report.Report{Accounting: report.Accounting{
		Targets: report.CountAccounting{Discovered: 10, Selected: 9, Executed: 8, Skipped: 1, Excluded: 1},
		Mutants: report.MutantAccounting{
			Discovered: 100, Selected: 90, Executed: 80, Killed: 70, Survived: 10,
			ReusedKilled: 40, ReusedSurvived: 5,
		},
		Race: report.CountAccounting{Discovered: 5, Selected: 5, Executed: 5},
	}}
	after := before
	after.Accounting.Mutants.Killed = 71
	after.Accounting.Mutants.Survived = 9
	after.Accounting.Mutants.ReusedKilled = 60

	result := compare(before, after)
	want := []string{
		"targets.discovered", "targets.selected", "targets.executed", "targets.skipped", "targets.excluded",
		"mutants.discovered", "mutants.selected", "mutants.executed", "mutants.killed", "mutants.survived",
		"mutants.inconclusive", "mutants.compile_rejected", "mutants.accepted", "mutants.out_of_scope",
		"mutants.unknown", "mutants.reused_killed", "mutants.reused_survived",
		"race.discovered", "race.selected", "race.executed", "race.skipped", "race.excluded",
	}
	var got []string
	for _, delta := range result.accounting {
		got = append(got, delta.name)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("accounting counters = %q, want %q", got, want)
	}
	for _, delta := range result.accounting {
		switch delta.name {
		case "mutants.killed":
			if delta.before != 70 || delta.after != 71 {
				t.Errorf("%s = %d -> %d, want 70 -> 71", delta.name, delta.before, delta.after)
			}
		case "mutants.reused_killed":
			// How much of a run was answered without running anything is the
			// number a comparison of two runs is read against, so it is a
			// delta of its own rather than something hidden inside killed.
			if delta.before != 40 || delta.after != 60 {
				t.Errorf("%s = %d -> %d, want 40 -> 60", delta.name, delta.before, delta.after)
			}
		case "mutants.discovered":
			if delta.before != 100 || delta.after != 100 {
				t.Errorf("%s = %d -> %d, want an unchanged counter to be reported too",
					delta.name, delta.before, delta.after)
			}
		}
	}
}
