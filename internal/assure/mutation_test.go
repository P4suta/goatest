// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package assure_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	gomutants "github.com/P4suta/go-mutants"
	"github.com/P4suta/goatest/internal/assure"
	goanalysis "github.com/P4suta/goatest/internal/golang"
	"github.com/P4suta/goatest/internal/report"
)

type fakeSession struct {
	catalog  gomutants.Catalog
	requests []gomutants.ExecRequest
	exec     func(gomutants.ExecRequest) (gomutants.MutantResult, error)
}

func (session *fakeSession) Catalog() gomutants.Catalog { return session.catalog }

func (session *fakeSession) Exec(_ context.Context, request gomutants.ExecRequest) (gomutants.MutantResult, error) {
	session.requests = append(session.requests, request)
	return session.exec(request)
}

type parallelSession struct {
	catalog   gomutants.Catalog
	started   chan string
	completed chan string
	releaseA  chan struct{}
	releaseB  chan struct{}
	mu        sync.Mutex
	active    int
	maximum   int
}

func (session *parallelSession) Catalog() gomutants.Catalog { return session.catalog }

func (session *parallelSession) Exec(_ context.Context, request gomutants.ExecRequest) (gomutants.MutantResult, error) {
	session.mu.Lock()
	session.active++
	if session.active > session.maximum {
		session.maximum = session.active
	}
	session.mu.Unlock()
	session.started <- request.Mutant
	if request.Mutant == "mutant-a" {
		<-session.releaseA
	} else {
		<-session.releaseB
	}
	session.completed <- request.Mutant
	session.mu.Lock()
	session.active--
	session.mu.Unlock()
	return gomutants.MutantResult{ID: request.Mutant, Outcome: gomutants.OutcomeKilled}, nil
}

func TestEvaluateRunsSeedMutantsConcurrentlyAndKeepsCatalogOrder(t *testing.T) {
	first, second := acceptedMutant(), acceptedMutant()
	first.ID, first.DisplayID = "mutant-a", "a"
	second.ID, second.DisplayID = "mutant-b", "b"
	session := &parallelSession{
		catalog: gomutants.Catalog{Mutants: []gomutants.Mutant{first, second}},
		started: make(chan string, 2), completed: make(chan string, 2),
		releaseA: make(chan struct{}), releaseB: make(chan struct{}),
	}
	type outcome struct {
		result assure.MutationEvaluation
		err    error
	}
	root := t.TempDir()
	progress := make(chan int, 2)
	done := make(chan outcome, 1)
	go func() {
		result, err := assure.EvaluateMutations(t.Context(), session, []assure.TargetEvidence{{
			Target: target("TestBoundary", goanalysis.KindTest), CoveredFiles: []string{"boundary.go"},
		}}, assure.MutationOptions{
			Root: root, Contract: "standard-v1", Jobs: 2,
			Progress: func(completed, _ int) { progress <- completed },
		})
		done <- outcome{result: result, err: err}
	}()
	for range 2 {
		select {
		case <-session.started:
		case <-time.After(5 * time.Second):
			t.Fatal("seed mutants did not overlap")
		}
	}
	close(session.releaseB)
	if got := <-session.completed; got != "mutant-b" {
		t.Fatalf("first completion = %q, want mutant-b", got)
	}
	close(session.releaseA)
	got := <-done
	if got.err != nil {
		t.Fatal(got.err)
	}
	if session.maximum != 2 {
		t.Fatalf("maximum concurrent executions = %d, want 2", session.maximum)
	}
	if len(got.result.Evidence) != 2 || got.result.Evidence[0].ID != "mutant-a" || got.result.Evidence[1].ID != "mutant-b" {
		t.Fatalf("evidence order = %+v", got.result.Evidence)
	}
	if first, second := <-progress, <-progress; first != 1 || second != 2 {
		t.Fatalf("progress = [%d %d], want [1 2]", first, second)
	}
}

