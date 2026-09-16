// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"
)

// event is the subset of a `go test -json` record this tool reads.
//
// The full record carries timings and output as well; none of that changes
// whether a suite ran, so none of it is decoded.
type event struct {
	// Action is one of start, run, pause, cont, pass, bench, fail, output,
	// skip. Only the three terminal ones are counted.
	Action string

	// Package is the import path the event belongs to.
	Package string

	// Test is the test the event belongs to, empty for a package-level event.
	Test string

	// Output is one line a test printed, carried by an output action.
	//
	// It is decoded so that this tool can show why a test failed. A `go test
	// -json` stream is not readable, and a task that hides the reason for a
	// failure behind a file nobody opens is a task nobody will run.
	Output string
}

// tally is what one package did.
type tally struct {
	// pkg is the import path.
	pkg string

	// passed, failed and skipped count the tests that reached each verdict.
	//
	// Subtests are counted too: a parent that skips every child has not
	// produced the evidence its name promises, and hiding that inside the
	// parent's own result is how a suite shrinks without saying so.
	passed  int
	failed  int
	skipped int

	// noTestFiles records a package-level skip, which `go test` emits for a
	// package holding no test files at all.
	//
	// It is not a shrinking suite. It is a package nobody has written tests
	// for, which is a different conversation and a different gate.
	noTestFiles bool

	// skippedNames is every test of this package that skipped, in the order
	// the run reported them.
	skippedNames []string

	// failedNames is every test of this package that failed, in the order the
	// run reported them.
	failedNames []string
}

// failure is one failed test and what it printed.
type failure struct {
	// pkg and test name the test.
	pkg  string
	test string

	// output is every line the test printed, in order.
	output []string
}

// result is the whole run.
type result struct {
	// packages is one tally per import path, sorted by path.
	packages []tally

	// passed, failed and skipped are the totals across every package.
	passed  int
	failed  int
	skipped int

	// failures is every failed test with its output, in package then report
	// order.
	failures []failure
}

// audit reads a `go test -json` stream and reports what ran.
//
// A malformed line is an error rather than a skipped line: this tool exists to
// say what a run did, and a reader of a partial answer cannot tell it from a
// complete one.
func audit(input io.Reader) (result, error) {
	tallies := make(map[string]*tally)
	output := make(map[string][]string)
	lookup := func(pkg string) *tally {
		found, ok := tallies[pkg]
		if !ok {
			found = &tally{pkg: pkg}
			tallies[pkg] = found
		}
		return found
	}
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 0, initialLineBuffer), maximumLineBuffer)
	for line := 1; scanner.Scan(); line++ {
		text := strings.TrimSpace(scanner.Text())
		if text == "" {
			continue
		}
		if !strings.HasPrefix(text, "{") {
			return result{}, fmt.Errorf("goatest: test event line %d is not JSON: %.80s", line, text)
		}
		var record event
		if err := json.Unmarshal([]byte(text), &record); err != nil {
			return result{}, fmt.Errorf("goatest: decode test event line %d: %w", line, err)
		}
		if record.Package == "" {
			continue
		}
		counted := lookup(record.Package)
		key := record.Package + " " + record.Test
		switch {
		case record.Action == "output" && record.Test != "":
			output[key] = append(output[key], strings.TrimRight(record.Output, "\n"))
		case record.Action == "pass" && record.Test != "":
			counted.passed++
			delete(output, key)
		case record.Action == "fail" && record.Test != "":
			counted.failed++
			counted.failedNames = append(counted.failedNames, record.Test)
		case record.Action == "skip" && record.Test != "":
			delete(output, key)
			counted.skipped++
			counted.skippedNames = append(counted.skippedNames, record.Test)
		case record.Action == "skip":
			counted.noTestFiles = true
		}
	}
	if err := scanner.Err(); err != nil {
		return result{}, fmt.Errorf("goatest: read the test event stream: %w", err)
	}
	return summarize(tallies, output), nil
}

// summarize orders the tallies, adds them up, and attaches the output of every
// test that failed.
func summarize(tallies map[string]*tally, output map[string][]string) result {
	var summary result
	for _, counted := range tallies {
		summary.packages = append(summary.packages, *counted)
		summary.passed += counted.passed
		summary.failed += counted.failed
		summary.skipped += counted.skipped
	}
	sort.Slice(summary.packages, func(first, second int) bool {
		return summary.packages[first].pkg < summary.packages[second].pkg
	})
	for _, counted := range summary.packages {
		for _, name := range counted.failedNames {
			summary.failures = append(summary.failures, failure{
				pkg:    counted.pkg,
				test:   name,
				output: output[counted.pkg+" "+name],
			})
		}
	}
	return summary
}

// silentPackages reports the packages that started tests and finished with no
// test having passed or failed.
//
// This is the accounting the product has had all along and the harness has
// not. A package whose every test stepped aside prints `ok` under a
// non-verbose `go test`, which is indistinguishable from a package whose every
// test ran. Two ways that happens are worth naming: a runner that lost a tool
// from PATH, and a TestMain that narrowed `-test.run` from the environment.
func (summary result) silentPackages() []string {
	var silent []string
	for _, counted := range summary.packages {
		if counted.noTestFiles || counted.skipped == 0 {
			continue
		}
		if counted.passed == 0 && counted.failed == 0 {
			silent = append(silent, counted.pkg)
		}
	}
	return silent
}

// unrecordedSkips reports the skips the ledger does not allow, as
// "package Test".
func (summary result) unrecordedSkips(allowed []string) []string {
	permitted := make(map[string]struct{}, len(allowed))
	for _, entry := range allowed {
		permitted[entry] = struct{}{}
	}
	var unrecorded []string
	for _, counted := range summary.packages {
		for _, name := range counted.skippedNames {
			entry := counted.pkg + " " + topLevelTest(name)
			if _, ok := permitted[entry]; ok {
				continue
			}
			unrecorded = append(unrecorded, entry)
		}
	}
	slices.Sort(unrecorded)
	return slices.Compact(unrecorded)
}

// topLevelTest reduces a subtest name to the test that owns it.
//
// The ledger records tests, not subtests, because a subtest name is often
// built from the case it covers and a ledger of those would turn over every
// time somebody adds a case.
func topLevelTest(name string) string {
	parent, _, _ := strings.Cut(name, "/")
	return parent
}

// render writes the human-readable summary.
//
// It is printed on success as well as on failure, because a number nobody sees
// until something breaks is a number nobody has been watching.
func render(out io.Writer, summary result) {
	for _, failed := range summary.failures {
		fmt.Fprintf(out, "--- FAIL: %s %s\n", failed.pkg, failed.test)
		for _, line := range failed.output {
			fmt.Fprintf(out, "    %s\n", line)
		}
	}
	fmt.Fprintf(out, "tests: %d passed, %d failed, %d skipped, across %d package(s)\n",
		summary.passed, summary.failed, summary.skipped, len(summary.packages))
	for _, counted := range summary.packages {
		if counted.skipped == 0 {
			continue
		}
		fmt.Fprintf(out, "  %s: %d skipped (%s)\n",
			counted.pkg, counted.skipped, strings.Join(counted.skippedNames, ", "))
	}
}
