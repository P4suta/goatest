// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// columnGap separates two columns of a table.
const columnGap = "  "

// durationMillisecondLimit is the largest magnitude of milliseconds that
// converts into a duration. A duration counts nanoseconds in a signed 64-bit
// integer, so a thousand times more milliseconds than that overflows it.
const durationMillisecondLimit = int64(math.MaxInt64) / int64(time.Millisecond)

// renderComparison renders the whole comparison of two reports, ending in a
// newline. It reads the comparison alone: the same pair of reports renders the
// same bytes on any machine at any moment.
func renderComparison(beforePath, afterPath string, result comparison) string {
	blocks := [][]string{
		headerBlock(beforePath, afterPath, result),
		accountingBlock(result),
		inventoryBlock(result),
		statusBlock(result),
		kindBlock(result),
		regressionBlock(result),
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

// headerBlock names the two reports and what each of them concluded, which is
// the first question about a pair of runs: did the verdict move at all.
func headerBlock(beforePath, afterPath string, result comparison) []string {
	return []string{
		"before: " + beforePath,
		"after: " + afterPath,
		"verdict: " + change(string(result.verdictBefore), string(result.verdictAfter)),
		"run: " + change(result.runBefore, result.runAfter),
		"duration: " + change(formatDuration(result.durationBefore), formatDuration(result.durationAfter)),
	}
}

// accountingBlock lists every counter of the report accounting, unchanged ones
// included, and says how many of them moved.
func accountingBlock(result comparison) []string {
	lines := []string{"accounting"}
	rows := make([][]string, 0, len(result.accounting))
	changed := 0
	for _, delta := range result.accounting {
		if delta.before != delta.after {
			changed++
		}
		rows = append(rows, []string{
			delta.name,
			strconv.Itoa(delta.before),
			strconv.Itoa(delta.after),
			formatDelta(delta.before, delta.after),
		})
	}
	columns := []column{{"counter", false}, {"before", true}, {"after", true}, {"delta", true}}
	lines = append(lines, renderTable(columns, rows)...)
	return append(lines, fmt.Sprintf("changed: %d of %d counters", changed, len(result.accounting)))
}

// inventoryBlock says how much of the two inventories can be compared at all.
// A mutant identity is content addressed, so a mutant in one report alone is a
// mutant the other run never discovered rather than a renamed one.
func inventoryBlock(result comparison) []string {
	return []string{
		"mutants",
		fmt.Sprintf("common: %d, only in the before report: %d, only in the after report: %d",
			result.commonMutants, result.onlyBefore, result.onlyAfter),
	}
}

// statusBlock renders the status transition matrix over the common mutants.
func statusBlock(result comparison) []string {
	lines := []string{"status transitions"}
	if len(result.statuses) == 0 {
		return append(lines, "no mutant is in both reports")
	}
	rows := make([][]string, 0, len(result.statuses))
	for _, transition := range result.statuses {
		rows = append(rows, []string{
			string(transition.before),
			string(transition.after),
			strconv.Itoa(transition.mutants),
		})
	}
	columns := []column{{"before", false}, {"after", false}, {"mutants", true}}
	return append(lines, renderTable(columns, rows)...)
}

// kindBlock renders what the findings said about the common mutants, and then
// the finding counts of the two reports as a whole. The two answer different
// questions: which mutants were reported differently, and whether the reports
// carry more findings than they did.
func kindBlock(result comparison) []string {
	lines := []string{"finding kinds per mutant"}
	if len(result.kinds) == 0 {
		lines = append(lines, "no mutant is in both reports")
	} else {
		rows := make([][]string, 0, len(result.kinds))
		for _, transition := range result.kinds {
			rows = append(rows, []string{transition.before, transition.after, strconv.Itoa(transition.mutants)})
		}
		columns := []column{{"before", false}, {"after", false}, {"mutants", true}}
		lines = append(lines, renderTable(columns, rows)...)
	}
	lines = append(lines, "", "finding kinds by count")
	if len(result.kindTotals) == 0 {
		return append(lines, "neither report carries a finding")
	}
	rows := make([][]string, 0, len(result.kindTotals))
	before, after := 0, 0
	for _, delta := range result.kindTotals {
		before += delta.before
		after += delta.after
		rows = append(rows, []string{
			delta.name,
			strconv.Itoa(delta.before),
			strconv.Itoa(delta.after),
			formatDelta(delta.before, delta.after),
		})
	}
	columns := []column{{"kind", false}, {"before", true}, {"after", true}, {"delta", true}}
	lines = append(lines, renderTable(columns, rows)...)
	return append(lines, fmt.Sprintf("findings: %d -> %d", before, after))
}

// regressionBlock names the mutants that stopped being killed, which is the
// one difference between two reports that no change is allowed to cause.
func regressionBlock(result comparison) []string {
	lines := []string{"regressions"}
	if len(result.regressions) == 0 {
		return append(lines, "no mutant that was killed stopped being killed")
	}
	lines = append(lines, fmt.Sprintf("mutants that stopped being killed: %d", len(result.regressions)))
	rows := make([][]string, 0, len(result.regressions))
	for _, regression := range result.regressions {
		rows = append(rows, []string{
			regression.path,
			strconv.Itoa(regression.line),
			string(regression.status),
			orNoValue(regression.rule),
			regression.id,
		})
	}
	columns := []column{{"path", false}, {"line", true}, {"status", false}, {"rule", false}, {"mutant", false}}
	return append(lines, renderTable(columns, rows)...)
}

// change renders one value on both sides of a comparison.
func change(before, after string) string {
	return orNoValue(before) + " -> " + orNoValue(after)
}

// formatDelta renders the movement of a counter, signed so that a gain and a
// loss are told apart at a glance.
func formatDelta(before, after int) string {
	difference := after - before
	if difference > 0 {
		return "+" + strconv.Itoa(difference)
	}
	return strconv.Itoa(difference)
}

// The table rendering below is deliberately duplicated from
// internal/devtools/tracesummary/summary.go rather than shared. These are two
// self-contained developer tools that happen to print aligned columns; a
// package existing only to hold twenty lines of padding would couple them, so
// that when one tool's output has to change the other would have to be
// re-reviewed with it.

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

// formatDuration renders recorded milliseconds. The report is the only source
// of the value, so the same pair prints the same duration forever.
//
// A duration counts nanoseconds, so a count past durationMillisecondLimit does
// not convert into one: the multiplication wraps, and a report claiming a
// hundred million years would print as a negative duration. Such a count is
// printed as the milliseconds the report carried, which says what was read
// rather than what it wrapped to.
func formatDuration(milliseconds int64) string {
	if milliseconds > durationMillisecondLimit || milliseconds < -durationMillisecondLimit {
		return strconv.FormatInt(milliseconds, 10) + "ms"
	}
	return (time.Duration(milliseconds) * time.Millisecond).String()
}

// orNoValue renders a value a report did not carry as a placeholder.
func orNoValue(value string) string {
	if value == "" {
		return noValue
	}
	return value
}
