// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package assure

import (
	"context"
	"errors"
	"math"
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
	"github.com/P4suta/goatest/internal/trace"
)

const comparativeDeadlineVariantCount = 2

func TestMutationCatalogFingerprintOrdersEveryIdentityField(t *testing.T) {
	catalog := gomutants.Catalog{Mutants: []gomutants.Mutant{
		{ID: "same", Path: "z.go", Package: "fixture.example/z", Rule: "z", Line: 1},
		{ID: "same", Path: "a.go", Package: "fixture.example/z", Rule: "z", Line: 1},
		{ID: "same", Path: "a.go", Package: "fixture.example/a", Rule: "z", Line: 1},
		{ID: "same", Path: "a.go", Package: "fixture.example/a", Rule: "a", Line: 1},
		{ID: "same", Path: "a.go", Package: "fixture.example/a", Rule: "a"},
	}}
	reordered := gomutants.Catalog{Mutants: slices.Clone(catalog.Mutants)}
	slices.Reverse(reordered.Mutants)
	if first, second := MutationCatalogFingerprint(catalog), MutationCatalogFingerprint(reordered); first != second {
		t.Fatalf("fingerprints differ by catalog order: %q != %q", first, second)
	}
}

func TestMutationAccountingSelectsOnlyMutationEvidenceAndCountsUnknown(t *testing.T) {
	const selectedMutants = 2
	catalog := gomutants.Catalog{Mutants: []gomutants.Mutant{
		{ID: "unknown", Accepted: true},
		{ID: "killed", Accepted: true},
	}}
	evaluation := MutationEvaluation{Evidence: []report.Evidence{
		{Kind: "target", ID: "unknown", Status: "killed"},
		{Kind: "mutation", ID: "killed", Status: "killed"},
		{Kind: "mutation", ID: "outside", Status: "killed"},
	}}
	accounting, dispositions := mutationAccounting(catalog, "", evaluation, nil, nil)
	if accounting.Discovered != selectedMutants || accounting.Selected != selectedMutants || accounting.Executed != 1 ||
		accounting.Killed != 1 || accounting.Unknown != 1 || len(dispositions) != selectedMutants {
		t.Fatalf("accounting = %+v, dispositions = %+v", accounting, dispositions)
	}
	if dispositions[0].ID != "unknown" || dispositions[0].Status != report.MutantUnknown ||
		dispositions[1].ID != "killed" || dispositions[1].Status != report.MutantKilled {
		t.Fatalf("dispositions = %+v", dispositions)
	}
}

