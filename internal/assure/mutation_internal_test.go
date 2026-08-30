// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package assure

import (
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	gomutants "github.com/P4suta/go-mutants"
	goanalysis "github.com/P4suta/goatest/internal/golang"
	"github.com/P4suta/goatest/internal/report"
)

type mutationUnitSession struct {
	catalog  gomutants.Catalog
	mu       sync.Mutex
	requests []gomutants.ExecRequest
	exec     func(gomutants.ExecRequest) (gomutants.MutantResult, error)
}

func (session *mutationUnitSession) Catalog() gomutants.Catalog { return session.catalog }

func (session *mutationUnitSession) Exec(_ context.Context, request gomutants.ExecRequest) (gomutants.MutantResult, error) {
	session.mu.Lock()
	session.requests = append(session.requests, request)
	session.mu.Unlock()
	if session.exec == nil {
		return gomutants.MutantResult{ID: request.Mutant, Outcome: gomutants.OutcomeSurvived}, nil
	}
	return session.exec(request)
}

func TestEvaluateMutationsValidatesInputsFiltersCatalogAndRecordsRejections(t *testing.T) {
	root := t.TempDir()
	if evaluation, err := EvaluateMutations(t.Context(), nil, nil, MutationOptions{Root: root}); err == nil || !reflect.DeepEqual(evaluation, MutationEvaluation{}) || err.Error() != "goatest: nil mutation session" {
		t.Fatalf("nil session = (%+v, %v)", evaluation, err)
	}
	session := &mutationUnitSession{exec: func(gomutants.ExecRequest) (gomutants.MutantResult, error) {
		t.Fatal("session executed for invalid or rejected catalog")
		return gomutants.MutantResult{}, nil
	}}
	if evaluation, err := EvaluateMutations(t.Context(), session, nil, MutationOptions{}); err == nil || !reflect.DeepEqual(evaluation, MutationEvaluation{}) || err.Error() != "goatest: mutation evaluation requires a repository root" {
		t.Fatalf("empty root = (%+v, %v)", evaluation, err)
	}
	session.catalog = gomutants.Catalog{
		Mutants:    []gomutants.Mutant{{ID: "rejected-id", Accepted: false}},
		Rejections: []gomutants.Rejection{{ID: "rejected-id", Diagnostic: "does not compile"}},
	}
	evaluation, err := EvaluateMutations(t.Context(), session, nil, MutationOptions{Root: root})
	want := MutationEvaluation{
		Evidence:   []report.Evidence{{Kind: "mutation", ID: "rejected-id", Status: "compile-rejected", Detail: "does not compile"}},
		Accounting: report.MutantAccounting{Discovered: 1, Selected: 1, CompileRejected: 1},
		Mutants: []report.MutantDisposition{{
			ID: "rejected-id", Status: report.MutantCompileRejected, Detail: "does not compile",
		}},
	}
	if err != nil || !reflect.DeepEqual(evaluation, want) || len(session.requests) != 0 {
		t.Fatalf("catalog evaluation = (%+v, %v), want %+v", evaluation, err, want)
	}
}

