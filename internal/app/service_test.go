// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package app_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/P4suta/goatest/internal/app"
	"github.com/P4suta/goatest/internal/assure"
	"github.com/P4suta/goatest/internal/cli"
	"github.com/P4suta/goatest/internal/config"
	"github.com/P4suta/goatest/internal/report"
)

func TestVerifyWritesEveryDeterministicReportAndReportReadsLatest(t *testing.T) {
	root := t.TempDir()
	result := report.Report{
		Schema: report.SchemaV1, Verdict: report.VerdictInsufficient, Contract: "deep-v1", Snapshot: "snapshot-a",
		Findings: []report.Finding{{ID: "finding-a", Kind: "survivor", Path: "a.go", Line: 3, Summary: "survived"}},
	}
	var got assure.Options
	var progress bytes.Buffer
	service := app.Service{
		Root: root, Progress: &progress,
		Run: func(_ context.Context, options assure.Options) (report.Report, error) {
			got = options
			return result, nil
		},
	}
	verified, err := service.Execute(t.Context(), cli.CommandVerify, cli.Request{
		Changed: true, ChangedRef: "origin/main", Contract: "deep-v1", NoApply: true,
	}, "")
	if err != nil || verified.Snapshot != result.Snapshot {
		t.Fatalf("verify = %+v, %v", verified, err)
	}
	if !got.Changed || got.ChangedRef != "origin/main" || got.Contract != "deep-v1" || !got.NoApply {
		t.Fatalf("runner options = %+v", got)
	}
	for _, path := range []string{
		".goatest/report.json", "reports/assurance-report-v1.json", "reports/assurance-report-v1.html",
		"reports/assurance-report-v1.sarif", "reports/assurance-report-v1.junit.xml", "reports/assurance-report-v1.schema.json",
	} {
		data, readErr := os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
		if readErr != nil || len(data) == 0 {
			t.Errorf("artifact %s = %d bytes, %v", path, len(data), readErr)
		}
	}
	loaded, err := service.Execute(t.Context(), cli.CommandReport, cli.Request{}, "")
	if err != nil || loaded.Snapshot != result.Snapshot || len(loaded.Findings) != 1 {
		t.Fatalf("report = %+v, %v", loaded, err)
	}
}

func TestNoTUIWritesDeterministicProgressImmediately(t *testing.T) {
	root := t.TempDir()
	var progress bytes.Buffer
	service := app.Service{
		Root: root, Progress: &progress,
		Run: func(_ context.Context, options assure.Options) (report.Report, error) {
			if options.Progress == nil {
				t.Fatal("--no-tui disabled progress")
			}
			options.Progress(assure.Event{Kind: "snapshot", Detail: "captured"})
			if got, want := progress.String(), "goatest: snapshot           captured\n"; got != want {
				t.Fatalf("progress before runner returned = %q, want %q", got, want)
			}
			return report.Report{Schema: report.SchemaV1, Verdict: report.VerdictAssured}, nil
		},
	}
	if _, err := service.Execute(t.Context(), cli.CommandVerify, cli.Request{NoTUI: true}, ""); err != nil {
		t.Fatal(err)
	}
}

func TestProgressEscapesTerminalControlCharacters(t *testing.T) {
	var progress bytes.Buffer
	service := app.Service{
		Root: t.TempDir(), Progress: &progress,
		Run: func(_ context.Context, options assure.Options) (report.Report, error) {
			options.Progress(assure.Event{Kind: "phase\nforged", Detail: "detail\x1b[31m"})
			return report.Report{Schema: report.SchemaV1, Verdict: report.VerdictAssured}, nil
		},
	}
	if _, err := service.Execute(t.Context(), cli.CommandVerify, cli.Request{NoTUI: true}, ""); err != nil {
		t.Fatal(err)
	}
	got := progress.String()
	if strings.Count(got, "\n") != 1 || strings.ContainsAny(got, "\r\x1b\t") {
		t.Fatalf("progress retained terminal control characters: %q", got)
	}
	for _, escaped := range []string{`phase\nforged`, `detail\u001b[31m`} {
		if !strings.Contains(got, escaped) {
			t.Errorf("progress omitted escaped text %q: %q", escaped, got)
		}
	}
}

func TestExplainAcceptAndReplayOperateOnStableFindingIdentity(t *testing.T) {
	root := t.TempDir()
	if err := config.Init(root); err != nil {
		t.Fatal(err)
	}
	original := report.Report{
		Schema: report.SchemaV1, Verdict: report.VerdictInsufficient, Contract: "standard-v1", Snapshot: "snapshot-a",
		Findings: []report.Finding{{ID: "finding-a", Kind: "survivor", Summary: "one"}, {ID: "finding-b", Kind: "coverage", Summary: "two"}},
	}
	service := app.Service{
		Root: root,
		Now:  func() time.Time { return time.Date(2026, 8, 28, 0, 0, 0, 0, time.UTC) },
		Run: func(_ context.Context, options assure.Options) (report.Report, error) {
			if !options.NoApply {
				t.Fatal("replay did not disable application")
			}
			return original, nil
		},
	}
	if err := app.WriteReports(root, original); err != nil {
		t.Fatal(err)
	}
	explained, err := service.Execute(t.Context(), cli.CommandExplain, cli.Request{}, "finding-b")
	if err != nil || len(explained.Findings) != 1 || explained.Findings[0].ID != "finding-b" {
		t.Fatalf("explain = %+v, %v", explained, err)
	}
	accepted, err := service.Execute(t.Context(), cli.CommandAccept, cli.Request{}, "finding-a")
	if err != nil || accepted.Verdict != report.VerdictAssured {
		t.Fatalf("accept = %+v, %v", accepted, err)
	}
	loaded, err := config.Load(root)
	if err != nil || len(loaded.Acceptance) != 1 || loaded.Acceptance[0].ID != "finding-a" || !loaded.Acceptance[0].Expires.Equal(time.Date(2026, 9, 27, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("config = %+v, %v", loaded, err)
	}
	replayed, err := service.Execute(t.Context(), cli.CommandReplay, cli.Request{}, "finding-b")
	if err != nil || replayed.Verdict != report.VerdictInsufficient {
		t.Fatalf("replay = %+v, %v", replayed, err)
	}
	if _, err := service.Execute(t.Context(), cli.CommandExplain, cli.Request{}, "missing"); err == nil {
		t.Fatal("missing finding was accepted")
	}
}

func TestInitCreatesStrictConfigWithoutRunningAssurance(t *testing.T) {
	root := t.TempDir()
	service := app.Service{Root: root, Run: func(context.Context, assure.Options) (report.Report, error) {
		t.Fatal("init invoked assurance")
		return report.Report{}, nil
	}}
	result, err := service.Execute(t.Context(), cli.CommandInit, cli.Request{}, "")
	if err != nil || result.Verdict != report.VerdictAssured {
		t.Fatalf("init = %+v, %v", result, err)
	}
	if _, err := config.Load(root); err != nil {
		t.Fatal(err)
	}
}