type mutationUnitSession struct {
	catalog  gomutants.Catalog
	mu       sync.Mutex
	requests []gomutants.ExecRequest
	exec     func(gomutants.ExecRequest) (gomutants.MutantResult, error)
	probes   []gomutants.ProbeRequest
	probe    func(gomutants.ProbeRequest) (gomutants.ProbeResult, error)
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

func (session *mutationUnitSession) Probe(_ context.Context, request gomutants.ProbeRequest) (gomutants.ProbeResult, error) {
	session.mu.Lock()
	session.probes = append(session.probes, request)
	session.mu.Unlock()
	if session.probe == nil {
		return gomutants.ProbeResult{Outcome: gomutants.ProbeUnavailable}, nil
	}
	return session.probe(request)
}

func (session *mutationUnitSession) probeRequests() []gomutants.ProbeRequest {
	session.mu.Lock()
	defer session.mu.Unlock()
	return slices.Clone(session.probes)
}

func TestEvaluateMutationsValidatesInputsFiltersCatalogAndRecordsRejections(t *testing.T) {
	if evaluation, err := EvaluateMutations(t.Context(), nil, nil, MutationOptions{}); err == nil || !reflect.DeepEqual(evaluation, MutationEvaluation{}) || err.Error() != "goatest: nil mutation session" {
		t.Fatalf("nil session = (%+v, %v)", evaluation, err)
	}
	session := &mutationUnitSession{exec: func(gomutants.ExecRequest) (gomutants.MutantResult, error) {
		t.Fatal("session executed for invalid or rejected catalog")
		return gomutants.MutantResult{}, nil
	}}
	session.catalog = gomutants.Catalog{
		Mutants:    []gomutants.Mutant{{ID: "rejected-id", Accepted: false}},
		Rejections: []gomutants.Rejection{{ID: "rejected-id", Diagnostic: "does not compile"}},
	}
	evaluation, err := EvaluateMutations(t.Context(), session, nil, MutationOptions{})
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
	evaluation, err := evaluateMutationsForTest(t.Context(), session, []TargetEvidence{target}, MutationOptions{
		Jobs: 2, ReplayMutantID: second.ID,
		Progress: func(completed, total int) { progress = append(progress, [2]int{completed, total}) },
	})
	if err != nil || len(evaluation.Evidence) != 1 || evaluation.Evidence[0].ID != second.ID || len(session.requests) != 1 ||
		session.requests[0].Mutant != second.ID || !reflect.DeepEqual(progress, [][2]int{{1, 1}}) {
		t.Fatalf("replay evaluation = (%+v, %v), requests=%+v progress=%v", evaluation, err, session.requests, progress)
	}
	rejected := &mutationUnitSession{
		catalog: session.catalog,
		exec: func(gomutants.ExecRequest) (gomutants.MutantResult, error) {
			t.Fatal("compile-rejected replay mutant executed")
			return gomutants.MutantResult{}, nil
		},
	}
	evaluation, err = evaluateMutationsForTest(t.Context(), rejected, []TargetEvidence{target}, MutationOptions{
		ReplayMutantID: "rejected-other",
	})
	if err != nil || len(evaluation.Evidence) != 1 || evaluation.Evidence[0].Status != "compile-rejected" ||
		evaluation.Accounting.Selected != 1 || evaluation.Accounting.CompileRejected != 1 || len(rejected.requests) != 0 {
		t.Fatalf("compile-rejected replay = (%+v, %v), requests=%+v", evaluation, err, rejected.requests)
	}

	absent := &mutationUnitSession{
		catalog: session.catalog,
		exec: func(gomutants.ExecRequest) (gomutants.MutantResult, error) {
			t.Fatal("absent replay mutant executed")
			return gomutants.MutantResult{}, nil
		},
	}
	evaluation, err = evaluateMutationsForTest(t.Context(), absent, []TargetEvidence{target}, MutationOptions{
		ReplayMutantID: "missing",
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
	evaluation, err := evaluateMutationsForTest(t.Context(), session, []TargetEvidence{
		internalTarget("TestValue", goanalysis.KindTest, time.Second),
	}, MutationOptions{Jobs: 1})
	if !errors.Is(err, cause) || !reflect.DeepEqual(evaluation, MutationEvaluation{}) || len(session.requests) != 1 {
		t.Fatalf("EvaluateMutations = (%+v, %v), requests=%d", evaluation, err, len(session.requests))
	}
}

func TestEvaluateMutationSeedCoversEveryOutcomeAndExecutionError(t *testing.T) {
	mutant := internalMutation("mutant-a")
	target := internalTarget("TestValue", goanalysis.KindTest, time.Second)
	for _, test := range []struct {
		name string

		outcome  gomutants.Outcome
		execErr  error
		resolved bool
		kind     string
		wantErr  bool
	}{
		{name: "killed", outcome: gomutants.OutcomeKilled, resolved: true},
		{name: "survived", outcome: gomutants.OutcomeSurvived, resolved: true, kind: "surviving-mutant"},
		{name: "timed out without a control", outcome: gomutants.OutcomeTimedOut, resolved: true, kind: "mutation-timeout"},
		{name: "inconclusive", outcome: gomutants.OutcomeInconclusive, resolved: true, kind: "mutation-inconclusive"},
		{name: "errored", outcome: gomutants.OutcomeErrored, wantErr: true},
		{name: "not run", outcome: gomutants.OutcomeNotRun, wantErr: true},
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
			reaching := []TargetEvidence{target}
			seed := evaluateMutationSeed(t.Context(), session, mutant, reaching, mutationOptionsForTest(MutationOptions{}))
			if seed.mutant.ID != mutant.ID || len(seed.reaching) != len(reaching) || seed.resolved != test.resolved || (seed.err != nil) != test.wantErr {
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
			}
		})
	}

	t.Run("survived then killed", func(t *testing.T) {
		first := internalTarget("TestFirst", goanalysis.KindTest, time.Second)
		second := internalTarget("TestSecond", goanalysis.KindTest, 2*time.Second)
		second.Environment = []string{"GROUP=second"}
		targets := []TargetEvidence{second, first}
		calls := 0
		session := &mutationUnitSession{exec: func(request gomutants.ExecRequest) (gomutants.MutantResult, error) {
			calls++
			outcome := gomutants.OutcomeSurvived
			if slices.Contains(request.Args, "-test.run=^TestSecond$") {
				outcome = gomutants.OutcomeKilled
			}
			return gomutants.MutantResult{Outcome: outcome}, nil
		}}
		seed := evaluateMutationSeed(t.Context(), session, mutant, targets, mutationOptionsForTest(MutationOptions{}))
		if calls != len(targets) || !seed.resolved || len(seed.evaluation.Evidence) != 1 || seed.evaluation.Evidence[0].Detail != "TestSecond" {
			t.Fatalf("seed = %+v, calls=%d", seed, calls)
		}
	})
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
	route := routeMutant(gomutants.Mutant{Path: filepath.FromSlash("pkg/value.go")}, targets, nil)
	want := []string{"Fast", "Slow", "UnknownA", "UnknownB"}
	gotNames := make([]string, 0, len(route.reaching))
	for _, target := range route.reaching {
		gotNames = append(gotNames, target.Target.Name)
	}
	if !slices.Equal(gotNames, want) {
		t.Fatalf("reaching target order = %v, want %v", gotNames, want)
	}
	if route.granularity != trace.GranularityFile || route.fallback != trace.FallbackPositionUnknown || route.fileCandidates != 4 {
		t.Fatalf("route = %+v, want the whole file for a mutant with no position", route)
	}
}

func TestMutationExecutionTimeoutSumsCleanObservationsAndKeepsTheConfiguredCeiling(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name    string
		limit   time.Duration
		samples []time.Duration
		want    time.Duration
	}{
		{name: "one observation", samples: []time.Duration{10 * time.Millisecond}, want: 10 * time.Millisecond},
		{name: "independent observations add", samples: []time.Duration{time.Second, 3 * time.Second}, want: 4 * time.Second},
		{name: "nonpositive observations are absent", limit: 9 * time.Second, samples: []time.Duration{0, -time.Second}, want: 0},
		{name: "no observation and no containment", want: 0},
		{name: "configured containment caps observations", limit: 7 * time.Second, samples: []time.Duration{time.Minute}, want: 7 * time.Second},
		{name: "overflow saturates", samples: []time.Duration{time.Duration(math.MaxInt64), time.Nanosecond}, want: time.Duration(math.MaxInt64)},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := mutationExecutionTimeout(test.limit, test.samples...); got != test.want {
				t.Fatalf("mutationExecutionTimeout(%s, %v) = %s, want %s",
					test.limit, test.samples, got, test.want)
			}
		})
	}
}

