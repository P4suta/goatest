// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunSummarizesTheNamedTrace(t *testing.T) {
	t.Parallel()
	path := filepath.Join("testdata", "sample-trace.jsonl")
	var stdout, stderr bytes.Buffer
	if code := run([]string{path}, &stdout, &stderr); code != exitSuccess {
		t.Fatalf("run exited %d, want %d; stderr: %s", code, exitSuccess, stderr.String())
	}
	if stderr.Len() != 0 {
		t.Errorf("run wrote %q to stderr, want nothing", stderr.String())
	}
	if got := stdout.String(); !strings.HasPrefix(got, "trace: "+path+"\n") {
		t.Errorf("the summary opens with %q, want the named trace", firstLine(got))
	}
	if !strings.HasSuffix(stdout.String(), "\n") {
		t.Error("the summary does not end with a newline")
	}
}

func TestRunReportsUsageWithoutOneTracePath(t *testing.T) {
	t.Parallel()
	for _, arguments := range [][]string{nil, {"a", "b"}} {
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

func TestRunReportsATraceItCannotRead(t *testing.T) {
	t.Parallel()
	missing := filepath.Join(t.TempDir(), "absent.jsonl")
	var stdout, stderr bytes.Buffer
	if code := run([]string{missing}, &stdout, &stderr); code != exitFailure {
		t.Fatalf("run exited %d, want %d", code, exitFailure)
	}
	if stdout.Len() != 0 {
		t.Errorf("run wrote %q to stdout, want nothing", stdout.String())
	}
	if !strings.Contains(stderr.String(), missing) {
		t.Errorf("run wrote %q to stderr, want the path it could not read", stderr.String())
	}
}

func TestRunReportsTheLineATraceDeviatesOn(t *testing.T) {
	t.Parallel()
	path := filepath.Join("testdata", "broken-trace.jsonl")
	var stdout, stderr bytes.Buffer
	if code := run([]string{path}, &stdout, &stderr); code != exitFailure {
		t.Fatalf("run exited %d, want %d", code, exitFailure)
	}
	if stdout.Len() != 0 {
		t.Errorf("run wrote %q to stdout, want nothing", stdout.String())
	}
	for _, want := range []string{path, "line 3", "duration_ns"} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("run wrote %q to stderr, want it to mention %q", stderr.String(), want)
		}
	}
}

// firstLine is the opening line of a summary, which is what a failure about
// the header should print rather than the whole report.
func firstLine(text string) string {
	line, _, _ := strings.Cut(text, "\n")
	return line
}