func TestEvaluateMutationsReplaysOnlyRequestedMutantAndFailsClosedWhenAbsent(t *testing.T) {
	first, second := internalMutation("mutant-a"), internalMutation("mutant-b")
	target := internalTarget("TestValue", goanalysis.KindTest, time.Second)
	progress := make([][2]int, 0, 1)
	session := &mutationUnitSession{
		catalog: gomutants.Catalog{
			Mutants:    []gomutants.Mutant{first, second, {ID: "rejected-other", Accepted: false}},
			Rejections: []gomutants.Rejection{{ID: "rejected-other", Diagnostic: "compile equivalent"}},
		},
		exec: func(request gomutants.ExecRequest) (gomutants.MutantResult, error) {
			return gomutants.MutantResult{ID: request.Mutant, Outcome: gomutants.OutcomeKilled}, nil
		},
	}
	evaluation, err := EvaluateMutations(t.Context(), session, []TargetEvidence{target}, MutationOptions{
		Root: t.TempDir(), Jobs: 2, ReplayMutantID: second.ID,
		Progress: func(completed, total int) { progress = append(progress, [2]int{completed, total}) },
	})
	if err != nil || len(evaluation.Evidence) != 1 || evaluation.Evidence[0].ID != second.ID || len(session.requests) != 1 ||
		session.requests[0].Mutant != second.ID || !reflect.DeepEqual(progress, [][2]int{{1, 1}}) {
		t.Fatalf("replay evaluation = (%+v, %v), requests=%+v progress=%v", evaluation, err, session.requests, progress)
	}

	absent := &mutationUnitSession{
		catalog: session.catalog,
		exec: func(gomutants.ExecRequest) (gomutants.MutantResult, error) {
			t.Fatal("absent replay mutant executed")
			return gomutants.MutantResult{}, nil
		},
	}
	evaluation, err = EvaluateMutations(t.Context(), absent, []TargetEvidence{target}, MutationOptions{
		Root: t.TempDir(), ReplayMutantID: "missing",
	})
	if err == nil || !strings.Contains(err.Error(), "replay mutant missing is absent") || !reflect.DeepEqual(evaluation, MutationEvaluation{}) || len(absent.requests) != 0 {
		t.Fatalf("absent replay = (%+v, %v), requests=%+v", evaluation, err, absent.requests)
	}
}

func TestEvaluateMutationsReturnsSeedExecutionErrorWithoutPartialEvidence(t *testing.T) {
	cause := errors.New("seed execution failed")
	mutant := internalMutation("mutant-a")
	session := &mutationUnitSession{
		catalog: gomutants.Catalog{Mutants: []gomutants.Mutant{mutant}},
		exec: func(gomutants.ExecRequest) (gomutants.MutantResult, error) {
			return gomutants.MutantResult{}, cause
		},
	}
	evaluation, err := EvaluateMutations(t.Context(), session, []TargetEvidence{
		internalTarget("TestValue", goanalysis.KindTest, time.Second),
	}, MutationOptions{Root: t.TempDir(), Jobs: 1})
	if !errors.Is(err, cause) || !reflect.DeepEqual(evaluation, MutationEvaluation{}) || len(session.requests) != 1 {
		t.Fatalf("EvaluateMutations = (%+v, %v), requests=%d", evaluation, err, len(session.requests))
	}
}

