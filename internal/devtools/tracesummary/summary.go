// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"cmp"
	"fmt"
	"math/bits"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/P4suta/goatest/internal/trace"
)

const (
	execClassWords = 6

	execClassLimit = 15
	mutantLimit    = 10

	columnGap = "  "
)

const (
	noCommand = "(no command)"
	noOutcome = "(none)"
	noValue   = "-"
	ellipsis  = "..."
)

func renderSummary(source string, events []trace.Event) string {
	if len(events) == 0 {
		return "trace: " + source + "\nthe stream carries no events\n"
	}
	blocks := [][]string{
		headerBlock(source, events),
		phaseBlock(events),
		prepareBlock(events),
		execBlock(events),
		routingBlock(events),
		probeBlock(events),
		controlBlock(events),
		mutantBlock(events),
		runBlock(events),
	}
	var lines []string
	for _, block := range blocks {
		if len(block) == 0 {
			continue
		}
		if len(lines) > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, block...)
	}
	return strings.Join(lines, "\n") + "\n"
}

func headerBlock(source string, events []trace.Event) []string {
	lines := []string{"trace: " + source}
	if schema := events[0].Schema; schema != "" {
		lines = append(lines, "schema: "+schema)
	}
	elapsed := int64(0)
	for _, event := range events {
		elapsed = max(elapsed, event.ElapsedMS)
	}
	lines = append(lines, "elapsed: "+formatDuration(elapsed))
	return append(lines, fmt.Sprintf("events: %d (%s)", len(events), census(events)))
}

func census(events []trace.Event) string {
	order := []string{
		trace.TypeRunStart, trace.TypePhaseStart, trace.TypePhaseEnd, trace.TypePrepare, trace.TypeExec,
		trace.TypeMutantExec, trace.TypeRoute, trace.TypeProbeExec, trace.TypeProgress,
		trace.TypeArtifact, trace.TypeRunEnd,
	}
	counts := make(map[string]int, len(order))
	for _, event := range events {
		counts[event.Type]++
	}
	parts := make([]string, 0, len(order))
	for _, eventType := range order {
		if count := counts[eventType]; count > 0 {
			parts = append(parts, fmt.Sprintf("%s %d", eventType, count))
		}
	}
	return strings.Join(parts, ", ")
}

type prepareTotal struct {
	phase     string
	duration  int64
	started   int
	finished  int
	succeeded int
	failed    int
	skipped   int
}

func prepareBlock(events []trace.Event) []string {
	totals := prepareTotals(events)
	if len(totals) == 0 {
		return nil
	}
	overall, started, finished := int64(0), 0, 0
	for _, total := range totals {
		overall += total.duration
		started += total.started
		finished += total.finished
	}
	rows := make([][]string, 0, len(totals))
	for _, total := range totals {
		rows = append(rows, []string{
			formatDuration(total.duration),
			formatShare(total.duration, overall),
			strconv.Itoa(total.started),
			strconv.Itoa(total.finished),
			strconv.Itoa(total.succeeded),
			strconv.Itoa(total.failed),
			strconv.Itoa(total.skipped),
			total.phase,
		})
	}
	columns := []column{
		{"duration", true},
		{"share", true},
		{"started", true},
		{"finished", true},
		{"succeeded", true},
		{"failed", true},
		{"skipped", true},
		{"stage", false},
	}
	lines := []string{"preparation by total duration"}
	lines = append(lines, renderTable(columns, rows)...)
	return append(lines, fmt.Sprintf("total: %s across %s and %s",
		formatDuration(overall), plural(started, "started stage", "started stages"),
		plural(finished, "finished stage", "finished stages")))
}

