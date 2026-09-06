// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package assure_test

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	gomutants "github.com/P4suta/go-mutants"
	"github.com/P4suta/goatest/internal/assure"
	goanalysis "github.com/P4suta/goatest/internal/golang"
)

const (
	controlObservationDuration = time.Millisecond
	controlContainmentTimeout  = time.Second
)

type controlRecorder struct {
	mu     sync.Mutex
	calls  map[string]int
	result gomutants.CommandResult
}

func (recorder *controlRecorder) run(_ context.Context, request gomutants.ExecRequest) (gomutants.CommandResult, error) {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	if recorder.calls == nil {
		recorder.calls = map[string]int{}
	}
	key := request.Package + "\x00" + strings.Join(request.Args, "\x00") + "\x00" + strings.Join(request.Env, "\x00")
	recorder.calls[key]++
	return recorder.result, nil
}

func (recorder *controlRecorder) total() int {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	total := 0
	for _, count := range recorder.calls {
		total += count
	}
	return total
}

type killingSession struct{ catalog gomutants.Catalog }

func (session *killingSession) Catalog() gomutants.Catalog { return session.catalog }

func (session *killingSession) Probe(context.Context, gomutants.ProbeRequest) (gomutants.ProbeResult, error) {
	return gomutants.ProbeResult{Outcome: gomutants.ProbeUnavailable}, nil
}

func (session *killingSession) Exec(_ context.Context, request gomutants.ExecRequest) (gomutants.MutantResult, error) {
	return gomutants.MutantResult{ID: request.Mutant, Outcome: gomutants.OutcomeKilled}, nil
}

type suiteControlSession struct {
	catalog  gomutants.Catalog
	mu       sync.Mutex
	requests []gomutants.ExecRequest
	outcomes []gomutants.Outcome
	exec     func(gomutants.ExecRequest) (gomutants.MutantResult, error)
}

func (session *suiteControlSession) Catalog() gomutants.Catalog { return session.catalog }

func (session *suiteControlSession) Probe(context.Context, gomutants.ProbeRequest) (gomutants.ProbeResult, error) {
	return gomutants.ProbeResult{Outcome: gomutants.ProbeUnavailable}, nil
}

func (session *suiteControlSession) Exec(_ context.Context, request gomutants.ExecRequest) (gomutants.MutantResult, error) {
	session.mu.Lock()
	session.requests = append(session.requests, request)
	index := len(session.requests) - 1
	session.mu.Unlock()
	if session.exec != nil {
		return session.exec(request)
	}
	outcome := gomutants.OutcomeSurvived
	if index < len(session.outcomes) {
		outcome = session.outcomes[index]
	}
	return gomutants.MutantResult{ID: request.Mutant, Outcome: outcome}, nil
}

func (session *suiteControlSession) recordedRequests() []gomutants.ExecRequest {
	session.mu.Lock()
	defer session.mu.Unlock()
	return append([]gomutants.ExecRequest(nil), session.requests...)
}

func controlMutants() []gomutants.Mutant {
	first, second, third, other := acceptedMutant(), acceptedMutant(), acceptedMutant(), acceptedMutant()
	first.ID, first.DisplayID = "mutant-a", "a"
	second.ID, second.DisplayID = "mutant-b", "b"
	third.ID, third.DisplayID = "mutant-c", "c"
	other.ID, other.DisplayID, other.Path = "mutant-d", "d", "other.go"
	return []gomutants.Mutant{first, second, third, other}
}

func controlTargets() []assure.TargetEvidence {
	return []assure.TargetEvidence{
		{Target: target("TestBoundary", goanalysis.KindTest), CoveredFiles: []string{"boundary.go"}, Duration: time.Millisecond},
		{Target: target("TestOther", goanalysis.KindTest), CoveredFiles: []string{"other.go"}, Duration: time.Millisecond},
	}
}

