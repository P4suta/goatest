// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package trace

import (
	"encoding/json"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// The documentation ledger for goatest-trace-v1.
//
// Each closed vocabulary of this contract is written down three times: as
// constants here, as an enum in schema.json, and as a table or a sentence in
// docs/trace-v1.md. The three are pinned to each other in both directions, so
// a value that exists in the code and not the page fails as an undocumented
// member, and a value on the page that the code no longer holds fails as a
// claim about a contract that has moved.
//
// Two of these vocabularies used to be written down five times. reader.go and
// internal/devtools/tracesummary each carried the prepare phases as their own
// switch, and nothing compared either copy to anything. A ledger over three of
// five is green while two drift, which is why vocabulary.go exists and why
// those two copies are gone: the ledger is only worth its cost if the set it
// checks is the set the code actually uses.

const (
	// schemaPath and documentationPath are the two documents this ledger
	// compares the vocabularies against, relative to this package.
	schemaPath        = "schema.json"
	documentationPath = "../../docs/trace-v1.md"

	// typeTableHeading is the row that opens the event-type table.
	//
	// The table is found by its heading rather than by line number, because a
	// line number is a fact about today's file and the heading is a fact about
	// the document.
	typeTableHeading = "| `type` | Payload | Recorded when |"
)

// backquotedValue matches one `value` in a documentation line.
var backquotedValue = regexp.MustCompile("`([^`]+)`")

func TestEveryEventTypeIsInTheSchemaAndTheDocumentation(t *testing.T) {
	t.Parallel()
	pinVocabulary(t, "event type", Types(), "/properties/type", documentedTypes(t))
}

func TestEveryPrepareVocabularyIsInTheSchemaAndTheDocumentation(t *testing.T) {
	t.Parallel()
	pinVocabulary(t, "prepare phase", PreparePhases(),
		"/$defs/prepare/properties/phase", documentedRow(t, "### `prepare`", "phase"))
	pinVocabulary(t, "prepare state", PrepareStates(),
		"/$defs/prepare/properties/state", documentedRow(t, "### `prepare`", "state"))
	pinVocabulary(t, "prepare result", PrepareResults(),
		"/$defs/prepare/properties/result", documentedRow(t, "### `prepare`", "result"))
}

func TestEveryRoutingVocabularyIsInTheSchemaAndTheDocumentation(t *testing.T) {
	t.Parallel()
	pinVocabulary(t, "route reason", RouteReasons(),
		"/$defs/route/properties/reason", documentedRow(t, "### `route`", "reason"))
	pinVocabulary(t, "routing granularity", Granularities(),
		"/$defs/route/properties/granularity", documentedRow(t, "### `route`", "granularity"))
	pinVocabulary(t, "routing fallback", RouteFallbacks(),
		"/$defs/route/properties/fallback", documentedRow(t, "### `route`", "fallback"))
	pinVocabulary(t, "discharge reason", DischargeReasons(),
		"/$defs/route/properties/discharged/items/properties/reason", nil)
}

func TestEveryProbeVocabularyIsInTheSchemaAndTheDocumentation(t *testing.T) {
	t.Parallel()
	pinVocabulary(t, "probe outcome", ProbeOutcomes(),
		"/$defs/probe/properties/outcome", documentedRow(t, "### `probe`", "outcome"))
	pinVocabulary(t, "whole-tree reason", WholeTreeReasons(),
		"/$defs/probe/properties/whole_tree_reason", nil)
	pinVocabulary(t, "whole-tree reason on a mutant", WholeTreeReasons(),
		"/$defs/mutant/properties/whole_tree_reason", nil)
}

// TestTheLedgerSeesAValueTheDocumentationDoesNotHold proves this ledger can
// fail.
//
// A gate that has only ever been observed passing is a gate whose failure has
// never been observed, and an empty list compares equal to an empty list, so
// the check has to be shown finding something.
func TestTheLedgerSeesAValueTheDocumentationDoesNotHold(t *testing.T) {
	t.Parallel()
	documented := documentedTypes(t)
	if slices.Contains(documented, "a-type-this-contract-does-not-have") {
		t.Fatal("the documentation holds the fixture value, so this test proves nothing")
	}
	if missing := missingFromList(append(Types(), "a-type-this-contract-does-not-have"), documented); len(missing) != 1 {
		t.Fatalf("missingFromList found %q, want the one fixture value", missing)
	}
}

// pinVocabulary compares one vocabulary against the schema enum at pointer and,
// when documented is non-nil, against the documentation as well.
//
// A nil documented list means the page describes the vocabulary in prose this
// ledger does not parse. That is recorded as an absence rather than passed over
// in silence: the schema half still holds, and the gap is named here instead of
// looking like coverage.
func pinVocabulary(t *testing.T, subject string, code []string, pointer string, documented []string) {
	t.Helper()
	schema := schemaEnum(t, pointer)
	if missing := missingFromList(code, schema); len(missing) != 0 {
		t.Errorf("%s: %q is declared in Go and absent from %s at %s", subject, missing, schemaPath, pointer)
	}
	if extra := missingFromList(schema, code); len(extra) != 0 {
		t.Errorf("%s: %q is in %s at %s and declared nowhere in Go", subject, extra, schemaPath, pointer)
	}
	if documented == nil {
		return
	}
	if missing := missingFromList(code, documented); len(missing) != 0 {
		t.Errorf("%s: %q is declared in Go and absent from %s", subject, missing, documentationPath)
	}
	if extra := missingFromList(documented, code); len(extra) != 0 {
		t.Errorf("%s: %q is in %s and declared nowhere in Go", subject, extra, documentationPath)
	}
}

// missingFromList reports the members of first that second does not hold.
func missingFromList(first, second []string) []string {
	var missing []string
	for _, value := range first {
		if !slices.Contains(second, value) {
			missing = append(missing, value)
		}
	}
	return missing
}

// schemaEnum reads one enum out of schema.json by JSON pointer.
func schemaEnum(t *testing.T, pointer string) []string {
	t.Helper()
	data, err := os.ReadFile(schemaPath)
	if err != nil {
		t.Fatalf("read %s: %v", schemaPath, err)
	}
	var document any
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatalf("decode %s: %v", schemaPath, err)
	}
	node := document
	for _, step := range strings.Split(strings.TrimPrefix(pointer, "/"), "/") {
		object, ok := node.(map[string]any)
		if !ok {
			t.Fatalf("%s: %s is not an object at %q", schemaPath, pointer, step)
		}
		node, ok = object[step]
		if !ok {
			t.Fatalf("%s: %s has no %q", schemaPath, pointer, step)
		}
	}
	object, ok := node.(map[string]any)
	if !ok {
		t.Fatalf("%s: %s is not an object", schemaPath, pointer)
	}
	raw, ok := object["enum"].([]any)
	if !ok {
		t.Fatalf("%s: %s has no enum", schemaPath, pointer)
	}
	values := make([]string, 0, len(raw))
	for _, value := range raw {
		text, ok := value.(string)
		if !ok {
			t.Fatalf("%s: %s holds a non-string member", schemaPath, pointer)
		}
		values = append(values, text)
	}
	return values
}