func TestEvaluateRunsOnlyCoveringTargetsAndRecordsKill(t *testing.T) {
	mutant := acceptedMutant()
	session := &fakeSession{catalog: gomutants.Catalog{Mutants: []gomutants.Mutant{mutant}}}
	session.exec = func(request gomutants.ExecRequest) (gomutants.MutantResult, error) {
		return gomutants.MutantResult{ID: mutant.ID, Outcome: gomutants.OutcomeKilled}, nil
	}
	targets := []assure.TargetEvidence{
		{Target: target("TestUnrelated", goanalysis.KindTest), CoveredFiles: []string{"other.go"}},
		{Target: target("TestBoundary", goanalysis.KindTest), CoveredFiles: []string{"boundary.go"}, Environment: []string{"DB_URL=fixture"}},
	}

	result, err := assure.EvaluateMutations(t.Context(), session, targets, assure.MutationOptions{
		Root: t.TempDir(), Contract: "standard-v1", FuzzExecutions: 10_000, Timeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Findings) != 0 || len(result.Evidence) != 1 || result.Applied {
		t.Fatalf("result = %+v", result)
	}
	if len(session.requests) != 1 {
		t.Fatalf("requests = %+v", session.requests)
	}
	request := session.requests[0]
	if request.Package != "fixture.example/module" || strings.Join(request.Args, " ") != "-test.run=^TestBoundary$" {
		t.Fatalf("request = %+v", request)
	}
	if strings.Join(request.Env, " ") != "DB_URL=fixture" {
		t.Fatalf("request env = %v", request.Env)
	}
}

func TestEvaluateTriesTheShortestMeasuredReachingTargetFirst(t *testing.T) {
	mutant := acceptedMutant()
	session := &fakeSession{catalog: gomutants.Catalog{Mutants: []gomutants.Mutant{mutant}}}
	session.exec = func(request gomutants.ExecRequest) (gomutants.MutantResult, error) {
		return gomutants.MutantResult{ID: mutant.ID, Outcome: gomutants.OutcomeKilled}, nil
	}
	targets := []assure.TargetEvidence{
		{Target: target("TestSlowE2E", goanalysis.KindTest), CoveredFiles: []string{"boundary.go"}, Duration: 90 * time.Second},
		{Target: target("TestUnknownDuration", goanalysis.KindTest), CoveredFiles: []string{"boundary.go"}},
		{Target: target("TestFastUnit", goanalysis.KindTest), CoveredFiles: []string{"boundary.go"}, Duration: 25 * time.Millisecond},
	}
	_, err := assure.EvaluateMutations(t.Context(), session, targets, assure.MutationOptions{
		Root: t.TempDir(), Contract: "standard-v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(session.requests) != 1 || !hasArg(session.requests[0].Args, "-test.run=^TestFastUnit$") {
		t.Fatalf("requests = %+v, want only the shortest measured target", session.requests)
	}
}

func TestEvaluateCalibratesEachMutationTimeoutFromItsBaseline(t *testing.T) {
	mutant := acceptedMutant()
	for _, testCase := range []struct {
		name     string
		contract string
		duration time.Duration
		override time.Duration
		want     time.Duration
	}{
		{name: "minimum", contract: "standard-v1", duration: time.Second, want: 30 * time.Second},
		{name: "measured", contract: "standard-v1", duration: 12 * time.Second, want: 65 * time.Second},
		{name: "standard-cap", contract: "standard-v1", duration: 10 * time.Minute, want: 30 * time.Minute},
		{name: "deep-cap", contract: "deep-v1", duration: 2 * time.Hour, want: 5 * time.Hour},
		{name: "explicit-override", contract: "standard-v1", duration: time.Hour, override: 7 * time.Second, want: 7 * time.Second},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			session := &fakeSession{catalog: gomutants.Catalog{Mutants: []gomutants.Mutant{mutant}}}
			session.exec = func(request gomutants.ExecRequest) (gomutants.MutantResult, error) {
				return gomutants.MutantResult{ID: mutant.ID, Outcome: gomutants.OutcomeKilled}, nil
			}
			_, err := assure.EvaluateMutations(t.Context(), session, []assure.TargetEvidence{{
				Target: target("TestBoundary", goanalysis.KindTest), CoveredFiles: []string{"boundary.go"}, Duration: testCase.duration,
			}}, assure.MutationOptions{Root: t.TempDir(), Contract: testCase.contract, Timeout: testCase.override})
			if err != nil {
				t.Fatal(err)
			}
			if len(session.requests) != 1 || session.requests[0].Timeout != testCase.want {
				t.Fatalf("requests = %+v, want timeout %s", session.requests, testCase.want)
			}
		})
	}
}

