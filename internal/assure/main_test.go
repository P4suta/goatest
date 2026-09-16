// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package assure

import (
	"context"
	"flag"
	"fmt"
	"math"
	"os"
	"testing"
	"time"

	gomutants "github.com/P4suta/go-mutants"
)

const (
	assureTestRunEnvironment = "GOATEST_INTERNAL_ASSURE_TEST_RUN"

	// narrowedFilterMarker is printed when assureTestRunEnvironment narrows
	// this package's run, and is what internal/devtools/testaudit refuses.
	//
	// The variable exists because -test.run is an assurance-owned flag that
	// internal/testargs refuses to forward, so a run of goatest over its own
	// source has no other way to keep this package from re-entering itself.
	// Nothing in this repository sets it today, which is the dangerous shape: a
	// live hole with no user, inherited from any parent process that happens to
	// carry it.
	//
	// Narrowing the largest test package in the repository is a decision. It
	// was being made in silence - no line in the log, no note in the report,
	// nothing for a reader of a green run to notice - and a decision nothing
	// records is one nobody made.
	narrowedFilterMarker = "goatest-testaudit: narrowed test filter"

	testFlagConfigurationExitCode = 2
	mutationTestContainment       = time.Duration(math.MaxInt64)
	mutationTestControlDuration   = time.Millisecond
)

func mutationOptionsForTest(options MutationOptions) MutationOptions {
	if options.Timeout <= 0 {
		options.Timeout = mutationTestContainment
	}
	if options.OriginalControl == nil {
		options.OriginalControl = func(context.Context, gomutants.ExecRequest) (gomutants.CommandResult, error) {
			return gomutants.CommandResult{Duration: mutationTestControlDuration}, nil
		}
	}
	options.freshControl = options.OriginalControl
	options.OriginalControl = memoizedOriginalControl(options.OriginalControl)
	return options
}

func evaluateMutationsForTest(ctx context.Context, session MutationSession, targets []TargetEvidence, options MutationOptions) (MutationEvaluation, error) {
	return EvaluateMutations(ctx, session, targets, mutationOptionsForTest(options))
}

func TestMain(testingMain *testing.M) {
	if pattern := os.Getenv(assureTestRunEnvironment); pattern != "" {
		if err := flag.Set("test.run", pattern); err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "set assure test filter: %v\n", err)
			os.Exit(testFlagConfigurationExitCode)
		}
		_, _ = fmt.Fprintf(os.Stdout, "%s: %s=%q\n", narrowedFilterMarker, assureTestRunEnvironment, pattern)
	}
	os.Exit(testingMain.Run())
}
