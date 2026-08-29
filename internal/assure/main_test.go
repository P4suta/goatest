// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package assure

import (
	"flag"
	"fmt"
	"os"
	"testing"
)

const assureTestRunEnvironment = "GOATEST_INTERNAL_ASSURE_TEST_RUN"

func TestMain(testingMain *testing.M) {
	if pattern := os.Getenv(assureTestRunEnvironment); pattern != "" {
		if err := flag.Set("test.run", pattern); err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "set assure test filter: %v\n", err)
			os.Exit(2)
		}
	}
	os.Exit(testingMain.Run())
}
