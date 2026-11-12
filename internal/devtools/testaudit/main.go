// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Command testaudit reads a `go test -json` stream and says what the run
// actually did.
//
// goatest refuses to call a verification assured when a mutant has no
// disposition: `discovered = executed + compile-rejected + accepted +
// out-of-scope + unknown`, and a single unknown makes the verdict ERROR. That
// accounting is why a goatest report can be read as evidence.
//
// Its own test suite had no such accounting. A non-verbose `go test` prints
// `ok` for a package whose every test was skipped, so a runner that lost `git`
// from PATH, or an environment variable that narrowed `-test.run`, could retire
// a whole package and leave CI green. This tool applies the product's rule to
// the product's own suite: a skip is recorded or it is a failure, and a package
// that produced no verdict at all is a failure whatever its exit status said.
//
// Usage:
//
//	go test -json ./... | go run ./internal/devtools/testaudit
//
// It exits 0 when every skip is recorded and every package produced at least
// one verdict, and 1 otherwise. It does not read the exit status of `go test`
// and does not replace it: a failing suite is still reported by `go test`.
package main

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

const (
	// initialLineBuffer and maximumLineBuffer size the scanner.
	//
	// One `go test -json` record holds a single line of a test's output, and a
	// test that prints a whole report in one call produces a long one. The
	// maximum is generous because the failure mode of being too small is this
	// tool refusing a run it should have audited.
	initialLineBuffer = 64 * 1024
	maximumLineBuffer = 8 * 1024 * 1024

	// ledgerName is the record of tests that are allowed to skip, beside this
	// source.
	ledgerName = "skip_ledger.txt"

	// failureExitCode is what this tool returns when the audit does not pass.
	failureExitCode = 1
)

// NarrowedFilterMarker is the line a TestMain prints when something cut its
// package's run down before it started.
//
// It is exported so that the package doing the narrowing and the tool refusing
// it name the same string. A marker spelt twice is a marker that will be spelt
// two ways.
const NarrowedFilterMarker = "goatest-testaudit: narrowed test filter"

func main() {
	os.Exit(run(os.Stdin, os.Stdout, os.Stderr))
}

// run is main with its streams passed in, so a test can drive it.
func run(input io.Reader, out, errorOut io.Writer) int {
	summary, err := audit(input)
	if err != nil {
		_, _ = fmt.Fprintln(errorOut, err)
		return failureExitCode
	}
	render(out, summary)

	allowed, err := readLedger(ledgerPath())
	if err != nil {
		_, _ = fmt.Fprintf(errorOut, "goatest: read %s: %v\n", ledgerName, err)
		return failureExitCode
	}

	failed := false
	if silent := summary.silentPackages(); len(silent) > 0 {
		_, _ = fmt.Fprintf(errorOut,
			"goatest: %d package(s) produced no verdict at all:\n  %s\n\n"+
				"Every test in them was skipped, which a non-verbose `go test` reports\n"+
				"as `ok`. A suite that stepped aside is not a suite that passed.\n",
			len(silent), strings.Join(silent, "\n  "))
		failed = true
	}
	if narrowed := summary.narrowedPackages(); len(narrowed) > 0 {
		_, _ = fmt.Fprintf(errorOut,
			"goatest: %d package(s) ran less than all of themselves:\n  %s\n\n"+
				"A test excluded before the run started is not a pass, a failure or a\n"+
				"skip, so the accounting balances over a suite that is missing most of\n"+
				"itself. Unset the variable, or run the suite that was asked for.\n",
			len(narrowed), strings.Join(narrowed, "\n  "))
		failed = true
	}
	if unrecorded := summary.unrecordedSkips(allowed); len(unrecorded) > 0 {
		_, _ = fmt.Fprintf(errorOut,
			"goatest: %d skip(s) are not recorded in %s:\n  %s\n\n"+
				"A skip is a test that did not run, and one nobody wrote down is one\n"+
				"nobody decided on. Record it with the reason, or stop skipping.\n",
			len(unrecorded), ledgerName, strings.Join(unrecorded, "\n  "))
		failed = true
	}
	if failed {
		return failureExitCode
	}
	return 0
}

// ledgerPath resolves the ledger beside this command's source.
//
// The tool is run through `go run ./internal/devtools/testaudit` from the
// module root, so the relative path is stable; an absolute override exists for
// a test that writes its own ledger.
func ledgerPath() string {
	if override := os.Getenv("GOATEST_TESTAUDIT_LEDGER"); override != "" {
		return override
	}
	return filepath.Join("internal", "devtools", "testaudit", ledgerName)
}

// readLedger reads the ledger: one "package Test" per line, with # comments
// and blank lines ignored.
//
// A ledger that is not there is an empty ledger rather than an error, so the
// first run of this tool in a tree that has not written one reports every skip
// rather than refusing to start.
func readLedger(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var allowed []string
	for number, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		fields := strings.Fields(trimmed)
		if len(fields) != ledgerFieldCount {
			return nil, fmt.Errorf("%s:%d: want \"package Test\", got %q", path, number+1, trimmed)
		}
		allowed = append(allowed, fields[0]+" "+fields[1])
	}
	return allowed, nil
}

// ledgerFieldCount is how many fields one ledger line holds.
const ledgerFieldCount = 2