func TestEvaluateRunsOneOriginalControlForEachDistinctControlCommand(t *testing.T) {
	t.Parallel()
	session := &killingSession{catalog: gomutants.Catalog{Mutants: controlMutants()}}
	recorder := &controlRecorder{result: gomutants.CommandResult{ExitCode: 0, Duration: controlObservationDuration}}
	evaluation, err := assure.EvaluateMutations(t.Context(), session, controlTargets(), assure.MutationOptions{
		Jobs:    1,
		Timeout: controlContainmentTimeout, OriginalControl: recorder.run,
	})
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	kills := 0
	for _, evidence := range evaluation.Evidence {
		if evidence.Kind == "mutation" && evidence.Status == "killed" {
			kills++
		}
	}
	if kills != 4 || len(evaluation.Findings) != 0 {
		t.Fatalf("kills = %d, findings = %+v, want 4 comparative kills", kills, evaluation.Findings)
	}
	if len(recorder.calls) != 2 || recorder.total() != 2 {
		t.Fatalf("original controls = %d across %d commands, want exactly one per distinct control command (2)",
			recorder.total(), len(recorder.calls))
	}
}

func TestEvaluateMemoizesAFailedControlWithoutRerunningIt(t *testing.T) {
	t.Parallel()
	mutants := controlMutants()[:3]
	session := &killingSession{catalog: gomutants.Catalog{Mutants: mutants}}
	recorder := &controlRecorder{result: gomutants.CommandResult{ExitCode: 1, Output: []byte("boundary regressed")}}
	evaluation, err := assure.EvaluateMutations(t.Context(), session, controlTargets()[:1], assure.MutationOptions{
		Jobs:    1,
		Timeout: controlContainmentTimeout, OriginalControl: recorder.run,
	})
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	unavailable := 0
	for _, finding := range evaluation.Findings {
		if finding.Kind == "mutation-control-failure" {
			unavailable++
		}
	}
	if unavailable != len(mutants) {
		t.Fatalf("findings = %+v, want 3 mutation-control-failure", evaluation.Findings)
	}
	if recorder.total() != 1 {
		t.Fatalf("original controls = %d, want the failure observed once and remembered", recorder.total())
	}
}

func TestEvaluateChecksAnUnreachedPackageControlOnceBeforeAnyMutant(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name    string
		control gomutants.CommandResult
		kind    string
	}{
		{name: "failed", control: gomutants.CommandResult{ExitCode: 1, Output: []byte("suite setup failed")}, kind: "mutation-control-failure"},
		{name: "timed out", control: gomutants.CommandResult{TimedOut: true}, kind: "mutation-control-timeout"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			mutants := controlMutants()[:3]
			session := &suiteControlSession{catalog: gomutants.Catalog{Mutants: mutants}}
			recorder := &controlRecorder{result: test.control}
			evaluation, err := assure.EvaluateMutations(t.Context(), session, nil, assure.MutationOptions{
				Jobs:    len(mutants),
				Timeout: time.Second, OriginalControl: recorder.run,
				SuiteCoverage: map[string]assure.PackageSuiteCoverage{mutants[0].Package: {Duration: controlObservationDuration}},
			})
			if err != nil {
				t.Fatal(err)
			}
			if recorder.total() != 1 || len(session.recordedRequests()) != 0 || len(evaluation.Findings) != len(mutants) {
				t.Fatalf("control calls = %d, mutant requests = %+v, findings = %+v",
					recorder.total(), session.recordedRequests(), evaluation.Findings)
			}
			for _, finding := range evaluation.Findings {
				if finding.Kind != test.kind {
					t.Fatalf("finding = %+v, want kind %q", finding, test.kind)
				}
			}
		})
	}
}

func TestPassingUnreachedPackageControlCalibratesTheMutantWatchdog(t *testing.T) {
	t.Parallel()
	mutant := controlMutants()[0]
	session := &suiteControlSession{catalog: gomutants.Catalog{Mutants: []gomutants.Mutant{mutant}}}
	recorder := &controlRecorder{result: gomutants.CommandResult{Duration: 250 * time.Millisecond}}
	evaluation, err := assure.EvaluateMutations(t.Context(), session, nil, assure.MutationOptions{
		Timeout: time.Second, OriginalControl: recorder.run,
		SuiteEnvironment: []string{"DB=ready"},
		SuiteCoverage:    map[string]assure.PackageSuiteCoverage{mutant.Package: {Duration: controlObservationDuration}},
	})
	if err != nil {
		t.Fatal(err)
	}
	requests := session.recordedRequests()
	if recorder.total() != 1 || len(requests) != 1 || requests[0].Timeout != 250*time.Millisecond+controlObservationDuration ||
		!slices.Equal(requests[0].Env, []string{"DB=ready"}) {
		t.Fatalf("control calls = %d, mutant requests = %+v", recorder.total(), requests)
	}
	if len(evaluation.Findings) != 1 || evaluation.Findings[0].Kind != "unreached-mutant" {
		t.Fatalf("evaluation = %+v", evaluation)
	}
}