func TestEvaluateMutationSeedCoversEveryOutcomeAndExecutionError(t *testing.T) {
	mutant := internalMutation("mutant-a")
	target := internalTarget("TestValue", goanalysis.KindTest, time.Second)
	for _, test := range []struct {
		name     string
		outcome  gomutants.Outcome
		execErr  error
		resolved bool
		kind     string
		wantErr  bool
	}{
		{name: "killed", outcome: gomutants.OutcomeKilled, resolved: true},
		{name: "survived", outcome: gomutants.OutcomeSurvived},
		{name: "timed out", outcome: gomutants.OutcomeTimedOut, resolved: true, kind: "mutation-timeout"},
		{name: "inconclusive", outcome: gomutants.OutcomeInconclusive, resolved: true, kind: "mutation-inconclusive"},
		{name: "errored", outcome: gomutants.OutcomeErrored, resolved: true, kind: "mutation-inconclusive"},
		{name: "not run", outcome: gomutants.OutcomeNotRun, resolved: true, kind: "mutation-inconclusive"},
		{name: "unknown", outcome: gomutants.Outcome("unknown"), wantErr: true},
		{name: "execution error", execErr: errors.New("exec failed"), wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			session := &mutationUnitSession{exec: func(request gomutants.ExecRequest) (gomutants.MutantResult, error) {
				if request.Mutant != mutant.ID || request.Package != target.Target.Package {
					t.Fatalf("request = %+v", request)
				}
				if test.execErr != nil {
					return gomutants.MutantResult{}, test.execErr
				}
				return gomutants.MutantResult{ID: mutant.ID, Outcome: test.outcome}, nil
			}}
			seed := evaluateMutationSeed(t.Context(), session, mutant, []TargetEvidence{target}, MutationOptions{Contract: "standard-v1"})
			if seed.mutant.ID != mutant.ID || len(seed.reaching) != 1 || seed.resolved != test.resolved || (seed.err != nil) != test.wantErr {
				t.Fatalf("seed = %+v", seed)
			}
			if test.execErr != nil && !errors.Is(seed.err, test.execErr) {
				t.Fatalf("seed error = %v, want cause %v", seed.err, test.execErr)
			}
			switch {
			case test.name == "killed":
				if len(seed.evaluation.Evidence) != 1 || seed.evaluation.Evidence[0].Status != "killed" {
					t.Fatalf("killed evaluation = %+v", seed.evaluation)
				}
			case test.kind != "":
				if len(seed.evaluation.Findings) != 1 || seed.evaluation.Findings[0].Kind != test.kind {
					t.Fatalf("finding evaluation = %+v", seed.evaluation)
				}
			case test.name == "survived":
				if !reflect.DeepEqual(seed.evaluation, MutationEvaluation{}) {
					t.Fatalf("survived evaluation = %+v", seed.evaluation)
				}
			}
		})
	}

	t.Run("survived then killed", func(t *testing.T) {
		first := internalTarget("TestFirst", goanalysis.KindTest, time.Second)
		second := internalTarget("TestSecond", goanalysis.KindTest, 2*time.Second)
		calls := 0
		session := &mutationUnitSession{exec: func(gomutants.ExecRequest) (gomutants.MutantResult, error) {
			calls++
			outcome := gomutants.OutcomeSurvived
			if calls == 2 {
				outcome = gomutants.OutcomeKilled
			}
			return gomutants.MutantResult{Outcome: outcome}, nil
		}}
		seed := evaluateMutationSeed(t.Context(), session, mutant, []TargetEvidence{second, first}, MutationOptions{})
		if calls != 2 || !seed.resolved || len(seed.evaluation.Evidence) != 1 || seed.evaluation.Evidence[0].Detail != "TestSecond" {
			// Durations sort TestFirst first, then TestSecond kills.
			t.Fatalf("seed = %+v, calls=%d", seed, calls)
		}
	})
}

func TestEvaluateMutationsCoversEveryTargetedFuzzOutcomeAndError(t *testing.T) {
	for _, test := range []struct {
		name      string
		outcome   gomutants.Outcome
		execErr   error
		noApply   bool
		artifacts []gomutants.Artifact
		kind      string
		wantErr   bool
	}{
		{name: "survived", outcome: gomutants.OutcomeSurvived, kind: "surviving-mutant"},
		{name: "timed out", outcome: gomutants.OutcomeTimedOut, kind: "fuzz-timeout"},
		{name: "inconclusive", outcome: gomutants.OutcomeInconclusive, kind: "fuzz-inconclusive"},
		{name: "errored", outcome: gomutants.OutcomeErrored, kind: "fuzz-inconclusive"},
		{name: "not run", outcome: gomutants.OutcomeNotRun, kind: "fuzz-inconclusive"},
		{name: "unknown", outcome: gomutants.Outcome("unknown"), wantErr: true},
		{name: "execution error", execErr: errors.New("fuzz exec failed"), wantErr: true},
		{name: "killed no apply", outcome: gomutants.OutcomeKilled, noApply: true, kind: "unpersisted-fuzz-kill"},
		{name: "killed without artifact", outcome: gomutants.OutcomeKilled, kind: "unpersisted-fuzz-kill"},
		{name: "promotion error", outcome: gomutants.OutcomeKilled, artifacts: []gomutants.Artifact{{Path: "testdata/fuzz/FuzzValue/bad", Data: []byte("invalid")}}, wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			mutant := internalMutation("mutant-a")
			calls := 0
			session := &mutationUnitSession{catalog: gomutants.Catalog{Mutants: []gomutants.Mutant{mutant}}}
			session.exec = func(request gomutants.ExecRequest) (gomutants.MutantResult, error) {
				calls++
				if !hasMutationArgPrefix(request.Args, "-test.fuzz=") {
					return gomutants.MutantResult{Outcome: gomutants.OutcomeSurvived}, nil
				}
				if test.execErr != nil {
					return gomutants.MutantResult{}, test.execErr
				}
				return gomutants.MutantResult{Outcome: test.outcome, Artifacts: test.artifacts}, nil
			}
			evaluation, err := EvaluateMutations(t.Context(), session, []TargetEvidence{
				internalTarget("FuzzValue", goanalysis.KindFuzz, time.Second),
			}, MutationOptions{Root: t.TempDir(), Contract: "standard-v1", Jobs: 1, NoApply: test.noApply})
			if calls != 2 || (err != nil) != test.wantErr {
				t.Fatalf("EvaluateMutations = (%+v, %v), calls=%d", evaluation, err, calls)
			}
			if test.execErr != nil && !errors.Is(err, test.execErr) {
				t.Fatalf("fuzz error = %v, want cause %v", err, test.execErr)
			}
			if test.wantErr {
				if !reflect.DeepEqual(evaluation, MutationEvaluation{}) {
					t.Fatalf("error evaluation = %+v", evaluation)
				}
				return
			}
			if len(evaluation.Findings) != 1 || evaluation.Findings[0].Kind != test.kind || evaluation.Applied {
				t.Fatalf("evaluation = %+v, want finding %q", evaluation, test.kind)
			}
		})
	}
}

