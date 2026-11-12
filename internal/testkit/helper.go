// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package testkit

import (
	"os"
	"strings"
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

// RunningAsHelper reports whether this process was started to act as the named
// helper, and asserts what an ordinary run can check when it was not.
//
// The entry point of a helper is written as a test because that is the only way
// a test binary offers to reach it. Three answers were tried for what it should
// do in an ordinary run, and the first two were both wrong.
//
// Returning early made it report as a pass, and a pass is a claim: four of them
// were counted among this repository's passing tests, claiming work nobody had
// done. Skipping said what happened, which was honest and still not right -
// goatest's own accounting treats a selected target that skipped as missing
// evidence, so the first dogfood run after the change reported INSUFFICIENT and
// named this function. The product was right. A test that does not run is not a
// test that passed, and saying so more politely does not make it one.
//
// So it runs. The assertion is small and it is the one that matters: that the
// variable did not select this process, and that the argv which would select it
// names this test. HelperEnabled's own documentation worries about the opposite
// reading - a value that looked falsy, so the helper ran the suite instead, and
// "would look exactly like a test that passed". This is that worry from the
// other side, and now something checks it.
func RunningAsHelper(t *testing.T, variable, testName string) bool {
	t.Helper()
	if HelperEnabled(variable) {
		return true
	}
	if value, set := os.LookupEnv(variable); set && value != "" && value != "0" {
		t.Fatalf("%s is %q, which is neither the 1 that selects this helper nor an absence that does not",
			variable, value)
	}
	argv := HelperArgv(testName)
	if len(argv) != helperArgvLength || !strings.Contains(argv[1], testName) {
		t.Fatalf("HelperArgv(%q) = %q, which would not select this test", testName, argv)
	}
	return false
}

// helperArgvLength is what HelperArgv returns: the binary and one -test.run.
const helperArgvLength = 2
