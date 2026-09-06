// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package assure_test

import (
	"context"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	gomutants "github.com/P4suta/go-mutants"
	"github.com/P4suta/goatest/internal/assure"
	goanalysis "github.com/P4suta/goatest/internal/golang"
	"github.com/P4suta/goatest/internal/report"
	"github.com/P4suta/goatest/internal/testkit"
)

const (
	concurrentMutationDeadline  = 5 * time.Second
	mutationTestContainment     = time.Duration(math.MaxInt64)
	mutationTestControlDuration = time.Millisecond
	mutationExecutionCount      = 1
)

func mutationOptionsForTest(options assure.MutationOptions) assure.MutationOptions {
	if options.Timeout <= 0 {
		options.Timeout = mutationTestContainment
	}
	if options.OriginalControl == nil {
		options.OriginalControl = func(context.Context, gomutants.ExecRequest) (gomutants.CommandResult, error) {
			return gomutants.CommandResult{Duration: mutationTestControlDuration}, nil
		}
	}
	return options
}

func evaluateMutationsForTest(ctx context.Context, session assure.MutationSession, targets []assure.TargetEvidence, options assure.MutationOptions) (assure.MutationEvaluation, error) {
	return assure.EvaluateMutations(ctx, session, targets, mutationOptionsForTest(options))
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

func (session *parallelSession) Probe(context.Context, gomutants.ProbeRequest) (gomutants.ProbeResult, error) {
	return gomutants.ProbeResult{Outcome: gomutants.ProbeUnavailable}, nil
}

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
	return gomutants.MutantResult{ID: request.Mutant, Outcome: gomutants.OutcomeSurvived}, nil
}