func prepareTotals(events []trace.Event) []prepareTotal {
	index := make(map[string]*prepareTotal)
	ordered := make([]*prepareTotal, 0)
	for _, event := range events {
		if event.Type != trace.TypePrepare || event.Prepare == nil {
			continue
		}
		record := event.Prepare
		total, kept := index[record.Phase]
		if !kept {
			total = &prepareTotal{phase: record.Phase}
			index[record.Phase] = total
			ordered = append(ordered, total)
		}
		switch record.State {
		case trace.PrepareStateStarted:
			total.started++
		case trace.PrepareStateFinished:
			total.finished++
			if record.DurationMS != nil {
				total.duration += *record.DurationMS
			}
			switch record.Result {
			case trace.PrepareResultSucceeded:
				total.succeeded++
			case trace.PrepareResultFailed:
				total.failed++
			case trace.PrepareResultSkipped:
				total.skipped++
			}
		}
	}
	totals := make([]prepareTotal, 0, len(ordered))
	for _, total := range ordered {
		totals = append(totals, *total)
	}
	slices.SortFunc(totals, func(first, second prepareTotal) int {
		if order := cmp.Compare(second.duration, first.duration); order != 0 {
			return order
		}
		if order := cmp.Compare(second.finished, first.finished); order != 0 {
			return order
		}
		if order := cmp.Compare(second.started, first.started); order != 0 {
			return order
		}
		return cmp.Compare(first.phase, second.phase)
	})
	return totals
}

type phaseTotal struct {
	name     string
	duration int64
	passes   int
}

func phaseBlock(events []trace.Event) []string {
	totals := phaseTotals(events)
	lines := []string{"phases by total duration"}
	if len(totals) == 0 {
		return append(lines, "no phase ended in this recording")
	}
	overall, passes := int64(0), 0
	for _, total := range totals {
		overall += total.duration
		passes += total.passes
	}
	rows := make([][]string, 0, len(totals))
	for _, total := range totals {
		rows = append(rows, []string{
			formatDuration(total.duration),
			formatShare(total.duration, overall),
			strconv.Itoa(total.passes),
			total.name,
		})
	}
	columns := []column{{"duration", true}, {"share", true}, {"passes", true}, {"phase", false}}
	lines = append(lines, renderTable(columns, rows)...)
	return append(lines, fmt.Sprintf("total: %s across %s in %s",
		formatDuration(overall), plural(len(totals), "phase", "phases"), plural(passes, "pass", "passes")))
}

func phaseTotals(events []trace.Event) []phaseTotal {
	index := make(map[string]*phaseTotal)
	ordered := make([]*phaseTotal, 0)
	for _, event := range events {
		if event.Type != trace.TypePhaseEnd || event.Phase == nil {
			continue
		}
		total, kept := index[event.Phase.Name]
		if !kept {
			total = &phaseTotal{name: event.Phase.Name}
			index[event.Phase.Name] = total
			ordered = append(ordered, total)
		}
		total.duration += event.Phase.DurationMS
		total.passes++
	}
	totals := make([]phaseTotal, 0, len(ordered))
	for _, total := range ordered {
		totals = append(totals, *total)
	}
	slices.SortFunc(totals, func(first, second phaseTotal) int {
		if order := cmp.Compare(second.duration, first.duration); order != 0 {
			return order
		}
		if order := cmp.Compare(second.passes, first.passes); order != 0 {
			return order
		}
		return cmp.Compare(first.name, second.name)
	})
	return totals
}

type execTotal struct {
	class    string
	duration int64
	calls    int
}

