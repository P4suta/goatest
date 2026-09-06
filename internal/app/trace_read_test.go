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
	"github.com/P4suta/goatest/internal/filemode"
	"github.com/P4suta/goatest/internal/report"
	"github.com/P4suta/goatest/internal/trace"
)

const (
	traceRunA                = "20260901T120000Z-1234"
	traceRunB                = "20260901T123000Z-5678"
	traceRunAPrepareDuration = time.Second
	traceRunBPrepareDuration = 2 * time.Second
)

func TestTraceSummaryAndDiffAreReadOnlyAndExposeCompleteness(t *testing.T) {
	root := t.TempDir()
	service := app.Service{Root: root}
	missing, err := service.Execute(t.Context(), cli.CommandTrace, cli.Request{}, "summary")
	if err != nil || missing.Verdict != report.VerdictCompleted || !hasEvidenceStatus(missing, "trace-summary", "missing") {
		t.Fatalf("missing trace summary = (%+v, %v)", missing, err)
	}

	traceRoot := filepath.Join(root, ".goatest", "trace")
	writeCompletedTrace(t, traceRoot, traceRunA, "ASSURED", 0, traceRunAPrepareDuration)
	writeCompletedTrace(t, traceRoot, traceRunB, "INSUFFICIENT", 2, traceRunBPrepareDuration)
	summary, err := service.Execute(t.Context(), cli.CommandTrace, cli.Request{IDs: []string{traceRunB}}, "summary")
	if err != nil || !hasEvidenceStatus(summary, "trace-summary", "lossy") || !evidenceContains(summary, "dropped=2") {
		t.Fatalf("lossy trace summary = (%+v, %v)", summary, err)
	}
	if !hasEvidenceStatus(summary, "trace-prepare", "observed") ||
		!evidenceContains(summary, "duration-ms="+strconv.FormatInt(traceRunBPrepareDuration.Milliseconds(), 10)) {
		t.Fatalf("preparation trace summary = %+v", summary)
	}
	difference, err := service.Execute(t.Context(), cli.CommandTrace, cli.Request{IDs: []string{traceRunA, traceRunB}}, "diff")
	if err != nil || !hasEvidenceStatus(difference, "trace-diff", "changed") || !evidenceContains(difference, "ASSURED->INSUFFICIENT") {
		t.Fatalf("trace diff = (%+v, %v)", difference, err)
	}
	wantDelta := traceRunBPrepareDuration - traceRunAPrepareDuration
	if !hasEvidenceStatus(difference, "trace-diff-prepare", "compared") ||
		!evidenceContains(difference, "duration-ms-delta=+"+strconv.FormatInt(wantDelta.Milliseconds(), 10)) {
		t.Fatalf("preparation trace diff = %+v", difference)
	}
}

func TestTraceSummarySkipsUnrelatedEntries(t *testing.T) {
	root := t.TempDir()
	service := app.Service{Root: root}
	traceRoot := filepath.Join(root, ".goatest", "trace")
	writeCompletedTrace(t, traceRoot, traceRunA, "ASSURED", 0, traceRunAPrepareDuration)
	if err := os.WriteFile(filepath.Join(traceRoot, "unrelated.txt"), []byte("not a trace"), filemode.PrivateFile); err != nil {
		t.Fatal(err)
	}
	summary, err := service.Execute(t.Context(), cli.CommandTrace, cli.Request{}, "summary")
	if err != nil || !hasEvidenceStatus(summary, "trace-summary", "complete") {
		t.Fatalf("summary with unrelated entry = (%+v, %v)", summary, err)
	}
}

func TestTraceSummaryRejectsExtraRunNames(t *testing.T) {
	service := app.Service{Root: t.TempDir()}
	_, err := service.Execute(t.Context(), cli.CommandTrace, cli.Request{IDs: []string{"run-a", "run-b"}}, "summary")
	if err == nil || !strings.Contains(err.Error(), "at most one run") {
		t.Fatalf("extra summary run error = %v", err)
	}
}

func TestTraceSummaryDoesNotRecognizeLatestAsARunAlias(t *testing.T) {
	root := t.TempDir()
	traceRoot := filepath.Join(root, ".goatest", "trace")
	writeCompletedTrace(t, traceRoot, "latest", "ASSURED", 0, traceRunAPrepareDuration)
	service := app.Service{Root: root}
	summary, err := service.Execute(t.Context(), cli.CommandTrace, cli.Request{}, "summary")
	if err != nil || !hasEvidenceStatus(summary, "trace-summary", "missing") {
		t.Fatalf("summary with only a latest alias = (%+v, %v)", summary, err)
	}
	if _, err := service.Execute(t.Context(), cli.CommandTrace, cli.Request{IDs: []string{"latest"}}, "summary"); err == nil || !strings.Contains(err.Error(), "invalid trace run") {
		t.Fatalf("explicit latest alias error = %v", err)
	}
}

func writeCompletedTrace(t *testing.T, root, run, verdict string, dropped int64, prepareDuration time.Duration) {
	t.Helper()
	sink, err := trace.NewDirSink(root, run, trace.Filesystem{})
	if err != nil {
		t.Fatal(err)
	}
	moment := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	recorder := trace.New(sink, func() time.Time { return moment })
	recorder.Prepare(trace.PreparePhaseDiscovery, trace.PrepareStateStarted, "", 0)
	recorder.Prepare(trace.PreparePhaseDiscovery, trace.PrepareStateFinished, trace.PrepareResultSucceeded, prepareDuration)
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
		if err := os.WriteFile(path, data, filemode.PrivateFile); err != nil {
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