func TestEvaluateRunsSeedMutantsConcurrentlyAndKeepsCatalogOrder(t *testing.T) {
	first, second := acceptedMutant(), acceptedMutant()
	first.ID, first.DisplayID = "mutant-a", "a"
	second.ID, second.DisplayID = "mutant-b", "b"
	mutants := []gomutants.Mutant{first, second}
	session := &parallelSession{
		catalog: gomutants.Catalog{Mutants: mutants},
		started: make(chan string, len(mutants)), completed: make(chan string, len(mutants)),
		releaseA: make(chan struct{}), releaseB: make(chan struct{}),
	}
	type outcome struct {
		result assure.MutationEvaluation
		err    error
	}
	progress := make(chan int, len(mutants))
	done := make(chan outcome, 1)
	go func() {
		result, err := evaluateMutationsForTest(t.Context(), session, []assure.TargetEvidence{{
			Target: target("TestBoundary", goanalysis.KindTest), CoveredFiles: []string{"boundary.go"}, Duration: time.Second,
		}}, assure.MutationOptions{
			Jobs:     len(mutants),
			Progress: func(completed, _ int) { progress <- completed },
		})

		done <- outcome{result: result, err: err}
	}()
	for range mutants {
		select {
		case <-session.started:
		case <-time.After(concurrentMutationDeadline):
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
	if session.maximum != len(mutants) {
		t.Fatalf("maximum concurrent executions = %d, want 2", session.maximum)
	}
	if len(got.result.Findings) != len(mutants) || got.result.Findings[0].MutantID != "mutant-a" || got.result.Findings[1].MutantID != "mutant-b" {
		t.Fatalf("finding order = %+v", got.result.Findings)
	}
	for want := 1; want <= len(mutants); want++ {
		select {
		case got := <-progress:
			if got != want {
				t.Fatalf("progress %d = %d, want %d", want, got, want)
			}
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for progress %d", want)
		}
	}
}

func TestEvaluateRunsOnlyCoveringTargetsAndRecordsKill(t *testing.T) {
	mutant := acceptedMutant()
	session := testkit.NewSession(gomutants.Catalog{Mutants: []gomutants.Mutant{mutant}})
	session.On(mutant.ID).Return(gomutants.MutantResult{ID: mutant.ID, Outcome: gomutants.OutcomeKilled})
	targets := []assure.TargetEvidence{
		{Target: target("TestUnrelated", goanalysis.KindTest), CoveredFiles: []string{"other.go"}, Duration: time.Second},
		{Target: target("TestBoundary", goanalysis.KindTest), CoveredFiles: []string{"boundary.go"}, Environment: []string{"DB_URL=fixture"}, Duration: time.Second},
	}

	result, err := evaluateMutationsForTest(t.Context(), session, targets, assure.MutationOptions{
		Timeout: time.Second,
	})

	if err != nil {
		t.Fatal(err)
	}
	if len(result.Findings) != 0 || len(result.Evidence) != 1 {
		t.Fatalf("result = %+v", result)
	}
	requests := session.Requests()
	if len(requests) != mutationExecutionCount {
		t.Fatalf("requests = %+v", requests)
	}
	request := requests[0]
	if request.Package != "fixture.example/module" || strings.Join(request.Args, " ") != "-test.run=^TestBoundary$" {
		t.Fatalf("request = %+v", request)
	}
	if strings.Join(request.Env, " ") != "DB_URL=fixture" {
		t.Fatalf("request env = %v", request.Env)
	}
}

func TestEvaluateAggregatesReachingTargetsInCanonicalOrder(t *testing.T) {
	mutant := acceptedMutant()
	session := testkit.NewSession(gomutants.Catalog{Mutants: []gomutants.Mutant{mutant}})
	session.On(mutant.ID).Return(gomutants.MutantResult{ID: mutant.ID, Outcome: gomutants.OutcomeKilled})
	targets := []assure.TargetEvidence{
		{Target: target("TestSlowE2E", goanalysis.KindTest), CoveredFiles: []string{"boundary.go"}, Duration: 90 * time.Second},
		{Target: target("TestUnknownDuration", goanalysis.KindTest), CoveredFiles: []string{"boundary.go"}},
		{Target: target("TestFastUnit", goanalysis.KindTest), CoveredFiles: []string{"boundary.go"}, Duration: 25 * time.Millisecond},
	}
	_, err := evaluateMutationsForTest(t.Context(), session, targets, assure.MutationOptions{})

	if err != nil {
		t.Fatal(err)
	}
	requests := session.Requests()
	if len(requests) != mutationExecutionCount || !hasArg(requests[0].Args, "-test.run=^(TestFastUnit|TestSlowE2E|TestUnknownDuration)$") {
		t.Fatalf("requests = %+v, want one exact aggregate in canonical order", requests)
	}
}

func TestEvaluateBoundsEachMutationBySameRunControlDuration(t *testing.T) {
	mutant := acceptedMutant()
	for _, testCase := range []struct {
		name     string
		contract string
		duration time.Duration
		probe    time.Duration
		limit    time.Duration
		want     time.Duration
	}{
		{name: "small control", contract: "standard-v1", duration: time.Second, want: time.Second + mutationTestControlDuration},
		{name: "configured limit above budget", contract: "standard-v1", duration: time.Second, limit: 10 * time.Minute, want: time.Second + mutationTestControlDuration},
		{name: "measured", contract: "standard-v1", duration: 12 * time.Second, want: 12*time.Second + mutationTestControlDuration},
		{name: "independent controls add", contract: "standard-v1", duration: time.Second, probe: 3 * time.Second, want: 4*time.Second + mutationTestControlDuration},
		{name: "standard contract adds no policy cap", contract: "standard-v1", duration: 10 * time.Minute, want: 10*time.Minute + mutationTestControlDuration},
		{name: "deep contract adds no policy cap", contract: "deep-v1", duration: 2 * time.Hour, want: 2*time.Hour + mutationTestControlDuration},
		{name: "configured-limit-below-calibration", contract: "standard-v1", duration: time.Hour, limit: 7 * time.Second, want: 7 * time.Second},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			session := testkit.NewSession(gomutants.Catalog{Mutants: []gomutants.Mutant{mutant}})
			session.On(mutant.ID).Return(gomutants.MutantResult{ID: mutant.ID, Outcome: gomutants.OutcomeKilled})
			_, err := evaluateMutationsForTest(t.Context(), session, []assure.TargetEvidence{{
				Target: target("TestBoundary", goanalysis.KindTest), CoveredFiles: []string{"boundary.go"},
				Duration: testCase.duration, ProbeDuration: testCase.probe,
			}}, assure.MutationOptions{Timeout: testCase.limit})

			if err != nil {
				t.Fatal(err)
			}
			requests := session.Requests()
			if len(requests) != mutationExecutionCount || requests[0].Timeout != testCase.want {
				t.Fatalf("requests = %+v, want timeout %s", requests, testCase.want)
			}
		})
	}
}