func TestTimeoutRunsAgainOnceUnderTheContainmentCeiling(t *testing.T) {
	t.Parallel()
	const controlDuration = time.Millisecond
	for _, test := range []struct {
		name               string
		controls           []gomutants.CommandResult
		outcomes           []gomutants.Outcome
		wantKind           string
		wantControls       int
		wantSecondDeadline time.Duration
	}{
		{
			name:     "a completed second control buys the containment ceiling",
			controls: []gomutants.CommandResult{{Duration: controlDuration}},
			outcomes: []gomutants.Outcome{gomutants.OutcomeTimedOut, gomutants.OutcomeSurvived},
			wantKind: "unreached-mutant", wantControls: 2, wantSecondDeadline: time.Second,
		},
		{
			name: "a second control that expires buys nothing",
			controls: []gomutants.CommandResult{
				{Duration: controlDuration}, {TimedOut: true},
			},
			outcomes: []gomutants.Outcome{gomutants.OutcomeTimedOut},
			wantKind: "mutation-timeout", wantControls: 2,
		},
		{
			name: "original timed out", controls: []gomutants.CommandResult{{TimedOut: true}},
			wantKind: "mutation-control-timeout", wantControls: 1,
		},
		{
			name: "original failed", controls: []gomutants.CommandResult{{ExitCode: 1, Output: []byte("control failed")}},
			wantKind: "mutation-control-failure", wantControls: 1,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			mutant := controlMutants()[0]
			session := &suiteControlSession{
				catalog: gomutants.Catalog{Mutants: []gomutants.Mutant{mutant}}, outcomes: test.outcomes,
			}
			controlCalls := 0
			control := func(context.Context, gomutants.ExecRequest) (gomutants.CommandResult, error) {
				result := test.controls[min(controlCalls, len(test.controls)-1)]
				controlCalls++
				return result, nil
			}
			evaluation, err := assure.EvaluateMutations(t.Context(), session, nil, assure.MutationOptions{
				Timeout: time.Second, OriginalControl: control,
				SuiteCoverage: map[string]assure.PackageSuiteCoverage{mutant.Package: {Duration: controlObservationDuration}},
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(evaluation.Findings) != 1 || evaluation.Findings[0].Kind != test.wantKind {
				t.Fatalf("findings = %+v, want %q", evaluation.Findings, test.wantKind)
			}
			requests := session.recordedRequests()
			if controlCalls != test.wantControls || len(requests) != len(test.outcomes) {
				t.Fatalf("control calls = %d, mutant requests = %+v", controlCalls, requests)
			}
			if test.wantSecondDeadline != 0 && requests[1].Timeout != test.wantSecondDeadline {
				t.Fatalf("second deadline = %s, want the containment ceiling %s", requests[1].Timeout, test.wantSecondDeadline)
			}
		})
	}
}

func TestKilledMutationRunsOnceAfterItsExactOriginalControl(t *testing.T) {
	t.Parallel()
	mutant := controlMutants()[0]
	session := &suiteControlSession{
		catalog: gomutants.Catalog{Mutants: []gomutants.Mutant{mutant}},
		outcomes: []gomutants.Outcome{
			gomutants.OutcomeKilled,
			gomutants.OutcomeTimedOut,
		},
	}
	controlCalls := 0
	control := func(context.Context, gomutants.ExecRequest) (gomutants.CommandResult, error) {
		controlCalls++
		return gomutants.CommandResult{Duration: time.Millisecond}, nil
	}
	evaluation, err := assure.EvaluateMutations(t.Context(), session, nil, assure.MutationOptions{
		Timeout: time.Second, OriginalControl: control,
		SuiteCoverage: map[string]assure.PackageSuiteCoverage{mutant.Package: {Duration: controlObservationDuration}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if controlCalls != 1 || len(session.recordedRequests()) != 1 {
		t.Fatalf("control calls = %d, mutant requests = %+v", controlCalls, session.recordedRequests())
	}
	if len(evaluation.Findings) != 0 || evaluation.Accounting.Killed != 1 {
		t.Fatalf("evaluation = %+v", evaluation)
	}
}

func TestExactOriginalAndMutantUseCompletedCleanDurations(t *testing.T) {
	t.Parallel()
	const (
		targetDuration        = 3 * time.Millisecond
		probeDuration         = 2 * time.Millisecond
		suiteDuration         = 5 * time.Millisecond
		controlDuration       = 7 * time.Millisecond
		cleanDurationSum      = targetDuration + probeDuration + suiteDuration
		expectedMutantTimeout = cleanDurationSum + controlDuration
	)
	mutant := controlMutants()[0]
	session := &suiteControlSession{catalog: gomutants.Catalog{Mutants: []gomutants.Mutant{mutant}}}
	session.exec = func(request gomutants.ExecRequest) (gomutants.MutantResult, error) {
		return gomutants.MutantResult{ID: request.Mutant, Outcome: gomutants.OutcomeKilled}, nil
	}
	var controlRequest gomutants.ExecRequest
	control := func(_ context.Context, request gomutants.ExecRequest) (gomutants.CommandResult, error) {
		controlRequest = request
		return gomutants.CommandResult{Duration: controlDuration}, nil
	}
	evaluation, err := assure.EvaluateMutations(t.Context(), session, []assure.TargetEvidence{{
		Target: target("TestBoundary", goanalysis.KindTest), CoveredFiles: []string{"boundary.go"},
		Duration: targetDuration, ProbeDuration: probeDuration,
	}}, assure.MutationOptions{
		Timeout: time.Second, OriginalControl: control,
		SuiteCoverage: map[string]assure.PackageSuiteCoverage{mutant.Package: {Duration: suiteDuration}},
	})
	if err != nil {
		t.Fatal(err)
	}
	requests := session.recordedRequests()
	if controlRequest.Timeout != time.Second || len(requests) != 1 || requests[0].Timeout != expectedMutantTimeout {
		t.Fatalf("control request = %+v, mutation requests = %+v", controlRequest, requests)
	}
	if evaluation.Accounting.Killed != 1 || len(evaluation.Findings) != 0 {
		t.Fatalf("evaluation = %+v", evaluation)
	}
}

func TestMutationWithoutAnObservationOrControlIsNotStarted(t *testing.T) {
	t.Parallel()
	mutant := controlMutants()[0]
	session := &suiteControlSession{
		catalog:  gomutants.Catalog{Mutants: []gomutants.Mutant{mutant}},
		outcomes: []gomutants.Outcome{gomutants.OutcomeTimedOut},
	}
	evaluation, err := assure.EvaluateMutations(t.Context(), session, nil, assure.MutationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(evaluation.Findings) != 1 || evaluation.Findings[0].Kind != "mutation-control-unavailable" ||
		len(session.recordedRequests()) != 0 {
		t.Fatalf("evaluation = %+v, mutant requests = %+v", evaluation, session.recordedRequests())
	}
}

func TestMutationWithoutAnExactControlIsNotStarted(t *testing.T) {
	t.Parallel()
	mutant := controlMutants()[0]
	session := &suiteControlSession{
		catalog:  gomutants.Catalog{Mutants: []gomutants.Mutant{mutant}},
		outcomes: []gomutants.Outcome{gomutants.OutcomeKilled},
	}
	evaluation, err := assure.EvaluateMutations(t.Context(), session, controlTargets()[:1], assure.MutationOptions{
		Timeout: controlContainmentTimeout,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(evaluation.Findings) != 1 || evaluation.Findings[0].Kind != "mutation-control-unavailable" ||
		len(session.recordedRequests()) != 0 {
		t.Fatalf("evaluation = %+v, mutant requests = %+v", evaluation, session.recordedRequests())
	}
}
