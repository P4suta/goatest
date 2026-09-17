// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package app_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/P4suta/goatest/internal/app"
	"github.com/P4suta/goatest/internal/assure"
	"github.com/P4suta/goatest/internal/cache"
	"github.com/P4suta/goatest/internal/cli"
	"github.com/P4suta/goatest/internal/report"
)

func interruptedService(t *testing.T, root string) app.Service {
	t.Helper()
	return app.Service{Root: root, Run: func(_ context.Context, options assure.Options) (report.Report, error) {
		options.Trace.Progress("mutation-progress", "3/512")
		return report.Report{}, fmt.Errorf("goatest: mutation phase: %w", context.Canceled)
	}}
}

func hasLimitationCode(limitations []report.Limitation, code string) bool {
	for _, limitation := range limitations {
		if limitation.Code == code {
			return true
		}
	}
	return false
}

func hasFindingKind(findings []report.Finding, kind string) bool {
	for _, finding := range findings {
		if finding.Kind == kind {
			return true
		}
	}
	return false
}

func TestAnInterruptedRunRecordsThatItWasStopped(t *testing.T) {
	t.Parallel()
	root := t.TempDir()

	result, err := interruptedService(t, root).Execute(t.Context(), cli.CommandVerify, cli.Request{}, "")

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("verify error = %v, want a cancellation", err)
	}
	if result.Verdict != report.VerdictInsufficient {
		t.Fatalf("verdict = %q, want INSUFFICIENT: a stopped run is missing evidence, not in error", result.Verdict)
	}
	if !hasLimitationCode(result.Limitations, "assurance-interrupted") {
		t.Fatalf("limitations = %+v, want the one saying the run was stopped", result.Limitations)
	}
	if !hasFindingKind(result.Findings, "interrupted") {
		t.Fatalf("findings = %+v, want one of kind interrupted", result.Findings)
	}
	published := filepath.Join(root, "reports", "runs", result.RunID, "assurance-report-v1.json")
	if _, statErr := os.Stat(published); statErr != nil {
		t.Fatalf("a stopped run published no report: %v", statErr)
	}
}

func TestAnInterruptedRunLeavesTheBundleThatSaysHowFarItGot(t *testing.T) {
	t.Parallel()
	root := t.TempDir()

	result, err := interruptedService(t, root).Execute(t.Context(), cli.CommandVerify, cli.Request{}, "")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("verify error = %v, want a cancellation", err)
	}

	// The report of a stopped run holds no measurements, because a cancelled
	// assure.Run hands back none. The bundle is where how-far-it-got lives, so
	// this is the assertion that the change is worth anything at all.
	bundle := diagnosticsBundle(t, root)
	if filepath.Base(bundle) != result.RunID {
		t.Fatalf("bundle %s is not named for run %s", bundle, result.RunID)
	}
	events := readTrace(t, bundle)
	if len(events) == 0 {
		t.Fatal("the bundled recording of a stopped run is empty")
	}
	var progressed bool
	for _, event := range events {
		if event.Progress != nil && event.Progress.Kind == "mutation-progress" {
			progressed = true
		}
	}
	if !progressed {
		t.Fatalf("bundled recording = %+v, want the progress the run had reached", events)
	}
}

func TestAnInterruptedRunDoesNotDisplaceTheReportOtherCommandsRead(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	finished := report.Report{
		Schema: report.SchemaV1, Verdict: report.VerdictAssured, Contract: "standard-v1", Snapshot: appTestDigest("a"),
	}
	service := app.Service{Root: root, Run: func(context.Context, assure.Options) (report.Report, error) {
		return finished, nil
	}}
	assured, err := service.Execute(t.Context(), cli.CommandVerify, cli.Request{}, "")
	if err != nil {
		t.Fatal(err)
	}

	stopped, err := interruptedService(t, root).Execute(t.Context(), cli.CommandVerify, cli.Request{}, "")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("verify error = %v, want a cancellation", err)
	}
	if stopped.RunID == assured.RunID {
		t.Fatal("the two runs share a run ID, so this test cannot tell them apart")
	}

	for _, index := range []string{
		filepath.Join(root, ".goatest", "latest-any.json"),
		filepath.Join(root, "reports", "latest-any.json"),
		filepath.Join(root, ".goatest", "latest-full.json"),
		filepath.Join(root, "reports", "latest-full.json"),
	} {
		data, readErr := os.ReadFile(index)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if !strings.Contains(string(data), assured.RunID) {
			t.Fatalf("%s no longer names the run that measured something", index)
		}
		if strings.Contains(string(data), stopped.RunID) {
			t.Fatalf("%s names the stopped run, which cannot answer what these commands ask", index)
		}
	}
}

func TestAProcessStoppedBeforeItsRunBeganPublishesNothing(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	held, err := cache.Acquire(t.Context(), filepath.Join(root, ".goatest", "cache"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if releaseErr := held.Release(); releaseErr != nil {
			t.Error(releaseErr)
		}
	}()

	called := false
	service := app.Service{Root: root, Run: func(context.Context, assure.Options) (report.Report, error) {
		called = true
		return report.Report{}, nil
	}}
	ctx, cancel := context.WithTimeout(t.Context(), cacheWaitCancellation)
	defer cancel()
	result, err := service.Execute(ctx, cli.CommandVerify, cli.Request{}, "")

	if err == nil || called {
		t.Fatalf("verify = %+v, %v, called=%t, want a stop before the runner", result, err, called)
	}
	if result.RunID != "" {
		t.Fatalf("a process that never began a run published report %s", result.RunID)
	}
	if _, statErr := os.Stat(filepath.Join(root, "reports")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("reports/ exists after a run that never began: %v", statErr)
	}
}
