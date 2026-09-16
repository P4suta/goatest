// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package cli

import (
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/P4suta/goatest/internal/report"
)

// The documentation ledger for the command line.
//
// Two sets are written down more than once here, and both are things a user
// greps for.
//
// The exit-code table maps every verdict to a number, so pinning it pins the
// verdict vocabulary as well - which is how the missing COMPLETED was found.
// report.go declared nine verdicts, docs/report-v1.md listed all nine, and
// README.md and docs/assurance-contract.md listed eight. A reader of either
// page could not learn what `goatest report --json` meant by the verdict it
// had just printed, and README.md contradicted itself: its exit-code paragraph
// mentioned a "completed operation" its own verdict list did not hold.
//
// The command set is written down four times inside this file alone - the
// constants, the help text, commandHelp, and the dispatch switch - and a fifth
// time in README.md.

const (
	// reportDocumentation is the page that holds the exit-code table.
	reportDocumentation = "../../docs/report-v1.md"

	// readmePath is the page that holds the command surface.
	readmePath = "../../README.md"

	// exitCodeTableHeading opens the exit-code table, and is how the table is
	// found. A line number is a fact about today's file; a heading is a fact
	// about the document.
	exitCodeTableHeading = "| Code | Meaning |"

	// commandSurfaceHeading opens the README's fenced command listing.
	commandSurfaceHeading = "The command surface is:"

	// exitCodeTableCells is how many pieces splitting a table row on its pipes
	// yields: the empty piece before the first pipe, the code, and the meaning.
	exitCodeTableCells = 3
)

// backquotedValue matches one `value` in a documentation line.
var backquotedValue = regexp.MustCompile("`([^`]+)`")

// verdicts is every verdict report.go declares.
//
// It is stated here rather than read from internal/report because that package
// declares them as untyped constants with no list beside them; adding one there
// and forgetting it here fails this ledger, which is the point.
var verdicts = []report.Verdict{
	report.VerdictAssured,
	report.VerdictChangeAssured,
	report.VerdictScopeAssured,
	report.VerdictDefect,
	report.VerdictInsufficient,
	report.VerdictError,
	report.VerdictReproduced,
	report.VerdictResolved,
	report.VerdictCompleted,
}

// TestEveryVerdictHasADocumentedExitCode pins three statements of one fact: the
// exitCode switch, the table in docs/report-v1.md, and the verdict list above.
func TestEveryVerdictHasADocumentedExitCode(t *testing.T) {
	t.Parallel()
	documented := documentedExitCodes(t)
	for _, verdict := range verdicts {
		code := exitCode(verdict)
		want, ok := documented[string(verdict)]
		if !ok {
			t.Errorf("verdict %s exits %d and is named nowhere in %s", verdict, code, reportDocumentation)
			continue
		}
		if want != code {
			t.Errorf("verdict %s exits %d, and %s says %d", verdict, code, reportDocumentation, want)
		}
	}
	for verdict := range documented {
		if !slices.Contains(verdicts, report.Verdict(verdict)) {
			t.Errorf("%s names verdict %s, which is declared nowhere", reportDocumentation, verdict)
		}
	}
}

// TestEveryExitCodeConstantIsDocumented covers the two codes no verdict
// produces.
//
// A signal does not arrive as a verdict, so the loop above cannot reach 130 or
// 143. They are the codes a user is most likely to meet on a bad day and least
// likely to find explained, which is reason enough to check them separately.
func TestEveryExitCodeConstantIsDocumented(t *testing.T) {
	t.Parallel()
	codes := documentedExitCodeNumbers(t)
	for _, code := range []int{
		ExitAssured, ExitDefect, ExitInsufficient, ExitError, ExitInterrupted, ExitTerminated,
	} {
		if !codes[code] {
			t.Errorf("exit code %d is declared in Go and absent from %s", code, reportDocumentation)
		}
	}
	for code := range codes {
		if !slices.Contains([]int{
			ExitAssured, ExitDefect, ExitInsufficient, ExitError, ExitInterrupted, ExitTerminated,
		}, code) {
			t.Errorf("%s documents exit code %d, which no constant declares", reportDocumentation, code)
		}
	}
}

// TestTheHelpTextNamesEveryExitCode keeps the one-line summary in the help
// honest, because it is what a user reads before they find the page.
func TestTheHelpTextNamesEveryExitCode(t *testing.T) {
	t.Parallel()
	for _, code := range []int{
		ExitAssured, ExitDefect, ExitInsufficient, ExitError, ExitInterrupted, ExitTerminated,
	} {
		if !strings.Contains(help, " "+strconv.Itoa(code)+" ") {
			t.Errorf("the help text does not name exit code %d", code)
		}
	}
}