func TestEvaluateMutationsStopsFuzzingAfterFirstKillOrBlockedOutcome(t *testing.T) {
	for _, outcome := range []gomutants.Outcome{gomutants.OutcomeKilled, gomutants.OutcomeTimedOut} {
		t.Run(string(outcome), func(t *testing.T) {
			mutant := internalMutation("mutant-a")
			fuzzCalls := 0
			session := &mutationUnitSession{catalog: gomutants.Catalog{Mutants: []gomutants.Mutant{mutant}}}
			session.exec = func(request gomutants.ExecRequest) (gomutants.MutantResult, error) {
				if !hasMutationArgPrefix(request.Args, "-test.fuzz=") {
					return gomutants.MutantResult{Outcome: gomutants.OutcomeSurvived}, nil
				}
				fuzzCalls++
				if fuzzCalls > 1 {
					t.Fatal("fuzzing continued after a terminal outcome")
				}
				return gomutants.MutantResult{Outcome: outcome}, nil
			}
			targets := []TargetEvidence{
				internalTarget("FuzzFirst", goanalysis.KindFuzz, time.Second),
				internalTarget("FuzzSecond", goanalysis.KindFuzz, 2*time.Second),
			}
			evaluation, err := EvaluateMutations(t.Context(), session, targets, MutationOptions{Root: t.TempDir(), Jobs: 1, NoApply: true})
			if err != nil || fuzzCalls != 1 || len(evaluation.Findings) != 1 {
				t.Fatalf("EvaluateMutations = (%+v, %v), fuzz calls=%d", evaluation, err, fuzzCalls)
			}
		})
	}
}

