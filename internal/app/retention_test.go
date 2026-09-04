// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package app_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/P4suta/goatest/internal/app"
	"github.com/P4suta/goatest/internal/assure"
	"github.com/P4suta/goatest/internal/cli"
	"github.com/P4suta/goatest/internal/report"
)

// historyService is a service whose runs do nothing but produce one persistable
// report each, which is all the durable history needs to exist.
func historyService(t *testing.T, root string) app.Service {
	t.Helper()
	return app.Service{
		Root: root, TempDirectory: t.TempDir(),
		Run: func(context.Context, assure.Options) (report.Report, error) {
			return report.Report{
				Schema: report.SchemaV1, Verdict: report.VerdictAssured, Contract: "standard-v1", Snapshot: "snapshot-a",
			}, nil
		},
	}
}

// historyRun performs one verification and returns the run it wrote.
func historyRun(t *testing.T, service app.Service, packages []string) string {
	t.Helper()
	result, err := service.Execute(t.Context(), cli.CommandVerify, cli.Request{Packages: packages}, "")
	if err != nil {
		t.Fatal(err)
	}
	return result.RunID
}

// findEvidence returns the one evidence entry with this kind and ID, and the
// index it was reported at, so a test can pin both what was said and where.
func findEvidence(t *testing.T, result report.Report, kind, id string) (report.Evidence, int) {
	t.Helper()
	for index, item := range result.Evidence {
		if item.Kind == kind && item.ID == id {
			return item, index
		}
	}
	t.Fatalf("evidence %s/%s is absent from %+v", kind, id, result.Evidence)
	return report.Evidence{}, -1
}

func TestCacheMaintenanceBoundsTheRunHistoryAndSparesReferencedRuns(t *testing.T) {
	root := t.TempDir()
	service := historyService(t, root)
	// The first run is full, so latest-full.json points at it and keeps
	// pointing at it while the two package-scoped runs advance latest-any.
	full := historyRun(t, service, nil)
	middle := historyRun(t, service, []string{"./pkg"})
	newest := historyRun(t, service, []string{"./pkg"})
	// A WriteReports that was killed between staging and publishing leaves this
	// behind. It is an ordinary entry of the history and is collected with it.
	staging := filepath.Join(root, "reports", "runs", ".goatest-run-crashed")
	if err := os.Mkdir(staging, 0o755); err != nil {
		t.Fatal(err)
	}
	stagedFile := filepath.Join(staging, "assurance-report-v1.json")
	if err := os.WriteFile(stagedFile, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	abandoned := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := os.Chtimes(stagedFile, abandoned, abandoned); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".goatest.toml"), []byte("version = 1\n[reports]\nkeep = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	status, err := service.Execute(t.Context(), cli.CommandCache, cli.Request{}, "status")
	if err != nil {
		t.Fatal(err)
	}
	if policy := status.Evidence[0]; !strings.Contains(policy.Detail, "reports-keep=1") {
		t.Fatalf("cache policy = %+v, want the report history bound in it", policy)
	}
	history, index := findEvidence(t, status, "reports", "runs-status")
	// The history is reported with the repository's other stores, after the
	// trace and diagnostics directories and before the machine's build cache.
	if index != 4 || status.Evidence[3].Kind != "diagnostic-retention" || history.Status != "ready" {
		t.Fatalf("history status = %+v at %d, evidence %+v", history, index, status.Evidence)
	}
	if !strings.Contains(history.Detail, "entries=4 ") || !strings.Contains(history.Detail, "keep=1 protected=2") {
		t.Fatalf("history status detail = %q, want four entries, a bound of one, and two referenced runs", history.Detail)
	}
	if _, err := os.Stat(staging); err != nil {
		t.Fatalf("'cache status' collected something: %v", err)
	}

	collected, err := service.Execute(t.Context(), cli.CommandCache, cli.Request{}, "gc")
	if err != nil {
		t.Fatal(err)
	}
	gc, gcIndex := findEvidence(t, collected, "reports", "runs-gc")
	if gcIndex != 6 || collected.Evidence[5].Kind != "diagnostic-retention" || gc.Status != "completed" {
		t.Fatalf("history gc = %+v at %d, evidence %+v", gc, gcIndex, collected.Evidence)
	}
	if !strings.Contains(gc.Detail, "removed-entries=2") || !strings.Contains(gc.Detail, "remaining=2") {
		t.Fatalf("history gc detail = %q, want the staging directory and the unreferenced middle run collected", gc.Detail)
	}
	for _, name := range []string{full, newest} {
		if _, err := os.Stat(filepath.Join(root, "reports", "runs", name)); err != nil {
			t.Fatalf("run %s is referenced or newest and was collected: %v", name, err)
		}
	}
	for _, path := range []string{filepath.Join(root, "reports", "runs", middle), staging} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("%s is beyond the bound and remained: %v", path, err)
		}
	}
}
