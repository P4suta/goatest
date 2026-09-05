// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package app_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/P4suta/goatest/internal/app"
	"github.com/P4suta/goatest/internal/assure"
	"github.com/P4suta/goatest/internal/cache"
	"github.com/P4suta/goatest/internal/cli"
	"github.com/P4suta/goatest/internal/evidence"
	"github.com/P4suta/goatest/internal/provider"
	"github.com/P4suta/goatest/internal/repair"
	"github.com/P4suta/goatest/internal/report"
)

type operationValidator struct{ original, kills, suite int }

func (validator *operationValidator) OriginalStable(context.Context, provider.Candidate) error {
	validator.original++
	return nil
}
func (validator *operationValidator) Kills(context.Context, report.Finding, provider.Candidate) error {
	validator.kills++
	return nil
}
func (validator *operationValidator) Suite(context.Context, provider.Candidate) error {
	validator.suite++
	return nil
}

func TestFixPreviewsThenFreshlyValidatesAndExplicitlyAppliesCandidate(t *testing.T) {
	root := t.TempDir()
	finding := report.Finding{ID: "finding-a", Kind: "surviving-mutant", Summary: "survived", MutantID: "mutant-a"}
	candidate := provider.Candidate{Kind: "patch", Path: "generated_test.go", Content: []byte("package fixture\n")}
	record := repair.CandidateRecord{
		Version: repair.CandidateVersion, ID: "0123456789abcdef", Snapshot: "snapshot-a",
		Finding: finding, Candidate: candidate, Validation: "passed",
	}
	if _, err := repair.StoreCandidate(root, record); err != nil {
		t.Fatal(err)
	}
	validator := &operationValidator{}
	service := app.Service{Root: root, FixValidator: validator}
	preview, err := service.Execute(t.Context(), cli.CommandFix, cli.Request{IDs: []string{record.ID}}, "")
	if err != nil || preview.Verdict != report.VerdictCompleted || len(preview.Repairs) != 1 || preview.Repairs[0].Status != "candidate" || !strings.Contains(preview.Repairs[0].Diff, "+++ b/generated_test.go") {
		t.Fatalf("preview = %+v, %v", preview, err)
	}
	if validator.original != 0 {
		t.Fatal("preview reran validation")
	}
	if _, err := os.Stat(filepath.Join(root, candidate.Path)); !os.IsNotExist(err) {
		t.Fatalf("preview changed source: %v", err)
	}
	applied, err := service.Execute(t.Context(), cli.CommandFix, cli.Request{IDs: []string{record.ID}, Apply: true}, "")
	if err != nil || applied.Verdict != report.VerdictCompleted || applied.Repairs[0].Status != "applied" {
		t.Fatalf("apply = %+v, %v", applied, err)
	}
	if validator.original != 3 || validator.kills != 2 || validator.suite != 1 {
		t.Fatalf("fresh validation calls = (%d,%d,%d)", validator.original, validator.kills, validator.suite)
	}
	contents, err := os.ReadFile(filepath.Join(root, candidate.Path))
	if err != nil || string(contents) != string(candidate.Content) {
		t.Fatalf("applied source = %q, %v", contents, err)
	}
}

