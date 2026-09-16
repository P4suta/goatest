// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package testkit

import (
	"os"
	"testing"
)

// HelperArgv is the command line that re-runs this test binary as one named
// test and nothing else.
//
// A helper process is how a test drives real process behaviour — a signal, an
// advisory lock, a provider speaking a protocol over its own stdin — without
// shipping a second binary to build.
func HelperArgv(testName string) []string {
	return []string{os.Args[0], "-test.run=^" + testName + "$"}
}

// HelperEnabled reports whether this process was started to act as the named
// helper.
func HelperEnabled(variable string) bool {
	return os.Getenv(variable) == "1"
}

// SkipUnlessHelper skips the test unless this process was started to act as
// the named helper.
//
// The entry point of a helper is written as a test because that is the only
// way a test binary offers to reach it, but it is not a test: in an ordinary
// run it has nothing to assert. Returning early made it report as a pass, and
// a pass is a claim. Four of them were counted among this repository's passing
// tests, inflating the total with work nobody had done.
//
// Skipping says what happened instead, and the skip is recorded in the audit's
// ledger like every other, so the count of tests that did not run stays honest.
func SkipUnlessHelper(t *testing.T, variable string) {
	t.Helper()
	if HelperEnabled(variable) {
		return
	}
	t.Skipf("this test is the %s helper process; it asserts nothing unless %s=1 selects it",
		variable, variable)
}
