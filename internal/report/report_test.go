// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package report_test

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"reflect"
	"strings"
	"testing"

	"github.com/P4suta/goatest/internal/report"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

func fixture() report.Report {
	return report.Report{
		Schema:   report.SchemaV1,
		Verdict:  report.VerdictInsufficient,
		Contract: "standard-v1",
		Snapshot: "abc123",
		Evidence: []report.Evidence{
			{Kind: "mutation", ID: "e2", Status: "survived"},
			{Kind: "baseline", ID: "e1", Status: "passed"},
		},
		Findings: []report.Finding{
			{ID: "f2", Kind: "survivor", Path: "z.go", Summary: "survived", Mutant: "lt-to-le: < -> <=", Replay: "goatest replay f2"},
			{ID: "f1", Kind: "coverage", Path: "a.go", Summary: "unreached"},
		},
		Repairs:       []report.Repair{{ID: "r1", Finding: "f2", Path: "z_test.go", Status: "applied"}},
		ResidualRisks: []string{"z risk", "a risk"},
	}
}

func TestJSONAndLineRenderersAreCanonical(t *testing.T) {
	first := report.JSON(fixture())
	second := report.JSON(fixture())
	if !bytes.Equal(first, second) {
		t.Fatal("JSON bytes changed for the same report")
	}
	if bytes.Index(first, []byte(`"id": "f1"`)) > bytes.Index(first, []byte(`"id": "f2"`)) {
		t.Errorf("findings are not canonically ordered:\n%s", first)
	}
	want := "INSUFFICIENT standard-v1 snapshot=abc123\n" +
		"evidence 2  findings 2  repairs 1  risks 2\n" +
		"FINDING f1 coverage a.go: unreached\n" +
		"FINDING f2 survivor z.go: survived\n" +
		"  MUTANT lt-to-le: < -> <=\n" +
		"  REPLAY goatest replay f2\n" +
		"REPAIR r1 applied z_test.go finding=f2\n" +
		"RISK a risk\n" +
		"RISK z risk\n"
	if got := report.Lines(fixture()); got != want {
		t.Errorf("lines =\n%s\nwant\n%s", got, want)
	}
}

func TestCanonicalReportDefaultsSchemaPreservesExplicitSchemaAndOrdersEveryCollection(t *testing.T) {
	input := report.Report{
		Evidence: []report.Evidence{
			{Kind: "z-kind", ID: "a-id"},
			{Kind: "a-kind", ID: "z-id"},
			{Kind: "a-kind", ID: "a-id"},
		},
		Findings: []report.Finding{{ID: "z-finding"}, {ID: "a-finding"}, {ID: "m-finding"}},
		Repairs:  []report.Repair{{ID: "z-repair"}, {ID: "a-repair"}, {ID: "m-repair"}},
	}
	var canonical report.Report
	if err := json.Unmarshal(report.JSON(input), &canonical); err != nil {
		t.Fatal(err)
	}
	if canonical.Schema != report.SchemaV1 {
		t.Fatalf("default schema = %q", canonical.Schema)
	}
	wantEvidence := []report.Evidence{
		{Kind: "a-kind", ID: "a-id"},
		{Kind: "a-kind", ID: "z-id"},
		{Kind: "z-kind", ID: "a-id"},
	}
	if !reflect.DeepEqual(canonical.Evidence, wantEvidence) {
		t.Fatalf("evidence order = %+v, want %+v", canonical.Evidence, wantEvidence)
	}
	wantFindings := []string{"a-finding", "m-finding", "z-finding"}
	for index, want := range wantFindings {
		if canonical.Findings[index].ID != want {
			t.Fatalf("finding order = %+v", canonical.Findings)
		}
	}
	wantRepairs := []string{"a-repair", "m-repair", "z-repair"}
	for index, want := range wantRepairs {
		if canonical.Repairs[index].ID != want {
			t.Fatalf("repair order = %+v", canonical.Repairs)
		}
	}

	explicit := input
	explicit.Schema = "future-schema"
	if err := json.Unmarshal(report.JSON(explicit), &canonical); err != nil {
		t.Fatal(err)
	}
	if canonical.Schema != "future-schema" {
		t.Fatalf("explicit schema overwritten with %q", canonical.Schema)
	}
}

