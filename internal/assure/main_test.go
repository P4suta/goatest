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
	assureTestRunEnvironment      = "GOATEST_INTERNAL_ASSURE_TEST_RUN"
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
	}
	os.Exit(testingMain.Run())
}
