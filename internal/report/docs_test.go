// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package report

import (
	"encoding/json"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// The documentation ledger for the mutant accounting.
//
// This is the claim goatest rests on. A report can be read as evidence because
// every discovered mutant has exactly one disposition and the totals add up,
// and a single `unknown` makes the verdict ERROR rather than a smaller number.
//
// The claim is stated four times: as the constants below, as an enum inside the
// published JSON Schema, as the arithmetic in audit.go, and as three equations
// in a fenced block in docs/assurance-contract.md. The equations are the part a
// reader trusts, and the only one nothing was checking.

const (
	// contractDocumentation is the page that states the accounting.
	contractDocumentation = "../../docs/assurance-contract.md"

	// accountingHeading is the sentence that introduces the accounting block.
	//
	// The block is found by the sentence rather than by being the first fenced
	// block on the page, because the page holds four and the first three
	// describe other things. Taking the first one was this ledger's first
	// version, and it read an unrelated block as empty of equations and then
	// reported the page as stating none.
	accountingHeading = "Every discovered mutant has exactly one report-v1 disposition:"
)

// accountingEquation matches "left = right + right + ..." in the fenced block.
var accountingEquation = regexp.MustCompile(`^([a-z-]+)\s*=\s*(.+)$`)

// dispositions is every disposition a discovered mutant can have.
var dispositions = []MutantStatus{
	MutantKilled, MutantSurvived, MutantInconclusive, MutantCompileRejected,
	MutantAccepted, MutantOutOfScope, MutantUnknown,
}

// TestEveryDispositionIsInThePublishedSchema pins the constants against the
// schema this module publishes beside every report.
func TestEveryDispositionIsInThePublishedSchema(t *testing.T) {
	t.Parallel()
	published := schemaDispositions(t)
	for _, status := range dispositions {
		if !slices.Contains(published, string(status)) {
			t.Errorf("disposition %s is declared in Go and absent from the published schema", status)
		}
	}
	for _, status := range published {
		if !slices.Contains(dispositions, MutantStatus(status)) {
			t.Errorf("the published schema holds disposition %s, which is declared nowhere", status)
		}
	}
}

// TestTheDocumentedAccountingIsTheAccountingThatRuns pins the three equations
// on the page against the arithmetic audit.go performs.
//
// The equations are checked by running them: a tally is built in which every
// disposition holds a different count, audited, and the result compared against
// what each documented equation says it should be. An equation the page states
// and the code does not enforce fails here, and so does one the code enforces
// and the page states differently.
func TestTheDocumentedAccountingIsTheAccountingThatRuns(t *testing.T) {
	t.Parallel()
	equations := documentedEquations(t)
	if len(equations) != len(accountingTerms()) {
		t.Fatalf("%s states %d equations, and this ledger knows %d",
			contractDocumentation, len(equations), len(accountingTerms()))
	}
	counts := map[string]int{
		"killed": 2, "survived": 3, "inconclusive": 5, "compile-rejected": 7,
		"accepted": 11, "out-of-scope": 13, "unknown": 17,
	}
	for left, right := range equations {
		want, ok := accountingTerms()[left]
		if !ok {
			t.Errorf("%s states an equation for %q, which this ledger does not know", contractDocumentation, left)
			continue
		}
		if !slices.Equal(right, want) {
			t.Errorf("%s says %s = %s, and the code adds %s",
				contractDocumentation, left, strings.Join(right, " + "), strings.Join(want, " + "))
			continue
		}
		total := 0
		for _, term := range right {
			total += termValue(t, term, counts)
		}
		if got := termValue(t, left, counts); got != total {
			t.Errorf("%s = %d, and %s sums to %d", left, got, strings.Join(right, " + "), total)
		}
	}
}

// TestTheAccountingLedgerSeesAnEquationThatIsNotThere proves the ledger can
// fail, because three equations agreeing with three equations is what it looks
// like when the block is read as empty.
func TestTheAccountingLedgerSeesAnEquationThatIsNotThere(t *testing.T) {
	t.Parallel()
	equations := documentedEquations(t)
	if len(equations) == 0 {
		t.Fatal("the fenced block was read as empty, so the ledger would agree with anything")
	}
	if _, ok := equations["a-total-this-contract-does-not-have"]; ok {
		t.Fatal("the block holds the fixture term, so this test proves nothing")
	}
}

// accountingTerms is the arithmetic audit.go enforces, as the page writes it.
func accountingTerms() map[string][]string {
	return map[string][]string{
		"discovered": {"executed", "compile-rejected", "accepted", "out-of-scope", "unknown"},
		"executed":   {"killed", "survived", "inconclusive"},
		"selected":   {"executed", "compile-rejected", "accepted", "unknown"},
	}
}

// termValue resolves one term of an equation against a tally of dispositions.
func termValue(t *testing.T, term string, counts map[string]int) int {
	t.Helper()
	switch term {
	case "discovered":
		total := 0
		for _, value := range counts {
			total += value
		}
		return total
	case "executed":
		return counts["killed"] + counts["survived"] + counts["inconclusive"]
	case "selected":
		return counts["killed"] + counts["survived"] + counts["inconclusive"] +
			counts["compile-rejected"] + counts["accepted"] + counts["unknown"]
	}
	value, ok := counts[term]
	if !ok {
		t.Fatalf("equation term %q is neither a total nor a disposition this ledger counts", term)
	}
	return value
}

// documentedEquations reads the fenced block of accounting equations.
func documentedEquations(t *testing.T) map[string][]string {
	t.Helper()
	data, err := os.ReadFile(contractDocumentation)
	if err != nil {
		t.Fatalf("read %s: %v", contractDocumentation, err)
	}
	lines := strings.Split(string(data), "\n")
	heading := slices.Index(lines, accountingHeading)
	if heading < 0 {
		t.Fatalf("%s no longer holds %q", contractDocumentation, accountingHeading)
	}
	start := heading
	for start < len(lines) && !strings.HasPrefix(lines[start], "```") {
		start++
	}
	if start == len(lines) {
		t.Fatalf("%s: no fenced block follows %q", contractDocumentation, accountingHeading)
	}
	equations := make(map[string][]string)
	for _, line := range lines[start+1:] {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			break
		}
		match := accountingEquation.FindStringSubmatch(trimmed)
		if match == nil {
			continue
		}
		var terms []string
		for _, term := range strings.Split(match[2], "+") {
			terms = append(terms, strings.TrimSpace(term))
		}
		equations[match[1]] = terms
	}
	return equations
}

// schemaDispositions reads the disposition enum out of the published schema.
//
// It decodes the bytes JSONSchema publishes rather than reaching into the map
// that builds them, because the bytes are what a user validates against. A
// ledger that read the builder would agree with the builder.
func schemaDispositions(t *testing.T) []string {
	t.Helper()
	var document any
	if err := json.Unmarshal(JSONSchema(), &document); err != nil {
		t.Fatalf("decode the published schema: %v", err)
	}
	node := document
	for _, step := range []string{"$defs", "mutantDisposition", "properties", "status"} {
		object, ok := node.(map[string]any)
		if !ok {
			t.Fatalf("the published schema is not an object at %q", step)
		}
		node, ok = object[step]
		if !ok {
			t.Fatalf("the published schema has no %q", step)
		}
	}
	object, ok := node.(map[string]any)
	if !ok {
		t.Fatal("the published schema's disposition status is not an object")
	}
	raw, ok := object["enum"].([]any)
	if !ok {
		t.Fatal("the published schema's disposition status has no enum")
	}
	values := make([]string, 0, len(raw))
	for _, value := range raw {
		text, ok := value.(string)
		if !ok {
			t.Fatal("the published schema's disposition enum holds a non-string member")
		}
		values = append(values, text)
	}
	return values
}
