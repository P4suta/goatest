// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package report_test

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"fmt"
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
		Repairs: []report.Repair{{ID: "r1", Finding: "f2", Path: "z_test.go", Status: "applied"}},
		Limitations: []report.Limitation{
			{Code: "z-risk", Summary: "z risk"},
			{Code: "a-risk", Summary: "a risk"},
		},
	}
}

func persistedFixture() report.Report {
	result := fixture()
	result.RunID = "fixture-run"
	result.RunKind = report.RunFull
	result.Scope = report.Scope{
		Requested: report.ScopeSpec{Kind: "full", Project: ".", Modules: []string{"example.test/fixture"}},
		Resolved:  report.ScopeSpec{Kind: "full", Project: ".", Modules: []string{"example.test/fixture"}},
	}
	result.Repository = report.Repository{
		Module: "example.test/fixture",
		Git:    report.Git{Available: true, Commit: "commit", MergeBase: "commit"},
	}
	result.Configuration = report.Configuration{Digest: strings.Repeat("a", 64)}
	result.Toolchain = report.Toolchain{Go: "go1.26.6", Goatest: "devel", GoMutants: "v0.1.2", OS: "linux", Arch: "amd64"}
	result.Timing = report.Timing{StartedAt: "2026-01-01T00:00:00Z", FinishedAt: "2026-01-01T00:00:01Z", DurationMS: 1000}
	result.Acceptances = []report.Acceptance{{ID: "accepted-fixture", Reason: "reviewed", Expires: "2026-12-01T00:00:00Z"}}
	return result
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
		"evidence 2  findings 2  repairs 1  acceptances 0  limitations 2\n" +
		"FINDING f1 coverage a.go: unreached\n" +
		"FINDING f2 survivor z.go: survived\n" +
		"  MUTANT lt-to-le: < -> <=\n" +
		"  REPLAY goatest replay f2\n" +
		"REPAIR r1 applied z_test.go finding=f2\n" +
		"LIMITATION a-risk a risk\n" +
		"LIMITATION z-risk z risk\n"
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
		Limitations: []report.Limitation{{Code: "risk", Summary: "risk\nREPAIR forged"}},
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
	html := report.HTML(persistedFixture())
	text := strings.ToLower(string(html))
	for _, forbidden := range []string{"http://", "https://", "<script src=", "<link rel="} {
		if strings.Contains(text, forbidden) {
			t.Errorf("HTML contains external dependency %q", forbidden)
		}
	}
	if !strings.Contains(text, "<!doctype html>") || !strings.Contains(text, "insufficient") {
		t.Errorf("HTML is incomplete: %s", html)
	}
	for _, required := range []string{
		"lt-to-le: &lt; -&gt; &lt;=", "goatest replay f2", "z_test.go", "a risk", "z risk",
		`id="report-search"`, `data-section="findings"`, "acceptances", "accounting",
	} {
		if !strings.Contains(text, required) {
			t.Errorf("HTML omitted %q: %s", required, html)
		}
	}
}

func TestTargetInventoryRanksSlowestFirstAndResumeIsOptionalAuditMetadata(t *testing.T) {
	input := persistedFixture()
	input.Accounting.Targets = report.CountAccounting{Discovered: 3, Selected: 3, Executed: 3}
	input.Targets = []report.TargetDisposition{
		{ID: "target-b", Name: "TestB", Kind: "test", Package: "example.test/fixture", Status: "passed", DurationMS: 100},
		{ID: "target-slow", Name: "TestSlow", Kind: "test", Package: "example.test/fixture", Status: "passed", DurationMS: 200},
		{ID: "target-a", Name: "TestA", Kind: "test", Package: "example.test/fixture", Status: "passed", DurationMS: 100},
	}
	input.Resume = &report.Resume{Attempts: 2, ReusedTargets: 1}
	data := report.JSON(input)
	if bytes.Index(data, []byte(`"id": "target-slow"`)) > bytes.Index(data, []byte(`"id": "target-a"`)) ||
		bytes.Index(data, []byte(`"id": "target-a"`)) > bytes.Index(data, []byte(`"id": "target-b"`)) {
		t.Fatalf("target inventory is not duration-descending with ID tie-break:\n%s", data)
	}
	html := string(report.HTML(input))
	if strings.Index(html, "target-slow") > strings.Index(html, "target-a") || !strings.Contains(html, "attempt 2") || !strings.Contains(html, "Targets (slowest first)") {
		t.Fatalf("HTML target ranking/resume metadata missing: %s", html)
	}
	if err := report.Validate(input); err != nil {
		t.Fatalf("target/resume report rejected: %v", err)
	}
	input.Resume.ReusedTargets = 4
	if err := report.Validate(input); err == nil || !strings.Contains(err.Error(), "resume") {
		t.Fatalf("impossible resume metadata error = %v", err)
	}
}

