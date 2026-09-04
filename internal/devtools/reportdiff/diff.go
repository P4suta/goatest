// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"cmp"
	"slices"
	"strings"

	"github.com/P4suta/goatest/internal/report"
)

// Placeholders for what a report did not carry, printed rather than left blank
// so that an absence reads as one.
const (
	noValue = "-"
	noKind  = "(none)"
)

// kindSeparator joins the finding kinds one mutant carries into the single
// label its row is counted under. A mutant with two findings moved as a pair
// or it did not move, so the pair is the unit of the transition matrix.
const kindSeparator = "+"

// comparison is everything one assurance report says about another. It is
// derived from the two reports alone: no clock, no filesystem, and no map
// iteration reaches it, so the same pair compares the same way forever.
type comparison struct {
	// The identity of the two runs and what they concluded.
	verdictBefore, verdictAfter   report.Verdict
	runBefore, runAfter           string
	durationBefore, durationAfter int64

	// accounting is every counter of the report accounting on both sides, in
	// a fixed order, unchanged counters included: a gate reads "killed did
	// not move" as an answer rather than as a missing row.
	accounting []counterDelta

	// commonMutants, onlyBefore and onlyAfter split the two inventories by
	// mutant identity. Only the common mutants can transition; a mutant one
	// report never discovered has nothing to compare against.
	commonMutants, onlyBefore, onlyAfter int

	// statuses is the status transition matrix over the common mutants, and
	// kinds the same matrix over the finding kinds attributed to them.
	statuses []statusTransition
	kinds    []kindTransition

	// kindTotals counts the findings of each kind in both reports, including
	// the ones no mutant is attributed to.
	kindTotals []counterDelta

	// regressions are the mutants a run killed and the later one did not.
	// They are the reason this tool exists: a routing or caching change may
	// move a mutant between surviving and unreached, and may never stop one
	// from being killed.
	regressions []regression
}

// counterDelta is one counter on both sides of a comparison.
type counterDelta struct {
	name          string
	before, after int
}

// statusTransition is how many common mutants moved between two statuses.
type statusTransition struct {
	before, after report.MutantStatus
	mutants       int
}

// kindTransition is how many common mutants moved between two sets of finding
// kinds.
type kindTransition struct {
	before, after string
	mutants       int
}

// regression is one mutant that was killed and no longer is. The location and
// the rule are the ones the earlier report recorded, because that is the
// report whose kill was lost; the status is the one it holds now.
type regression struct {
	id     string
	path   string
	line   int
	rule   string
	status report.MutantStatus
}

// compare derives the whole comparison of two reports.
func compare(before, after report.Report) comparison {
	result := comparison{
		verdictBefore:  before.Verdict,
		verdictAfter:   after.Verdict,
		runBefore:      before.RunID,
		runAfter:       after.RunID,
		durationBefore: before.Timing.DurationMS,
		durationAfter:  after.Timing.DurationMS,
		accounting:     accountingDeltas(before.Accounting, after.Accounting),
		kindTotals:     kindDeltas(before.Findings, after.Findings),
	}
	laterOf := mutantIndex(after.Mutants)
	earlierOf := mutantIndex(before.Mutants)
	beforeKinds := kindsByMutant(before.Findings)
	afterKinds := kindsByMutant(after.Findings)

	statuses := make(map[statusTransition]int)
	kinds := make(map[kindTransition]int)
	seen := make(map[string]struct{}, len(before.Mutants))
	for _, earlier := range before.Mutants {
		if _, repeated := seen[earlier.ID]; repeated {
			continue
		}
		seen[earlier.ID] = struct{}{}
		later, common := laterOf[earlier.ID]
		if !common {
			result.onlyBefore++
			continue
		}
		result.commonMutants++
		statuses[statusTransition{before: earlier.Status, after: later.Status}]++
		kinds[kindTransition{before: kindLabel(beforeKinds[earlier.ID]), after: kindLabel(afterKinds[earlier.ID])}]++
		if earlier.Status == report.MutantKilled && later.Status != report.MutantKilled {
			result.regressions = append(result.regressions, regression{
				id:     earlier.ID,
				path:   earlier.Path,
				line:   earlier.Line,
				rule:   earlier.Rule,
				status: later.Status,
			})
		}
	}
	for identity := range laterOf {
		if _, common := earlierOf[identity]; !common {
			result.onlyAfter++
		}
	}
	result.statuses = sortedStatuses(statuses)
	result.kinds = sortedKinds(kinds)
	slices.SortFunc(result.regressions, func(first, second regression) int {
		if order := strings.Compare(first.path, second.path); order != 0 {
			return order
		}
		if order := cmp.Compare(first.line, second.line); order != 0 {
			return order
		}
		return strings.Compare(first.id, second.id)
	})
	return result
}

// mutantIndex maps an inventory by mutant identity, keeping the first entry of
// a repeated identity so that a malformed report is compared rather than
// silently doubled.
func mutantIndex(dispositions []report.MutantDisposition) map[string]report.MutantDisposition {
	index := make(map[string]report.MutantDisposition, len(dispositions))
	for _, disposition := range dispositions {
		if _, kept := index[disposition.ID]; !kept {
			index[disposition.ID] = disposition
		}
	}
	return index
}

