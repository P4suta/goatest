// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package app_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/P4suta/goatest/internal/app"
	"github.com/P4suta/goatest/internal/assure"
	"github.com/P4suta/goatest/internal/cli"
	"github.com/P4suta/goatest/internal/report"
	"github.com/P4suta/goatest/internal/trace"
)

// diagnosticsBundle returns the one bundle a failed run left under the
// repository. A bundle belongs to a run, so a repository that saw one failure
// holds exactly one.
func diagnosticsBundle(t *testing.T, root string) string {
	t.Helper()
	directory := filepath.Join(root, ".goatest", "diagnostics")
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("%s holds %d bundles, want the one the failed run left", directory, len(entries))
	}
	return filepath.Join(directory, entries[0].Name())
}

// bundleFile reads one file of a bundle, failing the test when the bundle does
// not hold it.
func bundleFile(t *testing.T, bundle, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(bundle, name))
	if err != nil {
		t.Fatalf("the bundle holds no %s: %v", name, err)
	}
	return string(data)
}

func TestAFailedRunLeavesABundleOfWhatItKnew(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	scratch := filepath.Join(t.TempDir(), "baseline-scratch")
	sentinel := errors.New("mutation workspace failed")
	var progress bytes.Buffer
	service := app.Service{
		Root: root, Progress: &progress,
		Environment: []string{"GOATEST_DIAGNOSTICS_SECRET=super-secret-value", "PATH=/usr/bin"},
		Run: func(_ context.Context, options assure.Options) (report.Report, error) {
			options.Trace.Progress("snapshot", "captured")
			options.Trace.Artifact("baseline-scratch", scratch)
			return report.Report{}, fmt.Errorf("goatest: assurance run: %w", sentinel)
		},
	}
	result, err := service.Execute(t.Context(), cli.CommandVerify, cli.Request{}, "")
	// A bundle is diagnostic exhaust. It answers for nothing the run decided:
	// the error the run stopped on and the report it wrote are what they were.
	if !errors.Is(err, sentinel) || result.Verdict != report.VerdictError {
		t.Fatalf("verify = %+v, %v", result, err)
	}
	if _, statErr := os.Stat(filepath.Join(root, "reports", "runs", result.RunID, "assurance-report-v1.json")); statErr != nil {
		t.Fatal(statErr)
	}

	bundle := diagnosticsBundle(t, root)
	if filepath.Base(bundle) != result.RunID {
		t.Fatalf("bundle %s is not named for run %s", bundle, result.RunID)
	}

	// The recording of the run that failed, in the shape every recording has:
	// the first event the run opened with, and the verdict it ended on.
	events := readTrace(t, bundle)
	if len(events) < 3 || events[0].Type != trace.TypeRunStart {
		t.Fatalf("bundled recording = %+v", events)
	}
	last := events[len(events)-1]
	if last.Type != trace.TypeRunEnd || last.Run == nil || last.Run.Verdict != string(report.VerdictError) {
		t.Fatalf("bundled run-end = %+v", last)
	}
	if !strings.Contains(last.Run.Error, sentinel.Error()) {
		t.Fatalf("bundled run-end error = %q, want the error that ended the run", last.Run.Error)
	}

	// The error, chain and all, because the message a wrapper shows is rarely
	// the one that explains the failure.
	failure := bundleFile(t, bundle, "error.txt")
	if !strings.Contains(failure, sentinel.Error()) || !strings.Contains(failure, "goatest: assurance run") {
		t.Fatalf("error.txt = %q", failure)
	}
	if !strings.Contains(failure, result.RunID) {
		t.Fatalf("error.txt names no run: %q", failure)
	}

	// The environment a run could see, named and never quoted, so that a bundle
	// is safe to attach to a bug report from a machine holding real
	// credentials.
	environment := bundleFile(t, bundle, "environment.txt")
	if strings.Contains(environment, "super-secret-value") {
		t.Fatalf("environment.txt holds the value of a variable: %q", environment)
	}
	if !strings.Contains(environment, "GOATEST_DIAGNOSTICS_SECRET") || !strings.Contains(environment, "PATH") {
		t.Fatalf("environment.txt names no variable: %q", environment)
	}
	if !strings.Contains(environment, assure.GoatestVersion) {
		t.Fatalf("environment.txt names no toolchain: %q", environment)
	}
	_, names, listed := strings.Cut(environment, "environment variable names")
	if !listed {
		t.Fatalf("environment.txt lists no names: %q", environment)
	}
	for _, line := range strings.Split(names, "\n") {
		if strings.Contains(line, "=") {
			t.Fatalf("environment.txt lists a variable as a pair: %q", line)
		}
	}

	// What the run left on the disk, which is the part of a failure a bundle
	// cannot hold itself.
	preserved := bundleFile(t, bundle, "preserved-paths.txt")
	if !strings.Contains(preserved, scratch) || !strings.Contains(preserved, "baseline-scratch") {
		t.Fatalf("preserved-paths.txt = %q", preserved)
	}

	// A bundle nobody is told about is a bundle nobody reads.
	note := progress.String()
	if !strings.Contains(note, "diagnostics") || !strings.Contains(note, bundle) {
		t.Fatalf("progress = %q", note)
	}
	if strings.Count(note, "\n") != 1 {
		t.Fatalf("the bundle was announced in more than one line: %q", note)
	}
}

