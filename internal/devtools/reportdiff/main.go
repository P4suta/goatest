// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

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

const (
	exitSuccess             = 0
	exitFailure             = 1
	exitUsage               = 2
	reportPathArgumentCount = 2
)

const usage = "usage: reportdiff <before.json> <after.json>"

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(arguments []string, stdout, stderr io.Writer) int {
	if len(arguments) != reportPathArgumentCount {
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