func TestTargetInventoryMustAccountForEverySelectedTarget(t *testing.T) {
	for _, test := range []struct {
		name    string
		targets []report.TargetDisposition
	}{
		{name: "empty"},
		{name: "partial", targets: []report.TargetDisposition{{ID: "target-a", Name: "TestA", Kind: "test", Package: "example.test/fixture", Status: "passed"}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			input := persistedFixture()
			input.Accounting.Targets = report.CountAccounting{Discovered: 2, Selected: 2, Executed: 2}
			input.Targets = test.targets
			if err := report.ValidateForPersistence(input); err == nil || !strings.Contains(err.Error(), "target inventory") {
				t.Fatalf("incomplete target inventory error = %v", err)
			}
		})
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

func TestAuditValidationRejectsScopeMisrepresentationAndMissingMutants(t *testing.T) {
	valid := report.Report{
		Schema: report.SchemaV1, RunKind: report.RunChangeset, Verdict: report.VerdictChangeAssured,
		Scope: report.Scope{
			Requested: report.ScopeSpec{Kind: "changeset"},
			Resolved:  report.ScopeSpec{Kind: "changeset"},
		},
		Accounting: report.Accounting{Mutants: report.MutantAccounting{
			Discovered: 13, Selected: 13, Executed: 13, Killed: 13,
		}},
	}
	for index := range 13 {
		valid.Mutants = append(valid.Mutants, report.MutantDisposition{ID: fmt.Sprintf("mutant-%02d", index), Status: report.MutantKilled})
	}
	if err := report.Validate(valid); err != nil {
		t.Fatalf("valid report rejected: %v", err)
	}
	misrepresented := valid
	misrepresented.Verdict = report.VerdictAssured
	if err := report.Validate(misrepresented); err == nil || !strings.Contains(err.Error(), "ASSURED") {
		t.Fatalf("partial ASSURED error = %v", err)
	}
	missing := valid
	missing.Verdict = report.VerdictError
	missing.Accounting.Mutants.Unknown = 1
	missing.Accounting.Mutants.Executed = 12
	missing.Accounting.Mutants.Killed = 12
	missing.Mutants[0].Status = report.MutantUnknown
	if err := report.Validate(missing); err != nil {
		t.Fatalf("auditable ERROR report rejected: %v", err)
	}
	missing.Verdict = report.VerdictChangeAssured
	if err := report.Validate(missing); err == nil || !strings.Contains(err.Error(), "ERROR") {
		t.Fatalf("unknown mutant passed non-error verdict: %v", err)
	}
}

func TestPersistenceValidationRequiresUnambiguousAuditMetadata(t *testing.T) {
	valid := persistedFixture()
	if err := report.ValidateForPersistence(valid); err != nil {
		t.Fatalf("valid persisted report rejected: %v", err)
	}
	for _, testCase := range []struct {
		name   string
		change func(*report.Report)
		want   string
	}{
		{name: "verdict", change: func(value *report.Report) { value.Verdict = "" }, want: "verdict"},
		{name: "run-kind", change: func(value *report.Report) { value.RunKind = "future" }, want: "run_kind"},
		{name: "contract", change: func(value *report.Report) { value.Contract = "" }, want: "contract"},
		{name: "snapshot", change: func(value *report.Report) { value.Snapshot = "" }, want: "snapshot"},
		{name: "project", change: func(value *report.Report) { value.Scope.Resolved.Project = "" }, want: "project boundary"},
		{name: "module", change: func(value *report.Report) { value.Repository.Module = "" }, want: "module"},
		{name: "configuration", change: func(value *report.Report) { value.Configuration.Digest = "not-a-digest" }, want: "SHA-256"},
		{name: "toolchain", change: func(value *report.Report) { value.Toolchain.Go = "" }, want: "toolchain"},
		{name: "git-commit", change: func(value *report.Report) { value.Repository.Git.Commit = "" }, want: "Git identity"},
		{name: "cache-source", change: func(value *report.Report) { value.Cache.SourceRunID = "unexpected" }, want: "non-cache"},
		{name: "expired-acceptance", change: func(value *report.Report) {
			value.Acceptances = []report.Acceptance{value.Acceptances[0]}
			value.Acceptances[0].Expires = "2025-12-01T00:00:00Z"
		}, want: "was expired"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			input := valid
			testCase.change(&input)
			if err := report.ValidateForPersistence(input); err == nil || !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("validation error = %v, want %q", err, testCase.want)
			}
		})
	}

	unavailable := valid
	unavailable.Repository.Git = report.Git{Commit: "unavailable", MergeBase: "unavailable"}
	unavailable.Limitations = append(unavailable.Limitations, report.Limitation{
		Code: "git-metadata-unavailable", Summary: "Git metadata is unavailable",
	})
	if err := report.ValidateForPersistence(unavailable); err != nil {
		t.Fatalf("explicit unavailable Git identity rejected: %v", err)
	}
	unavailable.Repository.Git.Dirty = true
	if err := report.ValidateForPersistence(unavailable); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("partial unavailable Git identity error = %v", err)
	}
}

