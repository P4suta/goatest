// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Command tracesummary aggregates one goatest-trace-v1 stream into the
// performance breakdown of the run that recorded it: where the phases went,
// which commands the run repeated, and what became of the mutants it executed.
//
// It is a developer tool rather than part of the shipped binary, and it reads
// the stream and nothing else. Every number it prints comes from a recorded
// value, so summarizing one trace twice prints the same bytes on any machine
// at any moment.
//
//	go run ./internal/devtools/tracesummary .goatest/trace/<run>/trace.jsonl
//
// The stream is parsed strictly: a line the trace contract does not allow is
// an error naming the line, because a total that quietly skipped an event it
// did not understand would be a confident wrong number. A recording that stops
// without its run-end event is not that kind of deviation — a killed run
// leaves one — so it is summarized and reported as incomplete.
package main

import (
	"fmt"
	"io"
	"os"
)

// Exit codes. Usage is separated from failure so that a wrapper can tell a
// mistyped command line from a trace it could not summarize.
const (
	exitSuccess = 0
	exitFailure = 1
	exitUsage   = 2
)

// usage is the one command line the tool accepts.
const usage = "usage: tracesummary <trace.jsonl>"

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run summarizes the named trace onto stdout, reporting anything that stopped
// it on stderr. Nothing reaches stdout unless the whole stream was read, so a
// caller that redirects the summary to a file never keeps a partial one.
func run(arguments []string, stdout, stderr io.Writer) int {
	if len(arguments) != 1 {
		_, _ = fmt.Fprintln(stderr, usage)
		return exitUsage
	}
	path := arguments[0]
	file, err := os.Open(path)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "tracesummary: %v\n", err)
		return exitFailure
	}
	defer func() { _ = file.Close() }()
	events, err := readEvents(file)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "tracesummary: %s: %v\n", path, err)
		return exitFailure
	}
	_, _ = io.WriteString(stdout, renderSummary(path, events))
	return exitSuccess
}
