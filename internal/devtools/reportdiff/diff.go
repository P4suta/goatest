// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"cmp"
	"slices"
	"strings"

	"github.com/P4suta/goatest/internal/report"
)

const (
	noValue = "-"
	noKind  = "(none)"
)

const kindSeparator = "+"

type comparison struct {
	verdictBefore, verdictAfter   report.Verdict
	runBefore, runAfter           string
	durationBefore, durationAfter int64

	accounting []counterDelta

	commonMutants, onlyBefore, onlyAfter int

	statuses []statusTransition
	kinds    []kindTransition

	kindTotals []counterDelta

	regressions []regression
}

type counterDelta struct {
	name          string
	before, after int
}

type statusTransition struct {
	before, after report.MutantStatus
	mutants       int
}

type kindTransition struct {
	before, after string
	mutants       int
}

type regression struct {
	id     string
	path   string
	line   int
	rule   string
	status report.MutantStatus
}

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

func mutantIndex(dispositions []report.MutantDisposition) map[string]report.MutantDisposition {
	index := make(map[string]report.MutantDisposition, len(dispositions))
	for _, disposition := range dispositions {
		if _, kept := index[disposition.ID]; !kept {
			index[disposition.ID] = disposition
		}
	}
	return index
}

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

func kindLabel(kinds []string) string {
	if len(kinds) == 0 {
		return noKind
	}
	return strings.Join(kinds, kindSeparator)
}

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

func countKinds(findings []report.Finding) map[string]int {
	counts := make(map[string]int)
	for _, finding := range findings {
		counts[finding.Kind]++
	}
	return counts
}

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

	add("mutants.reused_killed", before.Mutants.ReusedKilled, after.Mutants.ReusedKilled)
	add("mutants.reused_survived", before.Mutants.ReusedSurvived, after.Mutants.ReusedSurvived)
	addCounts("race", before.Race, after.Race)
	return deltas
}

func sortedStatuses(counted map[statusTransition]int) []statusTransition {
	transitions := make([]statusTransition, 0, len(counted))
	for transition, mutants := range counted {
		transition.mutants = mutants
		transitions = append(transitions, transition)
	}
	slices.SortFunc(transitions, compareStatusTransitions)
	return transitions
}

func compareStatusTransitions(first, second statusTransition) int {
	if order := cmp.Compare(second.mutants, first.mutants); order != 0 {
		return order
	}
	if order := strings.Compare(string(first.before), string(second.before)); order != 0 {
		return order
	}
	return strings.Compare(string(first.after), string(second.after))
}

func sortedKinds(counted map[kindTransition]int) []kindTransition {
	transitions := make([]kindTransition, 0, len(counted))
	for transition, mutants := range counted {
		transition.mutants = mutants
		transitions = append(transitions, transition)
	}
	slices.SortFunc(transitions, compareKindTransitions)
	return transitions
}

func compareKindTransitions(first, second kindTransition) int {
	if order := cmp.Compare(second.mutants, first.mutants); order != 0 {
		return order
	}
	if order := strings.Compare(first.before, second.before); order != 0 {
		return order
	}
	return strings.Compare(first.after, second.after)
}