func TestEvaluateFailsClosedWhenNoTargetReachesMutant(t *testing.T) {
	mutant := acceptedMutant()
	session := testkit.NewSession(gomutants.Catalog{Mutants: []gomutants.Mutant{mutant}})
	session.On(mutant.ID).Do(func(request gomutants.ExecRequest) (gomutants.MutantResult, error) {
		if request.Package != mutant.Package {
			t.Fatalf("fallback package = %q", request.Package)
		}
		return gomutants.MutantResult{ID: mutant.ID, Outcome: gomutants.OutcomeSurvived}, nil
	})
	result, err := evaluateMutationsForTest(t.Context(), session, nil, assure.MutationOptions{
		SuiteCoverage: map[string]assure.PackageSuiteCoverage{mutant.Package: {Duration: time.Second}},
	})

	if err != nil {
		t.Fatal(err)
	}
	if len(result.Findings) != 1 || result.Findings[0].Kind != "unreached-mutant" || len(session.Requests()) != 1 {
		t.Fatalf("findings = %+v", result.Findings)
	}
}

func TestEvaluateHonoursAcceptanceForUnreachedMutant(t *testing.T) {
	mutant := acceptedMutant()
	session := testkit.NewSession(gomutants.Catalog{Mutants: []gomutants.Mutant{mutant}})
	session.On(mutant.ID).Do(func(request gomutants.ExecRequest) (gomutants.MutantResult, error) {
		return gomutants.MutantResult{ID: request.Mutant, Outcome: gomutants.OutcomeSurvived}, nil
	})
	findingID := report.FindingID("mutation", mutant.ID)
	result, err := evaluateMutationsForTest(t.Context(), session, nil, assure.MutationOptions{
		Accepted:      map[string]bool{findingID: true},
		SuiteCoverage: map[string]assure.PackageSuiteCoverage{mutant.Package: {Duration: time.Second}},
	})

	if err != nil {
		t.Fatal(err)
	}
	if len(result.Findings) != 0 || len(result.Evidence) != 1 || result.Evidence[0].Status != "accepted" || result.Evidence[0].Detail != findingID || len(session.Requests()) != 1 {
		t.Fatalf("result = %+v", result)
	}
}

func TestEvaluateRunsFuzzSeedCorpusAsDeterministicTarget(t *testing.T) {
	mutant := acceptedMutant()
	session := testkit.NewSession(gomutants.Catalog{Mutants: []gomutants.Mutant{mutant}})
	session.On(mutant.ID).Return(gomutants.MutantResult{ID: mutant.ID, Outcome: gomutants.OutcomeKilled})
	targets := []assure.TargetEvidence{{
		Target: target("FuzzBoundary", goanalysis.KindFuzz), CoveredFiles: []string{"boundary.go"}, Duration: time.Second,
	}}

	result, err := evaluateMutationsForTest(t.Context(), session, targets, assure.MutationOptions{
		Timeout: time.Second,
		OriginalControl: func(context.Context, gomutants.ExecRequest) (gomutants.CommandResult, error) {
			return gomutants.CommandResult{Duration: time.Millisecond}, nil
		},
	})

	if err != nil {
		t.Fatal(err)
	}
	if len(result.Findings) != 0 || len(result.Evidence) != 1 || result.Evidence[0].Status != "killed" {
		t.Fatalf("result = %+v", result)
	}
	requests := session.Requests()
	if len(requests) != mutationExecutionCount || !hasArg(requests[0].Args, "-test.run=^FuzzBoundary$") || hasArgPrefix(requests[0].Args, "-test.fuzz=") {
		t.Fatalf("requests = %+v", requests)
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
