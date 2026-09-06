// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package checkpoint_test

import (
	"bytes"
	"encoding/json"
	"slices"
	"testing"

	"github.com/P4suta/goatest/internal/checkpoint"
	"github.com/P4suta/goatest/internal/report"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

func checkpointFixture() checkpoint.State {
	digest := checkpointTestDigest("a")
	return checkpoint.State{
		Schema: checkpoint.SchemaV1, InputDigest: digest, Attempts: 2,
		Baseline: checkpoint.Baseline{BuildVetComplete: true, Complete: true, Evidence: []report.Evidence{{Kind: "baseline", ID: "go vet", Status: "passed"}}, Findings: []report.Finding{{
			ID: "finding-baseline", Kind: "coverage", Path: "value.go", Line: 4, Summary: "baseline finding", Replay: "goatest replay finding-baseline", Mutant: "comparison", MutantID: "mutant-a",
		}}, Targets: []checkpoint.BaselineTarget{{
			ID: "target-a", Executed: true, Inventory: report.TargetDisposition{ID: "target-a", Name: "TestA", Kind: "test", Package: "example.test/project", Path: "value_test.go", Line: 10, Status: "passed", DurationMS: 12},
			Target: &checkpoint.TargetEvidence{
				Target: checkpoint.Target{
					ID: "target-a", Name: "TestA", Kind: "test", Package: "example.test/project", Path: "value_test.go", Line: 10,
					Capabilities: []string{"redis", "postgres"},
				},
				CoveredFiles: []string{"value.go"}, DurationNS: 12_000_000, WholeTree: true,
				Coverage: &checkpoint.Coverage{Files: []checkpoint.FileCoverage{{
					Path: "value.go",
					Blocks: []checkpoint.CoverageBlock{{
						StartLine: 3, StartColumn: 2, EndLine: 4, EndColumn: 1,
					}},
				}}},
			},
		}}, Routing: &checkpoint.BaselineRouting{
			Instrumented: checkpoint.Coverage{Files: []checkpoint.FileCoverage{{
				Path: "value.go", Blocks: []checkpoint.CoverageBlock{{StartLine: 1, StartColumn: 1, EndLine: 8, EndColumn: 1}},
			}}},
			Suites: []checkpoint.SuiteCoverage{{
				Package: "example.test/project", DurationNS: 15_000_000, WholeTree: true,
				Covered: checkpoint.Coverage{Files: []checkpoint.FileCoverage{{
					Path: "value.go", Blocks: []checkpoint.CoverageBlock{{StartLine: 3, StartColumn: 2, EndLine: 4, EndColumn: 1}},
				}}},
				Instrumented: checkpoint.Coverage{Files: []checkpoint.FileCoverage{{
					Path: "value.go", Blocks: []checkpoint.CoverageBlock{{StartLine: 1, StartColumn: 1, EndLine: 8, EndColumn: 1}},
				}}},
			}},
		}},
		Race: &checkpoint.Race{Complete: true, Packages: []string{"example.test/project"}, Evidence: []report.Evidence{{Kind: "race", ID: "example.test/project", Status: "passed"}}},
		Mutation: &checkpoint.Mutation{CatalogFingerprint: checkpointTestDigest("b"), Probe: &checkpoint.MutationProbe{
			IndexFingerprint: checkpointTestDigest("d"),
			Targets: []checkpoint.TargetProbe{
				{ID: "target-a", Measured: true, DurationNS: 9_000_000, Infected: []uint32{1, 4}},
				{ID: "target-fuzz", Measured: false},
			},
			Suites: []checkpoint.SuiteProbe{{Package: "example.test/project", Measured: true, DurationNS: 15_000_000, Infected: []uint32{1}, WholeTree: true}},
		}, Results: []checkpoint.MutationResult{{
			ID: "mutant-a", Evidence: []report.Evidence{{Kind: "mutation", ID: "mutant-a", Status: "killed"}}, Findings: []report.Finding{{
				ID: "finding-mutant", Kind: "surviving-mutant", Path: "value.go", Line: 8, Summary: "mutation finding", Replay: "goatest replay finding-mutant", Mutant: "comparison", MutantID: "mutant-a",
			}},
			Provenance: "snapshot=" + checkpointTestDigest("c"),
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

	if got := decoded.Mutation.Results[0].Provenance; got != input.Mutation.Results[0].Provenance {
		t.Fatalf("resumed provenance = %q, want %q", got, input.Mutation.Results[0].Provenance)
	}
	if target := decoded.Baseline.Targets[0].Target; target == nil || !target.WholeTree {
		t.Fatalf("resumed repository boundary = %+v, want a measured whole-tree target", target)
	}
	if got := decoded.Baseline.Targets[0].Target.Target.Capabilities; !slices.Equal(got, []string{"postgres", "redis"}) {
		t.Fatalf("resumed target capabilities = %v", got)
	}
	if coverage := decoded.Baseline.Targets[0].Target.Coverage; coverage == nil || len(coverage.Files) != 1 || len(coverage.Files[0].Blocks) != 1 {
		t.Fatalf("resumed coverage = %+v, want one exact covered block", coverage)
	}
	if routing := decoded.Baseline.Routing; routing == nil || len(routing.Instrumented.Files) != 1 || len(routing.Suites) != 1 || !routing.Suites[0].WholeTree {
		t.Fatalf("resumed baseline routing = %+v, want exact instrumentation and suite", routing)
	}
	if probe := decoded.Mutation.Probe; probe == nil || probe.IndexFingerprint != checkpointTestDigest("d") || len(probe.Targets) != 2 || len(probe.Suites) != 1 || !probe.Suites[0].WholeTree {
		t.Fatalf("resumed mutation probe = %+v, want exact target and suite facts", probe)
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
	delete(baseline, "unknown")
	baseline["complete"] = false
	if err := compiled.Validate(invalid); err == nil {
		t.Fatal("checkpoint schema accepted routing before baseline completion")
	}
	baseline["complete"] = true
	delete(baseline, "routing")
	if err := compiled.Validate(invalid); err == nil {
		t.Fatal("checkpoint schema accepted completion without routing")
	}
}

func TestCheckpointRejectsTargetWithoutExactCoverage(t *testing.T) {
	var document map[string]any
	if err := json.Unmarshal(checkpoint.JSON(checkpointFixture()), &document); err != nil {
		t.Fatal(err)
	}
	targets := document["baseline"].(map[string]any)["targets"].([]any)
	target := targets[0].(map[string]any)["target"].(map[string]any)
	delete(target, "coverage")
	data, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := checkpoint.Decode(data); err == nil {
		t.Fatal("checkpoint without exact target coverage was accepted")
	}
}

func TestCheckpointRejectsSingularTargetCapability(t *testing.T) {
	var document map[string]any
	if err := json.Unmarshal(checkpoint.JSON(checkpointFixture()), &document); err != nil {
		t.Fatal(err)
	}
	targets := document["baseline"].(map[string]any)["targets"].([]any)
	target := targets[0].(map[string]any)["target"].(map[string]any)["target"].(map[string]any)
	target["capability"] = "db"
	data, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := checkpoint.Decode(data); err == nil {
		t.Fatal("checkpoint with a singular target capability was accepted")
	}
}

func TestCheckpointRoundTripsPartialPackageSuiteControls(t *testing.T) {
	state := checkpointFixture()
	state.Baseline.Complete = false
	state.Baseline.Routing = nil
	state.Baseline.Suites = []checkpoint.BaselineSuite{
		{Package: "example.test/project", Measured: true, DurationNS: 17,
			Covered:      &checkpoint.Coverage{Files: []checkpoint.FileCoverage{}},
			Instrumented: &checkpoint.Coverage{Files: []checkpoint.FileCoverage{}}},
		{Package: "example.test/unmeasured"},
	}
	decoded, err := checkpoint.Decode(checkpoint.JSON(state))
	if err != nil || len(decoded.Baseline.Suites) != 2 || !decoded.Baseline.Suites[0].Measured || decoded.Baseline.Suites[1].Measured {
		t.Fatalf("partial package suites = (%+v, %v)", decoded.Baseline.Suites, err)
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
		{name: "unknown", data: []byte(`{"schema":"assurance-checkpoint-v1","input_digest":"` + checkpointTestDigest("a") + `","attempts":1,"baseline":{"build_vet_complete":false,"complete":false,"evidence":[],"findings":[],"targets":[],"unknown":true}}`)},
		{name: "pending mutant", data: checkpoint.JSON(func() checkpoint.State {
			state := checkpointFixture()
			state.Mutation.Results[0].Evidence = nil
			state.Mutation.Results[0].Findings = nil
			return state
		}())},
		{name: "backwards coverage block", data: checkpoint.JSON(func() checkpoint.State {
			state := checkpointFixture()
			block := &state.Baseline.Targets[0].Target.Coverage.Files[0].Blocks[0]
			block.StartLine = block.EndLine + 1
			return state
		}())},
		{name: "backwards routing instrumentation block", data: checkpoint.JSON(func() checkpoint.State {
			state := checkpointFixture()
			block := &state.Baseline.Routing.Instrumented.Files[0].Blocks[0]
			block.StartLine = block.EndLine + 1
			return state
		}())},
		{name: "unmeasured partial suite has facts", data: checkpoint.JSON(func() checkpoint.State {
			state := checkpointFixture()
			state.Baseline.Complete = false
			state.Baseline.Routing = nil
			state.Baseline.Suites = []checkpoint.BaselineSuite{{
				Package: "example.test/project", Covered: &checkpoint.Coverage{Files: []checkpoint.FileCoverage{}},
			}}
			return state
		}())},
		{name: "completed baseline retains partial suite", data: checkpoint.JSON(func() checkpoint.State {
			state := checkpointFixture()
			state.Baseline.Suites = []checkpoint.BaselineSuite{{Package: "example.test/project"}}
			return state
		}())},
		{name: "routing before baseline completion", data: checkpoint.JSON(func() checkpoint.State {
			state := checkpointFixture()
			state.Baseline.Complete = false
			state.Baseline.Routing = &checkpoint.BaselineRouting{}
			return state
		}())},
		{name: "completion without routing", data: checkpoint.JSON(func() checkpoint.State {
			state := checkpointFixture()
			state.Baseline.Routing = nil
			return state
		}())},
		{name: "completed target duplicates routing instrumentation", data: checkpoint.JSON(func() checkpoint.State {
			state := checkpointFixture()
			state.Baseline.Targets[0].Target.Instrumented = &checkpoint.Coverage{Files: []checkpoint.FileCoverage{}}
			return state
		}())},
		{name: "duplicate probe target", data: checkpoint.JSON(func() checkpoint.State {
			state := checkpointFixture()
			state.Mutation.Probe.Targets = append(state.Mutation.Probe.Targets, state.Mutation.Probe.Targets[0])
			return state
		}())},
		{name: "unmeasured probe has facts", data: checkpoint.JSON(func() checkpoint.State {
			state := checkpointFixture()
			state.Mutation.Probe.Targets[1].DurationNS = 1
			return state
		}())},
		{name: "unordered probe infections", data: func() []byte {
			state := checkpointFixture()
			state.Mutation.Probe.Targets[0].Infected = []uint32{4, 1}
			data, _ := json.Marshal(state)
			return data
		}()},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := checkpoint.Decode(test.data); err == nil {
				t.Fatal("invalid checkpoint was accepted")
			}
		})
	}
}
