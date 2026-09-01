// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package app_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
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
		Changed: true, ChangedRef: "origin/main", Contract: "deep-v1",
	}, "")
	if err != nil || verified.Snapshot != result.Snapshot {
		t.Fatalf("verify = %+v, %v", verified, err)
	}
	if !got.Changed || got.ChangedRef != "origin/main" || got.Contract != "deep-v1" || !got.NoApply {
		t.Fatalf("runner options = %+v", got)
	}
	for _, path := range []string{
		".goatest/latest-any.json", "reports/latest-any.json",
		"reports/runs/" + verified.RunID + "/assurance-report-v1.json",
		"reports/runs/" + verified.RunID + "/assurance-report-v1.html",
		"reports/runs/" + verified.RunID + "/assurance-report-v1.sarif",
		"reports/runs/" + verified.RunID + "/assurance-report-v1.junit.xml",
		"reports/runs/" + verified.RunID + "/assurance-report-v1.schema.json",
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

func TestDefaultAllPatternIsFullButNarrowPatternIsPackageScope(t *testing.T) {
	for _, test := range []struct {
		name         string
		packages     []string
		packageScope bool
		kind         report.RunKind
		verdict      report.Verdict
	}{
		{name: "all", packages: []string{"./..."}, kind: report.RunFull, verdict: report.VerdictAssured},
		{name: "narrow", packages: []string{"./internal/report"}, packageScope: true, kind: report.RunPackage, verdict: report.VerdictScopeAssured},
	} {
		t.Run(test.name, func(t *testing.T) {
			var received assure.Options
			service := app.Service{Root: t.TempDir(), Run: func(_ context.Context, options assure.Options) (report.Report, error) {
				received = options
				return report.Report{Schema: report.SchemaV1, Verdict: report.VerdictAssured, Contract: "standard-v1", Snapshot: test.name}, nil
			}}
			result, err := service.Execute(t.Context(), cli.CommandVerify, cli.Request{Packages: test.packages}, "")
			if err != nil || result.RunKind != test.kind || result.Verdict != test.verdict || received.PackageScope != test.packageScope {
				t.Fatalf("verify = %+v, %v options=%+v", result, err, received)
			}
		})
	}
}

func TestChangesetHistoryNeverReplacesLatestFull(t *testing.T) {
	root := t.TempDir()
	result := report.Report{Schema: report.SchemaV1, Verdict: report.VerdictAssured, Contract: "standard-v1"}
	service := app.Service{Root: root, Run: func(_ context.Context, options assure.Options) (report.Report, error) {
		copy := result
		if options.Changed {
			copy.Snapshot = "changeset-snapshot"
			copy.Accounting.Mutants = report.MutantAccounting{
				Discovered: 2396, Selected: 13, Executed: 13, Killed: 13, OutOfScope: 2383,
			}
			copy.Scope.Resolved = report.ScopeSpec{Kind: "changeset", Project: ".", Files: []string{"changed.go"}}
			copy.Mutants = mutantInventory(2396, 13)
		} else {
			copy.Snapshot = "full-snapshot"
			copy.Accounting.Mutants = report.MutantAccounting{Discovered: 2396, Selected: 2396, Executed: 2396, Killed: 2396}
			copy.Mutants = mutantInventory(2396, 2396)
		}
		return copy, nil
	}}
	full, err := service.Execute(t.Context(), cli.CommandVerify, cli.Request{}, "")
	if err != nil || full.Verdict != report.VerdictAssured {
		t.Fatalf("full = %+v, %v", full, err)
	}
	changed, err := service.Execute(t.Context(), cli.CommandVerify, cli.Request{Changed: true}, "")
	if err != nil || changed.Verdict != report.VerdictChangeAssured || changed.RunID == full.RunID {
		t.Fatalf("changeset = %+v, %v", changed, err)
	}
	latestAny, err := service.Execute(t.Context(), cli.CommandReport, cli.Request{}, "")
	if err != nil || latestAny.RunID != changed.RunID || latestAny.Accounting.Mutants.Selected != 13 {
		t.Fatalf("latest-any = %+v, %v", latestAny, err)
	}
	latestFull, err := service.Execute(t.Context(), cli.CommandReport, cli.Request{ReportLatestFull: true}, "")
	if err != nil || latestFull.RunID != full.RunID || latestFull.Accounting.Mutants.Selected != 2396 {
		t.Fatalf("latest-full = %+v, %v", latestFull, err)
	}
	historical, err := service.Execute(t.Context(), cli.CommandReport, cli.Request{ReportRunID: changed.RunID}, "")
	if err != nil || historical.RunID != changed.RunID {
		t.Fatalf("historical = %+v, %v", historical, err)
	}
}

func mutantInventory(total, selected int) []report.MutantDisposition {
	result := make([]report.MutantDisposition, total)
	for index := range total {
		status := report.MutantOutOfScope
		if index < selected {
			status = report.MutantKilled
		}
		result[index] = report.MutantDisposition{ID: fmt.Sprintf("mutant-%04d", index), Status: status}
	}
	return result
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
			for _, path := range []string{
				".goatest/latest-any.json", "reports/latest-any.json",
				"reports/runs/" + result.RunID + "/assurance-report-v1.json",
				"reports/runs/" + result.RunID + "/assurance-report-v1.html",
				"reports/runs/" + result.RunID + "/assurance-report-v1.sarif",
				"reports/runs/" + result.RunID + "/assurance-report-v1.junit.xml",
			} {
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

func TestPlainUIWritesDeterministicProgressImmediately(t *testing.T) {
	root := t.TempDir()
	var progress bytes.Buffer
	service := app.Service{
		Root: root, Progress: &progress,
		Run: func(_ context.Context, options assure.Options) (report.Report, error) {
			if options.Progress == nil {
				t.Fatal("plain UI disabled progress")
			}
			options.Progress(assure.Event{Kind: "snapshot", Detail: "captured"})
			if got, want := progress.String(), "goatest: snapshot           captured\n"; got != want {
				t.Fatalf("progress before runner returned = %q, want %q", got, want)
			}
			return report.Report{Schema: report.SchemaV1, Verdict: report.VerdictAssured}, nil
		},
	}
	if _, err := service.Execute(t.Context(), cli.CommandVerify, cli.Request{UI: cli.UIPlain}, ""); err != nil {
		t.Fatal(err)
	}
}

func TestCacheHitProvenanceDoesNotDependOnAProgressWriter(t *testing.T) {
	service := app.Service{
		Root: t.TempDir(),
		Run: func(_ context.Context, options assure.Options) (report.Report, error) {
			if options.Progress == nil {
				t.Fatal("cache provenance callback is nil")
			}
			options.Progress(assure.Event{Kind: "cache-hit", Detail: "snapshot-cache"})
			return report.Report{
				Schema: report.SchemaV1, Verdict: report.VerdictAssured, Contract: "standard-v1",
				Snapshot: "snapshot-cache", RunID: "source-run",
			}, nil
		},
	}
	result, err := service.Execute(t.Context(), cli.CommandVerify, cli.Request{}, "")
	if err != nil || !result.Cache.Derived || result.Cache.SourceRunID != "source-run" {
		t.Fatalf("cache provenance = (%+v, %v)", result.Cache, err)
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
	if _, err := service.Execute(t.Context(), cli.CommandVerify, cli.Request{UI: cli.UIPlain}, ""); err != nil {
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
		Schema: report.SchemaV1, RunID: "identity-fixture", RunKind: report.RunFull,
		Verdict: report.VerdictInsufficient, Contract: "standard-v1", Snapshot: "snapshot-a",
		Scope: report.Scope{
			Requested: report.ScopeSpec{Kind: "full", Project: "."},
			Resolved:  report.ScopeSpec{Kind: "full", Project: "."},
		},
		Repository:    report.Repository{Module: "example.test/fixture", Git: report.Git{Available: true, Commit: "commit", MergeBase: "commit"}},
		Configuration: report.Configuration{Digest: strings.Repeat("a", 64)},
		Toolchain:     report.Toolchain{Go: "go1.26.6", Goatest: "devel", GoMutants: "v0.1.2", OS: "windows", Arch: "amd64"},
		Timing:        report.Timing{StartedAt: "2026-01-01T00:00:00Z", FinishedAt: "2026-01-01T00:00:01Z", DurationMS: 1000},
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
			if !options.NoApply || options.ReplayFindingID != "finding-b" || options.ReplayMutantID != "mutant-b" ||
				options.Now == nil || !options.Now().Equal(time.Date(2026, 8, 28, 0, 0, 0, 0, time.UTC)) {
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
	accepted, err := service.Execute(t.Context(), cli.CommandAccept, cli.Request{
		Reason: " reviewed boundary ", Expires: "2026-09-30T00:00:00Z", Owner: " alice ", Ticket: " GAP-42 ",
	}, "finding-a")
	if err != nil || accepted.Verdict != report.VerdictCompleted {
		t.Fatalf("accept = %+v, %v", accepted, err)
	}
	loaded, err := config.Load(root)
	if err != nil || len(loaded.Acceptance) != 1 || loaded.Acceptance[0].ID != "finding-a" ||
		loaded.Acceptance[0].Reason != "reviewed boundary" || loaded.Acceptance[0].Owner != "alice" || loaded.Acceptance[0].Ticket != "GAP-42" ||
		!loaded.Acceptance[0].Expires.Equal(time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("config = %+v, %v", loaded, err)
	}
	replayed, err := service.Execute(t.Context(), cli.CommandReplay, cli.Request{}, "finding-b")
	if err != nil || replayed.Verdict != report.VerdictReproduced {
		t.Fatalf("replay = %+v, %v", replayed, err)
	}
	if _, err := service.Execute(t.Context(), cli.CommandExplain, cli.Request{}, "missing"); err == nil {
		t.Fatal("missing finding was accepted")
	}
}

func TestReplayRejectsFindingWithoutMutantIdentityBeforeRunner(t *testing.T) {
	root := t.TempDir()
	writeLatestFixtureWithMutant(t, root, "")
	runnerCalled := false
	service := app.Service{Root: root, Run: func(context.Context, assure.Options) (report.Report, error) {
		runnerCalled = true
		return report.Report{}, nil
	}}
	result, err := service.Execute(t.Context(), cli.CommandReplay, cli.Request{}, "finding-a")
	if err == nil || !strings.Contains(err.Error(), "no mutant identity") || runnerCalled || result.Verdict != "" {
		t.Fatalf("non-mutation replay = %+v, %v runnerCalled=%t", result, err, runnerCalled)
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
		{name: "schema", content: report.JSON(report.Report{Schema: "future-report", Verdict: report.VerdictAssured}), write: true, want: "unsupported"},
		{name: "incomplete-audit", content: valid, write: true, want: "invalid latest report"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			root := t.TempDir()
			if testCase.write {
				path := filepath.Join(root, ".goatest", "latest-any.json")
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

func TestHistoricalReportLoadErrorsIdentifyTheRequestedRun(t *testing.T) {
	_, err := (app.Service{Root: t.TempDir()}).Execute(t.Context(), cli.CommandReport, cli.Request{ReportRunID: "missing-run"}, "")
	if err == nil || !strings.Contains(err.Error(), `read report run "missing-run"`) || strings.Contains(err.Error(), "latest report") {
		t.Fatalf("historical report error = %v", err)
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
		if _, err := (app.Service{Root: root}).Execute(t.Context(), cli.CommandAccept, cli.Request{
			Reason: "reviewed", Expires: "2026-12-01T00:00:00Z",
		}, "finding-a"); err == nil {
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
			path := filepath.Join(root, ".goatest", "latest-any.json")
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
	writeLatestFixtureWithMutant(t, root, "mutant-a")
}

func writeLatestFixtureWithMutant(t *testing.T, root, mutantID string) {
	t.Helper()
	input := report.Report{
		Schema: report.SchemaV1, RunID: "fixture-run", RunKind: report.RunFull,
		Verdict: report.VerdictInsufficient, Contract: "standard-v1", Snapshot: "snapshot",
		Scope: report.Scope{
			Requested: report.ScopeSpec{Kind: "full", Project: "."},
			Resolved:  report.ScopeSpec{Kind: "full", Project: "."},
		},
		Repository:    report.Repository{Module: "example.test/fixture", Git: report.Git{Available: true, Commit: "commit", MergeBase: "commit"}},
		Configuration: report.Configuration{Digest: strings.Repeat("a", 64)},
		Toolchain:     report.Toolchain{Go: "go1.26.6", Goatest: "devel", GoMutants: "v0.1.2", OS: "windows", Arch: "amd64"},
		Timing:        report.Timing{StartedAt: "2026-01-01T00:00:00Z", FinishedAt: "2026-01-01T00:00:01Z", DurationMS: 1000},
		Findings:      []report.Finding{{ID: "finding-a", Kind: "survivor", Summary: "survived", MutantID: mutantID}},
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
	if err != nil || result.Verdict != report.VerdictCompleted {
		t.Fatalf("init = %+v, %v", result, err)
	}
	if _, err := config.Load(root); err != nil {
		t.Fatal(err)
	}
	// The report guides a fresh project onward: the directories runs will
	// create, and the commands that come next.
	steps := make(map[string]string)
	for _, evidence := range result.Evidence {
		if evidence.Kind == "next-step" {
			steps[evidence.ID] = evidence.Detail
		}
	}
	if len(steps) != 3 || !strings.Contains(steps["gitignore"], ".goatest/") || !strings.Contains(steps["gitignore"], "reports/") ||
		!strings.Contains(steps["doctor"], "goatest doctor") || !strings.Contains(steps["verify"], "goatest verify ./...") {
		t.Fatalf("next steps = %+v", steps)
	}
}

// A jsonl UI streams one progress event per note to the output stream the
// final report event will follow on, and leaves the plain progress stream
// silent: one pipe carries the whole stream.
func TestJSONLUIStreamsProgressEventsToTheOutput(t *testing.T) {
	var output, progress bytes.Buffer
	service := app.Service{
		Root: t.TempDir(), Progress: &progress, Output: &output,
		Run: func(_ context.Context, options assure.Options) (report.Report, error) {
			options.Progress(assure.Event{Kind: "snapshot", Detail: "captured"})
			options.Progress(assure.Event{Kind: "mutation-progress", Detail: "3/9"})
			return report.Report{Schema: report.SchemaV1, Verdict: report.VerdictAssured}, nil
		},
	}
	if _, err := service.Execute(t.Context(), cli.CommandVerify, cli.Request{UI: cli.UIJSONL}, ""); err != nil {
		t.Fatal(err)
	}
	if progress.Len() != 0 {
		t.Fatalf("jsonl leaked plain progress: %q", progress.String())
	}
	lines := strings.Split(strings.TrimSuffix(output.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("stream = %q", output.String())
	}
	for index, want := range []struct{ kind, detail string }{{"snapshot", "captured"}, {"mutation-progress", "3/9"}} {
		var event struct {
			Type      string `json:"type"`
			Kind      string `json:"kind"`
			Detail    string `json:"detail"`
			ElapsedMS int64  `json:"elapsed_ms"`
		}
		if err := json.Unmarshal([]byte(lines[index]), &event); err != nil {
			t.Fatalf("line %d = %q: %v", index, lines[index], err)
		}
		if event.Type != "progress" || event.Kind != want.kind || event.Detail != want.detail || event.ElapsedMS < 0 {
			t.Fatalf("line %d = %+v, want %+v", index, event, want)
		}
	}
}

// A note the trace layer reports before the run - here a trace directory the
// snapshot would read as source - reaches the renderer the request selected.
func TestTraceUnavailableReachesTheSelectedUI(t *testing.T) {
	var output bytes.Buffer
	service := app.Service{
		Root: t.TempDir(), Output: &output,
		Run: func(context.Context, assure.Options) (report.Report, error) {
			return report.Report{Schema: report.SchemaV1, Verdict: report.VerdictAssured}, nil
		},
	}
	request := cli.Request{UI: cli.UIJSONL, Trace: true, TraceDirectory: "traces-inside-repository"}
	if _, err := service.Execute(t.Context(), cli.CommandVerify, request, ""); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), `"kind":"trace-unavailable"`) {
		t.Fatalf("trace note missed the jsonl stream: %q", output.String())
	}
}

// Without an output stream a jsonl request falls back to deterministic plain
// lines rather than guessing where the stream went.
func TestJSONLWithoutAnOutputWriterFallsBackToPlain(t *testing.T) {
	var progress bytes.Buffer
	service := app.Service{
		Root: t.TempDir(), Progress: &progress,
		Run: func(_ context.Context, options assure.Options) (report.Report, error) {
			options.Progress(assure.Event{Kind: "snapshot", Detail: "captured"})
			return report.Report{Schema: report.SchemaV1, Verdict: report.VerdictAssured}, nil
		},
	}
	if _, err := service.Execute(t.Context(), cli.CommandVerify, cli.Request{UI: cli.UIJSONL}, ""); err != nil {
		t.Fatal(err)
	}
	if got, want := progress.String(), "goatest: snapshot           captured\n"; got != want {
		t.Fatalf("progress = %q, want %q", got, want)
	}
}

// The auto UI renders plain lines wherever no composition root probed an
// interactive terminal: the zero value of Interactive can never start a
// dashboard, and neither can a probe that answered no.
func TestAutoUIWithoutATerminalRendersPlainLines(t *testing.T) {
	for name, interactive := range map[string]func(io.Writer) bool{
		"zero-value": nil,
		"probed-no":  func(io.Writer) bool { return false },
	} {
		var progress bytes.Buffer
		service := app.Service{
			Root: t.TempDir(), Progress: &progress, Interactive: interactive,
			Run: func(_ context.Context, options assure.Options) (report.Report, error) {
				options.Progress(assure.Event{Kind: "snapshot", Detail: "captured"})
				return report.Report{Schema: report.SchemaV1, Verdict: report.VerdictAssured}, nil
			},
		}
		if _, err := service.Execute(t.Context(), cli.CommandVerify, cli.Request{UI: cli.UIAuto}, ""); err != nil {
			t.Fatal(err)
		}
		if got, want := progress.String(), "goatest: snapshot           captured\n"; got != want {
			t.Fatalf("%s: progress = %q, want %q", name, got, want)
		}
	}
}

// On an interactive terminal the auto UI renders the in-place dashboard, and
// closing the run erases the status line.
func TestAutoUIRendersTheDashboardOnAnInteractiveTerminal(t *testing.T) {
	var progress lockedProgressBuffer
	service := app.Service{
		Root: t.TempDir(), Progress: &progress,
		Interactive: func(io.Writer) bool { return true },
		Run: func(_ context.Context, options assure.Options) (report.Report, error) {
			options.Progress(assure.Event{Kind: "baseline-target", Detail: "internal/report:TestLines"})
			return report.Report{Schema: report.SchemaV1, Verdict: report.VerdictAssured}, nil
		},
	}
	if _, err := service.Execute(t.Context(), cli.CommandVerify, cli.Request{UI: cli.UIAuto}, ""); err != nil {
		t.Fatal(err)
	}
	got := progress.String()
	if !strings.Contains(got, "\r\x1b[K") || !strings.Contains(got, "baseline") || !strings.Contains(got, "internal/report:TestLines") {
		t.Fatalf("dashboard frame missing: %q", got)
	}
	if !strings.HasSuffix(got, "\r\x1b[K") {
		t.Fatalf("dashboard was not erased on close: %q", got)
	}
}

// lockedProgressBuffer synchronizes writes, because a dashboard redraws from
// its own tick goroutine.
type lockedProgressBuffer struct {
	mutex  sync.Mutex
	buffer bytes.Buffer
}

func (buffer *lockedProgressBuffer) Write(data []byte) (int, error) {
	buffer.mutex.Lock()
	defer buffer.mutex.Unlock()
	return buffer.buffer.Write(data)
}

func (buffer *lockedProgressBuffer) String() string {
	buffer.mutex.Lock()
	defer buffer.mutex.Unlock()
	return buffer.buffer.String()
}
