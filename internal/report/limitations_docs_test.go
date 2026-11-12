// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package report

import (
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// The documentation ledger for the limitation vocabulary.
//
// A limitation code is a published identifier: it is what a reader greps after
// seeing `LIMITATION <code>` in plain output or `limitations[].code` in JSON.
// Until this ledger the set existed only as string literals at the sites that
// raised them, which is how `goatest doctor` came to say `git-unavailable` for
// the condition a run called `git-metadata-unavailable` - two names, one fact,
// and nothing that could notice.

const (
	// limitationsDocumentation is the page that lists the codes.
	limitationsDocumentation = "../../docs/limitations.md"

	// limitationTableHeading identifies the table among the page's others.
	limitationTableHeading = "| Code | What the run is saying |"
)

// limitationTableRow matches a row's code cell.
var limitationTableRow = regexp.MustCompile("^\\| `([a-z0-9-]+)` \\|")

// documentedLimitationCodes reads the codes the page lists.
func documentedLimitationCodes(t *testing.T) []string {
	t.Helper()
	page, err := os.ReadFile(limitationsDocumentation)
	if err != nil {
		t.Fatalf("read %s: %v", limitationsDocumentation, err)
	}
	lines := strings.Split(string(page), "\n")
	heading := slices.Index(lines, limitationTableHeading)
	if heading < 0 {
		t.Fatalf("%s holds no table headed %q", limitationsDocumentation, limitationTableHeading)
	}
	var codes []string
	for _, line := range lines[heading+1:] {
		if !strings.HasPrefix(line, "|") {
			break
		}
		match := limitationTableRow.FindStringSubmatch(line)
		if match != nil {
			codes = append(codes, match[1])
		}
	}
	if len(codes) == 0 {
		t.Fatalf("%s holds the table heading and no rows under it", limitationsDocumentation)
	}
	return codes
}

// TestEveryLimitationCodeIsDocumented pins the Go vocabulary against the page.
func TestEveryLimitationCodeIsDocumented(t *testing.T) {
	t.Parallel()
	documented := documentedLimitationCodes(t)
	for _, code := range LimitationCodes() {
		if !slices.Contains(documented, code) {
			t.Errorf("limitation %s is raised by this module and absent from %s", code, limitationsDocumentation)
		}
	}
}

// TestEveryDocumentedLimitationCodeExists pins the page against the vocabulary.
//
// This is the direction that catches a code renamed in Go and left on the page,
// and a page describing a caveat the tool stopped carrying.
func TestEveryDocumentedLimitationCodeExists(t *testing.T) {
	t.Parallel()
	for _, code := range documentedLimitationCodes(t) {
		if !KnownLimitationCode(code) {
			t.Errorf("%s documents limitation %s, which this module never raises", limitationsDocumentation, code)
		}
	}
}

// TestTheLimitationLedgerSeesADocumentedCodeThatDoesNotExist proves the check
// above can fail.
//
// Two agreeing lists is also what it looks like when one of them was read as
// empty, so the reading is exercised against a row that should not pass.
func TestTheLimitationLedgerSeesADocumentedCodeThatDoesNotExist(t *testing.T) {
	t.Parallel()
	forged := "| `a-caveat-nothing-raises` | invented by a test |"
	if limitationTableRow.FindStringSubmatch(forged) == nil {
		t.Fatal("the row pattern does not match a row of the shape the page uses")
	}
	if KnownLimitationCode("a-caveat-nothing-raises") {
		t.Fatal("a code nothing raises is reported as known")
	}
}

// TestTheLimitationCodesAreSorted keeps the vocabulary and the page in an order
// a reader can search by eye.
func TestTheLimitationCodesAreSorted(t *testing.T) {
	t.Parallel()
	codes := LimitationCodes()
	if !slices.IsSorted(codes) {
		t.Errorf("LimitationCodes() = %v, which is not sorted", codes)
	}
	documented := documentedLimitationCodes(t)
	if !slices.IsSorted(documented) {
		t.Errorf("%s lists %v, which is not sorted", limitationsDocumentation, documented)
	}
}
