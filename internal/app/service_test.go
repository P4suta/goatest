// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package app_test

import (
	"bytes"
	"context"
	"errors"
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

func TestVerifyAndReplayPersistInfrastructureErrorReports(t *testing.T) {
	for _, command := range []cli.Command{cli.CommandVerify, cli.CommandReplay} {
		t.Run(string(command), func(t *testing.T) {
			root := t.TempDir()
			if command == cli.CommandReplay {
				writeLatestFixture(t, root)
			}
			sentinel := errors.New("mutation workspace failed")
			service := app.Service{Root: root, Run: func(context.Context, assure.Options) (report.Report, error) {
				return report.Report{
					Contract: "deep-v1", Snapshot: "snapshot-before-failure",
					Evidence: []report.Evidence{{Kind: "preflight", ID: "build", Status: "passed"}},
				}, sentinel
			}}

			result, err := service.Execute(t.Context(), command, cli.Request{}, "finding-a")
			if !errors.Is(err, sentinel) {
				t.Fatalf("%s error = %v, want runner error", command, err)
			}
			if result.Verdict != report.VerdictError || result.Schema != report.SchemaV1 {
				t.Fatalf("%s result = %+v, want ERROR report", command, result)
			}
			if result.Contract != "deep-v1" || result.Snapshot != "snapshot-before-failure" || len(result.Evidence) != 1 {
				t.Errorf("%s discarded partial evidence: %+v", command, result)
			}
			if len(result.Findings) != 1 || result.Findings[0].Kind != "infrastructure" || !strings.Contains(result.Findings[0].Summary, sentinel.Error()) {
				t.Errorf("%s findings = %+v, want infrastructure diagnostic", command, result.Findings)
			}

			latest, loadErr := service.Execute(t.Context(), cli.CommandReport, cli.Request{}, "")
			if loadErr != nil || latest.Verdict != report.VerdictError || len(latest.Findings) != 1 {
				t.Fatalf("persisted %s report = %+v, %v", command, latest, loadErr)
			}
			for _, path := range []string{".goatest/report.json", "reports/assurance-report-v1.json", "reports/assurance-report-v1.html", "reports/assurance-report-v1.sarif", "reports/assurance-report-v1.junit.xml"} {
				if data, readErr := os.ReadFile(filepath.Join(root, filepath.FromSlash(path))); readErr != nil || len(data) == 0 {
					t.Errorf("error report artifact %s = %d bytes, %v", path, len(data), readErr)
				}
			}
		})
	}
}

func TestCancelledRunDoesNotReplaceTheLatestCompletedReport(t *testing.T) {
	root := t.TempDir()
	writeLatestFixture(t, root)
	service := app.Service{Root: root, Run: func(context.Context, assure.Options) (report.Report, error) {
		return report.Report{Schema: report.SchemaV1, Verdict: report.VerdictError}, context.Canceled
	}}

	result, err := service.Execute(t.Context(), cli.CommandVerify, cli.Request{}, "")
	if !errors.Is(err, context.Canceled) || result.Verdict != "" {
		t.Fatalf("cancelled verify = %+v, %v", result, err)
	}
	latest, loadErr := service.Execute(t.Context(), cli.CommandReport, cli.Request{}, "")
	if loadErr != nil || latest.Verdict != report.VerdictInsufficient || latest.Snapshot != "snapshot" {
		t.Fatalf("latest after cancellation = %+v, %v", latest, loadErr)
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
		Findings: []report.Finding{
			{ID: "finding-a", Kind: "survivor", Summary: "one"},
			{ID: "finding-b", Kind: "coverage", Summary: "two", MutantID: "mutant-b"},
		},
		Repairs: []report.Repair{
			{ID: "repair-a", Finding: "finding-a", Path: "a_test.go", Status: "applied"},
			{ID: "repair-b", Finding: "finding-b", Path: "b_test.go", Status: "candidate"},
		},
	}
	service := app.Service{
		Root: root,
		Now:  func() time.Time { return time.Date(2026, 8, 28, 0, 0, 0, 0, time.UTC) },
		Run: func(_ context.Context, options assure.Options) (report.Report, error) {
			if !options.NoApply || options.ReplayMutantID != "mutant-b" {
				t.Fatalf("replay options = %+v", options)
			}
			return original, nil
		},
	}
	if err := app.WriteReports(root, original); err != nil {
		t.Fatal(err)
	}
	explained, err := service.Execute(t.Context(), cli.CommandExplain, cli.Request{}, "finding-b")
	if err != nil || len(explained.Findings) != 1 || explained.Findings[0].ID != "finding-b" || len(explained.Evidence) != 0 || len(explained.Repairs) != 1 || explained.Repairs[0].ID != "repair-b" {
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

func TestReportRejectsMissingMalformedTrailingAndWrongSchemaArtifacts(t *testing.T) {
	valid := report.JSON(report.Report{Schema: report.SchemaV1, Verdict: report.VerdictAssured})
	for _, testCase := range []struct {
		name    string
		content []byte
		write   bool
		want    string
	}{
		{name: "missing", want: "read latest report"},
		{name: "malformed", content: []byte(`{`), write: true, want: "decode latest report"},
		{name: "trailing", content: append(append([]byte(nil), valid...), []byte(`{}`)...), write: true, want: "trailing data"},
		{name: "schema", content: report.JSON(report.Report{Schema: "future-report-v2", Verdict: report.VerdictAssured}), write: true, want: "unsupported"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			root := t.TempDir()
			if testCase.write {
				path := filepath.Join(root, ".goatest", "report.json")
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, testCase.content, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			_, err := (app.Service{Root: root}).Execute(t.Context(), cli.CommandReport, cli.Request{}, "")
			if err == nil || !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("report error = %v, want %q", err, testCase.want)
			}
		})
	}
}

func TestServicePropagatesInitRunnerPersistenceAndAcceptanceFailures(t *testing.T) {
	sentinel := errors.New("runner failed")
	t.Run("init-existing", func(t *testing.T) {
		root := t.TempDir()
		if err := config.Init(root); err != nil {
			t.Fatal(err)
		}
		if _, err := (app.Service{Root: root}).Execute(t.Context(), cli.CommandInit, cli.Request{}, ""); err == nil {
			t.Fatal("init overwrote an existing configuration")
		}
	})
	for _, command := range []cli.Command{cli.CommandVerify, cli.CommandReplay} {
		t.Run(string(command)+"-runner", func(t *testing.T) {
			root := t.TempDir()
			if command == cli.CommandReplay {
				writeLatestFixture(t, root)
			}
			service := app.Service{Root: root, Run: func(context.Context, assure.Options) (report.Report, error) {
				return report.Report{}, sentinel
			}}
			_, err := service.Execute(t.Context(), command, cli.Request{}, "finding-a")
			if !errors.Is(err, sentinel) {
				t.Fatalf("%s runner error = %v", command, err)
			}
		})
	}

	t.Run("verify-report-write", func(t *testing.T) {
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, ".goatest"), []byte("blocks directory"), 0o644); err != nil {
			t.Fatal(err)
		}
		service := app.Service{Root: root, Run: func(context.Context, assure.Options) (report.Report, error) {
			return report.Report{Schema: report.SchemaV1, Verdict: report.VerdictAssured}, nil
		}}
		if _, err := service.Execute(t.Context(), cli.CommandVerify, cli.Request{}, ""); err == nil {
			t.Fatal("verify ignored report persistence failure")
		}
	})

	t.Run("runner-error-report-write", func(t *testing.T) {
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, ".goatest"), []byte("blocks directory"), 0o644); err != nil {
			t.Fatal(err)
		}
		service := app.Service{Root: root, Run: func(context.Context, assure.Options) (report.Report, error) {
			return report.Report{}, sentinel
		}}
		result, err := service.Execute(t.Context(), cli.CommandVerify, cli.Request{}, "")
		if result.Verdict != report.VerdictError || !errors.Is(err, sentinel) || !strings.Contains(err.Error(), "write report") {
			t.Fatalf("runner/write failure = %+v, %v", result, err)
		}
	})

	t.Run("replay-report-write", func(t *testing.T) {
		root := t.TempDir()
		writeLatestFixture(t, root)
		if err := os.RemoveAll(filepath.Join(root, "reports")); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "reports"), []byte("blocks directory"), 0o644); err != nil {
			t.Fatal(err)
		}
		service := app.Service{Root: root, Run: func(context.Context, assure.Options) (report.Report, error) {
			return report.Report{Schema: report.SchemaV1, Verdict: report.VerdictAssured}, nil
		}}
		if _, err := service.Execute(t.Context(), cli.CommandReplay, cli.Request{}, "finding-a"); err == nil {
			t.Fatal("replay ignored report persistence failure")
		}
	})

	t.Run("accept-config", func(t *testing.T) {
		root := t.TempDir()
		if err := config.Init(root); err != nil {
			t.Fatal(err)
		}
		writeLatestFixture(t, root)
		if err := os.WriteFile(filepath.Join(root, config.FileName), []byte("unknown = true\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := (app.Service{Root: root}).Execute(t.Context(), cli.CommandAccept, cli.Request{}, "finding-a"); err == nil {
			t.Fatal("accept ignored invalid configuration")
		}
	})
}

