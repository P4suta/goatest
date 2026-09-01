// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package assure_test

import (
	"context"
	"strings"
	"sync"
	"testing"

	gomutants "github.com/P4suta/go-mutants"
	"github.com/P4suta/goatest/internal/assure"
	goanalysis "github.com/P4suta/goatest/internal/golang"
)

// controlRecorder answers every original control with one scripted result and
// remembers each distinct control command it was asked to run, so a test can
// state exactly how many controls a run of kills is allowed to cost.
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

// killingSession kills every mutant on its first and confirming execution.
type killingSession struct{ catalog gomutants.Catalog }

func (session *killingSession) Catalog() gomutants.Catalog { return session.catalog }

func (session *killingSession) Exec(_ context.Context, request gomutants.ExecRequest) (gomutants.MutantResult, error) {
	return gomutants.MutantResult{ID: request.Mutant, Outcome: gomutants.OutcomeKilled}, nil
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
		{Target: target("TestBoundary", goanalysis.KindTest), CoveredFiles: []string{"boundary.go"}},
		{Target: target("TestOther", goanalysis.KindTest), CoveredFiles: []string{"other.go"}},
	}
}

// Three kills confirmed by one target must cost one original control, not
// three: within a run the snapshot is frozen, so a control command's verdict
// cannot change between the kills that share it. A kill in another file goes
// through its own target and pays for its own control.
func TestEvaluateRunsOneOriginalControlForEachDistinctControlCommand(t *testing.T) {
	t.Parallel()
	session := &killingSession{catalog: gomutants.Catalog{Mutants: controlMutants()}}
	recorder := &controlRecorder{result: gomutants.CommandResult{ExitCode: 0}}
	evaluation, err := assure.EvaluateMutations(t.Context(), session, controlTargets(), assure.MutationOptions{
		Root: t.TempDir(), Contract: "standard-v1", Jobs: 1,
		OriginalControl: recorder.run,
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
		t.Fatalf("kills = %d, findings = %+v, want 4 confirmed kills", kills, evaluation.Findings)
	}
	if len(recorder.calls) != 2 || recorder.total() != 2 {
		t.Fatalf("original controls = %d across %d commands, want exactly one per distinct control command (2)",
			recorder.total(), len(recorder.calls))
	}
}

// A control that fails is as memoizable as one that passes: every kill that
// shares the command becomes the same flaky-mutation-control finding without
// paying to watch the control fail again.
func TestEvaluateMemoizesAFailedControlWithoutRerunningIt(t *testing.T) {
	t.Parallel()
	mutants := controlMutants()[:3]
	session := &killingSession{catalog: gomutants.Catalog{Mutants: mutants}}
	recorder := &controlRecorder{result: gomutants.CommandResult{ExitCode: 1, Output: []byte("boundary regressed")}}
	evaluation, err := assure.EvaluateMutations(t.Context(), session, controlTargets()[:1], assure.MutationOptions{
		Root: t.TempDir(), Contract: "standard-v1", Jobs: 1,
		OriginalControl: recorder.run,
	})
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	flaky := 0
	for _, finding := range evaluation.Findings {
		if finding.Kind == "flaky-mutation-control" {
			flaky++
		}
	}
	if flaky != 3 {
		t.Fatalf("findings = %+v, want 3 flaky-mutation-control", evaluation.Findings)
	}
	if recorder.total() != 1 {
		t.Fatalf("original controls = %d, want the failure observed once and remembered", recorder.total())
	}
}
