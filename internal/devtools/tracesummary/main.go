// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"fmt"
	"io"
	"os"
)

const (
	exitSuccess = 0
	exitFailure = 1
	exitUsage   = 2
)

const usage = "usage: tracesummary <trace.jsonl>"

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

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