func TestLineRendererEscapesControlCharactersAndCannotForgeRecords(t *testing.T) {
	input := report.Report{
		Verdict: report.VerdictDefect, Contract: "standard-v1", Snapshot: "snapshot",
		Findings: []report.Finding{{
			ID: "finding", Kind: "baseline", Path: "fixture.go\rRISK forged",
			Summary: "failed\nFINDING forged\x1b[31m", Mutant: "change\tforged", Replay: "goatest\nreplay",
		}},
		ResidualRisks: []string{"risk\nREPAIR forged"},
	}
	lines := report.Lines(input)
	if strings.Count(lines, "\nFINDING ") != 1 {
		t.Fatalf("control characters forged renderer records:\n%s", lines)
	}
	if strings.ContainsAny(lines, "\r\x1b\t") {
		t.Fatalf("renderer retained terminal control characters: %q", lines)
	}
	for _, escaped := range []string{`fixture.go\rRISK forged`, `failed\nFINDING forged\u001b[31m`, `change\tforged`, `goatest\nreplay`, `risk\nREPAIR forged`} {
		if !strings.Contains(lines, escaped) {
			t.Errorf("renderer omitted escaped text %q: %q", escaped, lines)
		}
	}
}

func TestHTMLIsSelfContainedAndOffline(t *testing.T) {
	html := report.HTML(fixture())
	text := strings.ToLower(string(html))
	for _, forbidden := range []string{"http://", "https://", "<script src=", "<link rel="} {
		if strings.Contains(text, forbidden) {
			t.Errorf("HTML contains external dependency %q", forbidden)
		}
	}
	if !strings.Contains(text, "<!doctype html>") || !strings.Contains(text, "insufficient") {
		t.Errorf("HTML is incomplete: %s", html)
	}
	for _, required := range []string{"lt-to-le: &lt; -&gt; &lt;=", "goatest replay f2", "z_test.go", "a risk", "z risk"} {
		if !strings.Contains(text, required) {
			t.Errorf("HTML omitted %q: %s", required, html)
		}
	}
}

func TestFindingIDIsStableAndInputSensitive(t *testing.T) {
	one := report.FindingID("survivor", "a.go", "mutant-a")
	two := report.FindingID("survivor", "a.go", "mutant-a")
	other := report.FindingID("survivor", "a.go", "mutant-b")
	if one != two || one == other || len(one) != 16 {
		t.Fatalf("ids = %q %q %q", one, two, other)
	}
}

func TestSARIFJUnitAndSchemaAreDeterministicAndWellFormed(t *testing.T) {
	firstSARIF := report.SARIF(fixture())
	secondSARIF := report.SARIF(fixture())
	if !bytes.Equal(firstSARIF, secondSARIF) {
		t.Fatal("SARIF bytes are not deterministic")
	}
	var sarif struct {
		Version string `json:"version"`
		Runs    []struct {
			Results []json.RawMessage `json:"results"`
		} `json:"runs"`
	}
	if err := json.Unmarshal(firstSARIF, &sarif); err != nil || sarif.Version != "2.1.0" || len(sarif.Runs) != 1 || len(sarif.Runs[0].Results) != 2 {
		t.Fatalf("SARIF = %+v, %v\n%s", sarif, err, firstSARIF)
	}

	junit := report.JUnit(fixture())
	var suite struct {
		XMLName  xml.Name `xml:"testsuite"`
		Tests    int      `xml:"tests,attr"`
		Failures int      `xml:"failures,attr"`
	}
	if err := xml.Unmarshal(junit, &suite); err != nil || suite.Tests != 4 || suite.Failures != 2 {
		t.Fatalf("JUnit = %+v, %v\n%s", suite, err, junit)
	}

	schema := report.JSONSchema()
	var document map[string]any
	if err := json.Unmarshal(schema, &document); err != nil || document["$id"] != report.SchemaV1 || document["additionalProperties"] != false {
		t.Fatalf("schema = %+v, %v\n%s", document, err, schema)
	}
}

