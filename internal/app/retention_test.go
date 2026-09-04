// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package app_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/P4suta/goatest/internal/app"
	"github.com/P4suta/goatest/internal/assure"
	"github.com/P4suta/goatest/internal/cache"
	"github.com/P4suta/goatest/internal/checkpoint"
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

// storedArtifact writes one file into a .goatest store, stamped so that the
// tests below know which of them is oldest.
func storedArtifact(t *testing.T, root, store, name string, moment time.Time) {
	t.Helper()
	directory := filepath.Join(root, ".goatest", store)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, name)
	if err := os.WriteFile(path, []byte("1234567890"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, moment, moment); err != nil {
		t.Fatal(err)
	}
}

func TestCacheMaintenanceBoundsStoredCandidatesAndPatches(t *testing.T) {
	root := t.TempDir()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	storedArtifact(t, root, "candidates", "0000000000000001.json", base)
	storedArtifact(t, root, "candidates", "0000000000000002.json", base.Add(time.Hour))
	storedArtifact(t, root, "patches", "0000000000000003.json", base)
	if err := os.WriteFile(filepath.Join(root, ".goatest.toml"), []byte("version = 1\n[cache]\nmax_bytes = 10\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// The clock is injected so that the byte budget is the only thing deciding
	// what goes: with a real one the fixtures would be older than the TTL and
	// the test would pass without a bound ever being applied.
	moment := base.Add(2 * time.Hour)
	service := app.Service{Root: root, TempDirectory: t.TempDir(), Now: func() time.Time { return moment }}

	status, err := service.Execute(t.Context(), cli.CommandCache, cli.Request{}, "status")
	if err != nil {
		t.Fatal(err)
	}
	candidates, index := findEvidence(t, status, "repair-retention", "candidates-status")
	patches, patchIndex := findEvidence(t, status, "repair-retention", "patches-status")
	// The two stores the repair path writes are reported after the run history
	// and before the machine's build cache: everything a reader sees up to here
	// is what this .goatest holds.
	if index != 5 || patchIndex != 6 || status.Evidence[7].Kind != "build-cache" {
		t.Fatalf("repair stores reported at %d and %d in %+v", index, patchIndex, status.Evidence)
	}
	if candidates.Status != "ready" || !strings.Contains(candidates.Detail, "entries=2 bytes=20") ||
		!strings.Contains(candidates.Detail, "oldest=2026-01-01T00:00:00Z") {
		t.Fatalf("candidates status = %+v", candidates)
	}
	if patches.Status != "ready" || !strings.Contains(patches.Detail, "entries=1 bytes=10") {
		t.Fatalf("patches status = %+v", patches)
	}

	collected, err := service.Execute(t.Context(), cli.CommandCache, cli.Request{}, "gc")
	if err != nil {
		t.Fatal(err)
	}
	candidatesGC, gcIndex := findEvidence(t, collected, "repair-retention", "candidates-gc")
	patchesGC, patchGCIndex := findEvidence(t, collected, "repair-retention", "patches-gc")
	if gcIndex != 7 || patchGCIndex != 8 || collected.Evidence[9].Kind != "build-cache" {
		t.Fatalf("repair collections reported at %d and %d in %+v", gcIndex, patchGCIndex, collected.Evidence)
	}
	if candidatesGC.Status != "completed" || !strings.Contains(candidatesGC.Detail, "removed-entries=1 removed-bytes=10") {
		t.Fatalf("candidates gc = %+v", candidatesGC)
	}
	if patchesGC.Status != "completed" || !strings.Contains(patchesGC.Detail, "removed-entries=0") {
		t.Fatalf("patches gc = %+v", patchesGC)
	}
	if _, err := os.Stat(filepath.Join(root, ".goatest", "candidates", "0000000000000002.json")); err != nil {
		t.Fatalf("the newest candidate was collected: %v", err)
	}
}

func TestStoredCandidatesSurviveWhileACheckpointCouldStillResume(t *testing.T) {
	root := t.TempDir()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	storedArtifact(t, root, "candidates", "0000000000000001.json", base)
	storedArtifact(t, root, "candidates", "0000000000000002.json", base.Add(time.Hour))
	storedArtifact(t, root, "patches", "0000000000000003.json", base)
	// A budget the candidate store is over and the verdict cache is not, so
	// that the checkpoint below survives its own store's collection and the
	// only thing standing between the candidates and removal is the guard.
	if err := os.WriteFile(filepath.Join(root, ".goatest.toml"), []byte("version = 1\n[cache]\nmax_bytes = 4096\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// An interrupted run left state behind. It re-validates the candidates it
	// checkpointed by ID when it resumes, so collecting one now would turn a
	// resumable run into a cold one.
	digest := strings.Repeat("a", 64)
	store := cache.New(filepath.Join(root, ".goatest", "cache"))
	if err := store.PutCheckpoint(digest, checkpoint.State{Schema: checkpoint.SchemaV1, InputDigest: digest, Attempts: 1}); err != nil {
		t.Fatal(err)
	}
	moment := base.Add(2 * time.Hour)
	service := app.Service{Root: root, TempDirectory: t.TempDir(), Now: func() time.Time { return moment }}
	collected, err := service.Execute(t.Context(), cli.CommandCache, cli.Request{}, "gc")
	if err != nil {
		t.Fatal(err)
	}
	candidatesGC, _ := findEvidence(t, collected, "repair-retention", "candidates-gc")
	if candidatesGC.Status != "skipped" || candidatesGC.Detail != "a checkpoint is in progress" {
		t.Fatalf("candidates gc under a checkpoint = %+v", candidatesGC)
	}
	for _, name := range []string{"0000000000000001.json", "0000000000000002.json"} {
		if _, err := os.Stat(filepath.Join(root, ".goatest", "candidates", name)); err != nil {
			t.Fatalf("candidate %s was collected while a run could resume: %v", name, err)
		}
	}
	// Nothing reads a stored patch artifact, so no checkpoint has a claim on it.
	patchesGC, _ := findEvidence(t, collected, "repair-retention", "patches-gc")
	if patchesGC.Status != "completed" {
		t.Fatalf("patches gc under a checkpoint = %+v", patchesGC)
	}
}

func TestEveryRunBoundsTheRepairStoresAndNotesWhatItCannotRead(t *testing.T) {
	root := t.TempDir()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	storedArtifact(t, root, "candidates", "0000000000000001.json", base)
	storedArtifact(t, root, "candidates", "0000000000000002.json", base.Add(time.Hour))
	if err := os.WriteFile(filepath.Join(root, ".goatest.toml"), []byte("version = 1\n[cache]\nmax_bytes = 10\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	service := historyService(t, root)
	historyRun(t, service, nil)
	if _, err := os.Stat(filepath.Join(root, ".goatest", "candidates", "0000000000000001.json")); !os.IsNotExist(err) {
		t.Fatalf("the run did not bound the candidate store: %v", err)
	}

	// A store holding something it cannot be: a note, and a run that still ends
	// exactly as it would have.
	if err := os.Mkdir(filepath.Join(root, ".goatest", "patches"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, ".goatest", "patches", "unexpected"), 0o755); err != nil {
		t.Fatal(err)
	}
	var progress bytes.Buffer
	noted := historyService(t, root)
	noted.Progress = &progress
	if _, err := noted.Execute(t.Context(), cli.CommandVerify, cli.Request{}, ""); err != nil {
		t.Fatalf("a store that cannot be read failed a run: %v", err)
	}
	if !strings.Contains(progress.String(), "repair-gc-unavailable") {
		t.Fatalf("progress = %q, want the unusable store reported as a note", progress.String())
	}
}

// boundedHistoryRoot is a repository whose history holds one run.
func boundedHistoryRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".goatest.toml"), []byte("version = 1\n[reports]\nkeep = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}

// historyEntries is what reports/runs holds right now.
func historyEntries(t *testing.T, root string) []string {
	t.Helper()
	children, err := os.ReadDir(filepath.Join(root, "reports", "runs"))
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(children))
	for _, child := range children {
		names = append(names, child.Name())
	}
	return names
}

func TestEveryRunBoundsTheHistoryItJustExtended(t *testing.T) {
	t.Run("a completed run collects the runs it made obsolete", func(t *testing.T) {
		root := boundedHistoryRoot(t)
		service := historyService(t, root)
		historyRun(t, service, nil)
		historyRun(t, service, nil)
		newest := historyRun(t, service, nil)
		// Nobody typed 'cache gc'. The run that just finished is the newest
		// entry and the one both indexes name, so it survives by construction.
		if entries := historyEntries(t, root); !slices.Equal(entries, []string{newest}) {
			t.Fatalf("history after three runs = %v, want only %s", entries, newest)
		}
	})

	t.Run("a run that failed collects as well", func(t *testing.T) {
		root := boundedHistoryRoot(t)
		service := app.Service{
			Root: root, TempDirectory: t.TempDir(),
			Run: func(context.Context, assure.Options) (report.Report, error) {
				return report.Report{}, errors.New("the toolchain went missing")
			},
		}
		for range 2 {
			if _, err := service.Execute(t.Context(), cli.CommandVerify, cli.Request{}, ""); err == nil {
				t.Fatal("a failing run reported success")
			}
		}
		// The bundle a failed run writes is exactly the history somebody needs,
		// so the collection has to run on that path too rather than letting a
		// repository that only ever fails grow without a bound.
		if entries := historyEntries(t, root); len(entries) != 1 {
			t.Fatalf("history after two failed runs = %v, want one run", entries)
		}
	})

	t.Run("a history that cannot be listed is a note rather than a failed run", func(t *testing.T) {
		root := boundedHistoryRoot(t)
		var progress bytes.Buffer
		service := historyService(t, root)
		service.Progress = &progress
		historyRun(t, service, nil)
		// Something the history is not supposed to hold. Nothing about it may
		// reach the verdict of a run.
		if err := os.WriteFile(filepath.Join(root, "reports", "runs", "notes.txt"), []byte("hand written"), 0o600); err != nil {
			t.Fatal(err)
		}
		newest := historyRun(t, service, nil)
		if !strings.Contains(progress.String(), "reports-gc-unavailable") {
			t.Fatalf("progress = %q, want the unusable history reported as a note", progress.String())
		}
		if _, err := os.Stat(filepath.Join(root, "reports", "runs", newest)); err != nil {
			t.Fatalf("the run that could not collect did not keep its own report: %v", err)
		}
	})
}

func TestACollectedRunIsNamedByReportAndLeavesEveryLatestCommandWorking(t *testing.T) {
	root := t.TempDir()
	finding := report.Finding{ID: "finding-a", Kind: "survivor", Path: "value.go", Line: 3, Summary: "survived", MutantID: "mutant-a"}
	service := app.Service{
		Root: root, TempDirectory: t.TempDir(),
		Run: func(context.Context, assure.Options) (report.Report, error) {
			return report.Report{
				Schema: report.SchemaV1, Verdict: report.VerdictInsufficient, Contract: "standard-v1", Snapshot: "snapshot-a",
				Findings: []report.Finding{finding},
			}, nil
		},
	}
	if err := os.WriteFile(filepath.Join(root, ".goatest.toml"), []byte("version = 1\n[reports]\nkeep = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	older := historyRun(t, service, nil)
	newest := historyRun(t, service, nil)
	if _, err := service.Execute(t.Context(), cli.CommandCache, cli.Request{}, "gc"); err != nil {
		t.Fatal(err)
	}

	_, err := service.Execute(t.Context(), cli.CommandReport, cli.Request{ReportRunID: older}, "")
	want := `goatest: report run "` + older + `" is not in reports/runs: it was collected or never written`
	if err == nil || err.Error() != want {
		t.Fatalf("collected report error = %v, want %s", err, want)
	}
	if kept, err := service.Execute(t.Context(), cli.CommandReport, cli.Request{ReportRunID: newest}, ""); err != nil || kept.RunID != newest {
		t.Fatalf("newest report = %+v, %v", kept, err)
	}
	// explain, accept and replay read .goatest/latest-any.json rather than the
	// history, so a bound of one leaves every one of them working.
	if explained, err := service.Execute(t.Context(), cli.CommandExplain, cli.Request{}, finding.ID); err != nil || len(explained.Findings) != 1 {
		t.Fatalf("explain after a bound of one = %+v, %v", explained, err)
	}
	if replayed, err := service.Execute(t.Context(), cli.CommandReplay, cli.Request{}, finding.ID); err != nil || replayed.RunKind != report.RunReplay {
		t.Fatalf("replay after a bound of one = %+v, %v", replayed, err)
	}
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
