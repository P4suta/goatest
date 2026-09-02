// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"cmp"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/P4suta/goatest/internal/trace"
)

// Shape of the report.
const (
	// execClassWords is how many arguments of a command line make its class.
	// Six is what keeps the package under test inside the class of a mutation
	// control run — "go test -count=1 <package> -args -test.run=<value>" — so
	// the packages a run pays for stay separate rather than collapsing into
	// one "go test" line.
	execClassWords = 6
	// execClassLimit and mutantLimit cap the two tables a real recording would
	// otherwise print thousands of rows of. What the cap leaves out is
	// accounted for in a line under the table rather than dropped.
	execClassLimit = 15
	mutantLimit    = 10
	// columnGap separates two columns of a table.
	columnGap = "  "
)

// Placeholders for a value the recording did not carry. They are printed
// rather than left blank so that a missing value reads as missing.
const (
	noCommand  = "(no command)"
	noOutcome  = "(none)"
	noValue    = "-"
	ellipsis   = "..."
	unrecorded = "(unrecorded)"
)

// renderSummary renders the whole breakdown of one recording, ending in a
// newline. It reads the events alone: the same events render the same bytes.
func renderSummary(source string, events []trace.Event) string {
	if len(events) == 0 {
		return "trace: " + source + "\nthe stream carries no events\n"
	}
	blocks := [][]string{
		headerBlock(source, events),
		phaseBlock(events),
		execBlock(events),
		routingBlock(events),
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

// headerBlock names the recording and counts what it holds, which is what
// tells a complete stream from a fragment before any total is read.
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

// census counts the events by type, in the order the contract lists them,
// naming only the types the recording carries.
func census(events []trace.Event) string {
	order := []string{
		trace.TypeRunStart, trace.TypePhaseStart, trace.TypePhaseEnd, trace.TypeExec,
		trace.TypeMutantExec, trace.TypeRoute, trace.TypeProgress, trace.TypeArtifact,
		trace.TypeRunEnd,
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

// phaseTotal is the time one phase cost across every pass through it. A run
// that promotes a repair passes through the mutation phase again, so a phase
// is a total and a count rather than a single duration.
type phaseTotal struct {
	name     string
	duration int64
	passes   int
}

// phaseBlock breaks the recording down by phase, which is the first question
// about a run's cost: which part of it was the run.
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

// phaseTotals sums the phases of a recording, longest first.
func phaseTotals(events []trace.Event) []phaseTotal {
	index := make(map[string]*phaseTotal)
	for _, event := range events {
		if event.Type != trace.TypePhaseEnd || event.Phase == nil {
			continue
		}
		total, kept := index[event.Phase.Name]
		if !kept {
			total = &phaseTotal{name: event.Phase.Name}
			index[event.Phase.Name] = total
		}
		total.duration += event.Phase.DurationMS
		total.passes++
	}
	totals := make([]phaseTotal, 0, len(index))
	for _, total := range index {
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

// execTotal is the time one class of command line cost across every call of
// it.
type execTotal struct {
	class    string
	duration int64
	calls    int
}

// execBlock breaks the executed commands down by class, which is what turns
// thousands of one-off command lines into the handful of commands a run
// actually repeats.
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

// execTotals sums the executed commands by class, most expensive first.
func execTotals(events []trace.Event) []execTotal {
	index := make(map[string]*execTotal)
	for _, event := range events {
		if event.Type != trace.TypeExec || event.Exec == nil {
			continue
		}
		class := execClass(event.Exec.Argv)
		total, kept := index[class]
		if !kept {
			total = &execTotal{class: class}
			index[class] = total
		}
		total.duration += event.Exec.DurationMS
		total.calls++
	}
	totals := make([]execTotal, 0, len(index))
	for _, total := range index {
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

// execClass reduces one argument vector to the class of command it belongs to:
// its opening arguments, with the parts that differ between two otherwise
// identical commands replaced by a placeholder.
//
//   - "-flag=value" becomes "-flag=<value>", which is what collapses the
//     per-mutant "-test.run=^TestSomething$" into one class;
//   - an absolute path becomes "<path>", which is what collapses the compiled
//     test binaries a run leaves in a temporary directory;
//   - every other argument is kept verbatim, so the package under test still
//     separates one class from another.
//
// A command line longer than the class ends in an ellipsis, so a class is
// never mistaken for a complete command line.
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

// classArgument replaces the part of one argument that varies between two runs
// of the same command.
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

// isAbsolutePath reports whether an argument is an absolute path, on the
// platform that recorded the trace rather than on the one reading it: a trace
// is read on any machine, so both forms are recognized everywhere.
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

// labelCount is one label of a tally and how often the recording carried it.
// A tally is built in a fixed order rather than by iterating a map, so the
// same recording prints the same line.
type labelCount struct {
	label string
	count int
}

// routeTotal is what the route events of a recording add up to: how many
// routes there were, over how many mutants, how each of them was decided, and
// how many targets they selected out of the ones the file alone would have.
type routeTotal struct {
	routes  int
	mutants int
	// reasons, granularities and fallbacks tally the routes by each of the
	// three labels a route carries, in the order the block prints them.
	reasons       []labelCount
	granularities []labelCount
	fallbacks     []labelCount
	// discharges tallies the targets a proof removed from a reaching set by
	// the proof that removed them, and dischargedRoutes counts the routes that
	// carry at least one. A target is discharged once, so the tally counts
	// targets while the count beside it counts routes.
	discharges       []labelCount
	dischargedRoutes int
	// fanOut counts the routes by how many targets they reached, one entry
	// per bucket of fanOutBucketLabels.
	fanOut []int
	// reaching is the targets every route selected, whatever it recorded of
	// how it was decided.
	reaching int
	// recorded is how many routes named a granularity, which is what marks a
	// route as carrying its routing metadata at all. recordedReaching and
	// candidates are the two sides of the reduction those routes bought: the
	// targets they selected, and the targets covering the mutated file that
	// they were selected from. Both are summed over the recorded routes
	// alone, so a route that recorded nothing cannot move either side.
	recorded         int
	recordedReaching int
	candidates       int
}

// fanOutBucketLabels names the buckets of the reaching-target histogram, which
// is the one place their number is decided. The buckets double, because the
// interesting difference between two routings is an order of magnitude of
// fan-out rather than a target or two.
func fanOutBucketLabels() []string {
	return []string{"0", "1", "2-3", "4-7", "8-15", "16-31", "32-63", "64+"}
}

// fanOutBucketIndex is the bucket a fan-out of the given size belongs to.
// Nothing and one target get a bucket each, because "no test reaches this" and
// "exactly one does" are the two answers a routing change is judged by;
// everything above the last bound falls into the open bucket that closes the
// histogram.
func fanOutBucketIndex(reaching int) int {
	if reaching <= 0 {
		return 0
	}
	last := len(fanOutBucketLabels()) - 1
	index := 1
	for bound := 2; index < last && reaching >= bound; bound *= 2 {
		index++
	}
	return index
}

// routingBlock breaks the routing decisions down: how many mutants were
// routed, on what evidence, and how much of the file-wide candidate set the
// routes actually selected. A recording made before routing reported its
// granularity carries none of it, and the block says so rather than reading
// the absence as a decision.
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
	// A recording no proof discharged a target in renders as it did before the
	// proofs existed, so the line is absent rather than a tally of zeroes.
	if discharged := countedLabels(total.discharges); discharged > 0 {
		lines = append(lines, fmt.Sprintf("discharged: %s across %s (%s)",
			plural(discharged, "target", "targets"),
			plural(total.dischargedRoutes, "route", "routes"),
			formatLabelCounts(total.discharges)))
	}
	lines = append(lines,
		"reduction: "+formatReduction(total.recorded, total.candidates, total.recordedReaching),
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

// routeTotals sums the route events of a recording.
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
		granularity := record.Granularity
		if granularity == "" {
			granularity = unrecorded
		}
		granularities[granularity]++
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
		if record.Granularity == "" {
			continue
		}
		total.recorded++
		total.recordedReaching += len(record.ReachingTargets)
		total.candidates += record.FileCandidates
	}
	total.mutants = len(mutants)
	total.reasons = tally(reasons, trace.ReasonCoverageReaching, trace.ReasonUnreached)
	total.granularities = tally(granularities, trace.GranularityBlock, trace.GranularityFile, unrecorded)
	total.fallbacks = tally(fallbacks, trace.FallbackPositionUnknown, trace.FallbackOutsideBlocks)
	total.discharges = tally(discharges, trace.DischargeBranchNeverTaken)
	return total
}

// tally projects counted labels onto the labels the contract names, in that
// order. The map is read by key alone, so no iteration order reaches the
// output.
func tally(counts map[string]int, labels ...string) []labelCount {
	projected := make([]labelCount, 0, len(labels))
	for _, label := range labels {
		projected = append(projected, labelCount{label: label, count: counts[label]})
	}
	return projected
}

// countedLabels is how many routes a whole tally accounts for.
func countedLabels(counts []labelCount) int {
	total := 0
	for _, count := range counts {
		total += count.count
	}
	return total
}

// formatLabelCounts renders a tally as one line.
func formatLabelCounts(counts []labelCount) string {
	parts := make([]string, 0, len(counts))
	for _, count := range counts {
		parts = append(parts, fmt.Sprintf("%s %d", count.label, count.count))
	}
	return strings.Join(parts, ", ")
}

// formatReduction renders what routing saved over the routes that recorded
// how they were decided: the targets the file alone would have selected
// against the ones those routes did.
//
// A granularity is what marks a route as carrying its routing metadata, so
// recorded is how many routes carry any of it. On such a route an absent
// candidate count is a count of zero, which is a file no test binary was ever
// linked against rather than a missing measurement; on a route without a
// granularity the metadata was never recorded at all, and nothing but that is
// reported. A zero candidate count has no share to take, so the reduction it
// bought is named rather than divided by nothing.
func formatReduction(recorded, candidates, reaching int) string {
	if recorded <= 0 {
		return "not recorded"
	}
	if candidates <= 0 {
		return fmt.Sprintf("file candidates %d -> reaching %d (nothing to reduce)", candidates, reaching)
	}
	return fmt.Sprintf("file candidates %d -> reaching %d (%s fewer)",
		candidates, reaching, formatShare(int64(candidates-reaching), int64(candidates)))
}

// mutantTotal is what one mutant cost across every execution of it.
type mutantTotal struct {
	id         string
	display    string
	pkg        string
	duration   int64
	executions int
}

// outcomeTotal is what one outcome cost across the executions that reached it.
type outcomeTotal struct {
	outcome    string
	duration   int64
	executions int
}

// dispositionTotal is what one final disposition cost. A mutant a repair round
// revisits is executed again and may end elsewhere than it started, so an
// outcome total answers "what did the executions conclude" while a disposition
// total answers "what did the mutants that concluded this way cost".
type dispositionTotal struct {
	disposition string
	mutants     int
	executions  int
	duration    int64
}

// dispositionTotals charges every execution of a mutant to the outcome its
// last execution reached, most executions first. That is the whole point of
// the table: a mutant that survives in the end was paid for by every execution
// it took to get there, including the ones that killed an earlier version.
func dispositionTotals(events []trace.Event) []dispositionTotal {
	type mutantRun struct {
		outcome    string
		executions int
		duration   int64
	}
	runs := make(map[string]*mutantRun)
	for _, event := range events {
		if event.Type != trace.TypeMutantExec || event.Mutant == nil {
			continue
		}
		record := event.Mutant
		run, kept := runs[record.ID]
		if !kept {
			run = &mutantRun{}
			runs[record.ID] = run
		}
		run.outcome = record.Outcome
		run.executions++
		run.duration += record.DurationMS
	}
	index := make(map[string]*dispositionTotal, len(runs))
	for _, run := range runs {
		disposition := run.outcome
		if disposition == "" {
			disposition = noOutcome
		}
		total, kept := index[disposition]
		if !kept {
			total = &dispositionTotal{disposition: disposition}
			index[disposition] = total
		}
		total.mutants++
		total.executions += run.executions
		total.duration += run.duration
	}
	totals := make([]dispositionTotal, 0, len(index))
	for _, total := range index {
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

// mutantBlock breaks the mutant executions down: how many there were, how many
// mutants they covered, what became of them, and which mutants were executed
// most. A trace records executions rather than a catalog, so the mutants are
// counted as the distinct identities the executions名 name.
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

// mutantName is the identity a mutant is reported by: the readable one the
// engine gave it, and the full identity when it gave none.
func mutantName(total mutantTotal) string {
	if total.display != "" {
		return total.display
	}
	return total.id
}

// mutantTotals sums the mutant executions by mutant and by outcome, returning
// the mutants most executed first, the outcomes most reached first, and the
// executions and their total duration.
func mutantTotals(events []trace.Event) ([]mutantTotal, []outcomeTotal, int, int64) {
	byMutant := make(map[string]*mutantTotal)
	byOutcome := make(map[string]*outcomeTotal)
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
		}
		total.duration += record.DurationMS
		total.executions++
		outcome, kept := byOutcome[record.Outcome]
		if !kept {
			outcome = &outcomeTotal{outcome: record.Outcome}
			byOutcome[record.Outcome] = outcome
		}
		outcome.duration += record.DurationMS
		outcome.executions++
	}
	mutants := make([]mutantTotal, 0, len(byMutant))
	for _, total := range byMutant {
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
	outcomes := make([]outcomeTotal, 0, len(byOutcome))
	for _, total := range byOutcome {
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

// runBlock closes the summary with the verdict and the event accounting, which
// is what says whether the totals above were read from a complete recording.
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

// column is one column of a rendered table: its heading, and whether its cells
// are numbers that read better against the right edge.
type column struct {
	header string
	right  bool
}

// renderTable renders a heading and its rows as aligned columns. Every line is
// free of trailing spaces, so a table stays the same bytes however wide its
// widest cell is.
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

// pad widens one cell to its column.
func pad(cell string, width int, right bool) string {
	padding := strings.Repeat(" ", max(width-utf8.RuneCountInString(cell), 0))
	if right {
		return padding + cell
	}
	return cell + padding
}

// formatDuration renders recorded milliseconds. The recording is the only
// source of the value, so the same trace prints the same duration forever.
func formatDuration(milliseconds int64) string {
	return (time.Duration(milliseconds) * time.Millisecond).String()
}

// formatMean renders the mean of a total over the calls that made it.
func formatMean(total int64, calls int) string {
	if calls <= 0 {
		return noValue
	}
	return formatDuration(total / int64(calls))
}

// formatShare renders a part of a total as a percentage, and a part of nothing
// as no share at all rather than as zero.
func formatShare(part, total int64) string {
	if total <= 0 {
		return noValue
	}
	return strconv.FormatFloat(100*float64(part)/float64(total), 'f', 1, 64) + "%"
}

// plural renders a count with the word for it.
func plural(count int, singular, plural string) string {
	if count == 1 {
		return "1 " + singular
	}
	return strconv.Itoa(count) + " " + plural
}

// orNoValue renders a value the recording did not carry as a placeholder.
func orNoValue(value string) string {
	if value == "" {
		return noValue
	}
	return value
}
