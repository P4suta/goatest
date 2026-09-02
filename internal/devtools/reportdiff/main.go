// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Command reportdiff compares two assurance-report-v1 documents: what the two
// runs concluded, how their accounting moved, what became of the mutants both
// of them discovered, and which mutants stopped being killed.
//
// It is a developer tool rather than part of the shipped binary, and it reads
// the two reports and nothing else. Every number it prints comes from a
// recorded value, so comparing one pair twice prints the same bytes on any
// machine at any moment.
//
//	go run ./internal/devtools/reportdiff before.json after.json
//
// It exists to judge a change that is meant to alter how a run works without
// altering what it concludes — routing, caching, resume — against the run it
// replaces. The question such a change has to answer is whether any mutant
// stopped being killed, which no single report can answer on its own.
//
// A regression is reported rather than failed on: the exit code says whether
// the two reports could be read, not what they said. A caller that wants a
// gate reads the regressions section. Refusing on a regression is a flag this
// tool does not have yet, so that adding one later cannot change the meaning
// of an exit code a script already relies on.
//
// The reports are decoded strictly — an unknown field, trailing data, or a
// schema other than assurance-report-v1 is an error naming the file — because
// a comparison that quietly ignored half of a document it did not understand
// would be a confident wrong answer. The stronger persistence validation is
// deliberately not run: this tool compares reports a run already wrote,
// including ones written by a version whose invariants have since changed, and
// refusing to read the older half would defeat the comparison.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/P4suta/goatest/internal/report"
)

// Exit codes. Usage is separated from failure so that a wrapper can tell a
// mistyped command line from a report it could not read.
const (
	exitSuccess = 0
	exitFailure = 1
	exitUsage   = 2
)

// usage is the one command line the tool accepts.
const usage = "usage: reportdiff <before.json> <after.json>"

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run compares the two named reports onto stdout, reporting anything that
// stopped it on stderr. Nothing reaches stdout unless both reports were read,
// so a caller that redirects the comparison to a file never keeps a partial
// one.
func run(arguments []string, stdout, stderr io.Writer) int {
	if len(arguments) != 2 {
		_, _ = fmt.Fprintln(stderr, usage)
		return exitUsage
	}
	beforePath, afterPath := arguments[0], arguments[1]
	before, err := loadReport(beforePath)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "reportdiff: %v\n", err)
		return exitFailure
	}
	after, err := loadReport(afterPath)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "reportdiff: %v\n", err)
		return exitFailure
	}
	_, _ = io.WriteString(stdout, renderComparison(beforePath, afterPath, compare(before, after)))
	return exitSuccess
}

// loadReport reads one assurance report, refusing anything the contract does
// not allow rather than comparing what it understood of it. It mirrors the
// strict decode the service performs when it loads a stored report, without
// the persistence validation that decode also applies.
func loadReport(path string) (report.Report, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return report.Report{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var result report.Report
	if err := decoder.Decode(&result); err != nil {
		return report.Report{}, fmt.Errorf("decode %s: %w", path, err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return report.Report{}, fmt.Errorf("%s has trailing data", path)
	}
	if result.Schema != report.SchemaV1 {
		return report.Report{}, fmt.Errorf("%s has schema %q, want %q", path, result.Schema, report.SchemaV1)
	}
	return result, nil
}