func TestMutationSeedSchedulerNormalizesJobsPreservesOrderAndHandlesEmpty(t *testing.T) {
	if results := evaluateMutationSeeds(t.Context(), &mutationUnitSession{}, nil, nil, MutationOptions{}); results != nil {
		t.Fatalf("empty results = %#v, want nil", results)
	}
	mutants := []gomutants.Mutant{internalMutation("a"), internalMutation("b"), internalMutation("c")}
	for _, jobs := range []int{-1, 0, 1, 99} {
		t.Run(string(rune(jobs+100)), func(t *testing.T) {
			var lock sync.Mutex
			var completed []int
			results := evaluateMutationSeeds(t.Context(), &mutationUnitSession{}, mutants, nil, MutationOptions{
				Jobs: jobs,
				Progress: func(done, total int) {
					lock.Lock()
					defer lock.Unlock()
					if total != len(mutants) {
						t.Errorf("progress total = %d", total)
					}
					completed = append(completed, done)
				},
			})
			if len(results) != len(mutants) {
				t.Fatalf("results = %d", len(results))
			}
			for index, result := range results {
				if result.mutant.ID != mutants[index].ID || !result.resolved || len(result.evaluation.Findings) != 1 {
					t.Fatalf("result %d = %+v", index, result)
				}
			}
			lock.Lock()
			defer lock.Unlock()
			if len(completed) != len(mutants) || !slices.Equal(slices.Sorted(slices.Values(completed)), []int{1, 2, 3}) {
				t.Fatalf("progress = %v", completed)
			}
		})
	}
}

func TestReachingTargetsSortsMeasuredShortestFirstAndKeepsUnmeasuredStable(t *testing.T) {
	t.Parallel()
	targets := []TargetEvidence{
		internalTarget("UnknownA", goanalysis.KindTest, 0),
		internalTarget("Slow", goanalysis.KindTest, 2*time.Second),
		internalTarget("UnknownB", goanalysis.KindTest, -time.Second),
		internalTarget("Fast", goanalysis.KindTest, time.Nanosecond),
		{Target: goanalysis.Target{Name: "Unrelated"}, CoveredFiles: []string{"other.go"}, Duration: time.Nanosecond},
	}
	got := reachingTargets(filepath.FromSlash("pkg/value.go"), targets)
	want := []string{"Fast", "Slow", "UnknownA", "UnknownB"}
	gotNames := make([]string, 0, len(got))
	for _, target := range got {
		gotNames = append(gotNames, target.Target.Name)
	}
	if !slices.Equal(gotNames, want) {
		t.Fatalf("reaching target order = %v, want %v", gotNames, want)
	}
}

func TestFuzzExecutionsHonorsContractAndBothBoundaries(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		contract  string
		requested int
		want      int
	}{
		{contract: "standard-v1", requested: -1, want: standardFuzzExecutions},
		{contract: "standard-v1", requested: 0, want: standardFuzzExecutions},
		{contract: "standard-v1", requested: 1, want: 1},
		{contract: "standard-v1", requested: standardFuzzExecutions, want: standardFuzzExecutions},
		{contract: "standard-v1", requested: standardFuzzExecutions + 1, want: standardFuzzExecutions},
		{contract: "deep-v1", requested: deepFuzzExecutions, want: deepFuzzExecutions},
		{contract: "deep-v1", requested: deepFuzzExecutions + 1, want: deepFuzzExecutions},
		{contract: "unknown", requested: deepFuzzExecutions, want: standardFuzzExecutions},
	} {
		if got := fuzzExecutions(test.contract, test.requested); got != test.want {
			t.Errorf("fuzzExecutions(%q, %d) = %d, want %d", test.contract, test.requested, got, test.want)
		}
	}
}

func TestCalibratedMutationTimeoutClampsEveryBoundaryWithoutOverflow(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		contract string
		baseline time.Duration
		override time.Duration
		want     time.Duration
	}{
		{contract: "standard-v1", baseline: time.Second, override: 7 * time.Second, want: 7 * time.Second},
		{contract: "standard-v1", baseline: time.Second, override: -time.Second, want: minimumMutationTimeout},
		{contract: "standard-v1", baseline: -time.Second, want: minimumMutationTimeout},
		{contract: "standard-v1", baseline: 0, want: minimumMutationTimeout},
		{contract: "standard-v1", baseline: 5 * time.Second, want: minimumMutationTimeout},
		{contract: "standard-v1", baseline: 12 * time.Second, want: 65 * time.Second},
		{contract: "standard-v1", baseline: time.Duration(math.MaxInt64), want: standardMutationTimeoutLimit},
		{contract: "deep-v1", baseline: 2 * time.Hour, want: deepMutationTimeoutLimit},
		{contract: "unknown", baseline: time.Hour, want: standardMutationTimeoutLimit},
	} {
		if got := calibratedMutationTimeout(test.contract, test.baseline, test.override); got != test.want {
			t.Errorf("calibratedMutationTimeout(%q, %s, %s) = %s, want %s", test.contract, test.baseline, test.override, got, test.want)
		}
	}
}

