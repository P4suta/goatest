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

func mutantExecutionRecord(request gomutants.ExecRequest, result gomutants.MutantResult, reason wholeTreeReason, err error) trace.MutantRecord {
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
	record.WholeTreeReason = string(reason)
	record.WholeTree = reason != wholeTreeObserved
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

func prepareMutationSession(ctx context.Context, workspace *mutationbridge.Workspace, options mutationbridge.PrepareOptions) (MutationSession, error) {
	session, err := workspace.Prepare(ctx, options)
	if err != nil {
		return nil, err
	}
	return session, nil
}

func traceMilliseconds(duration time.Duration) int64 {
	return max(duration.Milliseconds(), 0)
}