func TestABundleThatCannotBeWrittenWarnsAndLeavesTheRunAlone(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("mutation workspace failed")
	for _, testCase := range []struct {
		name  string
		hooks app.DiagnosticsFilesystem
		want  string
	}{
		{
			name:  "no-directory",
			hooks: app.DiagnosticsFilesystem{MkdirAll: func(string, fs.FileMode) error { return errors.New("permission denied") }},
			want:  "permission denied",
		},
		{
			name: "no-file",
			hooks: app.DiagnosticsFilesystem{
				WriteFile: func(string, []byte, fs.FileMode) error { return errors.New("no space left on device") },
			},
			want: "no space left on device",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			var progress bytes.Buffer
			service := app.Service{
				Root: root, Progress: &progress, DiagnosticsFilesystem: testCase.hooks,
				Run: func(context.Context, assure.Options) (report.Report, error) {
					return report.Report{}, sentinel
				},
			}
			result, err := service.Execute(t.Context(), cli.CommandVerify, cli.Request{}, "")
			// The bundle is the last thing a failed run does and the least of
			// what it owes: losing it costs the run neither its error nor the
			// report that records it.
			if !errors.Is(err, sentinel) || result.Verdict != report.VerdictError {
				t.Fatalf("verify = %+v, %v", result, err)
			}
			if _, statErr := os.Stat(filepath.Join(root, "reports", "runs", result.RunID, "assurance-report-v1.json")); statErr != nil {
				t.Fatal(statErr)
			}
			warning := progress.String()
			if !strings.Contains(warning, "diagnostics-unavailable") || !strings.Contains(warning, testCase.want) {
				t.Fatalf("warning = %q", warning)
			}
			// A bundle that was never written is not announced as one that was.
			if strings.Contains(warning, "written to") {
				t.Fatalf("a bundle nothing was written into was announced: %q", warning)
			}
			if strings.Count(warning, "\n") != 1 {
				t.Fatalf("warning is not one line: %q", warning)
			}
		})
	}
}

func TestARunThatDidNotFailLeavesNoBundle(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name   string
		runErr error
	}{
		// Nothing to diagnose.
		{name: "assured"},
		// A run the developer stopped is not a failure, and a bundle written
		// while a process is shutting down is litter rather than evidence.
		{name: "interrupted", runErr: context.Canceled},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			var progress bytes.Buffer
			service := app.Service{
				Root: root, Progress: &progress,
				Run: func(context.Context, assure.Options) (report.Report, error) {
					if testCase.runErr != nil {
						return report.Report{}, testCase.runErr
					}
					return report.Report{Schema: report.SchemaV1, Verdict: report.VerdictAssured, Contract: "standard-v1"}, nil
				},
			}
			if _, err := service.Execute(t.Context(), cli.CommandVerify, cli.Request{}, ""); !errors.Is(err, testCase.runErr) {
				t.Fatalf("verify error = %v, want %v", err, testCase.runErr)
			}
			if _, err := os.Stat(filepath.Join(root, ".goatest", "diagnostics")); !os.IsNotExist(err) {
				t.Fatalf("a run that did not fail left a bundle: %v", err)
			}
			if strings.Contains(progress.String(), "diagnostics") {
				t.Fatalf("progress = %q", progress.String())
			}
		})
	}
}

func TestABundleOfATracedRunNamesTheRecordingRatherThanCopyingIt(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	directory := filepath.Join(t.TempDir(), "trace")
	sentinel := errors.New("mutation workspace failed")
	service := app.Service{
		Root: root,
		Run: func(context.Context, assure.Options) (report.Report, error) {
			return report.Report{}, sentinel
		},
	}
	if _, err := service.Execute(t.Context(), cli.CommandVerify, cli.Request{Trace: true, TraceDirectory: directory}, ""); !errors.Is(err, sentinel) {
		t.Fatalf("verify error = %v", err)
	}

	// A traced run wrote its recording in full, output and all. Copying it into
	// the bundle would duplicate what the developer already has and truncate
	// what they asked for; naming it costs nothing and loses nothing.
	bundle := diagnosticsBundle(t, root)
	if _, err := os.Stat(filepath.Join(bundle, trace.FileName)); !os.IsNotExist(err) {
		t.Fatalf("the bundle of a traced run holds a second copy of the recording: %v", err)
	}
	preserved := bundleFile(t, bundle, "preserved-paths.txt")
	if recorded := traceRun(t, directory); !strings.Contains(preserved, recorded) {
		t.Fatalf("preserved-paths.txt = %q, want the recording at %s", preserved, recorded)
	}
	if failure := bundleFile(t, bundle, "error.txt"); !strings.Contains(failure, sentinel.Error()) {
		t.Fatalf("error.txt = %q", failure)
	}
	if environment := bundleFile(t, bundle, "environment.txt"); environment == "" {
		t.Fatal("environment.txt is empty")
	}
}