func TestServiceRejectsMissingFindingsAndUnsupportedCommands(t *testing.T) {
	root := t.TempDir()
	writeLatestFixture(t, root)
	service := app.Service{Root: root}
	for _, command := range []cli.Command{cli.CommandExplain, cli.CommandAccept, cli.CommandReplay} {
		if _, err := service.Execute(t.Context(), command, cli.Request{}, "missing"); err == nil || !strings.Contains(err.Error(), "absent") {
			t.Errorf("%s missing finding error = %v", command, err)
		}
	}
	if _, err := service.Execute(t.Context(), cli.Command("future"), cli.Request{}, ""); err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("unsupported command error = %v", err)
	}
}

func TestFindingCommandsPreserveLatestReportLoadFailures(t *testing.T) {
	for _, command := range []cli.Command{cli.CommandExplain, cli.CommandAccept, cli.CommandReplay} {
		t.Run(string(command), func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, ".goatest", "report.json")
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("{"), 0o644); err != nil {
				t.Fatal(err)
			}
			_, err := (app.Service{Root: root}).Execute(t.Context(), command, cli.Request{}, "finding-a")
			if err == nil || !strings.Contains(err.Error(), "decode latest report") {
				t.Fatalf("%s load error = %v", command, err)
			}
		})
	}
}

func writeLatestFixture(t *testing.T, root string) {
	t.Helper()
	input := report.Report{
		Schema: report.SchemaV1, Verdict: report.VerdictInsufficient, Contract: "standard-v1", Snapshot: "snapshot",
		Findings: []report.Finding{{ID: "finding-a", Kind: "survivor", Summary: "survived"}},
	}
	if err := app.WriteReports(root, input); err != nil {
		t.Fatal(err)
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