// kindsByMutant collects the finding kinds attributed to each mutant, sorted
// and deduplicated so that the order the findings were written in never
// reaches the matrix. A finding no mutant is attributed to belongs to no
// mutant's set.
func kindsByMutant(findings []report.Finding) map[string][]string {
	byMutant := make(map[string][]string)
	for _, finding := range findings {
		if finding.MutantID == "" {
			continue
		}
		byMutant[finding.MutantID] = append(byMutant[finding.MutantID], finding.Kind)
	}
	for identity, kinds := range byMutant {
		slices.Sort(kinds)
		byMutant[identity] = slices.Compact(kinds)
	}
	return byMutant
}

// kindLabel names one mutant's set of finding kinds, and names the empty set
// rather than leaving a blank cell.
func kindLabel(kinds []string) string {
	if len(kinds) == 0 {
		return noKind
	}
	return strings.Join(kinds, kindSeparator)
}

// kindDeltas counts the findings of each kind in both reports, in kind order.
func kindDeltas(before, after []report.Finding) []counterDelta {
	beforeCounts := countKinds(before)
	afterCounts := countKinds(after)
	names := make([]string, 0, len(beforeCounts)+len(afterCounts))
	for kind := range beforeCounts {
		names = append(names, kind)
	}
	for kind := range afterCounts {
		if _, counted := beforeCounts[kind]; !counted {
			names = append(names, kind)
		}
	}
	slices.Sort(names)
	deltas := make([]counterDelta, 0, len(names))
	for _, kind := range names {
		deltas = append(deltas, counterDelta{name: kind, before: beforeCounts[kind], after: afterCounts[kind]})
	}
	return deltas
}

// countKinds counts the findings of one report by kind.
func countKinds(findings []report.Finding) map[string]int {
	counts := make(map[string]int)
	for _, finding := range findings {
		counts[finding.Kind]++
	}
	return counts
}

// accountingDeltas lists every counter of the report accounting on both sides,
// in the order the report declares them.
func accountingDeltas(before, after report.Accounting) []counterDelta {
	var deltas []counterDelta
	add := func(name string, first, second int) {
		deltas = append(deltas, counterDelta{name: name, before: first, after: second})
	}
	addCounts := func(group string, first, second report.CountAccounting) {
		add(group+".discovered", first.Discovered, second.Discovered)
		add(group+".selected", first.Selected, second.Selected)
		add(group+".executed", first.Executed, second.Executed)
		add(group+".skipped", first.Skipped, second.Skipped)
		add(group+".excluded", first.Excluded, second.Excluded)
	}
	addCounts("targets", before.Targets, after.Targets)
	add("mutants.discovered", before.Mutants.Discovered, after.Mutants.Discovered)
	add("mutants.selected", before.Mutants.Selected, after.Mutants.Selected)
	add("mutants.executed", before.Mutants.Executed, after.Mutants.Executed)
	add("mutants.killed", before.Mutants.Killed, after.Mutants.Killed)
	add("mutants.survived", before.Mutants.Survived, after.Mutants.Survived)
	add("mutants.inconclusive", before.Mutants.Inconclusive, after.Mutants.Inconclusive)
	add("mutants.compile_rejected", before.Mutants.CompileRejected, after.Mutants.CompileRejected)
	add("mutants.accepted", before.Mutants.Accepted, after.Mutants.Accepted)
	add("mutants.out_of_scope", before.Mutants.OutOfScope, after.Mutants.OutOfScope)
	add("mutants.unknown", before.Mutants.Unknown, after.Mutants.Unknown)
	// How much of each run was answered without executing anything is part of
	// what two runs differ in, so it is compared like every other counter.
	add("mutants.reused_killed", before.Mutants.ReusedKilled, after.Mutants.ReusedKilled)
	add("mutants.reused_survived", before.Mutants.ReusedSurvived, after.Mutants.ReusedSurvived)
	addCounts("race", before.Race, after.Race)
	return deltas
}

// sortedStatuses orders the status matrix by how many mutants took each
// transition, then by the transition itself so that ties never reorder.
func sortedStatuses(counted map[statusTransition]int) []statusTransition {
	transitions := make([]statusTransition, 0, len(counted))
	for transition, mutants := range counted {
		transition.mutants = mutants
		transitions = append(transitions, transition)
	}
	slices.SortFunc(transitions, func(first, second statusTransition) int {
		if order := cmp.Compare(second.mutants, first.mutants); order != 0 {
			return order
		}
		if order := strings.Compare(string(first.before), string(second.before)); order != 0 {
			return order
		}
		return strings.Compare(string(first.after), string(second.after))
	})
	return transitions
}

// sortedKinds orders the finding-kind matrix the same way.
func sortedKinds(counted map[kindTransition]int) []kindTransition {
	transitions := make([]kindTransition, 0, len(counted))
	for transition, mutants := range counted {
		transition.mutants = mutants
		transitions = append(transitions, transition)
	}
	slices.SortFunc(transitions, func(first, second kindTransition) int {
		if order := cmp.Compare(second.mutants, first.mutants); order != 0 {
			return order
		}
		if order := strings.Compare(first.before, second.before); order != 0 {
			return order
		}
		return strings.Compare(first.after, second.after)
	})
	return transitions
}