func TestEvaluateFailsClosedWhenNoTargetReachesMutant(t *testing.T) {
	mutant := acceptedMutant()
	session := &fakeSession{
		catalog: gomutants.Catalog{Mutants: []gomutants.Mutant{mutant}},
		exec: func(gomutants.ExecRequest) (gomutants.MutantResult, error) {
			t.Fatal("unreachable mutant must not run a target")
			return gomutants.MutantResult{}, nil
		},
	}
	result, err := assure.EvaluateMutations(t.Context(), session, nil, assure.MutationOptions{Root: t.TempDir(), Contract: "standard-v1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Findings) != 1 || result.Findings[0].Kind != "unreached-mutant" {
		t.Fatalf("findings = %+v", result.Findings)
	}
}

func TestEvaluateHonoursAcceptanceForUnreachedMutant(t *testing.T) {
	mutant := acceptedMutant()
	session := &fakeSession{
		catalog: gomutants.Catalog{Mutants: []gomutants.Mutant{mutant}},
		exec: func(gomutants.ExecRequest) (gomutants.MutantResult, error) {
			t.Fatal("accepted unreachable mutant must not run a target")
			return gomutants.MutantResult{}, nil
		},
	}
	findingID := report.FindingID("mutation", mutant.ID)
	result, err := assure.EvaluateMutations(t.Context(), session, nil, assure.MutationOptions{
		Root: t.TempDir(), Contract: "standard-v1", Accepted: map[string]bool{findingID: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Findings) != 0 || len(result.Evidence) != 1 || result.Evidence[0].Status != "accepted" || result.Evidence[0].Detail != findingID {
		t.Fatalf("result = %+v", result)
	}
}

func TestEvaluatePromotesTargetedFuzzArtifactAndRequestsFreshRound(t *testing.T) {
	root := t.TempDir()
	mutant := acceptedMutant()
	session := &fakeSession{catalog: gomutants.Catalog{Mutants: []gomutants.Mutant{mutant}}}
	session.exec = func(request gomutants.ExecRequest) (gomutants.MutantResult, error) {
		if hasArgPrefix(request.Args, "-test.fuzz=") {
			return gomutants.MutantResult{
				ID: mutant.ID, Outcome: gomutants.OutcomeKilled,
				Artifacts: []gomutants.Artifact{{
					Path: "testdata/fuzz/FuzzBoundary/a1b2c3",
					Data: []byte("go test fuzz v1\n[]byte(\"boundary\")\n"),
				}},
			}, nil
		}
		return gomutants.MutantResult{ID: mutant.ID, Outcome: gomutants.OutcomeSurvived}, nil
	}
	targets := []assure.TargetEvidence{{
		Target: target("FuzzBoundary", goanalysis.KindFuzz), CoveredFiles: []string{"boundary.go"},
	}}

	result, err := assure.EvaluateMutations(t.Context(), session, targets, assure.MutationOptions{
		Root: root, Contract: "standard-v1", FuzzExecutions: 10_000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Applied || len(result.Repairs) != 1 || result.Repairs[0].Status != "applied" {
		t.Fatalf("result = %+v", result)
	}
	if len(session.requests) != 2 || !hasArg(session.requests[1].Args, "-test.fuzztime=10000x") {
		t.Fatalf("requests = %+v", session.requests)
	}
	data, err := os.ReadFile(filepath.Join(root, "testdata", "fuzz", "FuzzBoundary", "a1b2c3"))
	if err != nil || !strings.HasPrefix(string(data), "go test fuzz v1\n") {
		t.Fatalf("promoted corpus = %q, %v", data, err)
	}
}

func TestEvaluateNoApplyLeavesFuzzKillInsufficient(t *testing.T) {
	mutant := acceptedMutant()
	session := &fakeSession{catalog: gomutants.Catalog{Mutants: []gomutants.Mutant{mutant}}}
	session.exec = func(request gomutants.ExecRequest) (gomutants.MutantResult, error) {
		if hasArgPrefix(request.Args, "-test.fuzz=") {
			return gomutants.MutantResult{ID: mutant.ID, Outcome: gomutants.OutcomeKilled, Artifacts: []gomutants.Artifact{{
				Path: "testdata/fuzz/FuzzBoundary/candidate", Data: []byte("go test fuzz v1\n[]byte(\"x\")\n"),
			}}}, nil
		}
		return gomutants.MutantResult{ID: mutant.ID, Outcome: gomutants.OutcomeSurvived}, nil
	}
	result, err := assure.EvaluateMutations(t.Context(), session, []assure.TargetEvidence{{
		Target: target("FuzzBoundary", goanalysis.KindFuzz), CoveredFiles: []string{"boundary.go"},
	}}, assure.MutationOptions{Root: t.TempDir(), Contract: "standard-v1", NoApply: true})
	if err != nil {
		t.Fatal(err)
	}
	if result.Applied || len(result.Findings) != 1 || result.Findings[0].Kind != "unpersisted-fuzz-kill" {
		t.Fatalf("result = %+v", result)
	}
}

func acceptedMutant() gomutants.Mutant {
	return gomutants.Mutant{
		ID: "mutant-full-id", DisplayID: "mutant-1", Accepted: true,
		Path: "boundary.go", Package: "fixture.example/module", Line: 4,
		Rule: "lt-to-le", Original: "<", Replacement: "<=",
	}
}

func target(name string, kind goanalysis.TargetKind) goanalysis.Target {
	return goanalysis.Target{ID: "target-" + name, Name: name, Kind: kind, Package: "fixture.example/module", RelativeDir: "."}
}

func hasArg(arguments []string, want string) bool {
	for _, argument := range arguments {
		if argument == want {
			return true
		}
	}
	return false
}

func hasArgPrefix(arguments []string, prefix string) bool {
	for _, argument := range arguments {
		if strings.HasPrefix(argument, prefix) {
			return true
		}
	}
	return false
}