func TestAcceptanceMetadataMustExplainEveryAcceptedMutation(t *testing.T) {
	input := report.Report{
		Accounting: report.Accounting{Mutants: report.MutantAccounting{Discovered: 1, Selected: 1, Accepted: 1}},
		Mutants:    []report.MutantDisposition{{ID: "mutant-a", Status: report.MutantAccepted, Detail: "finding-a"}},
		Evidence:   []report.Evidence{{Kind: "mutation", ID: "mutant-a", Status: "accepted", Detail: "finding-a"}},
	}
	if err := report.Validate(input); err == nil || !strings.Contains(err.Error(), "no acceptance metadata") {
		t.Fatalf("unexplained acceptance error = %v", err)
	}
	input.Acceptances = []report.Acceptance{{
		ID: "finding-a", Reason: "reviewed equivalent boundary", Expires: "2026-12-01T00:00:00Z",
		Owner: "quality-team", Ticket: "QA-123",
	}}
	if err := report.Validate(input); err != nil {
		t.Fatalf("explained acceptance rejected: %v", err)
	}
	lines := report.Lines(input)
	for _, expected := range []string{"ACCEPTANCE finding-a", "reason=reviewed equivalent boundary", "owner=quality-team", "ticket=QA-123"} {
		if !strings.Contains(lines, expected) {
			t.Errorf("acceptance output omitted %q: %s", expected, lines)
		}
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
	valid := report.JSON(persistedFixture())
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
	for _, field := range []string{"acceptances", "evidence", "findings", "repairs", "limitations"} {
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

// reusedFixture is a report of three mutants, one of them killed by evidence
// an earlier run established rather than by an execution of this one.
func reusedFixture() report.Report {
	result := report.Report{
		Schema: report.SchemaV1, RunKind: report.RunFull, Verdict: report.VerdictAssured,
		Scope: report.Scope{
			Requested: report.ScopeSpec{Kind: "full"},
			Resolved:  report.ScopeSpec{Kind: "full"},
		},
		Accounting: report.Accounting{Mutants: report.MutantAccounting{
			Discovered: 3, Selected: 3, Executed: 3, Killed: 3, ReusedKilled: 1,
		}},
		Mutants: []report.MutantDisposition{
			{
				ID: "mutant-01", Status: report.MutantKilled, Detail: "TestValue",
				Reused: true, Provenance: "snapshot=" + strings.Repeat("b", 64),
			},
			{ID: "mutant-02", Status: report.MutantKilled, Detail: "TestValue"},
			{ID: "mutant-03", Status: report.MutantKilled, Detail: "TestOther"},
		},
	}
	return result
}

// TestValidateReconcilesReusedCountsWithTheMutantInventory pins the audit that
// makes the reuse counters worth reading: they are a summary of the inventory,
// and a summary that disagrees with what it summarises is refused.
func TestValidateReconcilesReusedCountsWithTheMutantInventory(t *testing.T) {
	t.Parallel()
	valid := reusedFixture()
	if err := report.Validate(valid); err != nil {
		t.Fatalf("a report carrying reused evidence was rejected: %v", err)
	}
	for _, test := range []struct {
		name   string
		change func(*report.Report)
	}{
		{name: "a reuse the counter does not know about", change: func(input *report.Report) {
			input.Mutants[1].Reused = true
			input.Mutants[1].Provenance = "snapshot=" + strings.Repeat("c", 64)
		}},
		{name: "a counter no disposition accounts for", change: func(input *report.Report) {
			input.Accounting.Mutants.ReusedKilled = 2
		}},
		{name: "a survived counter no disposition accounts for", change: func(input *report.Report) {
			input.Accounting.Mutants.ReusedSurvived = 1
		}},
		{name: "a negative reuse count", change: func(input *report.Report) {
			input.Accounting.Mutants.ReusedKilled = -1
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			invalid := reusedFixture()
			test.change(&invalid)
			if err := report.Validate(invalid); err == nil {
				t.Fatalf("%s passed validation", test.name)
			}
		})
	}
}

// TestValidateRejectsAReusedMutantWithoutProvenance pins the audit trail. A
// disposition that says it was not established by this run has to name the run
// that did establish it, or the claim cannot be traced to anything.
func TestValidateRejectsAReusedMutantWithoutProvenance(t *testing.T) {
	t.Parallel()
	invalid := reusedFixture()
	invalid.Mutants[0].Provenance = ""
	if err := report.Validate(invalid); err == nil || !strings.Contains(err.Error(), "provenance") {
		t.Fatalf("a reused mutant without provenance = %v", err)
	}
}

// TestValidateRejectsProvenanceWithoutTheReusedFlag pins the other direction:
// provenance is what a reuse is audited by, so a disposition carrying one
// while claiming this run established it contradicts itself.
func TestValidateRejectsProvenanceWithoutTheReusedFlag(t *testing.T) {
	t.Parallel()
	invalid := reusedFixture()
	invalid.Mutants[0].Reused = false
	invalid.Accounting.Mutants.ReusedKilled = 0
	if err := report.Validate(invalid); err == nil || !strings.Contains(err.Error(), "provenance") {
		t.Fatalf("provenance without a reuse = %v", err)
	}
}

// TestValidateRejectsAReusedMutantWithoutAnExecutionDisposition pins what a
// reuse may be about: a mutant that reached a terminal execution disposition.
// Nothing else was ever executed, so nothing else can be reused.
func TestValidateRejectsAReusedMutantWithoutAnExecutionDisposition(t *testing.T) {
	t.Parallel()
	invalid := reusedFixture()
	invalid.Mutants[0].Status = report.MutantCompileRejected
	invalid.Accounting.Mutants = report.MutantAccounting{
		Discovered: 3, Selected: 3, Executed: 2, Killed: 2, CompileRejected: 1, ReusedKilled: 1,
	}
	if err := report.Validate(invalid); err == nil {
		t.Fatal("a reused compile rejection passed validation")
	}
}

// TestValidateAcceptsAReusedMutantThisRunAccepted pins the one disposition a
// reuse reaches without an execution of its own. A reused verdict raises its
// finding again here, so this run's acceptances decide it, and an acceptance
// that still holds silences the finding: the mutant was reused and reports as
// accepted. It is outside the executed counts, which is why the reuse counters
// stay empty while the flag and its provenance stay.
func TestValidateAcceptsAReusedMutantThisRunAccepted(t *testing.T) {
	t.Parallel()
	accepted := reusedFixture()
	accepted.Mutants[0].Status = report.MutantAccepted
	accepted.Mutants[0].Detail = "finding-01"
	accepted.Acceptances = []report.Acceptance{
		{ID: "finding-01", Reason: "tracked elsewhere", Expires: "2099-01-01T00:00:00Z"},
	}
	accepted.Accounting.Mutants = report.MutantAccounting{
		Discovered: 3, Selected: 3, Executed: 2, Killed: 2, Accepted: 1,
	}
	if err := report.Validate(accepted); err != nil {
		t.Fatalf("a reused acceptance = %v", err)
	}
}

// TestValidateRejectsReusedCountsExceedingExecuted pins the inequality that
// holds however the counters were produced: a run cannot have reused more
// verdicts than it has mutants with a verdict.
func TestValidateRejectsReusedCountsExceedingExecuted(t *testing.T) {
	t.Parallel()
	invalid := report.Report{
		Schema: report.SchemaV1, Verdict: report.VerdictAssured,
		Accounting: report.Accounting{Mutants: report.MutantAccounting{
			Discovered: 1, Selected: 1, Executed: 1, Killed: 1, ReusedKilled: 1, ReusedSurvived: 1,
		}},
		Mutants: []report.MutantDisposition{{
			ID: "mutant-01", Status: report.MutantKilled,
			Reused: true, Provenance: "snapshot=" + strings.Repeat("b", 64),
		}},
	}
	if err := report.Validate(invalid); err == nil || !strings.Contains(err.Error(), "reused") {
		t.Fatalf("more reuse than execution = %v", err)
	}
}

// TestJSONSchemaAcceptsReusedFieldsAndAReportWithoutThem pins that the two
// additions are optional. Every report written before they existed still
// validates, which is what keeps the reports already on disk readable.
func TestJSONSchemaAcceptsReusedFieldsAndAReportWithoutThem(t *testing.T) {
	t.Parallel()
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
	carrying := persistedFixture()
	carrying.Accounting.Mutants = report.MutantAccounting{
		Discovered: 1, Selected: 1, Executed: 1, Killed: 1, ReusedKilled: 1,
	}
	carrying.Mutants = []report.MutantDisposition{{
		ID: "mutant-01", Status: report.MutantKilled, Path: "value.go", Line: 8,
		Package: "example.test/fixture", Rule: "arithmetic", Detail: "TestValue",
		Reused: true, Provenance: "snapshot=" + strings.Repeat("b", 64),
	}}
	for _, test := range []struct {
		name  string
		input report.Report
	}{
		{name: "a report carrying the fields", input: carrying},
		{name: "a report written before they existed", input: persistedFixture()},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if err := report.Validate(test.input); err != nil {
				t.Fatalf("%s was rejected by the audit: %v", test.name, err)
			}
			encoded := report.JSON(test.input)
			instance, err := jsonschema.UnmarshalJSON(bytes.NewReader(encoded))
			if err != nil {
				t.Fatal(err)
			}
			if err := compiled.Validate(instance); err != nil {
				t.Fatalf("%s was rejected by the schema: %v", test.name, err)
			}
			var decoded report.Report
			decoder := json.NewDecoder(bytes.NewReader(encoded))
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&decoded); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(report.JSON(decoded), encoded) {
				t.Fatalf("%s did not round trip", test.name)
			}
		})
	}
	// A report written before the fields existed carries neither of them: an
	// absent reuse is not a recorded false.
	if strings.Contains(string(report.JSON(persistedFixture())), "reused") {
		t.Fatal("a report that reused nothing wrote a reuse field")
	}
}
