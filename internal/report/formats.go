// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package report

import (
	"encoding/json"
	"encoding/xml"
	"slices"
)

type sarifDocument struct {
	Schema  string     `json:"$schema"`
	Version string     `json:"version"`
	Runs    []sarifRun `json:"runs"`
}

type sarifRun struct {
	Tool       sarifTool      `json:"tool"`
	Results    []sarifResult  `json:"results"`
	Properties map[string]any `json:"properties"`
}

type sarifTool struct {
	Driver sarifDriver `json:"driver"`
}

type sarifDriver struct {
	Name            string      `json:"name"`
	InformationURI  string      `json:"informationUri"`
	SemanticVersion string      `json:"semanticVersion"`
	Rules           []sarifRule `json:"rules"`
}

type sarifRule struct {
	ID   string       `json:"id"`
	Name string       `json:"name"`
	Help sarifMessage `json:"shortDescription"`
}

type sarifResult struct {
	RuleID     string          `json:"ruleId"`
	Level      string          `json:"level"`
	Message    sarifMessage    `json:"message"`
	Locations  []sarifLocation `json:"locations,omitempty"`
	Properties map[string]any  `json:"properties"`
}

type sarifMessage struct {
	Text string `json:"text"`
}

type sarifLocation struct {
	PhysicalLocation sarifPhysicalLocation `json:"physicalLocation"`
}

type sarifPhysicalLocation struct {
	ArtifactLocation sarifArtifactLocation `json:"artifactLocation"`
	Region           *sarifRegion          `json:"region,omitempty"`
}

type sarifArtifactLocation struct {
	URI string `json:"uri"`
}

type sarifRegion struct {
	StartLine int `json:"startLine"`
}

// SARIF projects findings to deterministic SARIF 2.1.0 bytes.
func SARIF(input Report) []byte {
	canonical := canonical(input)
	kinds := make(map[string]struct{})
	for _, finding := range canonical.Findings {
		kinds[finding.Kind] = struct{}{}
	}
	names := make([]string, 0, len(kinds))
	for kind := range kinds {
		names = append(names, kind)
	}
	slices.Sort(names)
	rules := make([]sarifRule, 0, len(names))
	for _, name := range names {
		rules = append(rules, sarifRule{ID: name, Name: name, Help: sarifMessage{Text: "goatest assurance finding: " + name}})
	}
	results := make([]sarifResult, 0, len(canonical.Findings))
	for _, finding := range canonical.Findings {
		result := sarifResult{
			RuleID: finding.Kind, Level: "error", Message: sarifMessage{Text: finding.Summary},
			Properties: map[string]any{"findingId": finding.ID, "mutant": finding.Mutant, "mutantId": finding.MutantID, "replay": finding.Replay},
		}
		if finding.Path != "" {
			physical := sarifPhysicalLocation{ArtifactLocation: sarifArtifactLocation{URI: finding.Path}}
			if finding.Line > 0 {
				physical.Region = &sarifRegion{StartLine: finding.Line}
			}
			result.Locations = []sarifLocation{{PhysicalLocation: physical}}
		}
		results = append(results, result)
	}
	document := sarifDocument{
		Schema: "https://json.schemastore.org/sarif-2.1.0.json", Version: "2.1.0",
		Runs: []sarifRun{{
			Tool: sarifTool{Driver: sarifDriver{
				Name: "goatest", InformationURI: "https://github.com/P4suta/goatest", SemanticVersion: "0.1.0", Rules: rules,
			}},
			Results:    results,
			Properties: map[string]any{"contract": canonical.Contract, "snapshot": canonical.Snapshot, "verdict": canonical.Verdict},
		}},
	}
	data, _ := json.MarshalIndent(document, "", "  ")
	return append(data, '\n')
}

type junitSuite struct {
	XMLName    xml.Name        `xml:"testsuite"`
	Name       string          `xml:"name,attr"`
	Tests      int             `xml:"tests,attr"`
	Failures   int             `xml:"failures,attr"`
	Properties []junitProperty `xml:"properties>property"`
	Cases      []junitCase     `xml:"testcase"`
}

