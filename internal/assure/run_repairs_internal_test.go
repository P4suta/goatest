// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package assure

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/P4suta/goatest/internal/config"
	"github.com/P4suta/goatest/internal/evidence"
	goanalysis "github.com/P4suta/goatest/internal/golang"
	"github.com/P4suta/goatest/internal/mutationbridge"
	"github.com/P4suta/goatest/internal/report"
)

func TestRunCoordinatorClosesRoundOnPrepareMutationGenerationAndCloseFailures(t *testing.T) {
	cause := errors.New("round failed")
	for _, test := range []struct {
		name   string
		change func(*runCoordinatorHarness)
	}{
		{name: "prepare", change: func(h *runCoordinatorHarness) {
			h.dependencies.prepareSession = func(context.Context, *mutationbridge.Workspace, mutationbridge.PrepareOptions) (MutationSession, error) {
				return nil, cause
			}
		}},
		{name: "mutation", change: func(h *runCoordinatorHarness) {
			h.dependencies.evaluateMutations = func(context.Context, MutationSession, []TargetEvidence, MutationOptions) (MutationEvaluation, error) {
				return MutationEvaluation{}, cause
			}
		}},
		{name: "generation", change: func(h *runCoordinatorHarness) {
			h.dependencies.attemptRepairs = func(context.Context, string, []report.Finding, GenerationOptions) (GenerationEvaluation, error) {
				return GenerationEvaluation{}, cause
			}
		}},
		{name: "manager close", change: func(h *runCoordinatorHarness) { h.manager.err = cause }},
		{name: "workspace close", change: func(h *runCoordinatorHarness) {
			h.dependencies.closeWorkspace = func(*mutationbridge.Workspace) error { h.workspaceCloses++; return cause }
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			harness := newRunCoordinatorHarness(t)
			test.change(harness)
			result, err := harness.run(Options{})
			if !errors.Is(err, cause) || !reflect.DeepEqual(result, report.Report{}) || harness.manager.calls != 1 || harness.workspaceCloses != 1 ||
				harness.buildGraphCalls != 1 || harness.mergeGraphCalls != 1 || harness.saveGraphCalls != 1 {
				t.Fatalf("run = (%+v, %v), harness=%+v", result, err, harness)
			}
		})
	}
}

func TestRunCoordinatorReopensFreshSnapshotAfterMutationCorpusRepair(t *testing.T) {
	harness := newRunCoordinatorHarness(t)
	firstRepair := report.Repair{ID: "corpus-a", Finding: "finding-a", Path: "testdata/fuzz/FuzzValue/seed", Status: "applied"}
	secondRepair := report.Repair{ID: "observed-b", Finding: "finding-b", Path: "candidate_test.go", Status: "rejected"}
	harness.dependencies.evaluateMutations = func(_ context.Context, _ MutationSession, _ []TargetEvidence, options MutationOptions) (MutationEvaluation, error) {
		harness.mutationCalls++
		harness.mutationOptions = options
		if harness.mutationCalls == 1 {
			return MutationEvaluation{Applied: true, Repairs: []report.Repair{firstRepair}}, nil
		}
		return MutationEvaluation{Repairs: []report.Repair{secondRepair}}, nil
	}
	result, err := harness.run(Options{})
	if err != nil || result.Verdict != report.VerdictAssured || !reflect.DeepEqual(result.Repairs, []report.Repair{firstRepair, secondRepair}) ||
		harness.openCalls != 2 || harness.workspaceCloses != 2 || harness.manager.calls != 2 || harness.inputCalls != 3 || harness.generationCalls != 1 || len(harness.cache.puts) != 1 {
		t.Fatalf("run = (%+v, %v), harness=%+v", result, err, harness)
	}
	var repairs []Event
	for _, event := range harness.events {
		if event.Kind == "repair-applied" {
			repairs = append(repairs, event)
		}
	}
	if !reflect.DeepEqual(repairs, []Event{{Kind: "repair-applied", Detail: "1 files"}}) {
		t.Fatalf("repair events = %+v", repairs)
	}
}

func TestRunCoordinatorReopensFreshSnapshotAfterGeneratedRepair(t *testing.T) {
	harness := newRunCoordinatorHarness(t)
	finding := report.Finding{ID: "finding-a", Kind: "surviving-mutant", Summary: "gap"}
	repair := report.Repair{ID: "generated-a", Finding: finding.ID, Path: "value_test.go", Status: "applied"}
	harness.dependencies.evaluateMutations = func(context.Context, MutationSession, []TargetEvidence, MutationOptions) (MutationEvaluation, error) {
		harness.mutationCalls++
		if harness.mutationCalls == 1 {
			return MutationEvaluation{Findings: []report.Finding{finding}}, nil
		}
		return MutationEvaluation{}, nil
	}
	harness.dependencies.attemptRepairs = func(_ context.Context, _ string, findings []report.Finding, _ GenerationOptions) (GenerationEvaluation, error) {
		harness.generationCalls++
		if harness.generationCalls == 1 {
			if !reflect.DeepEqual(findings, []report.Finding{finding}) {
				t.Fatalf("generation findings = %+v", findings)
			}
			return GenerationEvaluation{Findings: slices.Clone(findings), Repairs: []report.Repair{repair}, Applied: true}, nil
		}
		return GenerationEvaluation{}, nil
	}
	result, err := harness.run(Options{})
	if err != nil || result.Verdict != report.VerdictAssured || !reflect.DeepEqual(result.Repairs, []report.Repair{repair}) ||
		harness.openCalls != 2 || harness.generationCalls != 2 || harness.workspaceCloses != 2 {
		t.Fatalf("run = (%+v, %v), harness=%+v", result, err, harness)
	}
}

