// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package assure

import (
	"errors"
	"slices"
	"testing"
	"time"

	gomutants "github.com/P4suta/go-mutants"
	"github.com/P4suta/goatest/internal/mutationbridge"
	"github.com/P4suta/goatest/internal/trace"
)

var traceSessionOrigin = time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)

func newTraceRecording() (*trace.MemorySink, *trace.Recorder) {
	sink := trace.NewMemorySink(0)
	return sink, trace.New(sink, func() time.Time { return traceSessionOrigin })
}

func recordedMutants(sink *trace.MemorySink) []trace.MutantRecord {
	var records []trace.MutantRecord
	for _, event := range sink.Events() {
		if event.Type == trace.TypeMutantExec && event.Mutant != nil {
			records = append(records, *event.Mutant)
		}
	}
	return records
}

func TestMutationExecutionRecordsTheSemanticAttempt(t *testing.T) {
	t.Parallel()
	catalog := gomutants.Catalog{Mutants: []gomutants.Mutant{{ID: "mutant-1", Accepted: true}}}
	session := &mutationUnitSession{catalog: catalog, exec: func(gomutants.ExecRequest) (gomutants.MutantResult, error) {
		return gomutants.MutantResult{
			ID: "mutant-1", DisplayID: "boundary#1", Outcome: gomutants.OutcomeKilled,
			KilledBy: "TestBoundary", Duration: 2500 * time.Millisecond,
		}, nil
	}}
	sink, recorder := newTraceRecording()
	args := []string{"-test.run=^TestBoundary$", "-test.testlogfile=/tmp/private-action.log"}
	result, _, err := executeMutation(t.Context(), session, gomutants.ExecRequest{
		Mutant: "mutant", Package: "fixture.example/module/pkg", Args: args, Timeout: 30 * time.Second,
	}, MutationOptions{Trace: recorder})
	if err != nil || result.Outcome != gomutants.OutcomeKilled || result.KilledBy != "TestBoundary" {
		t.Fatalf("Exec = (%+v, %v)", result, err)
	}
	if len(session.requests) != 1 || session.requests[0].Mutant != "mutant" {
		t.Fatalf("forwarded requests = %+v", session.requests)
	}
	args[0] = "mutated"
	records := recordedMutants(sink)
	if len(records) != 1 {
		t.Fatalf("mutant records = %+v", records)
	}
	record := records[0]
	if record.ID != "mutant-1" || record.DisplayID != "boundary#1" || record.Package != "fixture.example/module/pkg" {
		t.Fatalf("recorded identity = %+v", record)
	}
	if !slices.Equal(record.Args, []string{"-test.run=^TestBoundary$"}) || record.TimeoutMS != 30_000 {
		t.Fatalf("recorded plan = %+v", record)
	}
	if record.Outcome != string(gomutants.OutcomeKilled) || record.KilledBy != "TestBoundary" || record.DurationMS != 2500 || record.Error != "" {
		t.Fatalf("recorded outcome = %+v", record)
	}
}

func TestMutationExecutionRecordsAnExecutionThatProducedNoOutcome(t *testing.T) {
	t.Parallel()
	cause := errors.New("mutant execution failed")
	session := &mutationUnitSession{exec: func(gomutants.ExecRequest) (gomutants.MutantResult, error) {
		return gomutants.MutantResult{}, cause
	}}
	sink, recorder := newTraceRecording()
	if _, _, err := executeMutation(t.Context(), session, gomutants.ExecRequest{Mutant: "mutant-1", Timeout: -time.Second}, MutationOptions{Trace: recorder}); !errors.Is(err, cause) {
		t.Fatalf("Exec error = %v", err)
	}
	records := recordedMutants(sink)
	if len(records) != 1 {
		t.Fatalf("mutant records = %+v", records)
	}
	record := records[0]
	if record.ID != "mutant-1" || record.Outcome != "" || record.Error != "mutant execution failed" {
		t.Fatalf("recorded failure = %+v", record)
	}
	if record.TimeoutMS != 0 || record.DurationMS != 0 {
		t.Fatalf("recorded failure carries nonsense measurements: %+v", record)
	}
}

func TestMutationExecutionWithoutRecorderStaysTransparent(t *testing.T) {
	t.Parallel()
	session := &mutationUnitSession{catalog: gomutants.Catalog{Mutants: []gomutants.Mutant{{ID: "mutant-1", Accepted: true}}}}
	result, _, err := executeMutation(t.Context(), session, gomutants.ExecRequest{Mutant: "mutant-1"}, MutationOptions{})
	if err != nil || result.Outcome != gomutants.OutcomeSurvived {
		t.Fatalf("Exec = (%+v, %v)", result, err)
	}
	if len(session.requests) != 1 {
		t.Fatalf("forwarded requests = %+v", session.requests)
	}
}

func TestPrepareMutationSessionReportsTheWorkspaceFailure(t *testing.T) {
	t.Parallel()
	session, err := prepareMutationSession(t.Context(), nil, mutationbridge.PrepareOptions{Contract: "standard-v1"})
	if session != nil || err == nil || err.Error() != "goatest: nil mutation workspace" {
		t.Fatalf("prepareMutationSession = (%+v, %v)", session, err)
	}
	if session, err := productionRunDependencies().prepareSession(t.Context(), nil, mutationbridge.PrepareOptions{Contract: "standard-v1"}); session != nil || err == nil {
		t.Fatalf("production prepareSession = (%+v, %v)", session, err)
	}
}