type junitProperty struct {
	Name  string `xml:"name,attr"`
	Value string `xml:"value,attr"`
}

type junitCase struct {
	Classname string        `xml:"classname,attr"`
	Name      string        `xml:"name,attr"`
	Failure   *junitFailure `xml:"failure,omitempty"`
}

type junitFailure struct {
	Message string `xml:"message,attr"`
	Type    string `xml:"type,attr"`
	Body    string `xml:",chardata"`
}

// JUnit projects all evidence as passing cases and findings as failing cases.
func JUnit(input Report) []byte {
	canonical := canonical(input)
	suite := junitSuite{
		Name: "goatest", Tests: len(canonical.Evidence) + len(canonical.Findings), Failures: len(canonical.Findings),
		Properties: []junitProperty{
			{Name: "contract", Value: canonical.Contract},
			{Name: "snapshot", Value: canonical.Snapshot},
			{Name: "verdict", Value: string(canonical.Verdict)},
		},
	}
	for _, item := range canonical.Evidence {
		suite.Cases = append(suite.Cases, junitCase{Classname: "goatest.evidence." + item.Kind, Name: item.ID + " " + item.Status})
	}
	for _, finding := range canonical.Findings {
		suite.Cases = append(suite.Cases, junitCase{
			Classname: "goatest.finding." + finding.Kind, Name: finding.ID,
			Failure: &junitFailure{Message: finding.Summary, Type: finding.Kind, Body: finding.Path},
		})
	}
	data, _ := xml.MarshalIndent(suite, "", "  ")
	return append([]byte(xml.Header), append(data, '\n')...)
}

// JSONSchema returns the self-contained assurance-report-v1 JSON Schema.
func JSONSchema() []byte {
	stringType := map[string]any{"type": "string"}
	document := map[string]any{
		"$schema":              "https://json-schema.org/draft/2020-12/schema",
		"$id":                  SchemaV1,
		"title":                "goatest assurance report v1",
		"type":                 "object",
		"additionalProperties": false,
		"required":             []string{"schema", "verdict", "evidence", "findings", "repairs", "residual_risks"},
		"properties": map[string]any{
			"schema":         map[string]any{"const": SchemaV1},
			"verdict":        map[string]any{"enum": []string{string(VerdictAssured), string(VerdictDefect), string(VerdictInsufficient), string(VerdictError)}},
			"contract":       stringType,
			"snapshot":       stringType,
			"evidence":       map[string]any{"type": "array", "items": map[string]any{"$ref": "#/$defs/evidence"}},
			"findings":       map[string]any{"type": "array", "items": map[string]any{"$ref": "#/$defs/finding"}},
			"repairs":        map[string]any{"type": "array", "items": map[string]any{"$ref": "#/$defs/repair"}},
			"residual_risks": map[string]any{"type": "array", "items": stringType},
		},
		"$defs": map[string]any{
			"evidence": objectSchema([]string{"kind", "id", "status"}, map[string]any{"kind": stringType, "id": stringType, "status": stringType, "detail": stringType}),
			"finding": objectSchema([]string{"id", "kind", "summary"}, map[string]any{
				"id": stringType, "kind": stringType, "path": stringType, "line": map[string]any{"type": "integer", "minimum": 0},
				"summary": stringType, "replay": stringType, "mutant": stringType, "mutant_id": stringType,
			}),
			"repair": objectSchema([]string{"id", "finding", "path", "status"}, map[string]any{"id": stringType, "finding": stringType, "path": stringType, "status": stringType}),
		},
	}
	data, _ := json.MarshalIndent(document, "", "  ")
	return append(data, '\n')
}

func objectSchema(required []string, properties map[string]any) map[string]any {
	return map[string]any{"type": "object", "additionalProperties": false, "required": required, "properties": properties}
}
