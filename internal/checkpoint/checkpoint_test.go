// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package checkpoint_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/P4suta/goatest/internal/checkpoint"
	"github.com/P4suta/goatest/internal/report"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

func checkpointFixture() checkpoint.State {
	digest := strings.Repeat("a", 64)
	return checkpoint.State{
		Schema: checkpoint.SchemaV1, InputDigest: digest, Attempts: 2,
		Baseline: checkpoint.Baseline{BuildVetComplete: true, Complete: true, Evidence: []report.Evidence{{Kind: "baseline", ID: "go vet", Status: "passed"}}, Findings: []report.Finding{{
			ID: "finding-baseline", Kind: "coverage", Path: "value.go", Line: 4, Summary: "baseline finding", Replay: "goatest replay finding-baseline", Mutant: "comparison", MutantID: "mutant-a",
		}}, Targets: []checkpoint.BaselineTarget{{
			ID: "target-a", Executed: true, Inventory: report.TargetDisposition{ID: "target-a", Name: "TestA", Kind: "test", Package: "example.test/project", Path: "value_test.go", Line: 10, Status: "passed", DurationMS: 12},
			Target: &checkpoint.TargetEvidence{Target: checkpoint.Target{ID: "target-a", Name: "TestA", Kind: "test", Package: "example.test/project", Path: "value_test.go", Line: 10}, CoveredFiles: []string{"value.go"}, DurationNS: 12_000_000},
		}}},
		Race: &checkpoint.Race{Complete: true, Packages: []string{"example.test/project"}, Evidence: []report.Evidence{{Kind: "race", ID: "example.test/project", Status: "passed"}}},
		Mutation: &checkpoint.Mutation{CatalogFingerprint: strings.Repeat("b", 64), Results: []checkpoint.MutationResult{{
			ID: "mutant-a", Evidence: []report.Evidence{{Kind: "mutation", ID: "mutant-a", Status: "killed"}}, Findings: []report.Finding{{
				ID: "finding-mutant", Kind: "surviving-mutant", Path: "value.go", Line: 8, Summary: "mutation finding", Replay: "goatest replay finding-mutant", Mutant: "comparison", MutantID: "mutant-a",
			}}, Repairs: []report.Repair{{
				ID: "repair-a", Finding: "finding-mutant", Path: "value_test.go", Status: "applied", Diff: "+test", Validation: "passed", Reason: "cover mutant", Provenance: "goatest",
			}},
			Provenance: "snapshot=" + strings.Repeat("c", 64),
		}}},
	}
}

func TestCheckpointStrictRoundTripAndSchema(t *testing.T) {
	input := checkpointFixture()
	data := checkpoint.JSON(input)
	decoded, err := checkpoint.Decode(data)
	if err != nil || decoded.Attempts != 2 || len(decoded.Baseline.Targets) != 1 || len(decoded.Mutation.Results) != 1 {
		t.Fatalf("checkpoint round trip = (%+v, %v)", decoded, err)
	}
	// A mutant whose verdict the round resolved from an earlier run's evidence
	// carries the run that established it, so a resume reports the reuse the
	// interrupted run reported rather than claiming it observed the verdict.
	if got := decoded.Mutation.Results[0].Provenance; got != input.Mutation.Results[0].Provenance {
		t.Fatalf("resumed provenance = %q, want %q", got, input.Mutation.Results[0].Provenance)
	}
	document, err := jsonschema.UnmarshalJSON(bytes.NewReader(checkpoint.JSONSchema()))
	if err != nil {
		t.Fatal(err)
	}
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	if err := compiler.AddResource("https://goatest.invalid/assurance-checkpoint-v1.schema.json", document); err != nil {
		t.Fatal(err)
	}
	compiled, err := compiler.Compile("https://goatest.invalid/assurance-checkpoint-v1.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	instance, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil || compiled.Validate(instance) != nil {
		t.Fatalf("valid checkpoint failed schema: parse=%v validate=%v\n%s", err, compiled.Validate(instance), data)
	}
	var invalid map[string]any
	if err := json.Unmarshal(data, &invalid); err != nil {
		t.Fatal(err)
	}
	invalid["unknown"] = true
	if err := compiled.Validate(invalid); err == nil {
		t.Fatal("checkpoint schema accepted an unknown top-level field")
	}
	baseline := invalid["baseline"].(map[string]any)
	delete(invalid, "unknown")
	baseline["unknown"] = true
	if err := compiled.Validate(invalid); err == nil {
		t.Fatal("checkpoint schema accepted an unknown baseline field")
	}
}

func TestCheckpointDecoderRejectsCorruptionUnknownFieldsAndPendingUnits(t *testing.T) {
	valid := checkpoint.JSON(checkpointFixture())
	for _, test := range []struct {
		name string
		data []byte
	}{
		{name: "truncated", data: valid[:len(valid)/2]},
		{name: "trailing", data: append(append([]byte(nil), valid...), []byte("{}\n")...)},
		{name: "unknown", data: []byte(`{"schema":"assurance-checkpoint-v1","input_digest":"` + strings.Repeat("a", 64) + `","attempts":1,"baseline":{"build_vet_complete":false,"complete":false,"evidence":[],"findings":[],"targets":[],"unknown":true}}`)},
		{name: "pending mutant", data: checkpoint.JSON(func() checkpoint.State {
			state := checkpointFixture()
			state.Mutation.Results[0].Evidence = nil
			state.Mutation.Results[0].Findings = nil
			return state
		}())},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := checkpoint.Decode(test.data); err == nil {
				t.Fatal("invalid checkpoint was accepted")
			}
		})
	}
}
