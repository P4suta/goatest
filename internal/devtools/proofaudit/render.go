// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	// columnGap separates two columns of a table.
	columnGap = "  "
	// displayWidth is how wide a mutant is named: the first twenty characters
	// of its content address, which is the display identity the engine gives
	// it. A recording that carries none is cut to the same width so that one
	// column stays one width whatever wrote the trace.
	displayWidth = 20
	// noValue marks a field the recording did not carry, printed rather than
	// left blank so that a missing value reads as missing.
	noValue = "-"
)

// renderAudit renders the whole audit of one recording, ending in a newline.
// It reads the audit alone: the same recording renders the same bytes on any
// machine at any moment.
func renderAudit(tracePath, profilesPath, modulePath string, result auditResult) string {
	blocks := [][]string{
		headerBlock(tracePath, profilesPath, modulePath),
		countBlock(result),
		layerBlock(result),
		unverifiableBlock(result),
		violationBlock(result),
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

// headerBlock names the two halves of the recording and the module their paths
// are read against, which is what says the audit was given the run it claims.
func headerBlock(tracePath, profilesPath, modulePath string) []string {
	return []string{
		"trace: " + tracePath,
		"profiles: " + profilesPath,
		"module: " + modulePath,
	}
}

// countBlock says how much of the recording was audited, and how much of it
// was not. The kills no single target can be attributed are counted beside the
// pairs so that the audited number is read as the share of the run it is.
func countBlock(result auditResult) []string {
	rows := [][]string{
		{"routes", strconv.Itoa(result.routes)},
		{"targets with profiles", strconv.Itoa(result.targets)},
		{"killed executions", strconv.Itoa(result.killedExecutions)},
		{"kill pairs audited", strconv.Itoa(result.pairs)},
		{"package-suite kills", strconv.Itoa(result.packageSuiteKills)},
		{"batch kills", strconv.Itoa(result.batchKills)},
		{"unattributed kills", strconv.Itoa(result.unattributedKills)},
		{"truncated trailing lines", strconv.Itoa(result.truncatedLines)},
	}
	columns := []column{{"counter", false}, {"count", true}}
	return append([]string{"audit"}, renderTable(columns, rows)...)
}

// layerBlock renders what every audited layer concluded. A layer is sound for
// this recording when it drops none of the killers the run proved, so the
// violations column is the one this whole tool exists to print a zero in.
func layerBlock(result auditResult) []string {
	lines := []string{"layers"}
	if len(result.layers) == 0 {
		return append(lines, "no layer was audited")
	}
	rows := make([][]string, 0, len(result.layers))
	for _, audited := range result.layers {
		rows = append(rows, []string{
			audited.name,
			strconv.Itoa(audited.audited),
			strconv.Itoa(audited.kept),
			strconv.Itoa(audited.unverifiable),
			strconv.Itoa(audited.violations),
		})
	}
	columns := []column{
		{"layer", false}, {"audited", true}, {"kept", true}, {"unverifiable", true}, {"violations", true},
	}
	return append(lines, renderTable(columns, rows)...)
}

// unverifiableBlock names the pairs a layer could not decide from the
// recording. They are not violations — goatest keeps a target it has no block
// evidence for — but they are the part of a run the audit did not prove
// anything about, so they are named one by one.
func unverifiableBlock(result auditResult) []string {
	lines := []string{"unverifiable"}
	if len(result.unverifiable) == 0 {
		return append(lines, "every layer could decide every kill pair")
	}
	lines = append(lines, fmt.Sprintf("kill pairs a layer could not decide: %d", len(result.unverifiable)))
	return append(lines, renderRows(result.unverifiable)...)
}

// violationBlock names the pairs a layer would drop. Every one of them is a
// kill a recorded run proved and the layer would lose, which is the finding
// this tool exists for.
func violationBlock(result auditResult) []string {
	lines := []string{"violations"}
	if len(result.violations) == 0 {
		return append(lines, "no layer drops a killer a recorded run proved")
	}
	lines = append(lines, fmt.Sprintf("killers a layer would drop: %d", len(result.violations)))
	return append(lines, renderRows(result.violations)...)
}

// renderRows renders reported pairs as one table: which mutant, where it is,
// which target killed it, and what the layer said about the pair.
func renderRows(reported []auditRow) []string {
	rows := make([][]string, 0, len(reported))
	for _, row := range reported {
		rows = append(rows, []string{
			mutantName(row.pair),
			orNoValue(row.pair.rule),
			position(row.pair),
			row.pair.target,
			row.layer,
			row.why,
		})
	}
	columns := []column{
		{"mutant", false}, {"rule", false}, {"position", false},
		{"killer target", false}, {"layer", false}, {"why", false},
	}
	return renderTable(columns, rows)
}

// mutantName is the identity a mutant is reported by.
func mutantName(pair killPair) string {
	if pair.display != "" {
		return pair.display
	}
	if len(pair.mutant) > displayWidth {
		return pair.mutant[:displayWidth]
	}
	return pair.mutant
}

// position renders where a mutant is, carrying exactly what the recording
// carried: a route records a position or nothing, and a mutant the engine
// could not place is one a reader wants to see as unplaced rather than as
// sitting at line zero.
func position(pair killPair) string {
	if pair.line <= 0 {
		return pair.path
	}
	if pair.column <= 0 {
		return pair.path + ":" + strconv.Itoa(pair.line)
	}
	return pair.path + ":" + strconv.Itoa(pair.line) + ":" + strconv.Itoa(pair.column)
}

// orNoValue renders a value the recording did not carry as a placeholder.
func orNoValue(value string) string {
	if value == "" {
		return noValue
	}
	return value
}

// The table rendering below is deliberately duplicated from
// internal/devtools/reportdiff/render.go rather than shared, for the reason
// recorded there: these are self-contained developer tools that happen to
// print aligned columns, and a package existing only to hold twenty lines of
// padding would couple them, so that when one tool's output has to change the
// others would have to be re-reviewed with it.

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
