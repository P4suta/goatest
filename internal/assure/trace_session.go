// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package assure

import (
	"context"
	"slices"
	"strings"
	"time"

	gomutants "github.com/P4suta/go-mutants"
	"github.com/P4suta/goatest/internal/mutationbridge"
	"github.com/P4suta/goatest/internal/trace"
)

// tracedSession records every execution of the mutation session it wraps.
//
// The wrapper is unconditional: a nil recorder records nothing, so a run that
// asked for no trace keeps the call path of one that did, and the evaluator
// never learns whether it is being traced.
type tracedSession struct {
	session  MutationSession
	recorder *trace.Recorder
}

// newTracedSession wraps session so that its executions reach recorder.
func newTracedSession(session MutationSession, recorder *trace.Recorder) MutationSession {
	return tracedSession{session: session, recorder: recorder}
}

// Catalog answers from the wrapped session. Preparing a catalog is a phase of
// the run rather than an execution, so it records nothing of its own.
func (traced tracedSession) Catalog() gomutants.Catalog { return traced.session.Catalog() }

// Exec runs one mutant and records what became of it. Executions run
// concurrently, and the recorder serialises them, so the stream holds one
// complete line per execution in completion order.
func (traced tracedSession) Exec(ctx context.Context, request gomutants.ExecRequest) (gomutants.MutantResult, error) {
	result, err := traced.session.Exec(ctx, request)
	traced.recorder.MutantExec(mutantExecutionRecord(request, result, err))
	return result, err
}

// Probe measures one target against the probe tree. It records nothing of its
// own: a probe request names the package and the flags but not the target
// identity a record is read by, so the probe pass records what it measured.
func (traced tracedSession) Probe(ctx context.Context, request gomutants.ProbeRequest) (gomutants.ProbeResult, error) {
	return traced.session.Probe(ctx, request)
}

// mutantExecutionRecord describes one mutant execution for the trace. The
// identity is the one the engine resolved, falling back to the one that was
// requested when an execution produced no result to resolve it from.
func mutantExecutionRecord(request gomutants.ExecRequest, result gomutants.MutantResult, err error) trace.MutantRecord {
	record := trace.MutantRecord{
		ID:         request.Mutant,
		DisplayID:  result.DisplayID,
		Package:    request.Package,
		Args:       mutationTraceArguments(request.Args),
		TimeoutMS:  traceMilliseconds(request.Timeout),
		Outcome:    string(result.Outcome),
		KilledBy:   result.KilledBy,
		DurationMS: traceMilliseconds(result.Duration),
	}
	if result.ID != "" {
		record.ID = result.ID
	}
	if err != nil {
		record.Error = err.Error()
	}
	return record
}

func mutationTraceArguments(arguments []string) []string {
	result := slices.Clone(arguments)
	return slices.DeleteFunc(result, func(argument string) bool {
		return strings.HasPrefix(argument, "-test.testlogfile=")
	})
}

// prepareTracedSession prepares the mutation session of a workspace and traces
// it under the recording the workspace itself runs under, so that the commands
// of a run and the mutants of a run reach one stream.
func prepareTracedSession(ctx context.Context, workspace *mutationbridge.Workspace, options mutationbridge.PrepareOptions) (MutationSession, error) {
	session, err := workspace.Prepare(ctx, options)
	if err != nil {
		return nil, err
	}
	return newTracedSession(session, workspace.Trace()), nil
}

// traceMilliseconds is the millisecond count a trace records for a duration. A
// negative duration, which the engine contract forbids, is recorded as none
// rather than as a nonsense measurement.
func traceMilliseconds(duration time.Duration) int64 {
	return max(duration.Milliseconds(), 0)
}