func execBlock(events []trace.Event) []string {
	totals := execTotals(events)
	lines := []string{"exec classes by total duration"}
	if len(totals) == 0 {
		return append(lines, "no command was executed in this recording")
	}
	overall, calls := int64(0), 0
	for _, total := range totals {
		overall += total.duration
		calls += total.calls
	}
	shown := totals
	if len(shown) > execClassLimit {
		shown = shown[:execClassLimit]
		lines[0] += fmt.Sprintf(" (top %d of %d)", execClassLimit, len(totals))
	}
	rows := make([][]string, 0, len(shown))
	for _, total := range shown {
		rows = append(rows, []string{
			formatDuration(total.duration),
			formatShare(total.duration, overall),
			strconv.Itoa(total.calls),
			formatMean(total.duration, total.calls),
			total.class,
		})
	}
	columns := []column{{"duration", true}, {"share", true}, {"calls", true}, {"mean", true}, {"class", false}}
	lines = append(lines, renderTable(columns, rows)...)
	if rest := totals[len(shown):]; len(rest) > 0 {
		restDuration, restCalls := int64(0), 0
		for _, total := range rest {
			restDuration += total.duration
			restCalls += total.calls
		}
		lines = append(lines, fmt.Sprintf("%s: %s, %s",
			plural(len(rest), "more class", "more classes"),
			plural(restCalls, "call", "calls"), formatDuration(restDuration)))
	}
	return append(lines, fmt.Sprintf("total: %s across %s in %s",
		formatDuration(overall), plural(calls, "call", "calls"),
		plural(len(totals), "class", "classes")))
}

func execTotals(events []trace.Event) []execTotal {
	index := make(map[string]*execTotal)
	ordered := make([]*execTotal, 0)
	for _, event := range events {
		if event.Type != trace.TypeExec || event.Exec == nil {
			continue
		}
		class := execClass(event.Exec.Argv)
		total, kept := index[class]
		if !kept {
			total = &execTotal{class: class}
			index[class] = total
			ordered = append(ordered, total)
		}
		total.duration += event.Exec.DurationMS
		total.calls++
	}
	totals := make([]execTotal, 0, len(ordered))
	for _, total := range ordered {
		totals = append(totals, *total)
	}
	slices.SortFunc(totals, func(first, second execTotal) int {
		if order := cmp.Compare(second.duration, first.duration); order != 0 {
			return order
		}
		if order := cmp.Compare(second.calls, first.calls); order != 0 {
			return order
		}
		return cmp.Compare(first.class, second.class)
	})
	return totals
}

func execClass(argv []string) string {
	if len(argv) == 0 {
		return noCommand
	}
	words := make([]string, 0, execClassWords+1)
	for _, argument := range argv[:min(len(argv), execClassWords)] {
		words = append(words, classArgument(argument))
	}
	if len(argv) > execClassWords {
		words = append(words, ellipsis)
	}
	return strings.Join(words, " ")
}

func classArgument(argument string) string {
	if isAbsolutePath(argument) {
		return "<path>"
	}
	if strings.HasPrefix(argument, "-") {
		if name, _, found := strings.Cut(argument, "="); found {
			return name + "=<value>"
		}
	}
	return argument
}

func isAbsolutePath(argument string) bool {
	if strings.HasPrefix(argument, "/") {
		return true
	}
	if len(argument) < 3 || argument[1] != ':' {
		return false
	}
	drive := argument[0] | ' '
	return drive >= 'a' && drive <= 'z' && (argument[2] == '\\' || argument[2] == '/')
}

type labelCount struct {
	label string
	count int
}

type routeTotal struct {
	routes  int
	mutants int

	reasons       []labelCount
	granularities []labelCount
	fallbacks     []labelCount

	discharges       []labelCount
	dischargedRoutes int

	suiteCoverage  int
	suiteReached   int
	suiteUnreached int

	probed int

	reused int

	fanOut []int

	reaching int

	candidates int
}

func fanOutBucketLabels() []string {
	return []string{"0", "1", "2-3", "4-7", "8-15", "16-31", "32-63", "64+"}
}

func fanOutBucketIndex(reaching int) int {
	if reaching <= 0 {
		return 0
	}
	last := len(fanOutBucketLabels()) - 1
	return min(bits.Len(uint(reaching)), last)
}