func TestSeedAndFuzzRequestsAreExactAndCloneEnvironment(t *testing.T) {
	t.Parallel()
	mutant := internalMutation("mutant-a")
	target := internalTarget("Fuzz(Value+)", goanalysis.KindFuzz, time.Second)
	target.Environment = []string{"DB=ready"}
	seed := seedRequest(mutant, target, 7*time.Second)
	if seed.Mutant != mutant.ID || seed.Package != target.Target.Package || !slices.Equal(seed.Args, []string{`-test.run=^Fuzz\(Value\+\)$`}) || !slices.Equal(seed.Env, target.Environment) || seed.Timeout != 7*time.Second {
		t.Fatalf("seed request = %+v", seed)
	}
	fuzz := fuzzRequest(mutant, target, 123, 9*time.Second)
	wantFuzzArgs := []string{"-test.run=^$", `-test.fuzz=^Fuzz\(Value\+\)$`, "-test.fuzztime=123x"}
	if fuzz.Mutant != mutant.ID || fuzz.Package != target.Target.Package || !slices.Equal(fuzz.Args, wantFuzzArgs) || !slices.Equal(fuzz.Env, target.Environment) || fuzz.Timeout != 9*time.Second {
		t.Fatalf("fuzz request = %+v", fuzz)
	}
	target.Environment[0] = "MUTATED=yes"
	if seed.Env[0] != "DB=ready" || fuzz.Env[0] != "DB=ready" {
		t.Fatal("request aliases environment")
	}
}

func TestPromoteTargetArtifactsFiltersTargetAndHandlesAddedExistingAndErrors(t *testing.T) {
	mutant := internalMutation("mutant-a")
	validData := []byte("go test fuzz v1\n[]byte(\"value\")\n")
	t.Run("unrelated", func(t *testing.T) {
		root := t.TempDir()
		var evaluation MutationEvaluation
		promoted, err := promoteTargetArtifacts(root, mutant, "FuzzValue", []gomutants.Artifact{{
			Path: "testdata/fuzz/FuzzOther/seed", Data: validData,
		}}, &evaluation)
		if err != nil || promoted || !reflect.DeepEqual(evaluation, MutationEvaluation{}) {
			t.Fatalf("unrelated promotion = (%t, %+v, %v)", promoted, evaluation, err)
		}
		if _, err := os.Stat(filepath.Join(root, "testdata", "fuzz", "FuzzOther", "seed")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("unrelated artifact was written: %v", err)
		}
	})

	t.Run("added nested", func(t *testing.T) {
		root := t.TempDir()
		var evaluation MutationEvaluation
		path := "pkg/testdata/fuzz/FuzzValue/seed"
		promoted, err := promoteTargetArtifacts(root, mutant, "FuzzValue", []gomutants.Artifact{{Path: path, Data: validData}}, &evaluation)
		if err != nil || !promoted || len(evaluation.Repairs) != 1 || evaluation.Repairs[0].Path != path || evaluation.Repairs[0].Status != "applied" {
			t.Fatalf("added promotion = (%t, %+v, %v)", promoted, evaluation, err)
		}
	})

	t.Run("identical existing", func(t *testing.T) {
		root := t.TempDir()
		path := filepath.Join(root, "testdata", "fuzz", "FuzzValue", "seed")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, validData, 0o600); err != nil {
			t.Fatal(err)
		}
		var evaluation MutationEvaluation
		promoted, err := promoteTargetArtifacts(root, mutant, "FuzzValue", []gomutants.Artifact{{Path: "testdata/fuzz/FuzzValue/seed", Data: validData}}, &evaluation)
		if err != nil || !promoted || len(evaluation.Repairs) != 0 {
			t.Fatalf("existing promotion = (%t, %+v, %v)", promoted, evaluation, err)
		}
	})

	t.Run("invalid matching artifact", func(t *testing.T) {
		var evaluation MutationEvaluation
		promoted, err := promoteTargetArtifacts(t.TempDir(), mutant, "FuzzValue", []gomutants.Artifact{{
			Path: "testdata/fuzz/FuzzValue/bad", Data: []byte("invalid"),
		}}, &evaluation)
		if err == nil || promoted || !reflect.DeepEqual(evaluation, MutationEvaluation{}) || !strings.Contains(err.Error(), "promote fuzz artifact for mutant") {
			t.Fatalf("invalid promotion = (%t, %+v, %v)", promoted, evaluation, err)
		}
	})
}

