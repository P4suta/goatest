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
