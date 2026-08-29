// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package report_test

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
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
	first, err := report.JSON(fixture())
	if err != nil {
		t.Fatal(err)
	}
	second, err := report.JSON(fixture())
	if err != nil {
		t.Fatal(err)
	}
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

func TestHTMLIsSelfContainedAndOffline(t *testing.T) {
	html, err := report.HTML(fixture())
	if err != nil {
		t.Fatal(err)
	}
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
	firstSARIF, err := report.SARIF(fixture())
	if err != nil {
		t.Fatal(err)
	}
	secondSARIF, _ := report.SARIF(fixture())
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

	junit, err := report.JUnit(fixture())
	if err != nil {
		t.Fatal(err)
	}
	var suite struct {
		XMLName  xml.Name `xml:"testsuite"`
		Tests    int      `xml:"tests,attr"`
		Failures int      `xml:"failures,attr"`
	}
	if err := xml.Unmarshal(junit, &suite); err != nil || suite.Tests != 4 || suite.Failures != 2 {
		t.Fatalf("JUnit = %+v, %v\n%s", suite, err, junit)
	}

	schema, err := report.JSONSchema()
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(schema, &document); err != nil || document["$id"] != report.SchemaV1 || document["additionalProperties"] != false {
		t.Fatalf("schema = %+v, %v\n%s", document, err, schema)
	}
}

func TestJSONSchemaCompilesValidatesReportAndRejectsUnknownFields(t *testing.T) {
	schemaBytes, err := report.JSONSchema()
	if err != nil {
		t.Fatal(err)
	}
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
	valid, err := report.JSON(fixture())
	if err != nil {
		t.Fatal(err)
	}
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
}