func TestPlanDispatchIsReadOnlyAndCacheCommandsReportAndCollect(t *testing.T) {
	root := t.TempDir()
	planCalls, runCalls := 0, 0
	service := app.Service{
		Root: root,
		// The cache commands below sweep the temporary directory they are
		// given, so this test gives them one of its own.
		TempDirectory: t.TempDir(),
		Plan: func(_ context.Context, options assure.Options) (report.Report, error) {
			planCalls++
			if !options.NoApply || !options.PackageScope || len(options.Packages) != 1 {
				t.Fatalf("plan options = %+v", options)
			}
			return report.Report{Schema: report.SchemaV1, RunKind: report.RunOperation, Verdict: report.VerdictCompleted}, nil
		},
		Run: func(context.Context, assure.Options) (report.Report, error) {
			runCalls++
			return report.Report{}, nil
		},
	}
	planned, err := service.Execute(t.Context(), cli.CommandPlan, cli.Request{Packages: []string{"./pkg"}}, "")
	if err != nil || planned.Verdict != report.VerdictCompleted || planCalls != 1 || runCalls != 0 {
		t.Fatalf("plan = %+v, %v calls=%d/%d", planned, err, planCalls, runCalls)
	}
	if err := report.ValidateForPersistence(planned); err != nil {
		t.Fatalf("plan operation report is not self-contained: %v", err)
	}
	store := cache.New(filepath.Join(root, ".goatest", "cache"))
	if err := store.Put("entry-a", report.Report{Schema: report.SchemaV1, Snapshot: "entry-a"}); err != nil {
		t.Fatal(err)
	}
	status, err := service.Execute(t.Context(), cli.CommandCache, cli.Request{}, "status")
	if err != nil || status.Verdict != report.VerdictCompleted || !strings.Contains(status.Evidence[1].Detail, "entries=1") {
		t.Fatalf("cache status = %+v, %v", status, err)
	}
	if err := os.WriteFile(filepath.Join(root, ".goatest.toml"), []byte("version = 1\ncontract = \"standard-v1\"\n[cache]\nmax_bytes = 1\nttl = \"720h\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	collected, err := service.Execute(t.Context(), cli.CommandCache, cli.Request{}, "gc")
	if err != nil || collected.Verdict != report.VerdictCompleted || !strings.Contains(collected.Evidence[2].Detail, "removed-entries=1") {
		t.Fatalf("cache gc = %+v, %v", collected, err)
	}
}

func TestCacheStatusReportsMutationEvidenceAndFlushForgetsOnlyReusableResults(t *testing.T) {
	root := t.TempDir()
	service := app.Service{Root: root, TempDirectory: t.TempDir()}
	cacheRoot := filepath.Join(root, ".goatest", "cache")
	store := cache.New(cacheRoot)
	if err := store.Put("entry-a", report.Report{Schema: report.SchemaV1, Snapshot: "entry-a"}); err != nil {
		t.Fatal(err)
	}
	digest := func(character string) string { return strings.Repeat(character, 64) }
	mutationPath := filepath.Join(cacheRoot, evidence.MutationFileName)
	if err := evidence.SaveMutation(mutationPath, evidence.MutationStore{
		ModulePath: "example/module",
		Records: []evidence.MutationRecord{{
			MutantID: digest("a"), Path: "value.go", Package: "example/module/pkg",
			Outcome: evidence.MutationOutcomeKilled, Provenance: "snapshot=" + digest("f"),
			KilledBy: &evidence.TargetKey{
				Package: "example/module/pkg", Name: "TestValue", Kind: "test", Key: digest("1"),
			},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	preserved := []string{
		filepath.Join(root, ".goatest", "trace", "run-a", "events.jsonl"),
		filepath.Join(root, ".goatest", "diagnostics", "run-a", "detail.txt"),
		filepath.Join(root, ".goatest", "candidates", "candidate.json"),
		filepath.Join(root, ".goatest", "patches", "patch.json"),
		filepath.Join(root, "reports", "runs", "run-a", "report.json"),
	}
	for _, path := range preserved {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("preserve"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	status, err := service.Execute(t.Context(), cli.CommandCache, cli.Request{}, "status")
	if err != nil {
		t.Fatal(err)
	}
	mutationStatus, _ := findEvidence(t, status, "mutation-evidence", "status")
	if mutationStatus.Status != "ready" || !strings.Contains(mutationStatus.Detail, "records=1 killed=1") ||
		!strings.Contains(mutationStatus.Detail, "module=example/module") {
		t.Fatalf("mutation status = %+v", mutationStatus)
	}
	collected, err := service.Execute(t.Context(), cli.CommandCache, cli.Request{}, "gc")
	if err != nil {
		t.Fatal(err)
	}
	mutationGC, _ := findEvidence(t, collected, "mutation-evidence", "gc")
	if mutationGC.Status != "retained" || !strings.Contains(mutationGC.Detail, "records=1 killed=1") {
		t.Fatalf("mutation gc status = %+v", mutationGC)
	}
	if _, found, err := evidence.LoadMutation(mutationPath, "example/module"); err != nil || !found {
		t.Fatalf("mutation evidence after gc found=%v err=%v", found, err)
	}

	flushed, err := service.Execute(t.Context(), cli.CommandCache, cli.Request{}, "flush")
	if err != nil || flushed.Verdict != report.VerdictCompleted {
		t.Fatalf("cache flush = %+v, %v", flushed, err)
	}
	flushEvidence, _ := findEvidence(t, flushed, "cache", "flush")
	if !strings.Contains(flushEvidence.Detail, "removed-entries=1") ||
		!strings.Contains(flushEvidence.Detail, "mutation-removed=true mutation-records=1") {
		t.Fatalf("flush evidence = %+v", flushEvidence)
	}
	mutationAfter, _ := findEvidence(t, flushed, "mutation-evidence", "flush-after")
	if mutationAfter.Status != "missing" {
		t.Fatalf("mutation after flush = %+v", mutationAfter)
	}
	if _, found, err := store.Get("entry-a"); err != nil || found {
		t.Fatalf("exact cache after flush found=%v err=%v", found, err)
	}
	if _, err := os.Stat(mutationPath); !os.IsNotExist(err) {
		t.Fatalf("mutation evidence remains after flush: %v", err)
	}
	for _, path := range preserved {
		if contents, err := os.ReadFile(path); err != nil || string(contents) != "preserve" {
			t.Fatalf("preserved artifact %s = %q, %v", path, contents, err)
		}
	}

	again, err := service.Execute(t.Context(), cli.CommandCache, cli.Request{}, "flush")
	if err != nil {
		t.Fatal(err)
	}
	againEvidence, _ := findEvidence(t, again, "cache", "flush")
	if !strings.Contains(againEvidence.Detail, "removed-entries=0") || !strings.Contains(againEvidence.Detail, "mutation-removed=false") {
		t.Fatalf("idempotent flush = %+v", againEvidence)
	}
	if err := os.WriteFile(mutationPath, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	invalid, err := service.Execute(t.Context(), cli.CommandCache, cli.Request{}, "status")
	if err != nil {
		t.Fatalf("status of malformed evidence: %v", err)
	}
	invalidEvidence, _ := findEvidence(t, invalid, "mutation-evidence", "status")
	if invalidEvidence.Status != "invalid" || !strings.Contains(invalidEvidence.Detail, "decode mutation evidence") {
		t.Fatalf("malformed mutation status = %+v", invalidEvidence)
	}
	if _, err := service.Execute(t.Context(), cli.CommandCache, cli.Request{}, "flush"); err != nil {
		t.Fatalf("flush of malformed evidence: %v", err)
	}
	if _, err := os.Stat(mutationPath); !os.IsNotExist(err) {
		t.Fatalf("malformed mutation evidence remains: %v", err)
	}
}

func TestCacheFlushPreflightsEvidenceBeforeRemovingExactCache(t *testing.T) {
	root := t.TempDir()
	cacheRoot := filepath.Join(root, ".goatest", "cache")
	store := cache.New(cacheRoot)
	if err := store.Put("entry-a", report.Report{Schema: report.SchemaV1, Snapshot: "entry-a"}); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(cacheRoot, evidence.MutationFileName), 0o700); err != nil {
		t.Fatal(err)
	}
	service := app.Service{Root: root, TempDirectory: t.TempDir()}
	if _, err := service.Execute(t.Context(), cli.CommandCache, cli.Request{}, "flush"); err == nil || !strings.Contains(err.Error(), "refusing") {
		t.Fatalf("cache flush error = %v", err)
	}
	if _, found, err := store.Get("entry-a"); err != nil || !found {
		t.Fatalf("exact cache changed before evidence refusal: found=%v err=%v", found, err)
	}
}

func TestDoctorReturnsAuditableErrorInsteadOfRunningAssurance(t *testing.T) {
	root := t.TempDir()
	service := app.Service{Root: root, GoBinary: "definitely-missing-goatest-go", Run: func(context.Context, assure.Options) (report.Report, error) {
		t.Fatal("doctor ran assurance")
		return report.Report{}, nil
	}}
	result, err := service.Execute(t.Context(), cli.CommandDoctor, cli.Request{}, "")
	if err != nil || result.Verdict != report.VerdictError || len(result.Findings) != 1 || result.Findings[0].Kind != "doctor-toolchain" {
		t.Fatalf("doctor = %+v, %v", result, err)
	}
}

var _ repair.Validator = (*operationValidator)(nil)