func routingBlock(events []trace.Event) []string {
	total := routeTotals(events)
	lines := []string{"routing"}
	if total.routes == 0 {
		return append(lines, "no mutant was routed in this recording")
	}
	lines = append(lines,
		fmt.Sprintf("routes: %d across %s", total.routes, plural(total.mutants, "mutant", "mutants")),
		"reasons: "+formatLabelCounts(total.reasons),
		"granularity: "+formatLabelCounts(total.granularities))
	if countedLabels(total.fallbacks) > 0 {
		lines = append(lines, "fallbacks: "+formatLabelCounts(total.fallbacks))
	}

	if discharged := countedLabels(total.discharges); discharged > 0 {
		lines = append(lines, fmt.Sprintf("discharged: %s across %s (%s)",
			plural(discharged, "target", "targets"),
			plural(total.dischargedRoutes, "route", "routes"),
			formatLabelCounts(total.discharges)))
	}
	if total.suiteCoverage > 0 {
		lines = append(lines, fmt.Sprintf("suite coverage: %s (%d reached, %d unreached)",
			plural(total.suiteCoverage, "route", "routes"), total.suiteReached, total.suiteUnreached))
	}
	lines = append(lines, probedLines(total.probed)...)
	if total.reused > 0 {
		lines = append(lines, fmt.Sprintf("reused: %s of %d",
			plural(total.reused, "route", "routes"), total.routes))
	}
	lines = append(lines,
		"reduction: "+formatReduction(total.candidates, total.reaching),
		"", "reaching targets per route")
	labels := fanOutBucketLabels()
	rows := make([][]string, 0, len(labels))
	for index, count := range total.fanOut {
		if count == 0 {
			continue
		}
		rows = append(rows, []string{labels[index], strconv.Itoa(count), formatShare(int64(count), int64(total.routes))})
	}
	columns := []column{{"targets", false}, {"routes", true}, {"share", true}}
	return append(lines, renderTable(columns, rows)...)
}

func routeTotals(events []trace.Event) routeTotal {
	reasons := make(map[string]int)
	granularities := make(map[string]int)
	fallbacks := make(map[string]int)
	discharges := make(map[string]int)
	mutants := make(map[string]struct{})
	total := routeTotal{fanOut: make([]int, len(fanOutBucketLabels()))}
	for _, event := range events {
		if event.Type != trace.TypeRoute || event.Route == nil {
			continue
		}
		record := event.Route
		total.routes++
		mutants[record.MutantID] = struct{}{}
		reasons[record.Reason]++
		granularities[record.Granularity]++
		if record.Fallback != "" {
			fallbacks[record.Fallback]++
		}
		total.fanOut[fanOutBucketIndex(len(record.ReachingTargets))]++
		total.reaching += len(record.ReachingTargets)
		if len(record.Discharged) > 0 {
			total.dischargedRoutes++
		}
		for _, discharge := range record.Discharged {
			discharges[discharge.Reason]++
		}
		if record.SuiteCoverage != "" {
			total.suiteCoverage++
			if record.SuiteReached {
				total.suiteReached++
			} else {
				total.suiteUnreached++
			}
		}
		if record.Probed {
			total.probed++
		}
		if record.Reused {
			total.reused++
		}
		total.candidates += record.FileCandidates
	}
	total.mutants = len(mutants)
	total.reasons = tally(reasons, trace.ReasonCoverageReaching, trace.ReasonProbeReaching, trace.ReasonUnreached)
	total.granularities = tally(granularities, trace.GranularityBlock, trace.GranularityFile)
	total.fallbacks = tally(fallbacks, trace.FallbackPositionUnknown, trace.FallbackOutsideBlocks)
	total.discharges = tally(discharges, trace.DischargeBranchNeverTaken, trace.DischargeNeverInfected)
	return total
}

func tally(counts map[string]int, labels ...string) []labelCount {
	projected := make([]labelCount, 0, len(labels))
	for _, label := range labels {
		projected = append(projected, labelCount{label: label, count: counts[label]})
	}
	return projected
}

func countedLabels(counts []labelCount) int {
	total := 0
	for _, count := range counts {
		total += count.count
	}
	return total
}

func formatLabelCounts(counts []labelCount) string {
	parts := make([]string, 0, len(counts))
	for _, count := range counts {
		parts = append(parts, fmt.Sprintf("%s %d", count.label, count.count))
	}
	return strings.Join(parts, ", ")
}