func TestSARIFPreservesRulesLocationsAndPositiveLines(t *testing.T) {
	input := report.Report{Findings: []report.Finding{
		{ID: "without-location", Kind: "coverage", Summary: "none"},
		{ID: "path-only", Kind: "survivor", Path: "path-only.go", Summary: "path"},
		{ID: "with-line", Kind: "survivor", Path: "line.go", Line: 7, Summary: "line"},
	}}
	var document struct {
		Runs []struct {
			Tool struct {
				Driver struct {
					Rules []struct {
						ID string `json:"id"`
					} `json:"rules"`
				} `json:"driver"`
			} `json:"tool"`
			Results []struct {
				Properties map[string]any `json:"properties"`
				Locations  []struct {
					Physical struct {
						Artifact struct {
							URI string `json:"uri"`
						} `json:"artifactLocation"`
						Region *struct {
							StartLine int `json:"startLine"`
						} `json:"region"`
					} `json:"physicalLocation"`
				} `json:"locations"`
			} `json:"results"`
		} `json:"runs"`
	}
	if err := json.Unmarshal(report.SARIF(input), &document); err != nil {
		t.Fatal(err)
	}
	if len(document.Runs) != 1 || len(document.Runs[0].Tool.Driver.Rules) != 2 || document.Runs[0].Tool.Driver.Rules[0].ID != "coverage" || document.Runs[0].Tool.Driver.Rules[1].ID != "survivor" {
		t.Fatalf("SARIF rules = %+v", document.Runs)
	}
	results := make(map[string]struct {
		locations int
		uri       string
		line      int
		hasRegion bool
	})
	for _, result := range document.Runs[0].Results {
		id, _ := result.Properties["findingId"].(string)
		entry := struct {
			locations int
			uri       string
			line      int
			hasRegion bool
		}{locations: len(result.Locations)}
		if len(result.Locations) != 0 {
			entry.uri = result.Locations[0].Physical.Artifact.URI
			if result.Locations[0].Physical.Region != nil {
				entry.hasRegion = true
				entry.line = result.Locations[0].Physical.Region.StartLine
			}
		}
		results[id] = entry
	}
	if got := results["without-location"]; got.locations != 0 {
		t.Fatalf("locationless finding = %+v", got)
	}
	if got := results["path-only"]; got.locations != 1 || got.uri != "path-only.go" || got.hasRegion {
		t.Fatalf("path-only finding = %+v", got)
	}
	if got := results["with-line"]; got.locations != 1 || got.uri != "line.go" || !got.hasRegion || got.line != 7 {
		t.Fatalf("line finding = %+v", got)
	}
}

func TestJUnitSanitizesInvalidXMLCharacters(t *testing.T) {
	junit := report.JUnit(report.Report{Findings: []report.Finding{{ID: "invalid", Kind: "fixture", Summary: "bad\x00xml"}}})
	if bytes.Contains(junit, []byte{0}) {
		t.Fatal("JUnit retained an XML-forbidden control character")
	}
	var suite junitSuiteFixture
	if err := xml.Unmarshal(junit, &suite); err != nil {
		t.Fatalf("sanitized JUnit is not well formed: %v\n%s", err, junit)
	}
}

type junitSuiteFixture struct {
	XMLName xml.Name `xml:"testsuite"`
}

func TestJSONSchemaCompilesValidatesReportAndRejectsUnknownFields(t *testing.T) {
	schemaBytes := report.JSONSchema()
	document, err := jsonschema.UnmarshalJSON(bytes.NewReader(schemaBytes))
	if err != nil {
		t.Fatal(err)
	}
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	if err := compiler.AddResource("https://goatest.invalid/assurance-report-v1.schema.json", document); err != nil {
		t.Fatal(err)
	}
	compiled, err := compiler.Compile("https://goatest.invalid/assurance-report-v1.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	valid := report.JSON(fixture())
	instance, err := jsonschema.UnmarshalJSON(bytes.NewReader(valid))
	if err != nil {
		t.Fatal(err)
	}
	if err := compiled.Validate(instance); err != nil {
		t.Fatalf("valid assurance report was rejected: %v", err)
	}
	var invalid map[string]any
	if err := json.Unmarshal(valid, &invalid); err != nil {
		t.Fatal(err)
	}
	invalid["unknown"] = true
	if err := compiled.Validate(invalid); err == nil {
		t.Fatal("unknown report field passed assurance schema")
	}
	for _, field := range []string{"evidence", "findings", "repairs"} {
		var nested map[string]any
		if err := json.Unmarshal(valid, &nested); err != nil {
			t.Fatal(err)
		}
		items := nested[field].([]any)
		items[0].(map[string]any)["unknown"] = true
		if err := compiled.Validate(nested); err == nil {
			t.Errorf("unknown %s item field passed assurance schema", field)
		}
	}
}