// documentedTypes reads the first column of the event-type table.
func documentedTypes(t *testing.T) []string {
	t.Helper()
	lines := documentationLines(t)
	heading := slices.Index(lines, typeTableHeading)
	if heading < 0 {
		t.Fatalf("%s no longer holds the event-type table heading %q", documentationPath, typeTableHeading)
	}
	var documented []string
	for _, line := range lines[heading+2:] {
		if !strings.HasPrefix(line, "| `") {
			break
		}
		match := backquotedValue.FindStringSubmatch(line)
		if match == nil {
			break
		}
		documented = append(documented, match[1])
	}
	return documented
}

// documentedRow reads the members a payload table lists for one field.
//
// Both the section and the field are needed. `outcome` names two different
// vocabularies in this document - a mutant's and a probe's - and `result`
// appears under more than one payload, so a search for the first row with a
// matching field name silently reads the wrong table. That is not a
// hypothetical: it is what the first version of this ledger did, and it
// reported the probe outcomes as undocumented while accepting the mutant
// outcomes in their place.
//
// Only the second cell is read, and only up to the first semicolon, because a
// row states its members and then explains them - "`succeeded`, `failed`, or
// `skipped`; present only on `finished`" - and the explanation is prose that
// happens to be quoted.
func documentedRow(t *testing.T, section, field string) []string {
	t.Helper()
	lines := documentationLines(t)
	start := slices.Index(lines, section)
	if start < 0 {
		t.Fatalf("%s no longer holds the section %q", documentationPath, section)
	}
	prefix := "| `" + field + "` |"
	for _, line := range lines[start+1:] {
		if strings.HasPrefix(line, "### ") || strings.HasPrefix(line, "## ") {
			break
		}
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		cells := strings.Split(line, "|")
		if len(cells) < documentedRowCells {
			break
		}
		members, _, _ := strings.Cut(cells[2], ";")
		matches := backquotedValue.FindAllStringSubmatch(members, -1)
		documented := make([]string, 0, len(matches))
		for _, match := range matches {
			documented = append(documented, match[1])
		}
		return documented
	}
	t.Fatalf("%s: section %q holds no row for %q", documentationPath, section, field)
	return nil
}

// documentedRowCells is how many pieces splitting a table row on its pipes
// yields before the members cell can be read: the empty piece before the first
// pipe, the field name, and the members.
const documentedRowCells = 3

// documentationLines reads docs/trace-v1.md once per call.
func documentationLines(t *testing.T) []string {
	t.Helper()
	data, err := os.ReadFile(documentationPath)
	if err != nil {
		t.Fatalf("read %s: %v", documentationPath, err)
	}
	return strings.Split(string(data), "\n")
}
