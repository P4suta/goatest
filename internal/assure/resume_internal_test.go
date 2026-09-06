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
	"github.com/P4suta/goatest/internal/filemode"
	goanalysis "github.com/P4suta/goatest/internal/golang"
	"github.com/P4suta/goatest/internal/report"
)

const (
	baselineResumeCommandCount = 2
	journalUnitCount           = 2
	resumedProbeDuration       = 11 * time.Millisecond
)

type journalCheckpointCache struct {
	coordinatorCache
	baselineUnits      []checkpoint.BaselineTarget
	baselineSuiteUnits []checkpoint.BaselineSuite
	mutationUnits      []checkpoint.MutationResult
	journalErr         error
}

func (cache *journalCheckpointCache) AppendBaselineSuiteCheckpoint(_ string, unit checkpoint.BaselineSuite) error {
	if cache.journalErr != nil {
		return cache.journalErr
	}
	cache.baselineSuiteUnits = append(cache.baselineSuiteUnits, unit)
	return nil
}

func (cache *journalCheckpointCache) AppendBaselineCheckpoint(_ string, unit checkpoint.BaselineTarget) error {
	if cache.journalErr != nil {
		return cache.journalErr
	}
	cache.baselineUnits = append(cache.baselineUnits, unit)
	return nil
}

func (cache *journalCheckpointCache) AppendMutationCheckpoint(_ string, unit checkpoint.MutationResult) error {
	if cache.journalErr != nil {
		return cache.journalErr
	}
	cache.mutationUnits = append(cache.mutationUnits, unit)
	return nil
}

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
			if err := os.WriteFile(profile, []byte("mode: set\nfixture.example/module/value.go:1.1,2.1 1 1\n"), filemode.PrivateFile); err != nil {
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
	if len(second.commands) != baselineResumeCommandCount {
		t.Fatalf("resumed commands = %d, want compile and pending target", len(second.commands))
	}
	for _, command := range second.commands {
		if strings.Contains(strings.Join(command.Argv, " "), "TestA") {
			t.Fatalf("completed target executed again: %+v", command.Argv)
		}
	}
}

func TestCheckpointControllerJournalsUnitsAndCompactsInDeterministicOrder(t *testing.T) {
	t.Parallel()
	digest := digestText("journal-checkpoint")
	store := &journalCheckpointCache{}
	controller := openRunCheckpoint(store, digest, Options{}, true)
	controller.saveBaseline(checkpoint.Baseline{BuildVetComplete: true})
	targetA := checkpoint.BaselineTarget{
		ID: "target-a", Executed: true,
		Inventory: report.TargetDisposition{ID: "target-a", Name: "TestA", Status: "passed"},
	}
	targetB := checkpoint.BaselineTarget{
		ID: "target-b", Executed: true,
		Inventory: report.TargetDisposition{ID: "target-b", Name: "TestB", Status: "passed"},
	}
	controller.saveBaseline(checkpoint.Baseline{BuildVetComplete: true, Targets: []checkpoint.BaselineTarget{targetA}})

	controller.saveBaseline(checkpoint.Baseline{BuildVetComplete: true, Targets: []checkpoint.BaselineTarget{targetB, targetA}})
	suite := checkpoint.BaselineSuite{Package: "example.test/project", Measured: false}
	controller.saveBaseline(checkpoint.Baseline{
		BuildVetComplete: true, Targets: []checkpoint.BaselineTarget{targetA, targetB}, Suites: []checkpoint.BaselineSuite{suite},
	})
	controller.saveBaseline(checkpoint.Baseline{BuildVetComplete: true, Complete: true, Targets: []checkpoint.BaselineTarget{targetA, targetB}})
	if len(store.baselineUnits) != journalUnitCount {
		t.Fatalf("baseline journal units = %+v, want two", store.baselineUnits)
	}
	if got := []string{store.baselineUnits[0].ID, store.baselineUnits[1].ID}; !slices.Equal(got, []string{"target-a", "target-b"}) {
		t.Fatalf("baseline journal = %v", got)
	}
	if !reflect.DeepEqual(store.baselineSuiteUnits, []checkpoint.BaselineSuite{suite}) {
		t.Fatalf("baseline suite journal = %+v, want %+v", store.baselineSuiteUnits, suite)
	}

	catalog := gomutants.Catalog{Mutants: []gomutants.Mutant{
		{ID: "mutant-z", Path: "z.go", Package: "fixture.example/module", Rule: "rule", Accepted: true},
		{ID: "mutant-a", Path: "a.go", Package: "fixture.example/module", Rule: "rule", Accepted: true},
	}}
	controller.mutation(catalog)
	for _, id := range []string{"mutant-z", "mutant-a"} {
		controller.saveMutant(id, MutationEvaluation{Evidence: []report.Evidence{{Kind: "mutation", ID: id, Status: "killed"}}})
	}
	controller.completeMutation()
	if len(store.mutationUnits) != len(catalog.Mutants) {
		t.Fatalf("mutation journal units = %+v, want two", store.mutationUnits)
	}
	if got := []string{store.mutationUnits[0].ID, store.mutationUnits[1].ID}; !slices.Equal(got, []string{"mutant-z", "mutant-a"}) {
		t.Fatalf("mutation journal lost completions = %v", got)
	}
	results := store.checkpoint.Mutation.Results
	if len(results) != len(catalog.Mutants) {
		t.Fatalf("compacted mutation results = %+v, want two", results)
	}
	if got := []string{results[0].ID, results[1].ID}; !slices.Equal(got, []string{"mutant-a", "mutant-z"}) {
		t.Fatalf("compacted mutation order = %v, want deterministic ID order", got)
	}
}