func formatReduction(candidates, reaching int) string {
	if candidates <= 0 {
		return fmt.Sprintf("file candidates %d -> reaching %d (nothing to reduce)", candidates, reaching)
	}
	return fmt.Sprintf("file candidates %d -> reaching %d (%s fewer)",
		candidates, reaching, formatShare(int64(candidates-reaching), int64(candidates)))
}

func probedLines(routes int) []string {
	if routes <= 0 {
		return nil
	}
	return []string{"probed: " + plural(routes, "route", "routes")}
}

const (
	probeError        = "error"
	wholeTreeUnstated = "unstated"
)

type probeTotal struct {
	executions int
	targets    int
	suites     int
	outcomes   []labelCount

	pairs         int
	mutants       int
	barrenTargets int
	barrenSuites  int

	wholeTreeSuites  int
	wholeTreeTargets int
	wholeTreeReasons []labelCount
}

func probeBlock(events []trace.Event) []string {
	total := probeTotals(events)
	if total.executions == 0 {
		return []string{"probe: not recorded"}
	}
	scope := plural(total.targets, "target", "targets")
	pair := "(target, mutant) pair"
	pairs := "(target, mutant) pairs"
	barren := plural(total.barrenTargets, "measured target", "measured targets") + " infected nothing"
	if total.suites != 0 {
		scope += " and " + plural(total.suites, "package suite", "package suites")
		pair, pairs = "(probe, mutant) pair", "(probe, mutant) pairs"
		barren += "; " + plural(total.barrenSuites, "measured package suite", "measured package suites") + " infected nothing"
	}
	lines := []string{
		fmt.Sprintf("probe: %s across %s", plural(total.executions, "execution", "executions"), scope),
		"outcomes: " + formatLabelCounts(total.outcomes),
		fmt.Sprintf("infections: %s across %s; %s",
			plural(total.pairs, pair, pairs), plural(total.mutants, "mutant", "mutants"), barren),
	}
	if total.wholeTreeSuites != 0 || total.wholeTreeTargets != 0 {
		scope := fmt.Sprintf("%d of %d targets", total.wholeTreeTargets, total.targets)
		if total.suites != 0 {
			scope += fmt.Sprintf(", %d of %d package suites", total.wholeTreeSuites, total.suites)
		}
		lines = append(lines, "whole-tree keys: "+scope+"; "+formatLabelCounts(total.wholeTreeReasons))
	}
	return lines
}

func probeTotals(events []trace.Event) probeTotal {
	outcomes := make(map[string]int)
	targets := make(map[string]struct{})
	suites := make(map[string]struct{})
	mutants := make(map[string]struct{})

	measuredTargets := make(map[string]bool)
	measuredSuites := make(map[string]bool)
	wholeTreeReasons := make(map[string]int)
	wholeTreeSuites := make(map[string]struct{})
	wholeTreeTargets := make(map[string]struct{})
	total := probeTotal{}
	for _, event := range events {
		if event.Type != trace.TypeProbeExec || event.Probe == nil || event.Probe.Control {
			continue
		}
		record := event.Probe
		total.executions++
		if record.Suite {
			suites[record.Target] = struct{}{}
		} else {
			targets[record.Target] = struct{}{}
		}
		if record.WholeTree {
			widened := wholeTreeTargets
			if record.Suite {
				widened = wholeTreeSuites
			}
			widened[record.Target] = struct{}{}
			reason := record.WholeTreeReason
			if reason == "" {
				reason = wholeTreeUnstated
			}
			wholeTreeReasons[reason]++
		}
		switch {
		case record.Outcome != "":
			outcomes[record.Outcome]++
		case record.Error != "":
			outcomes[probeError]++
		}
		if record.Outcome == trace.ProbeOutcomeMeasured {
			measured := measuredTargets
			if record.Suite {
				measured = measuredSuites
			}
			measured[record.Target] = measured[record.Target] || len(record.Infected) > 0
		}
		total.pairs += len(record.Infected)
		for _, mutant := range record.Infected {
			mutants[mutant] = struct{}{}
		}
	}
	total.targets = len(targets)
	total.suites = len(suites)
	total.mutants = len(mutants)

	for _, infected := range measuredTargets {
		if !infected {
			total.barrenTargets++
		}
	}
	for _, infected := range measuredSuites {
		if !infected {
			total.barrenSuites++
		}
	}
	total.outcomes = tally(outcomes, trace.ProbeOutcomeMeasured, trace.ProbeOutcomeTestFailed,
		trace.ProbeOutcomeTimedOut, trace.ProbeOutcomeUnavailable, probeError)
	total.wholeTreeSuites = len(wholeTreeSuites)
	total.wholeTreeTargets = len(wholeTreeTargets)
	total.wholeTreeReasons = tally(wholeTreeReasons,
		trace.WholeTreeStaticUnobservable, trace.WholeTreeLogUnavailable, trace.WholeTreeLogAmbiguous,
		trace.WholeTreeDirectoryAccess, trace.WholeTreeOutsideInput, wholeTreeUnstated)
	return total
}