func TestRunCoordinatorStopsAtThreeRepairRoundsWithExplicitFinding(t *testing.T) {
	harness := newRunCoordinatorHarness(t)
	harness.dependencies.evaluateMutations = func(context.Context, MutationSession, []TargetEvidence, MutationOptions) (MutationEvaluation, error) {
		harness.mutationCalls++
		return MutationEvaluation{Applied: true, Repairs: []report.Repair{{ID: "repair-" + string(rune('0'+harness.mutationCalls)), Status: "applied"}}}, nil
	}
	result, err := harness.run(Options{})
	if err != nil || result.Verdict != report.VerdictInsufficient || len(result.Repairs) != maximumRounds || len(result.Findings) != 1 ||
		result.Findings[0].Kind != "repair-round-limit" || harness.openCalls != maximumRounds || harness.workspaceCloses != maximumRounds || harness.manager.calls != maximumRounds ||
		harness.generationCalls != 0 || len(harness.cache.puts) != 0 || harness.buildGraphCalls != maximumRounds ||
		harness.mergeGraphCalls != maximumRounds || harness.saveGraphCalls != maximumRounds {
		t.Fatalf("run = (%+v, %v), harness=%+v", result, err, harness)
	}
}

func TestRunCoordinatorRejectsFinalInputErrorsAndRepositoryDrift(t *testing.T) {
	cause := errors.New("final scan failed")
	t.Run("scan error", func(t *testing.T) {
		harness := newRunCoordinatorHarness(t)
		harness.dependencies.assuranceInputs = func(string, string, Options, config.Config, roundMetadata) (evidence.Inputs, string, error) {
			harness.inputCalls++
			if harness.inputCalls == 2 {
				return evidence.Inputs{}, "", cause
			}
			return harness.inputs, harness.digest, nil
		}
		result, err := harness.run(Options{})
		if !errors.Is(err, cause) || !reflect.DeepEqual(result, report.Report{}) || harness.workspaceCloses != 1 || harness.inputCalls != 2 ||
			harness.buildGraphCalls != 1 || harness.mergeGraphCalls != 1 || harness.saveGraphCalls != 1 {
			t.Fatalf("run = (%+v, %v), harness=%+v", result, err, harness)
		}
	})
	t.Run("drift", func(t *testing.T) {
		harness := newRunCoordinatorHarness(t)
		harness.dependencies.assuranceInputs = func(string, string, Options, config.Config, roundMetadata) (evidence.Inputs, string, error) {
			harness.inputCalls++
			if harness.inputCalls == 2 {
				return evidence.Inputs{Contract: "digest-b"}, "digest-b", nil
			}
			return harness.inputs, harness.digest, nil
		}
		result, err := harness.run(Options{})
		if err == nil || !strings.Contains(err.Error(), "repository changed") || !reflect.DeepEqual(result, report.Report{}) ||
			harness.buildGraphCalls != 1 || harness.mergeGraphCalls != 1 || harness.saveGraphCalls != 1 || len(harness.cache.puts) != 0 {
			t.Fatalf("run = (%+v, %v), harness=%+v", result, err, harness)
		}
	})
}

func TestRunCoordinatorReportsResidualRiskAfterCheckpointingImpactGraph(t *testing.T) {
	harness := newRunCoordinatorHarness(t)
	finding := report.Finding{ID: "finding-a", Kind: "surviving-mutant", Summary: "gap"}
	harness.mutation.Findings = []report.Finding{finding}
	harness.generation.Findings = []report.Finding{finding}
	result, err := harness.run(Options{})
	if err != nil || result.Verdict != report.VerdictInsufficient || !reflect.DeepEqual(result.Findings, []report.Finding{finding}) ||
		!reflect.DeepEqual(result.ResidualRisks, []string{"unresolved mutation evidence gaps remain"}) || harness.buildGraphCalls != 1 ||
		harness.mergeGraphCalls != 1 || harness.saveGraphCalls != 1 || len(harness.cache.puts) != 1 {
		t.Fatalf("run = (%+v, %v), harness=%+v", result, err, harness)
	}
}

func TestRunCoordinatorPropagatesGraphAndFinalCacheFailures(t *testing.T) {
	cause := errors.New("terminal failed")
	for _, test := range []struct {
		name   string
		change func(*runCoordinatorHarness)
	}{
		{name: "build graph", change: func(h *runCoordinatorHarness) {
			h.dependencies.buildGraph = func(string, goanalysis.Model, []TargetEvidence) (evidence.Graph, error) {
				return evidence.Graph{}, cause
			}
		}},
		{name: "save graph", change: func(h *runCoordinatorHarness) {
			h.dependencies.saveGraph = func(string, evidence.GraphRecord) error { return cause }
		}},
		{name: "cache", change: func(h *runCoordinatorHarness) { h.cache.putErr = cause }},
	} {
		t.Run(test.name, func(t *testing.T) {
			harness := newRunCoordinatorHarness(t)
			test.change(harness)
			result, err := harness.run(Options{})
			if !errors.Is(err, cause) || !reflect.DeepEqual(result, report.Report{}) {
				t.Fatalf("run = (%+v, %v)", result, err)
			}
			switch test.name {
			case "build graph":
				if harness.mergeGraphCalls != 0 || harness.saveGraphCalls != 0 || len(harness.cache.puts) != 0 {
					t.Fatalf("work after build failure: %+v", harness)
				}
			case "save graph":
				if harness.mergeGraphCalls != 1 || len(harness.cache.puts) != 0 {
					t.Fatalf("work after save failure: %+v", harness)
				}
			case "cache":
				if harness.saveGraphCalls != 1 || len(harness.cache.puts) != 1 {
					t.Fatalf("cache failure state: %+v", harness)
				}
			}
		})
	}
}