func TestBaselineResumeSkipsTerminalPackageSuite(t *testing.T) {
	target := baselineTestTarget("TestResumed")
	block := []goanalysis.FileCoverage{{Path: "value.go", Blocks: []goanalysis.CoverageBlock{infectionBlock()}}}
	evidence := TargetEvidence{
		Target: target, CoveredFiles: []string{"value.go"}, Covered: block, Instrumented: block,
		Duration: 25 * time.Millisecond,
	}
	unit := baselineClassifiedUnit(
		BaselineTarget{Target: target}, "passed", "", evidence.Duration, true, false, &evidence,
		[]report.Evidence{{Kind: "target", ID: target.ID, Status: "passed"}}, nil,
	)
	for _, test := range []struct {
		name     string
		measured bool
	}{
		{name: "measured", measured: true},
		{name: "unmeasured terminal control"},
	} {
		t.Run(test.name, func(t *testing.T) {
			run := packageSuiteCoverageRun{importPath: target.Package, measured: test.measured}
			if test.measured {
				run.suite = PackageSuiteCoverage{Covered: block, Instrumented: block, Duration: 40 * time.Millisecond}
			}
			resume := checkpoint.Baseline{
				BuildVetComplete: true, Targets: []checkpoint.BaselineTarget{unit},
				Suites: []checkpoint.BaselineSuite{checkpointBaselineSuite(run)},
			}
			workspace := &baselineFakeWorkspace{exec: func(command gomutants.Command) (gomutants.CommandResult, error) {
				t.Fatalf("resumed completed baseline unit executed %+v", command.Argv)
				return gomutants.CommandResult{}, nil
			}}
			result, err := CollectBaseline(t.Context(), workspace, baselineModel(), []BaselineTarget{{Target: target}}, BaselineOptions{
				ArtifactDirectory: t.TempDir(), PackageSuites: true, Resume: &resume,
			})
			if err != nil || len(workspace.commands) != 0 || len(result.Targets) != 1 {
				t.Fatalf("resumed baseline = (%+v, %v), commands %+v", result, err, workspace.commands)
			}
			_, restored := result.Suites[target.Package]
			if restored != test.measured {
				t.Fatalf("restored suite present = %t, want %t", restored, test.measured)
			}
		})
	}
}

type resumeMutationSession struct {
	catalog  gomutants.Catalog
	calls    []string
	requests []gomutants.ExecRequest
	fail     map[string]error

	survive map[string]bool
}

func (session *resumeMutationSession) Catalog() gomutants.Catalog { return session.catalog }

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

func findingKinds(evaluation MutationEvaluation) []string {
	kinds := make([]string, 0, len(evaluation.Findings))
	for _, finding := range evaluation.Findings {
		kinds = append(kinds, finding.Kind)
	}
	return kinds
}

func survivingMutant(id string, line int) gomutants.Mutant {
	return gomutants.Mutant{
		ID: id, DisplayID: id, Path: "value.go", Package: "fixture.example/module", Line: line, Accepted: true,
	}
}