func TestOriginalControlMemoizationIncludesTheComparativeDeadline(t *testing.T) {
	t.Parallel()
	calls := 0
	control := memoizedOriginalControl(func(context.Context, gomutants.ExecRequest) (gomutants.CommandResult, error) {
		calls++
		return gomutants.CommandResult{}, nil
	})
	request := gomutants.ExecRequest{Package: "fixture.example/module", Args: []string{"-test.run=^TestValue$"}, Timeout: time.Second}
	if _, err := control(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	if _, err := control(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	request.Timeout += time.Second
	if _, err := control(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	if calls != comparativeDeadlineVariantCount {
		t.Fatalf("control calls = %d, want one for each distinct deadline", calls)
	}
}

func TestSeedRequestIsExactAndClonesEnvironment(t *testing.T) {
	t.Parallel()
	mutant := internalMutation("mutant-a")
	target := internalTarget("Fuzz(Value+)", goanalysis.KindFuzz, time.Second)
	target.Environment = []string{"DB=ready"}
	seed := seedRequest(mutant, target, 7*time.Second)
	if seed.Mutant != mutant.ID || seed.Package != target.Target.Package || !slices.Equal(seed.Args, []string{`-test.run=^Fuzz\(Value\+\)$`}) || !slices.Equal(seed.Env, target.Environment) || seed.Timeout != 7*time.Second {
		t.Fatalf("seed request = %+v", seed)
	}
	target.Environment[0] = "MUTATED=yes"
	if seed.Env[0] != "DB=ready" {
		t.Fatal("request aliases environment")
	}
}

func TestMutationEvaluationAppendAndFindingHelpersPreserveAllFields(t *testing.T) {
	t.Parallel()
	mutant := internalMutation("mutant-a")
	var evaluation MutationEvaluation
	evaluation.append(MutationEvaluation{
		Evidence: []report.Evidence{{ID: "evidence"}}, Findings: []report.Finding{{ID: "finding"}},
	})
	if len(evaluation.Evidence) != 1 || len(evaluation.Findings) != 1 {
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

var _ MutationSession = (*mutationUnitSession)(nil)
