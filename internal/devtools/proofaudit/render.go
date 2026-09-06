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
	columnGap = "  "

	displayWidth = 20

	noValue = "-"

	whyBranchNotAudited = "branch: not audited (no -catalog given)"

	whyInfectionNotAudited  = "infection: not audited (the recording holds no probe pass)"
	whySuiteReachNotAudited = "suite-reach: not audited (the recording holds no package-suite coverage profile)"

	branchDischargeHeading    = "branch discharge"
	infectionDischargeHeading = "infection discharge"
)

func renderAudit(tracePath, profilesPath, modulePath string, result auditResult) string {
	blocks := [][]string{
		headerBlock(tracePath, profilesPath, modulePath),
		countBlock(result),
		layerBlock(result),
		branchBlock(result),
		infectionBlock(result),
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

func headerBlock(tracePath, profilesPath, modulePath string) []string {
	return []string{
		"trace: " + tracePath,
		"profiles: " + profilesPath,
		"module: " + modulePath,
	}
}

func countBlock(result auditResult) []string {
	rows := [][]string{
		{"routes", strconv.Itoa(result.routes)},
		{"reused routes", strconv.Itoa(result.reusedRoutes)},
		{"targets with profiles", strconv.Itoa(result.targets)},
		{"package suites with coverage profiles", strconv.Itoa(result.suiteCoverageProfiles)},
		{"probe executions", strconv.Itoa(result.probeExecutions)},
		{"targets the probe measured", strconv.Itoa(result.probeMeasured)},
		{"package-suite probe executions", strconv.Itoa(result.suiteProbeExecutions)},
		{"package suites the probe measured", strconv.Itoa(result.suiteProbeMeasured)},
		{"killed executions", strconv.Itoa(result.killedExecutions)},
		{"target kill pairs audited", strconv.Itoa(result.pairs)},
		{"package-suite kills", strconv.Itoa(result.packageSuiteKills)},
		{"package-suite kill pairs audited", strconv.Itoa(result.suitePairs)},
		{"batch kills", strconv.Itoa(result.batchKills)},
		{"unattributed kills", strconv.Itoa(result.unattributedKills)},
		{"truncated trailing lines", strconv.Itoa(result.truncatedLines)},
	}
	columns := []column{{"counter", false}, {"count", true}}
	return append([]string{"audit"}, renderTable(columns, rows)...)
}

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
			strconv.Itoa(audited.inapplicable),
			strconv.Itoa(audited.unverifiable),
			strconv.Itoa(audited.violations),
		})
	}
	columns := []column{
		{"layer", false}, {"audited", true}, {"kept", true},
		{"inapplicable", true}, {"unverifiable", true}, {"violations", true},
	}
	lines = append(lines, renderTable(columns, rows)...)
	if !result.branchAudited {
		lines = append(lines, whyBranchNotAudited)
	}
	if !result.infectionAudited {
		lines = append(lines, whyInfectionNotAudited)
	}
	if result.suiteCoverageProfiles == 0 {
		lines = append(lines, whySuiteReachNotAudited)
	}
	return lines
}

func branchBlock(result auditResult) []string {
	if !result.branchAudited {
		return nil
	}
	saved := result.branch
	rows := [][]string{
		{"routes with a branch proof", strconv.Itoa(saved.routes)},
		{"reaching targets the proof discharges", fmt.Sprintf("%d of %d", saved.discharged, saved.reaching)},
		{"routes left with no reaching target", strconv.Itoa(saved.emptied)},
		{"recorded executions it would have saved", strconv.Itoa(saved.executions)},
	}
	columns := []column{{"counter", false}, {"count", true}}
	return append([]string{branchDischargeHeading}, renderTable(columns, rows)...)
}

func infectionBlock(result auditResult) []string {
	if !result.infectionAudited {
		return nil
	}
	saved := result.infection
	rows := [][]string{
		{"routes of a probed mutant", strconv.Itoa(saved.routes)},
		{"reaching targets the probe discharges", fmt.Sprintf("%d of %d", saved.discharged, saved.reaching)},
		{"routes left with no reaching target", strconv.Itoa(saved.emptied)},
		{"recorded executions it would have saved", strconv.Itoa(saved.executions)},
	}
	columns := []column{{"counter", false}, {"count", true}}
	return append([]string{infectionDischargeHeading}, renderTable(columns, rows)...)
}

func unverifiableBlock(result auditResult) []string {
	lines := []string{"unverifiable"}
	if len(result.unverifiable) == 0 {
		return append(lines, "every layer could decide every kill pair")
	}
	lines = append(lines, fmt.Sprintf("kill pairs a layer could not decide: %d", len(result.unverifiable)))
	return append(lines, renderRows(result.unverifiable)...)
}

func violationBlock(result auditResult) []string {
	lines := []string{"violations"}
	if len(result.violations) == 0 {
		return append(lines, "no layer drops a killer a recorded run proved")
	}
	lines = append(lines, fmt.Sprintf("killers a layer would drop: %d", len(result.violations)))
	return append(lines, renderRows(result.violations)...)
}

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

func mutantName(pair killPair) string {
	if pair.display != "" {
		return pair.display
	}
	if len(pair.mutant) > displayWidth {
		return pair.mutant[:displayWidth]
	}
	return pair.mutant
}

func position(pair killPair) string {
	if pair.line <= 0 {
		return pair.path
	}
	if pair.column <= 0 {
		return pair.path + ":" + strconv.Itoa(pair.line)
	}
	return pair.path + ":" + strconv.Itoa(pair.line) + ":" + strconv.Itoa(pair.column)
}

func orNoValue(value string) string {
	if value == "" {
		return noValue
	}
	return value
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
