// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package report

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
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
			Results: results,
			Properties: map[string]any{
				"runId": canonical.RunID, "runKind": canonical.RunKind, "contract": canonical.Contract,
				"snapshot": canonical.Snapshot, "verdict": canonical.Verdict,
				"requestedScope": canonical.Scope.Requested.Kind, "resolvedScope": canonical.Scope.Resolved.Kind,
				"scope": canonical.Scope, "repository": canonical.Repository, "configuration": canonical.Configuration,
				"toolchain": canonical.Toolchain, "timing": canonical.Timing, "cache": canonical.Cache,
				"accounting": canonical.Accounting, "mutants": canonical.Mutants,
				"acceptances": canonical.Acceptances, "limitations": canonical.Limitations,
			},
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
			{Name: "run_id", Value: canonical.RunID},
			{Name: "run_kind", Value: string(canonical.RunKind)},
			{Name: "contract", Value: canonical.Contract},
			{Name: "snapshot", Value: canonical.Snapshot},
			{Name: "verdict", Value: string(canonical.Verdict)},
			{Name: "requested_scope", Value: canonical.Scope.Requested.Kind},
			{Name: "resolved_scope", Value: canonical.Scope.Resolved.Kind},
			{Name: "repository_module", Value: canonical.Repository.Module},
			{Name: "git_commit", Value: canonical.Repository.Git.Commit},
			{Name: "git_dirty", Value: fmt.Sprint(canonical.Repository.Git.Dirty)},
			{Name: "configuration_digest", Value: canonical.Configuration.Digest},
			{Name: "go_version", Value: canonical.Toolchain.Go},
			{Name: "goatest_version", Value: canonical.Toolchain.Goatest},
			{Name: "go_mutants_version", Value: canonical.Toolchain.GoMutants},
			{Name: "cache_derived", Value: fmt.Sprint(canonical.Cache.Derived)},
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
	nonEmptyString := map[string]any{"type": "string", "minLength": 1}
	digestType := map[string]any{"type": "string", "pattern": "^[0-9a-f]{64}$"}
	integerType := map[string]any{"type": "integer", "minimum": 0}
	document := map[string]any{
		"$schema":              "https://json-schema.org/draft/2020-12/schema",
		"$id":                  SchemaV1,
		"title":                "goatest assurance report v1",
		"type":                 "object",
		"additionalProperties": false,
		"required": []string{
			"schema", "run_id", "run_kind", "verdict", "contract", "snapshot", "scope", "repository",
			"configuration", "toolchain", "timing", "cache", "accounting", "targets", "mutants", "acceptances", "evidence", "findings",
			"repairs", "limitations",
		},
		"properties": map[string]any{
			"schema":   map[string]any{"const": SchemaV1},
			"run_id":   nonEmptyString,
			"run_kind": map[string]any{"enum": []string{string(RunFull), string(RunChangeset), string(RunPackage), string(RunReplay), string(RunOperation)}},
			"verdict": map[string]any{"enum": []string{
				string(VerdictAssured), string(VerdictChangeAssured), string(VerdictScopeAssured),
				string(VerdictDefect), string(VerdictInsufficient), string(VerdictError),
				string(VerdictReproduced), string(VerdictResolved), string(VerdictCompleted),
			}},
			"contract":      nonEmptyString,
			"snapshot":      nonEmptyString,
			"scope":         map[string]any{"$ref": "#/$defs/scope"},
			"repository":    map[string]any{"$ref": "#/$defs/repository"},
			"configuration": map[string]any{"$ref": "#/$defs/configuration"},
			"toolchain":     map[string]any{"$ref": "#/$defs/toolchain"},
			"timing":        map[string]any{"$ref": "#/$defs/timing"},
			"cache":         map[string]any{"$ref": "#/$defs/cache"},
			"accounting":    map[string]any{"$ref": "#/$defs/accounting"},
			"targets":       map[string]any{"type": "array", "items": map[string]any{"$ref": "#/$defs/targetDisposition"}},
			"mutants":       map[string]any{"type": "array", "items": map[string]any{"$ref": "#/$defs/mutantDisposition"}},
			"resume":        map[string]any{"$ref": "#/$defs/resume"},
			"acceptances":   map[string]any{"type": "array", "items": map[string]any{"$ref": "#/$defs/acceptance"}},
			"evidence":      map[string]any{"type": "array", "items": map[string]any{"$ref": "#/$defs/evidence"}},
			"findings":      map[string]any{"type": "array", "items": map[string]any{"$ref": "#/$defs/finding"}},
			"repairs":       map[string]any{"type": "array", "items": map[string]any{"$ref": "#/$defs/repair"}},
			"limitations":   map[string]any{"type": "array", "items": map[string]any{"$ref": "#/$defs/limitation"}},
		},
		"$defs": map[string]any{
			"scope": objectSchema([]string{"requested", "resolved"}, map[string]any{
				"requested": map[string]any{"$ref": "#/$defs/scopeSpec"},
				"resolved":  map[string]any{"$ref": "#/$defs/scopeSpec"},
			}),
			"scopeSpec": objectSchema([]string{"kind", "project", "modules", "packages", "files"}, map[string]any{
				"kind": nonEmptyString, "project": nonEmptyString,
				"modules":  map[string]any{"type": "array", "items": stringType},
				"packages": map[string]any{"type": "array", "items": stringType},
				"files":    map[string]any{"type": "array", "items": stringType},
				"ref":      stringType,
			}),
			"git": objectSchema([]string{"available", "commit", "dirty", "merge_base", "changed_files"}, map[string]any{
				"available": map[string]any{"type": "boolean"}, "commit": nonEmptyString,
				"dirty": map[string]any{"type": "boolean"}, "merge_base": nonEmptyString,
				"changed_files": map[string]any{"type": "array", "items": stringType},
			}),
			"repository": objectSchema([]string{"module", "packages", "git"}, map[string]any{
				"module": nonEmptyString, "packages": map[string]any{"type": "array", "items": stringType}, "git": map[string]any{"$ref": "#/$defs/git"},
			}),
			"configuration": objectSchema([]string{"digest"}, map[string]any{"digest": digestType}),
			"toolchain": objectSchema([]string{"go", "goatest", "go_mutants", "os", "arch"}, map[string]any{
				"go": nonEmptyString, "goatest": nonEmptyString, "go_mutants": nonEmptyString, "os": nonEmptyString, "arch": nonEmptyString,
			}),
			"timing": objectSchema([]string{"started_at", "finished_at", "duration_ms"}, map[string]any{
				"started_at": stringType, "finished_at": stringType, "duration_ms": integerType,
			}),
			"cache": objectSchema([]string{"derived"}, map[string]any{
				"derived": map[string]any{"type": "boolean"}, "source_run_id": stringType,
			}),
			"accounting": objectSchema([]string{"targets", "mutants", "race"}, map[string]any{
				"targets": map[string]any{"$ref": "#/$defs/countAccounting"},
				"mutants": map[string]any{"$ref": "#/$defs/mutantAccounting"},
				"race":    map[string]any{"$ref": "#/$defs/countAccounting"},
			}),
			"countAccounting": objectSchema([]string{"discovered", "selected", "executed", "skipped", "excluded"}, map[string]any{
				"discovered": integerType, "selected": integerType, "executed": integerType, "skipped": integerType, "excluded": integerType,
			}),
			"targetDisposition": objectSchema([]string{"id", "name", "kind", "package", "path", "line", "status", "duration_ms"}, map[string]any{
				"id": nonEmptyString, "name": nonEmptyString, "kind": nonEmptyString, "package": nonEmptyString,
				"path": stringType, "line": integerType, "status": nonEmptyString, "duration_ms": integerType, "detail": stringType,
			}),
			"resume": objectSchema([]string{"attempts", "reused_targets", "reused_race_packages", "reused_mutants"}, map[string]any{
				"attempts":       map[string]any{"type": "integer", "minimum": 1},
				"reused_targets": integerType, "reused_race_packages": integerType, "reused_mutants": integerType,
			}),
			"mutantAccounting": objectSchema([]string{
				"discovered", "selected", "executed", "killed", "survived", "inconclusive",
				"compile_rejected", "accepted", "out_of_scope", "unknown",
			}, map[string]any{
				"discovered": integerType, "selected": integerType, "executed": integerType,
				"killed": integerType, "survived": integerType, "inconclusive": integerType,
				"compile_rejected": integerType, "accepted": integerType, "out_of_scope": integerType, "unknown": integerType,
				// The reuse counters are optional, so every report written
				// before evidence was ever reused still validates.
				"reused_killed": integerType, "reused_survived": integerType,
			}),
			"mutantDisposition": objectSchema([]string{"id", "status", "path", "line", "package", "rule"}, map[string]any{
				"id": nonEmptyString,
				"status": map[string]any{"enum": []string{
					string(MutantKilled), string(MutantSurvived), string(MutantInconclusive), string(MutantCompileRejected),
					string(MutantAccepted), string(MutantOutOfScope), string(MutantUnknown),
				}},
				"path": stringType, "line": integerType, "package": stringType, "rule": stringType, "detail": stringType,
				"reused": map[string]any{"type": "boolean"}, "provenance": stringType,
			}),
			"acceptance": objectSchema([]string{"id", "reason", "expires"}, map[string]any{
				"id": nonEmptyString, "reason": nonEmptyString, "expires": nonEmptyString,
				"owner": stringType, "ticket": stringType,
			}),
			"evidence": objectSchema([]string{"kind", "id", "status"}, map[string]any{"kind": stringType, "id": stringType, "status": stringType, "detail": stringType}),
			"finding": objectSchema([]string{"id", "kind", "summary"}, map[string]any{
				"id": stringType, "kind": stringType, "path": stringType, "line": map[string]any{"type": "integer", "minimum": 0},
				"summary": stringType, "replay": stringType, "mutant": stringType, "mutant_id": stringType,
			}),
			"repair": objectSchema([]string{"id", "finding", "path", "status"}, map[string]any{
				"id": stringType, "finding": stringType, "path": stringType, "status": stringType,
				"diff": stringType, "validation": stringType, "reason": stringType, "provenance": stringType,
			}),
			"limitation": objectSchema([]string{"code", "summary"}, map[string]any{
				"code": stringType, "summary": stringType, "estimated": map[string]any{"type": "boolean"},
			}),
		},
	}
	data, _ := json.MarshalIndent(document, "", "  ")
	return append(data, '\n')
}

func objectSchema(required []string, properties map[string]any) map[string]any {
	return map[string]any{"type": "object", "additionalProperties": false, "required": required, "properties": properties}
}