func TestMutationEvaluationAppendAndFindingHelpersPreserveAllFields(t *testing.T) {
	t.Parallel()
	mutant := internalMutation("mutant-a")
	evaluation := MutationEvaluation{Applied: false}
	evaluation.append(MutationEvaluation{
		Evidence: []report.Evidence{{ID: "evidence"}}, Findings: []report.Finding{{ID: "finding"}},
		Repairs: []report.Repair{{ID: "repair"}}, Applied: true,
	})
	if len(evaluation.Evidence) != 1 || len(evaluation.Findings) != 1 || len(evaluation.Repairs) != 1 || !evaluation.Applied {
		t.Fatalf("append = %+v", evaluation)
	}
	evaluation.addKill(mutant, "TestValue")
	if got := evaluation.Evidence[len(evaluation.Evidence)-1]; got.Kind != "mutation" || got.ID != mutant.ID || got.Status != "killed" || got.Detail != "TestValue" {
		t.Fatalf("kill evidence = %+v", got)
	}
	finding := mutationFinding(mutant, "surviving-mutant", "summary")
	wantID := report.FindingID("mutation", mutant.ID)
	if finding.ID != wantID || finding.Kind != "surviving-mutant" || finding.Path != mutant.Path || finding.Line != mutant.Line || finding.Summary != "summary" || finding.Replay != "goatest replay "+wantID || finding.MutantID != mutant.ID || !strings.Contains(finding.Mutant, mutant.Rule) {
		t.Fatalf("mutation finding = %+v", finding)
	}
	accepted := MutationEvaluation{}
	accepted.addFinding(mutant, "surviving-mutant", "summary", map[string]bool{wantID: true})
	if len(accepted.Findings) != 0 || !reflect.DeepEqual(accepted.Evidence, []report.Evidence{{Kind: "mutation", ID: mutant.ID, Status: "accepted", Detail: wantID}}) {
		t.Fatalf("accepted finding = %+v", accepted)
	}
	unaccepted := MutationEvaluation{}
	unaccepted.addFinding(mutant, "surviving-mutant", "summary", nil)
	if len(unaccepted.Findings) != 1 || len(unaccepted.Evidence) != 0 {
		t.Fatalf("unaccepted finding = %+v", unaccepted)
	}
}

func internalMutation(id string) gomutants.Mutant {
	return gomutants.Mutant{
		ID: id, DisplayID: id + "-display", Accepted: true, Path: "pkg/value.go", Package: "fixture.example/module",
		Line: 7, Rule: "lt-to-le", Original: "<", Replacement: "<=",
	}
}

func internalTarget(name string, kind goanalysis.TargetKind, duration time.Duration) TargetEvidence {
	return TargetEvidence{
		Target:       goanalysis.Target{ID: "target-" + name, Name: name, Kind: kind, Package: "fixture.example/module", RelativeDir: "."},
		CoveredFiles: []string{"pkg/value.go"}, Duration: duration,
	}
}

func hasMutationArgPrefix(arguments []string, prefix string) bool {
	for _, argument := range arguments {
		if strings.HasPrefix(argument, prefix) {
			return true
		}
	}
	return false
}

var _ MutationSession = (*mutationUnitSession)(nil)
