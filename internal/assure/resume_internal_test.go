// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package assure

import (
	"context"
	"errors"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	gomutants "github.com/P4suta/go-mutants"
	"github.com/P4suta/goatest/internal/checkpoint"
	goanalysis "github.com/P4suta/goatest/internal/golang"
	"github.com/P4suta/goatest/internal/repair"
	"github.com/P4suta/goatest/internal/report"
)

func TestBaselineCancellationCheckpointsClassifiedTargetAndResumeSkipsIt(t *testing.T) {
	model := baselineModel()
	targets := []BaselineTarget{{Target: baselineTestTarget("TestA")}, {Target: baselineTestTarget("TestB")}}
	var saved checkpoint.Baseline
	first := &baselineFakeWorkspace{}
	first.exec = func(command gomutants.Command) (gomutants.CommandResult, error) {
		joined := strings.Join(command.Argv, " ")
		if strings.Contains(joined, "-test.run=^TestB$") {
			return gomutants.CommandResult{}, context.Canceled
		}
		if len(command.Argv) > 0 && command.Argv[0] != "go" {
			profile := coverageProfileArgument(command)
			if err := os.WriteFile(profile, []byte("mode: set\nfixture.example/module/value.go:1.1,2.1 1 1\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		return gomutants.CommandResult{Duration: 25 * time.Millisecond}, nil
	}
	_, err := CollectBaseline(t.Context(), first, model, targets, BaselineOptions{
		ArtifactDirectory: t.TempDir(), Checkpoint: func(state checkpoint.Baseline) { saved = state },
	})
	if !errors.Is(err, context.Canceled) || !saved.BuildVetComplete || saved.Complete || len(saved.Targets) != 1 || saved.Targets[0].ID != "target-TestA" {
		t.Fatalf("interrupted baseline = error %v checkpoint %+v", err, saved)
	}

	second := &baselineFakeWorkspace{exec: passingBaselineExec(t, model.ModulePath, true)}
	var completed checkpoint.Baseline
	result, err := CollectBaseline(t.Context(), second, model, targets, BaselineOptions{
		ArtifactDirectory: t.TempDir(), Resume: &saved, Checkpoint: func(state checkpoint.Baseline) { completed = state },
	})
	if err != nil || result.Executed != 2 || len(result.Targets) != 2 || len(result.Inventory) != 2 || !completed.Complete {
		t.Fatalf("resumed baseline = (%+v, %v), checkpoint %+v", result, err, completed)
	}
	if len(second.commands) != 2 {
		t.Fatalf("resumed commands = %d, want compile and pending target", len(second.commands))
	}
	for _, command := range second.commands {
		if strings.Contains(strings.Join(command.Argv, " "), "TestA") {
			t.Fatalf("completed target executed again: %+v", command.Argv)
		}
	}
}

type resumeMutationSession struct {
	catalog  gomutants.Catalog
	calls    []string
	requests []gomutants.ExecRequest
	fail     map[string]error
	// survive names the mutants no execution kills, which is the evidence a
	// dying run is most expensive to lose.
	survive map[string]bool
}

func (session *resumeMutationSession) Catalog() gomutants.Catalog { return session.catalog }

// Probe answers with no facts: these tests resume mutation work, and a resumed
// target is one the probe pass never measured.
func (session *resumeMutationSession) Probe(context.Context, gomutants.ProbeRequest) (gomutants.ProbeResult, error) {
	return gomutants.ProbeResult{Outcome: gomutants.ProbeUnavailable}, nil
}

func (session *resumeMutationSession) Exec(_ context.Context, request gomutants.ExecRequest) (gomutants.MutantResult, error) {
	session.calls = append(session.calls, request.Mutant)
	session.requests = append(session.requests, request)
	if err := session.fail[request.Mutant]; err != nil {
		return gomutants.MutantResult{}, err
	}
	if session.survive[request.Mutant] {
		return gomutants.MutantResult{Outcome: gomutants.OutcomeSurvived}, nil
	}
	return gomutants.MutantResult{Outcome: gomutants.OutcomeKilled}, nil
}

// findingKinds names the findings of one evaluation in order.
func findingKinds(evaluation MutationEvaluation) []string {
	kinds := make([]string, 0, len(evaluation.Findings))
	for _, finding := range evaluation.Findings {
		kinds = append(kinds, finding.Kind)
	}
	return kinds
}

// survivingMutant is a mutant every target of reachedMutationTargets reaches,
// since the catalog reports no column for it and routing then keeps every
// target that ran the file.
func survivingMutant(id string, line int) gomutants.Mutant {
	return gomutants.Mutant{
		ID: id, DisplayID: id, Path: "value.go", Package: "fixture.example/module", Line: line, Accepted: true,
	}
}

// TestMutationSurvivorIsCheckpointedBeforeALaterMutantFails pins that a mutant
// every reaching test passed is durable the moment those tests pass. A
// checkpoint written only once every seed has finished is a checkpoint a dying
// run never writes, and survivors are the mutants a resumed run pays the most
// to execute a second time.
func TestMutationSurvivorIsCheckpointedBeforeALaterMutantFails(t *testing.T) {
	catalog := gomutants.Catalog{Mutants: []gomutants.Mutant{survivingMutant("mutant-a", 4), survivingMutant("mutant-b", 8)}}
	session := &resumeMutationSession{
		catalog: catalog,
		survive: map[string]bool{"mutant-a": true},
		fail:    map[string]error{"mutant-b": context.Canceled},
	}
	var saved, executedWhenSaved []string
	checkpointed := make(map[string]MutationEvaluation)
	_, err := EvaluateMutations(t.Context(), session, reachedMutationTargets()[:9], MutationOptions{
		Root: t.TempDir(), Contract: "standard-v1", Jobs: 1,
		Checkpoint: func(id string, unit MutationEvaluation) {
			saved = append(saved, id)
			checkpointed[id] = unit
			if id == "mutant-a" {
				executedWhenSaved = slices.Clone(session.calls)
			}
		},
	})
	if !errors.Is(err, context.Canceled) || !slices.Equal(saved, []string{"mutant-a"}) {
		t.Fatalf("interrupted mutation = %v, saved=%v", err, saved)
	}
	if kinds := findingKinds(checkpointed["mutant-a"]); !slices.Equal(kinds, []string{"surviving-mutant"}) {
		t.Fatalf("checkpointed survivor = %+v, want one surviving-mutant finding", checkpointed["mutant-a"])
	}
	if slices.Contains(executedWhenSaved, "mutant-b") {
		t.Fatalf("survivor checkpointed only after a later mutant ran: executions %v", executedWhenSaved)
	}
}

// TestMutationSurvivorIsCheckpointedOnceWithItsFinalEvaluation pins that
// finalising a survivor early neither saves it twice nor lets the serial pass
// find the same mutation a second time.
func TestMutationSurvivorIsCheckpointedOnceWithItsFinalEvaluation(t *testing.T) {
	session := &resumeMutationSession{
		catalog: gomutants.Catalog{Mutants: []gomutants.Mutant{survivingMutant("mutant-a", 4)}},
		survive: map[string]bool{"mutant-a": true},
	}
	var saved []string
	checkpointed := make(map[string]MutationEvaluation)
	evaluation, err := EvaluateMutations(t.Context(), session, reachedMutationTargets()[:9], MutationOptions{
		Root: t.TempDir(), Contract: "standard-v1", Jobs: 1,
		Checkpoint: func(id string, unit MutationEvaluation) {
			saved = append(saved, id)
			checkpointed[id] = unit
		},
	})
	if err != nil || !slices.Equal(saved, []string{"mutant-a"}) || evaluation.Accounting.Survived != 1 {
		t.Fatalf("surviving mutation = (%+v, %v), saved=%v", evaluation, err, saved)
	}
	unit := checkpointed["mutant-a"]
	if kinds := findingKinds(unit); !slices.Equal(kinds, []string{"surviving-mutant"}) {
		t.Fatalf("checkpointed survivor = %+v, want one surviving-mutant finding", unit)
	}
	// What a resumed run reads back is exactly what this run reported.
	if !reflect.DeepEqual(unit.Findings, evaluation.Findings) || !reflect.DeepEqual(unit.Evidence, evaluation.Evidence) {
		t.Fatalf("checkpointed evaluation = %+v, want the reported %+v", unit, evaluation)
	}
}

// TestMutationSurvivorReachedByAFuzzTargetIsCheckpointedAfterFuzzing pins the
// one survivor that is not finished when its tests pass: fuzzing may still
// kill it, so it is finalised only once the fuzzing has run.
func TestMutationSurvivorReachedByAFuzzTargetIsCheckpointedAfterFuzzing(t *testing.T) {
	session := &resumeMutationSession{
		catalog: gomutants.Catalog{Mutants: []gomutants.Mutant{survivingMutant("mutant-a", 4)}},
		survive: map[string]bool{"mutant-a": true},
	}
	var saved []string
	checkpointed := make(map[string]MutationEvaluation)
	evaluation, err := EvaluateMutations(t.Context(), session, reachedMutationTargets(), MutationOptions{
		Root: t.TempDir(), Contract: "standard-v1", Jobs: 1,
		Checkpoint: func(id string, unit MutationEvaluation) {
			saved = append(saved, id)
			checkpointed[id] = unit
		},
	})
	if err != nil || !slices.Equal(saved, []string{"mutant-a"}) || evaluation.Accounting.Survived != 1 {
		t.Fatalf("fuzz-reached surviving mutation = (%+v, %v), saved=%v", evaluation, err, saved)
	}
	if kinds := findingKinds(checkpointed["mutant-a"]); !slices.Equal(kinds, []string{"surviving-mutant"}) {
		t.Fatalf("checkpointed survivor = %+v, want one surviving-mutant finding", checkpointed["mutant-a"])
	}
	fuzzed := 0
	for index, request := range session.requests {
		if !slices.ContainsFunc(request.Args, func(arg string) bool { return strings.HasPrefix(arg, "-test.fuzz=") }) {
			continue
		}
		fuzzed++
		if index != len(session.requests)-1 {
			t.Fatalf("fuzz request %d of %d ran before the unit executions finished", index+1, len(session.requests))
		}
	}
	if fuzzed != 1 {
		t.Fatalf("fuzz requests = %d, want the one that follows the unit executions: %+v", fuzzed, session.requests)
	}
}

func TestMutationResumeReusesOnlyTerminalCatalogMatches(t *testing.T) {
	catalog := gomutants.Catalog{Mutants: []gomutants.Mutant{
		{ID: "mutant-a", DisplayID: "a", Path: "value.go", Package: "example.test/project", Rule: "lt-to-le", Line: 4, Accepted: true},
		{ID: "mutant-b", DisplayID: "b", Path: "value.go", Package: "example.test/project", Rule: "lt-to-le", Line: 8, Accepted: true},
	}}
	session := &resumeMutationSession{catalog: catalog}
	resumed := MutationEvaluation{Evidence: []report.Evidence{{Kind: "mutation", ID: "mutant-a", Status: "killed", Detail: "TestA"}}}
	var saved []string
	evaluation, err := EvaluateMutations(t.Context(), session, nil, MutationOptions{
		Root: t.TempDir(), Contract: "standard-v1", Jobs: 1,
		Resume:     map[string]MutationEvaluation{"mutant-a": resumed},
		Checkpoint: func(id string, _ MutationEvaluation) { saved = append(saved, id) },
	})
	if err != nil || !slices.Equal(session.calls, []string{"mutant-b"}) || !slices.Equal(saved, []string{"mutant-b"}) || evaluation.Accounting.Killed != 2 || evaluation.Accounting.Unknown != 0 {
		t.Fatalf("resumed mutation = (%+v, %v), calls=%v saved=%v", evaluation, err, session.calls, saved)
	}
	firstFingerprint := MutationCatalogFingerprint(catalog)
	catalog.Mutants[1].Line++
	if second := MutationCatalogFingerprint(catalog); firstFingerprint == second {
		t.Fatal("catalog fingerprint ignored source line")
	}
}

func TestMutationCancellationKeepsEarlierTerminalUnitPendingNeverBecomesUnknown(t *testing.T) {
	catalog := gomutants.Catalog{Mutants: []gomutants.Mutant{
		{ID: "mutant-a", DisplayID: "a", Path: "value.go", Package: "example.test/project", Accepted: true},
		{ID: "mutant-b", DisplayID: "b", Path: "value.go", Package: "example.test/project", Accepted: true},
	}}
	session := &resumeMutationSession{catalog: catalog, fail: map[string]error{"mutant-b": context.Canceled}}
	var saved []string
	evaluation, err := EvaluateMutations(t.Context(), session, nil, MutationOptions{
		Root: t.TempDir(), Jobs: 1, Checkpoint: func(id string, _ MutationEvaluation) { saved = append(saved, id) },
	})
	if !errors.Is(err, context.Canceled) || len(evaluation.Mutants) != 0 || !slices.Equal(saved, []string{"mutant-a"}) {
		t.Fatalf("cancelled mutation = (%+v, %v), saved=%v", evaluation, err, saved)
	}
}

func TestCheckpointTargetConversionPreservesRoutingIdentity(t *testing.T) {
	input := TargetEvidence{Target: goanalysis.Target{
		ID: "target", Name: "TestValue", Kind: goanalysis.KindTest, Package: "example.test/project", RelativeDir: ".", Path: "value_test.go", Line: 7,
		Capabilities: []string{"db"}, Dependencies: []string{"example.test/dependency"},
	}, CoveredFiles: []string{"value.go"}, Environment: []string{"DB=ready"}, Duration: 17 * time.Millisecond,
		WholeTree: true, RepositoryObserved: true, Probed: true, Infected: []uint32{1, 4},
		Covered: []goanalysis.FileCoverage{{Path: "value.go", Blocks: []goanalysis.CoverageBlock{
			{StartLine: 1, StartColumn: 1, EndLine: 2, EndColumn: 1},
		}}}}
	restored := restoreTargetEvidence(*checkpointTargetEvidence(input))
	if restored.Target.ID != input.Target.ID || restored.Target.Kind != input.Target.Kind || !slices.Equal(restored.CoveredFiles, input.CoveredFiles) || !slices.Equal(restored.Environment, input.Environment) || restored.Duration != input.Duration || restored.WholeTree != input.WholeTree || restored.RepositoryObserved != input.RepositoryObserved {
		t.Fatalf("restored target = %+v, want %+v", restored, input)
	}
	// Blocks are far too large to rewrite on every checkpoint, so a checkpoint
	// carries none of them and a restored target says so with a nil Covered.
	if restored.Covered != nil {
		t.Fatalf("restored blocks = %+v, want none", restored.Covered)
	}
	// Infection facts belong to the probe pass of one run and are never
	// checkpointed, so a restored target says it was never probed and is
	// treated as infecting every mutant it reaches.
	if restored.Probed || restored.Infected != nil {
		t.Fatalf("restored infection facts = %+v, want none", restored)
	}
}

func TestCollectBaselineKeepsBlocksForFreshTargetsAndNoneForResumedOnes(t *testing.T) {
	model := baselineModel()
	resumedTarget := baselineTestTarget("TestResumed")
	freshTarget := baselineTestTarget("TestFresh")
	resumed := TargetEvidence{
		Target: resumedTarget, CoveredFiles: []string{"value.go"}, Duration: 11 * time.Millisecond,
		Covered: []goanalysis.FileCoverage{{Path: "value.go", Blocks: []goanalysis.CoverageBlock{
			{StartLine: 5, StartColumn: 29, EndLine: 6, EndColumn: 16},
		}}},
	}
	resume := &checkpoint.Baseline{BuildVetComplete: true, Targets: []checkpoint.BaselineTarget{{
		ID: resumedTarget.ID, Executed: true, Target: checkpointTargetEvidence(resumed),
		Inventory: report.TargetDisposition{
			ID: resumedTarget.ID, Name: resumedTarget.Name, Kind: "test", Package: resumedTarget.Package, Status: "passed",
		},
	}}}
	workspace := &baselineFakeWorkspace{exec: func(command gomutants.Command) (gomutants.CommandResult, error) {
		if len(command.Argv) > 0 && command.Argv[0] != "go" {
			contents := "mode: set\n" +
				"fixture.example/module/value.go:5.29,6.16 1 1\n" +
				"fixture.example/module/value.go:7.3,8.4 1 0\n"
			if err := os.WriteFile(coverageProfileArgument(command), []byte(contents), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		return gomutants.CommandResult{Duration: 13 * time.Millisecond}, nil
	}}
	result, err := CollectBaseline(t.Context(), workspace, model, []BaselineTarget{
		{Target: resumedTarget}, {Target: freshTarget},
	}, BaselineOptions{ArtifactDirectory: t.TempDir(), Resume: resume})
	if err != nil || len(result.Targets) != 2 {
		t.Fatalf("CollectBaseline = (%+v, %v)", result, err)
	}
	for _, target := range result.Targets {
		switch target.Target.Name {
		case resumedTarget.Name:
			if target.Covered != nil || !slices.Equal(target.CoveredFiles, []string{"value.go"}) {
				t.Errorf("resumed target = %+v, want file evidence without blocks", target)
			}
		case freshTarget.Name:
			want := []goanalysis.FileCoverage{{Path: "value.go", Blocks: []goanalysis.CoverageBlock{
				{StartLine: 5, StartColumn: 29, EndLine: 6, EndColumn: 16},
			}}}
			if !reflect.DeepEqual(target.Covered, want) {
				t.Errorf("fresh target blocks = %+v, want %+v", target.Covered, want)
			}
		default:
			t.Errorf("unexpected target %+v", target)
		}
	}
	wantInstrumented := []goanalysis.FileCoverage{{Path: "value.go", Blocks: []goanalysis.CoverageBlock{
		{StartLine: 5, StartColumn: 29, EndLine: 6, EndColumn: 16},
		{StartLine: 7, StartColumn: 3, EndLine: 8, EndColumn: 4},
	}}}
	if !reflect.DeepEqual(result.Instrumented, wantInstrumented) {
		t.Fatalf("instrumented = %+v, want %+v", result.Instrumented, wantInstrumented)
	}
}

func TestCheckpointClaimFailureForcesColdRunAndCatalogMismatchPreservesBaseline(t *testing.T) {
	digest := strings.Repeat("d", 64)
	target := goanalysis.Target{ID: "target", Name: "TestValue", Kind: goanalysis.KindTest, Package: "example.test/project", Path: "value_test.go", Line: 5}
	baseline := checkpoint.Baseline{BuildVetComplete: true, Targets: []checkpoint.BaselineTarget{{
		ID: "target", Executed: true,
		Inventory: report.TargetDisposition{ID: "target", Name: "TestValue", Kind: "test", Package: "example.test/project", Path: "value_test.go", Line: 5, Status: "passed"},
	}}}
	t.Run("claim write failure", func(t *testing.T) {
		cache := &coordinatorCache{
			checkpointFound: true, checkpointPutErr: errors.New("disk full"),
			checkpoint: checkpoint.State{Schema: checkpoint.SchemaV1, InputDigest: digest, Attempts: 1, Baseline: baseline},
		}
		var events []Event
		controller := openRunCheckpoint(cache, digest, Options{Progress: func(event Event) { events = append(events, event) }}, true)
		if resumed := controller.baseline([]goanalysis.Target{target}); resumed != nil || cache.checkpointDeletes != 1 || len(events) != 1 || events[0].Kind != "checkpoint-warning" {
			t.Fatalf("claim failure resumed=%+v cache=%+v events=%+v", resumed, cache, events)
		}
	})
	t.Run("catalog mismatch", func(t *testing.T) {
		oldCatalog := gomutants.Catalog{Mutants: []gomutants.Mutant{{ID: "mutant", Path: "value.go", Package: "example.test/project", Rule: "old", Line: 4, Accepted: true}}}
		cache := &coordinatorCache{checkpointFound: true, checkpoint: checkpoint.State{
			Schema: checkpoint.SchemaV1, InputDigest: digest, Attempts: 1, Baseline: baseline,
			Mutation: &checkpoint.Mutation{
				CatalogFingerprint: MutationCatalogFingerprint(oldCatalog),
				Results: []checkpoint.MutationResult{{
					ID:       "mutant",
					Evidence: []report.Evidence{{Kind: "mutation", ID: "mutant", Status: "killed"}},
				}},
			},
		}}
		controller := openRunCheckpoint(cache, digest, Options{}, true)
		newCatalog := oldCatalog
		newCatalog.Mutants = slices.Clone(oldCatalog.Mutants)
		newCatalog.Mutants[0].Rule = "new"
		if resumed := controller.mutation(newCatalog, t.TempDir()); len(resumed) != 0 || !controller.state.Baseline.BuildVetComplete || len(controller.state.Baseline.Targets) != 1 || controller.state.Mutation.CatalogFingerprint != MutationCatalogFingerprint(newCatalog) {
			t.Fatalf("catalog mismatch resumed=%+v state=%+v", resumed, controller.state)
		}
	})
}

func TestCheckpointRaceAndCandidateValidationDiscardOnlyUnsafePhases(t *testing.T) {
	digest := strings.Repeat("e", 64)
	baseline := checkpoint.Baseline{BuildVetComplete: true}

	t.Run("matching race inventory is reused", func(t *testing.T) {
		cache := &coordinatorCache{checkpointFound: true, checkpoint: checkpoint.State{
			Schema: checkpoint.SchemaV1, InputDigest: digest, Attempts: 2, Baseline: baseline,
			Race: &checkpoint.Race{Complete: true, Packages: []string{"example.test/b", "example.test/a"}},
		}}
		controller := openRunCheckpoint(cache, digest, Options{}, true)
		resumed, ok := controller.race([]string{"example.test/a", "example.test/b"})
		metadata := controller.resumeMetadata()
		if !ok || resumed == nil || metadata.Attempts != 3 || metadata.ReusedRacePackages != 2 {
			t.Fatalf("race resume = (%+v, %t), metadata=%+v", resumed, ok, metadata)
		}
	})

	t.Run("changed race inventory discards race and mutation", func(t *testing.T) {
		cache := &coordinatorCache{checkpointFound: true, checkpoint: checkpoint.State{
			Schema: checkpoint.SchemaV1, InputDigest: digest, Attempts: 1, Baseline: baseline,
			Race:     &checkpoint.Race{Complete: true, Packages: []string{"example.test/old"}},
			Mutation: &checkpoint.Mutation{CatalogFingerprint: strings.Repeat("f", 64)},
		}}
		controller := openRunCheckpoint(cache, digest, Options{}, true)
		if resumed, ok := controller.race([]string{"example.test/new"}); ok || resumed != nil || controller.state.Race != nil || controller.state.Mutation != nil || !controller.state.Baseline.BuildVetComplete {
			t.Fatalf("race mismatch = (%+v, %t), state=%+v", resumed, ok, controller.state)
		}
	})

	t.Run("missing candidate discards mutation but preserves baseline", func(t *testing.T) {
		catalog := gomutants.Catalog{Mutants: []gomutants.Mutant{{ID: "mutant", Path: "value.go", Package: "example.test/project", Rule: "lt-to-le", Line: 4, Accepted: true}}}
		fingerprint := MutationCatalogFingerprint(catalog)
		cache := &coordinatorCache{checkpointFound: true, checkpoint: checkpoint.State{
			Schema: checkpoint.SchemaV1, InputDigest: digest, Attempts: 1, Baseline: baseline,
			Mutation: &checkpoint.Mutation{CatalogFingerprint: fingerprint, Results: []checkpoint.MutationResult{{
				ID: "mutant", Findings: []report.Finding{{ID: "finding", MutantID: "mutant", Kind: "unpersisted-fuzz-kill", Summary: "candidate"}},
				Repairs: []report.Repair{{ID: "missing-candidate", Status: string(repair.StatusCandidate)}},
			}}},
		}}
		controller := openRunCheckpoint(cache, digest, Options{}, true)
		if resumed := controller.mutation(catalog, t.TempDir()); len(resumed) != 0 || !controller.state.Baseline.BuildVetComplete || controller.state.Mutation.CatalogFingerprint != fingerprint || len(controller.state.Mutation.Results) != 0 {
			t.Fatalf("missing artifact resumed=%+v state=%+v", resumed, controller.state)
		}
	})
}
