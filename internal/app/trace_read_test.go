// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package app_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/P4suta/goatest/internal/app"
	"github.com/P4suta/goatest/internal/cli"
	"github.com/P4suta/goatest/internal/report"
	"github.com/P4suta/goatest/internal/trace"
)

func TestTraceSummaryAndDiffAreReadOnlyAndExposeCompleteness(t *testing.T) {
	root := t.TempDir()
	service := app.Service{Root: root}
	missing, err := service.Execute(t.Context(), cli.CommandTrace, cli.Request{}, "summary")
	if err != nil || missing.Verdict != report.VerdictCompleted || !hasEvidenceStatus(missing, "trace-summary", "missing") {
		t.Fatalf("missing trace summary = (%+v, %v)", missing, err)
	}

	traceRoot := filepath.Join(root, ".goatest", "trace")
	writeCompletedTrace(t, traceRoot, "run-a", "ASSURED", 0)
	writeCompletedTrace(t, traceRoot, "run-b", "INSUFFICIENT", 2)
	summary, err := service.Execute(t.Context(), cli.CommandTrace, cli.Request{IDs: []string{"run-b"}}, "summary")
	if err != nil || !hasEvidenceStatus(summary, "trace-summary", "lossy") || !evidenceContains(summary, "dropped=2") {
		t.Fatalf("lossy trace summary = (%+v, %v)", summary, err)
	}
	difference, err := service.Execute(t.Context(), cli.CommandTrace, cli.Request{IDs: []string{"run-a", "run-b"}}, "diff")
	if err != nil || !hasEvidenceStatus(difference, "trace-diff", "changed") || !evidenceContains(difference, "ASSURED->INSUFFICIENT") {
		t.Fatalf("trace diff = (%+v, %v)", difference, err)
	}
}

func writeCompletedTrace(t *testing.T, root, run, verdict string, dropped int64) {
	t.Helper()
	sink, err := trace.NewDirSink(root, run, trace.Filesystem{})
	if err != nil {
		t.Fatal(err)
	}
	moment := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	recorder := trace.New(sink, func() time.Time { return moment })
	recorder.RunEnd(verdict, nil)
	if err := sink.Close(); err != nil {
		t.Fatal(err)
	}
	if dropped != 0 {
		path := filepath.Join(root, run, trace.FileName)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		data = bytes.Replace(data, []byte(`"events_dropped":0`), []byte(`"events_dropped":`+strconv.FormatInt(dropped, 10)), 1)
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func hasEvidenceStatus(input report.Report, kind, status string) bool {
	for _, item := range input.Evidence {
		if item.Kind == kind && item.Status == status {
			return true
		}
	}
	return false
}

func evidenceContains(input report.Report, text string) bool {
	for _, item := range input.Evidence {
		if strings.Contains(item.Detail, text) {
			return true
		}
	}
	return false
}