func controlBlock(events []trace.Event) []string {
	outcomes := make(map[string]int)
	executions := 0
	var duration int64
	for _, event := range events {
		if event.Type != trace.TypeProbeExec || event.Probe == nil || !event.Probe.Control {
			continue
		}
		executions++
		duration += event.Probe.DurationMS
		switch {
		case event.Probe.Outcome != "":
			outcomes[event.Probe.Outcome]++
		case event.Probe.Error != "":
			outcomes[probeError]++
		}
	}
	if executions == 0 {
		return nil
	}
	return []string{
		fmt.Sprintf("exact original preflights: %s in %s", plural(executions, "execution", "executions"), formatDuration(duration)),
		"outcomes: " + formatLabelCounts(tally(outcomes,
			trace.ProbeOutcomeMeasured, trace.ProbeOutcomeTestFailed,
			trace.ProbeOutcomeTimedOut, trace.ProbeOutcomeUnavailable, probeError)),
	}
}

type mutantTotal struct {
	id         string
	display    string
	pkg        string
	duration   int64
	executions int
}

type outcomeTotal struct {
	outcome    string
	duration   int64
	executions int
}

type dispositionTotal struct {
	disposition string
	mutants     int
	executions  int
	duration    int64
}

func dispositionTotals(events []trace.Event) []dispositionTotal {
	type mutantRun struct {
		outcome    string
		executions int
		duration   int64
	}
	runs := make(map[string]*mutantRun)
	orderedRuns := make([]*mutantRun, 0)
	for _, event := range events {
		if event.Type != trace.TypeMutantExec || event.Mutant == nil {
			continue
		}
		record := event.Mutant
		run, kept := runs[record.ID]
		if !kept {
			run = &mutantRun{}
			runs[record.ID] = run
			orderedRuns = append(orderedRuns, run)
		}
		run.outcome = record.Outcome
		run.executions++
		run.duration += record.DurationMS
	}
	index := make(map[string]*dispositionTotal, len(runs))
	orderedTotals := make([]*dispositionTotal, 0)
	for _, run := range orderedRuns {
		disposition := run.outcome
		if disposition == "" {
			disposition = noOutcome
		}
		total, kept := index[disposition]
		if !kept {
			total = &dispositionTotal{disposition: disposition}
			index[disposition] = total
			orderedTotals = append(orderedTotals, total)
		}
		total.mutants++
		total.executions += run.executions
		total.duration += run.duration
	}
	totals := make([]dispositionTotal, 0, len(orderedTotals))
	for _, total := range orderedTotals {
		totals = append(totals, *total)
	}
	slices.SortFunc(totals, func(first, second dispositionTotal) int {
		if order := cmp.Compare(second.executions, first.executions); order != 0 {
			return order
		}
		if order := cmp.Compare(second.duration, first.duration); order != 0 {
			return order
		}
		return cmp.Compare(first.disposition, second.disposition)
	})
	return totals
}

