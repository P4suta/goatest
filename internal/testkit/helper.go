// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package testkit

import "os"

// HelperArgv is the argument vector that re-executes this test binary and runs
// only testName. A test that must drive a real subprocess - an external
// resource or generation provider, for example - scripts one of its own
// helper tests instead of building a separate program.
func HelperArgv(testName string) []string {
	return []string{os.Args[0], "-test.run=^" + testName + "$"}
}

// HelperEnabled reports whether the parent test activated the helper half of a
// re-executed test through variable. A helper stays inert during an ordinary
// run of the package, so it costs nothing when nobody scripted it.
func HelperEnabled(variable string) bool {
	return os.Getenv(variable) == "1"
}