func TestMutationSurvivorIsCheckpointedBeforeALaterMutantFails(t *testing.T) {
	catalog := gomutants.Catalog{Mutants: []gomutants.Mutant{survivingMutant("mutant-a", 4), survivingMutant("mutant-b", 8)}}
	session := &resumeMutationSession{
		catalog: catalog,
		survive: map[string]bool{"mutant-a": true},
		fail:    map[string]error{"mutant-b": context.Canceled},
	}
	var saved, executedWhenSaved []string
	checkpointed := make(map[string]MutationEvaluation)
	_, err := evaluateMutationsForTest(t.Context(), session, reachedMutationTargets()[:9], MutationOptions{
		Jobs: 1,
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

func TestMutationSurvivorIsCheckpointedOnceWithItsFinalEvaluation(t *testing.T) {
	session := &resumeMutationSession{
		catalog: gomutants.Catalog{Mutants: []gomutants.Mutant{survivingMutant("mutant-a", 4)}},
		survive: map[string]bool{"mutant-a": true},
	}
	var saved []string
	checkpointed := make(map[string]MutationEvaluation)
	evaluation, err := evaluateMutationsForTest(t.Context(), session, reachedMutationTargets()[:9], MutationOptions{
		Jobs: 1,
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

	if !reflect.DeepEqual(unit.Findings, evaluation.Findings) || !reflect.DeepEqual(unit.Evidence, evaluation.Evidence) {
		t.Fatalf("checkpointed evaluation = %+v, want the reported %+v", unit, evaluation)
	}
}

func TestMutationSurvivorReachedByAFuzzTargetIsCheckpointedAfterSeedExecution(t *testing.T) {
	session := &resumeMutationSession{
		catalog: gomutants.Catalog{Mutants: []gomutants.Mutant{survivingMutant("mutant-a", 4)}},
		survive: map[string]bool{"mutant-a": true},
	}
	var saved []string
	checkpointed := make(map[string]MutationEvaluation)
	evaluation, err := evaluateMutationsForTest(t.Context(), session, reachedMutationTargets(), MutationOptions{
		Jobs: 1, Timeout: time.Second,
		OriginalControl: func(context.Context, gomutants.ExecRequest) (gomutants.CommandResult, error) {
			return gomutants.CommandResult{Duration: time.Millisecond}, nil
		},
		Checkpoint: func(id string, unit MutationEvaluation) {
			saved = append(saved, id)
			checkpointed[id] = unit
		},
	})

	if err != nil || !slices.Equal(saved, []string{"mutant-a"}) || evaluation.Accounting.Survived != 1 {
		t.Fatalf("fuzz-seed-reached surviving mutation = (%+v, %v), saved=%v", evaluation, err, saved)
	}
	if kinds := findingKinds(checkpointed["mutant-a"]); !slices.Equal(kinds, []string{"surviving-mutant"}) {
		t.Fatalf("checkpointed survivor = %+v, want one surviving-mutant finding", checkpointed["mutant-a"])
	}
	fuzzSeedExecutions := 0
	for index, request := range session.requests {
		if !slices.ContainsFunc(request.Args, func(arg string) bool { return strings.Contains(arg, "FuzzValue") }) {
			continue
		}
		fuzzSeedExecutions++
		if index != len(session.requests)-1 {
			t.Fatalf("fuzz seed request %d of %d ran before the shorter unit executions finished", index+1, len(session.requests))
		}
		if slices.ContainsFunc(request.Args, func(arg string) bool { return strings.HasPrefix(arg, "-test.fuzz=") }) {
			t.Fatalf("exploratory fuzzing was requested: %+v", request)
		}
	}
	if fuzzSeedExecutions != 1 {
		t.Fatalf("fuzz seed executions = %d, want one deterministic execution: %+v", fuzzSeedExecutions, session.requests)
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
	evaluation, err := evaluateMutationsForTest(t.Context(), session, nil, MutationOptions{
		Jobs:       1,
		Resume:     map[string]MutationEvaluation{"mutant-a": resumed},
		Checkpoint: func(id string, _ MutationEvaluation) { saved = append(saved, id) },
		SuiteCoverage: map[string]PackageSuiteCoverage{
			"example.test/project": {Duration: time.Second},
		},
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
	evaluation, err := evaluateMutationsForTest(t.Context(), session, nil, MutationOptions{
		Jobs: 1, Checkpoint: func(id string, _ MutationEvaluation) { saved = append(saved, id) },
		SuiteCoverage: map[string]PackageSuiteCoverage{
			"example.test/project": {Duration: time.Second},
		},
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
		ProbeDuration: 11 * time.Millisecond,
		WholeTree:     true, Probed: true, Infected: []uint32{1, 4},
		Covered: []goanalysis.FileCoverage{{Path: "value.go", Blocks: []goanalysis.CoverageBlock{
			{StartLine: 1, StartColumn: 1, EndLine: 2, EndColumn: 1},
		}}}}
	restored := restoreTargetEvidence(*checkpointTargetEvidence(input))
	if restored.Target.ID != input.Target.ID || restored.Target.Kind != input.Target.Kind ||
		!slices.Equal(restored.Target.Capabilities, input.Target.Capabilities) || !slices.Equal(restored.Target.Dependencies, input.Target.Dependencies) ||
		!slices.Equal(restored.CoveredFiles, input.CoveredFiles) || !slices.Equal(restored.Environment, input.Environment) || restored.Duration != input.Duration || restored.WholeTree != input.WholeTree ||
		restored.Probed != input.Probed || restored.ProbeDuration != input.ProbeDuration || !slices.Equal(restored.Infected, input.Infected) {
		t.Fatalf("restored target = %+v, want %+v", restored, input)
	}

	if !reflect.DeepEqual(restored.Covered, input.Covered) {
		t.Fatalf("restored blocks = %+v, want %+v", restored.Covered, input.Covered)
	}

	if restored.Instrumented == nil {
		t.Fatal("restored instrumentation is unknown, want an exact empty set")
	}
}

func TestCollectBaselineKeepsBlocksForFreshAndResumedTargets(t *testing.T) {
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
			if err := os.WriteFile(coverageProfileArgument(command), []byte(contents), filemode.PrivateFile); err != nil {
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
			want := []goanalysis.FileCoverage{{Path: "value.go", Blocks: []goanalysis.CoverageBlock{
				{StartLine: 5, StartColumn: 29, EndLine: 6, EndColumn: 16},
			}}}
			if !reflect.DeepEqual(target.Covered, want) || !slices.Equal(target.CoveredFiles, []string{"value.go"}) {
				t.Errorf("resumed target = %+v, want exact checkpointed blocks %+v", target, want)
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

func TestCompletedBaselineRoutingResumesWithoutCompileOrSuiteCommands(t *testing.T) {
	model := baselineModel()
	target := baselineTestTarget("TestResumed")
	evidence := TargetEvidence{
		Target: target, CoveredFiles: []string{"value.go"}, Duration: 11 * time.Millisecond,
		Covered: []goanalysis.FileCoverage{{Path: "value.go", Blocks: []goanalysis.CoverageBlock{{
			StartLine: 5, StartColumn: 29, EndLine: 6, EndColumn: 16,
		}}}},
	}
	instrumented := []goanalysis.FileCoverage{{Path: "value.go", Blocks: []goanalysis.CoverageBlock{{
		StartLine: 5, StartColumn: 29, EndLine: 8, EndColumn: 4,
	}}}}
	suites := map[string]PackageSuiteCoverage{target.Package: {
		Covered: evidence.Covered, Instrumented: instrumented, Duration: 17 * time.Millisecond, WholeTree: true,
	}}
	resume := &checkpoint.Baseline{
		BuildVetComplete: true, Complete: true, Routing: checkpointBaselineRouting(instrumented, suites),
		Targets: []checkpoint.BaselineTarget{{
			ID: target.ID, Executed: true, Target: checkpointTargetEvidence(evidence),
			Inventory: report.TargetDisposition{
				ID: target.ID, Name: target.Name, Kind: string(target.Kind), Package: target.Package, Status: "passed",
			},
		}},
	}
	workspace := &baselineFakeWorkspace{exec: func(command gomutants.Command) (gomutants.CommandResult, error) {
		t.Fatalf("completed baseline executed %+v", command.Argv)
		return gomutants.CommandResult{}, nil
	}}
	var completed checkpoint.Baseline
	result, err := CollectBaseline(t.Context(), workspace, model, []BaselineTarget{{Target: target}}, BaselineOptions{
		ArtifactDirectory: t.TempDir(), PackageSuites: true, Resume: resume,
		Checkpoint: func(state checkpoint.Baseline) { completed = state },
	})
	if err != nil || len(workspace.commands) != 0 || !reflect.DeepEqual(result.Instrumented, instrumented) ||
		!reflect.DeepEqual(result.Suites, suites) || completed.Routing == nil {
		t.Fatalf("resumed baseline = (%+v, %v), commands=%+v checkpoint=%+v", result, err, workspace.commands, completed)
	}
}

func TestCheckpointClaimFailureForcesColdRunAndCatalogMismatchPreservesBaseline(t *testing.T) {
	digest := digestText("claim-failure")
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
		if resumed := controller.mutation(newCatalog); len(resumed) != 0 || !controller.state.Baseline.BuildVetComplete || len(controller.state.Baseline.Targets) != 1 || controller.state.Mutation.CatalogFingerprint != MutationCatalogFingerprint(newCatalog) {
			t.Fatalf("catalog mismatch resumed=%+v state=%+v", resumed, controller.state)
		}
	})
}

func TestCheckpointControllerPersistsCompleteProbeAndRejectsChangedInventory(t *testing.T) {
	t.Parallel()
	digest := digestText("probe-checkpoint")
	catalog := probeCatalog()
	targets := []TargetEvidence{probeEvidence("TestValue", goanalysis.KindTest, 17*time.Millisecond)}
	probed := slices.Clone(targets)
	probed[0].Probed = true
	probed[0].ProbeDuration = resumedProbeDuration
	probed[0].Infected = []uint32{0, 2}
	evaluation := ProbeEvaluation{Targets: probed, Measured: 1}
	store := &coordinatorCache{}
	first := openRunCheckpoint(store, digest, Options{}, true)
	if resumed := first.mutation(catalog); len(resumed) != 0 {
		t.Fatalf("new catalogue resumed %+v", resumed)
	}
	first.saveProbe(catalog, evaluation)
	first.saveMutant("mutant-a", MutationEvaluation{
		Evidence: []report.Evidence{{Kind: "mutation", ID: "mutant-a", Status: "killed"}},
	})

	second := openRunCheckpoint(store, digest, Options{}, true)
	resumedMutants := second.mutation(catalog)
	restored, reused, valid := second.probe(catalog, targets, nil)
	if !valid || !reused || len(resumedMutants) != 1 || !reflect.DeepEqual(restored, evaluation) {
		t.Fatalf("resume = probe (%+v, reused=%t valid=%t), mutants=%+v", restored, reused, valid, resumedMutants)
	}

	changed := slices.Clone(targets)
	changed[0].Target.ID = "changed-target"
	if _, reused, valid := second.probe(catalog, changed, nil); valid || reused {
		t.Fatalf("changed inventory reused=%t valid=%t", reused, valid)
	}
	if second.state.Mutation == nil || second.state.Mutation.Probe != nil || len(second.state.Mutation.Results) != 0 || second.reusedMutants != 0 {
		t.Fatalf("changed inventory retained dependent work: %+v", second.state.Mutation)
	}
}

func TestCheckpointRaceAndCandidateValidationDiscardOnlyUnsafePhases(t *testing.T) {
	digest := digestText("phase-checkpoint")
	baseline := checkpoint.Baseline{BuildVetComplete: true}

	t.Run("matching race inventory is reused", func(t *testing.T) {
		cache := &coordinatorCache{checkpointFound: true, checkpoint: checkpoint.State{
			Schema: checkpoint.SchemaV1, InputDigest: digest, Attempts: 2, Baseline: baseline,
			Race: &checkpoint.Race{Complete: true, Packages: []string{"example.test/b", "example.test/a"}},
		}}
		wantAttempts := cache.checkpoint.Attempts + 1
		wantPackages := len(cache.checkpoint.Race.Packages)
		controller := openRunCheckpoint(cache, digest, Options{}, true)
		resumed, ok := controller.race([]string{"example.test/a", "example.test/b"})
		metadata := controller.resumeMetadata()
		if !ok || resumed == nil || metadata.Attempts != wantAttempts || metadata.ReusedRacePackages != wantPackages {
			t.Fatalf("race resume = (%+v, %t), metadata=%+v", resumed, ok, metadata)
		}
	})

	t.Run("changed race inventory discards race and mutation", func(t *testing.T) {
		cache := &coordinatorCache{checkpointFound: true, checkpoint: checkpoint.State{
			Schema: checkpoint.SchemaV1, InputDigest: digest, Attempts: 1, Baseline: baseline,
			Race:     &checkpoint.Race{Complete: true, Packages: []string{"example.test/old"}},
			Mutation: &checkpoint.Mutation{CatalogFingerprint: digestText("changed-catalog")},
		}}
		controller := openRunCheckpoint(cache, digest, Options{}, true)
		if resumed, ok := controller.race([]string{"example.test/new"}); ok || resumed != nil || controller.state.Race != nil || controller.state.Mutation != nil || !controller.state.Baseline.BuildVetComplete {
			t.Fatalf("race mismatch = (%+v, %t), state=%+v", resumed, ok, controller.state)
		}
	})
}