func mutantBlock(events []trace.Event) []string {
	mutants, outcomes, executions, duration := mutantTotals(events)
	lines := []string{"mutant executions"}
	if executions == 0 {
		return append(lines, "no mutant was executed in this recording")
	}
	lines = append(lines,
		fmt.Sprintf("executions: %d across %s (%s per mutant)",
			executions, plural(len(mutants), "mutant", "mutants"),
			strconv.FormatFloat(float64(executions)/float64(len(mutants)), 'f', 2, 64)),
		fmt.Sprintf("duration: %s total, %s mean", formatDuration(duration), formatMean(duration, executions)),
		"",
		"outcomes by executions")
	rows := make([][]string, 0, len(outcomes))
	for _, total := range outcomes {
		outcome := total.outcome
		if outcome == "" {
			outcome = noOutcome
		}
		rows = append(rows, []string{
			outcome,
			strconv.Itoa(total.executions),
			formatShare(int64(total.executions), int64(executions)),
			formatDuration(total.duration),
		})
	}
	outcomeColumns := []column{{"outcome", false}, {"executions", true}, {"share", true}, {"duration", true}}
	lines = append(lines, renderTable(outcomeColumns, rows)...)

	dispositions := dispositionTotals(events)
	lines = append(lines, "", "executions by final disposition")
	rows = make([][]string, 0, len(dispositions))
	for _, total := range dispositions {
		rows = append(rows, []string{
			total.disposition,
			strconv.Itoa(total.mutants),
			strconv.Itoa(total.executions),
			formatShare(int64(total.executions), int64(executions)),
			formatDuration(total.duration),
		})
	}
	dispositionColumns := []column{
		{"disposition", false}, {"mutants", true}, {"executions", true}, {"share", true}, {"duration", true},
	}
	lines = append(lines, renderTable(dispositionColumns, rows)...)

	heading := "mutants by executions"
	shown := mutants
	if len(shown) > mutantLimit {
		shown = shown[:mutantLimit]
		heading += fmt.Sprintf(" (top %d of %d)", mutantLimit, len(mutants))
	}
	lines = append(lines, "", heading)
	rows = make([][]string, 0, len(shown))
	for _, total := range shown {
		rows = append(rows, []string{
			strconv.Itoa(total.executions),
			formatDuration(total.duration),
			mutantName(total),
			orNoValue(total.pkg),
		})
	}
	mutantColumns := []column{{"executions", true}, {"duration", true}, {"mutant", false}, {"package", false}}
	lines = append(lines, renderTable(mutantColumns, rows)...)
	if rest := mutants[len(shown):]; len(rest) > 0 {
		restDuration, restExecutions := int64(0), 0
		for _, total := range rest {
			restDuration += total.duration
			restExecutions += total.executions
		}
		lines = append(lines, fmt.Sprintf("%s: %s, %s",
			plural(len(rest), "more mutant", "more mutants"),
			plural(restExecutions, "execution", "executions"), formatDuration(restDuration)))
	}
	return lines
}

func mutantName(total mutantTotal) string {
	if total.display != "" {
		return total.display
	}
	return total.id
}

