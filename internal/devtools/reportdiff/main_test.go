// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/P4suta/goatest/internal/report"
)

// writeReport writes one report into a temporary directory and returns its
// path. The fixture is encoded by the report package itself, so the tool is
// always handed the bytes a run would actually have written.
func writeReport(t *testing.T, name string, input report.Report) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, report.JSON(input), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return path
}

func TestRunDiffsTheNamedReports(t *testing.T) {
	t.Parallel()
	before, after := sampleReports()
	beforePath := writeReport(t, "before.json", before)
	afterPath := writeReport(t, "after.json", after)

	var stdout, stderr bytes.Buffer
	// A regression is something the comparison reports rather than something
	// it fails on: the exit code says whether the two reports could be read.
	if code := run([]string{beforePath, afterPath}, &stdout, &stderr); code != exitSuccess {
		t.Fatalf("run exited %d, want %d; stderr: %s", code, exitSuccess, stderr.String())
	}
	if stderr.Len() != 0 {
		t.Errorf("run wrote %q to stderr, want nothing", stderr.String())
	}
	got := stdout.String()
	if !strings.HasPrefix(got, "before: "+beforePath+"\n") {
		t.Errorf("the comparison opens with %q, want the named reports", firstLine(got))
	}
	if !strings.Contains(got, "after: "+afterPath) {
		t.Errorf("the comparison does not name the report it compared against:\n%s", got)
	}
	if !strings.HasSuffix(got, "\n") {
		t.Error("the comparison does not end with a newline")
	}
	if !strings.Contains(got, "m-02") {
		t.Errorf("the comparison does not name the mutant that stopped being killed:\n%s", got)
	}
}

func TestRunReportsUsageWithoutTwoReportPaths(t *testing.T) {
	t.Parallel()
	for _, arguments := range [][]string{nil, {"one.json"}, {"a.json", "b.json", "c.json"}} {
		var stdout, stderr bytes.Buffer
		if code := run(arguments, &stdout, &stderr); code != exitUsage {
			t.Errorf("run(%q) exited %d, want %d", arguments, code, exitUsage)
		}
		if stdout.Len() != 0 {
			t.Errorf("run(%q) wrote %q to stdout, want nothing", arguments, stdout.String())
		}
		if !strings.Contains(stderr.String(), "usage:") {
			t.Errorf("run(%q) wrote %q to stderr, want the usage", arguments, stderr.String())
		}
	}
}

func TestRunReportsAReportItCannotRead(t *testing.T) {
	t.Parallel()
	before, _ := sampleReports()
	readable := writeReport(t, "before.json", before)
	missing := filepath.Join(t.TempDir(), "absent.json")

	for _, arguments := range [][]string{{missing, readable}, {readable, missing}} {
		var stdout, stderr bytes.Buffer
		if code := run(arguments, &stdout, &stderr); code != exitFailure {
			t.Errorf("run(%q) exited %d, want %d", arguments, code, exitFailure)
		}
		if stdout.Len() != 0 {
			t.Errorf("run(%q) wrote %q to stdout, want nothing", arguments, stdout.String())
		}
		if !strings.Contains(stderr.String(), missing) {
			t.Errorf("run(%q) wrote %q to stderr, want the path it could not read", arguments, stderr.String())
		}
	}
}

func TestRunRejectsAReportOfAnotherSchema(t *testing.T) {
	t.Parallel()
	before, _ := sampleReports()
	readable := writeReport(t, "before.json", before)
	other := before
	other.Schema = "assurance-report-v2"
	unsupported := writeReport(t, "after.json", other)

	var stdout, stderr bytes.Buffer
	if code := run([]string{readable, unsupported}, &stdout, &stderr); code != exitFailure {
		t.Fatalf("run exited %d, want %d", code, exitFailure)
	}
	if stdout.Len() != 0 {
		t.Errorf("run wrote %q to stdout, want nothing", stdout.String())
	}
	for _, want := range []string{unsupported, "assurance-report-v2", report.SchemaV1} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("run wrote %q to stderr, want it to mention %q", stderr.String(), want)
		}
	}
}

func TestRunRejectsAReportWithTrailingData(t *testing.T) {
	t.Parallel()
	before, _ := sampleReports()
	readable := writeReport(t, "before.json", before)
	trailing := filepath.Join(t.TempDir(), "trailing.json")
	if err := os.WriteFile(trailing, append(report.JSON(before), []byte("{}\n")...), 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	if code := run([]string{readable, trailing}, &stdout, &stderr); code != exitFailure {
		t.Fatalf("run exited %d, want %d", code, exitFailure)
	}
	if !strings.Contains(stderr.String(), "trailing data") {
		t.Errorf("run wrote %q to stderr, want the trailing data it refused", stderr.String())
	}
}

// firstLine is the opening line of a comparison, which is what a failure about
// the header should print rather than the whole report.
func firstLine(text string) string {
	line, _, _ := strings.Cut(text, "\n")
	return line
}
