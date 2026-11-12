// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package testkit

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// RequireToolsEnv names the variable that turns a missing tool from a skip into
// a failure.
//
// Set it in CI and leave it unset on a developer's machine. The two audiences
// want opposite things from the same condition: a developer without a Go
// toolchain on PATH is running the unit tier on purpose and should see the
// toolchain tests step aside, while a CI job without one is a broken job
// reporting a smaller suite as a passing suite.
const RequireToolsEnv = "GOATEST_TEST_REQUIRE_TOOLS"

// pinnedRequireTools is RequireToolsEnv as this process started with it.
//
// The fallback is not a nicety. A goatest run composes the environment of the
// children it starts from an allowlist, and a test that drives that machinery
// hands it an environment this variable is not on. Without a value pinned
// before any test ran, such a test would reach for a tool through an
// environment that had quietly switched CI's requirement back off, and the
// suite would shrink in exactly the job that was configured to refuse that.
var pinnedRequireTools = os.Getenv(RequireToolsEnv)

// RequireTools reports whether a missing tool must fail the test rather than
// skip it.
func RequireTools() bool {
	if value, ok := os.LookupEnv(RequireToolsEnv); ok {
		return truthy(value)
	}
	return truthy(pinnedRequireTools)
}

// truthy reads the spellings a person types into a workflow file or a shell.
//
// Anything that is not one of the recognised falsehoods is true, so a typo
// enables the requirement rather than disabling it. That direction is chosen:
// the failure mode of a mistyped "yes" is a job that insists on its tools, and
// the failure mode of a mistyped "1" would be a job that stopped insisting
// without saying so.
func truthy(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "0", "false", "no", "off":
		return false
	}
	return true
}

// toolPath looks one tool up on PATH and applies the policy RequireTools
// states.
//
// The message names the variable in both directions, because a reader of a
// skipped test needs to know that the skip was a choice, and a reader of a
// failed one needs to know which setting made it fatal.
func toolPath(t *testing.T, name string) string {
	t.Helper()
	path, err := exec.LookPath(name)
	if err == nil {
		return path
	}
	if RequireTools() {
		t.Fatalf("%s is not on PATH and %s is set, so this test may not be skipped: %v",
			name, RequireToolsEnv, err)
	} else {
		t.Skipf("%s is not on PATH, so this test cannot run here (set %s=1 to make this a failure): %v",
			name, RequireToolsEnv, err)
	}
	return ""
}