func mutantTotals(events []trace.Event) ([]mutantTotal, []outcomeTotal, int, int64) {
	byMutant := make(map[string]*mutantTotal)
	byOutcome := make(map[string]*outcomeTotal)
	orderedMutants := make([]*mutantTotal, 0)
	orderedOutcomes := make([]*outcomeTotal, 0)
	executions, duration := 0, int64(0)
	for _, event := range events {
		if event.Type != trace.TypeMutantExec || event.Mutant == nil {
			continue
		}
		record := event.Mutant
		executions++
		duration += record.DurationMS
		total, kept := byMutant[record.ID]
		if !kept {
			total = &mutantTotal{id: record.ID, display: record.DisplayID, pkg: record.Package}
			byMutant[record.ID] = total
			orderedMutants = append(orderedMutants, total)
		}
		total.duration += record.DurationMS
		total.executions++
		outcome, kept := byOutcome[record.Outcome]
		if !kept {
			outcome = &outcomeTotal{outcome: record.Outcome}
			byOutcome[record.Outcome] = outcome
			orderedOutcomes = append(orderedOutcomes, outcome)
		}
		outcome.duration += record.DurationMS
		outcome.executions++
	}
	mutants := make([]mutantTotal, 0, len(orderedMutants))
	for _, total := range orderedMutants {
		mutants = append(mutants, *total)
	}
	slices.SortFunc(mutants, func(first, second mutantTotal) int {
		if order := cmp.Compare(second.executions, first.executions); order != 0 {
			return order
		}
		if order := cmp.Compare(second.duration, first.duration); order != 0 {
			return order
		}
		return cmp.Compare(first.id, second.id)
	})
	outcomes := make([]outcomeTotal, 0, len(orderedOutcomes))
	for _, total := range orderedOutcomes {
		outcomes = append(outcomes, *total)
	}
	slices.SortFunc(outcomes, func(first, second outcomeTotal) int {
		if order := cmp.Compare(second.executions, first.executions); order != 0 {
			return order
		}
		if order := cmp.Compare(second.duration, first.duration); order != 0 {
			return order
		}
		return cmp.Compare(first.outcome, second.outcome)
	})
	return mutants, outcomes, executions, duration
}

func runBlock(events []trace.Event) []string {
	lines := []string{"run"}
	last := events[len(events)-1]
	if last.Type != trace.TypeRunEnd || last.Run == nil {
		return append(lines, "no run-end event; the recording is incomplete and every total above is a partial one")
	}
	lines = append(lines, "verdict: "+orNoValue(last.Run.Verdict))
	if last.Run.Error != "" {
		lines = append(lines, "error: "+last.Run.Error)
	}
	return append(lines,
		fmt.Sprintf("events emitted: %d", last.Run.EventsEmitted),
		fmt.Sprintf("events dropped: %d", last.Run.EventsDropped))
}

type column struct {
	header string
	right  bool
}

func renderTable(columns []column, rows [][]string) []string {
	widths := make([]int, len(columns))
	for index, definition := range columns {
		widths[index] = utf8.RuneCountInString(definition.header)
	}
	for _, row := range rows {
		for index, cell := range row {
			widths[index] = max(widths[index], utf8.RuneCountInString(cell))
		}
	}
	lines := make([]string, 0, len(rows)+1)
	heading := make([]string, 0, len(columns))
	for index, definition := range columns {
		heading = append(heading, pad(definition.header, widths[index], definition.right))
	}
	lines = append(lines, strings.TrimRight(strings.Join(heading, columnGap), " "))
	for _, row := range rows {
		cells := make([]string, 0, len(row))
		for index, cell := range row {
			cells = append(cells, pad(cell, widths[index], columns[index].right))
		}
		lines = append(lines, strings.TrimRight(strings.Join(cells, columnGap), " "))
	}
	return lines
}

func pad(cell string, width int, right bool) string {
	padding := strings.Repeat(" ", max(width-utf8.RuneCountInString(cell), 0))
	if right {
		return padding + cell
	}
	return cell + padding
}

func formatDuration(milliseconds int64) string {
	return (time.Duration(milliseconds) * time.Millisecond).String()
}

func formatMean(total int64, calls int) string {
	if calls <= 0 {
		return noValue
	}
	return formatDuration(total / int64(calls))
}

func formatShare(part, total int64) string {
	if total <= 0 {
		return noValue
	}
	return strconv.FormatFloat(100*float64(part)/float64(total), 'f', 1, 64) + "%"
}

func plural(count int, singular, plural string) string {
	if count == 1 {
		return "1 " + singular
	}
	return strconv.Itoa(count) + " " + plural
}

func orNoValue(value string) string {
	if value == "" {
		return noValue
	}
	return value
}