// TestEveryCommandIsDispatchedHelpedAndDocumented pins the command set against
// the three other places this file states it and the one in README.md.
func TestEveryCommandIsDispatchedHelpedAndDocumented(t *testing.T) {
	t.Parallel()
	commands := []Command{
		CommandVerify, CommandInit, CommandExplain, CommandReplay, CommandAccept,
		CommandReport, CommandPlan, CommandDoctor, CommandFix, CommandCache, CommandTrace,
	}
	documented := documentedCommands(t)
	for _, command := range commands {
		if _, ok := commandHelp(command); !ok {
			t.Errorf("command %s has no help text", command)
		}
		if !helpNamesCommand(command) {
			t.Errorf("command %s is absent from the usage lines of the help text", command)
		}
		if !slices.Contains(documented, string(command)) {
			t.Errorf("command %s is absent from the command surface in %s", command, readmePath)
		}
	}
	for _, command := range documented {
		if command == "help" {
			continue
		}
		if !slices.Contains(commands, Command(command)) {
			t.Errorf("%s documents command %s, which is declared nowhere", readmePath, command)
		}
	}
}

// helpNamesCommand reports whether a usage line of the help text names one
// command.
//
// The line is matched rather than the whole text, because several command names
// also appear in the prose beneath it, and a check that prose satisfies is a
// check a missing usage line passes.
func helpNamesCommand(command Command) bool {
	for _, line := range strings.Split(help, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= usageLineFields && fields[0] == "goatest" && fields[1] == string(command) {
			return true
		}
	}
	return false
}

// usageLineFields is the shortest usage line: the binary and a command.
const usageLineFields = 2

// TestTheExitCodeLedgerSeesAVerdictTheTableDoesNotHold proves the ledger can
// fail, which an agreement between two lists cannot show on its own.
func TestTheExitCodeLedgerSeesAVerdictTheTableDoesNotHold(t *testing.T) {
	t.Parallel()
	documented := documentedExitCodes(t)
	if _, ok := documented["A_VERDICT_THIS_TOOL_DOES_NOT_HAVE"]; ok {
		t.Fatal("the table holds the fixture verdict, so this test proves nothing")
	}
	if len(documented) == 0 {
		t.Fatal("the table was read as empty, so the ledger would agree with anything")
	}
}

// documentedExitCodeNumbers reads the first column of the exit-code table.
//
// It is separate from documentedExitCodes because two rows carry no verdict at
// all: a signal is not a verdict, so 130 and 143 describe themselves in prose.
// Reading the table only through its verdicts would have left the two codes a
// user is most likely to meet on a bad day unchecked.
func documentedExitCodeNumbers(t *testing.T) map[int]bool {
	t.Helper()
	codes := make(map[int]bool)
	for _, row := range exitCodeRows(t) {
		codes[row.code] = true
	}
	return codes
}

// exitCodeRow is one row of the exit-code table.
type exitCodeRow struct {
	code    int
	meaning string
}

// exitCodeRows reads the table, found by its heading rather than its line
// number.
func exitCodeRows(t *testing.T) []exitCodeRow {
	t.Helper()
	lines := documentationLines(t, reportDocumentation)
	heading := slices.Index(lines, exitCodeTableHeading)
	if heading < 0 {
		t.Fatalf("%s no longer holds the exit-code table heading %q", reportDocumentation, exitCodeTableHeading)
	}
	var rows []exitCodeRow
	for _, line := range lines[heading+2:] {
		if !strings.HasPrefix(line, "|") {
			break
		}
		cells := strings.Split(line, "|")
		if len(cells) < exitCodeTableCells {
			break
		}
		code, err := strconv.Atoi(strings.TrimSpace(cells[1]))
		if err != nil {
			t.Fatalf("%s: exit-code row %q does not open with a number", reportDocumentation, line)
		}
		rows = append(rows, exitCodeRow{code: code, meaning: cells[2]})
	}
	return rows
}

// documentedExitCodes reads the exit-code table as a verdict-to-code map.
//
// Only the backquoted members of a row are read. Two rows describe a signal
// rather than a verdict - "interrupted" and "terminated" - and they are prose
// in the table for the same reason they are absent from the verdict list: a
// signal does not arrive as a verdict. Reading them as verdicts was the first
// version of this function, and it reported them as declared nowhere, which
// was true and useless.
func documentedExitCodes(t *testing.T) map[string]int {
	t.Helper()
	documented := make(map[string]int)
	for _, row := range exitCodeRows(t) {
		for _, match := range backquotedValue.FindAllStringSubmatch(row.meaning, -1) {
			documented[match[1]] = row.code
		}
	}
	return documented
}

// documentedCommands reads the first word of each line of the README's fenced
// command surface.
func documentedCommands(t *testing.T) []string {
	t.Helper()
	lines := documentationLines(t, readmePath)
	start := slices.Index(lines, commandSurfaceHeading)
	if start < 0 {
		t.Fatalf("%s no longer holds %q", readmePath, commandSurfaceHeading)
	}
	var documented []string
	inside := false
	for _, line := range lines[start+1:] {
		if strings.HasPrefix(line, "```") {
			if inside {
				break
			}
			inside = true
			continue
		}
		if !inside {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[0] != "goatest" {
			continue
		}
		if !slices.Contains(documented, fields[1]) {
			documented = append(documented, fields[1])
		}
	}
	return documented
}

// documentationLines reads one page.
func documentationLines(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return strings.Split(string(data), "\n")
}
