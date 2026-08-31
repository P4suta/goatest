// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package app_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/P4suta/goatest/internal/app"
	"github.com/P4suta/goatest/internal/assure"
	"github.com/P4suta/goatest/internal/cli"
	"github.com/P4suta/goatest/internal/report"
	"github.com/P4suta/goatest/internal/trace"
)

// readTrace decodes the recorded stream of a trace directory, oldest event
// first, failing the test if the stream is absent or holds a line no reader of
// the contract could decode.
func readTrace(t *testing.T, directory string) []trace.Event {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(directory, trace.FileName))
	if err != nil {
		t.Fatal(err)
	}
	var events []trace.Event
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 0, 64*1024), 8<<20)
	for scanner.Scan() {
		var event trace.Event
		decoder := json.NewDecoder(bytes.NewReader(scanner.Bytes()))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&event); err != nil {
			t.Fatalf("trace line %q: %v", scanner.Text(), err)
		}
		events = append(events, event)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return events
}

// traceRun returns the run directory a traced run wrote under a trace root.
// A root collects the recordings of the runs written into it, one directory
// each, so a test that asked for one trace finds exactly one.
func traceRun(t *testing.T, root string) string {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	var runs []string
	for _, entry := range entries {
		if entry.IsDir() {
			runs = append(runs, filepath.Join(root, entry.Name()))
		}
	}
	if len(runs) != 1 {
		t.Fatalf("trace root %s holds %d recordings, want one", root, len(runs))
	}
	return runs[0]
}

// traceOfType returns the events of one type, in the order they were recorded.
func traceOfType(events []trace.Event, kind string) []trace.Event {
	var selected []trace.Event
	for _, event := range events {
		if event.Type == kind {
			selected = append(selected, event)
		}
	}
	return selected
}

func TestTraceRequestRecordsTheRunAndClosesItWithItsVerdict(t *testing.T) {
	t.Parallel()
	directory := filepath.Join(t.TempDir(), "trace")
	var recorded *trace.Recorder
	service := app.Service{
		Root: t.TempDir(),
		Run: func(_ context.Context, options assure.Options) (report.Report, error) {
			recorded = options.Trace
			// A run forwards its own notes to the recorder it was handed,
			// beside the callback it answers.
			options.Progress(assure.Event{Kind: "snapshot", Detail: "captured"})
			options.Trace.Progress("snapshot", "captured")
			return report.Report{Schema: report.SchemaV1, Verdict: report.VerdictAssured, Contract: "standard-v1", Snapshot: "snapshot-a"}, nil
		},
	}
	result, err := service.Execute(t.Context(), cli.CommandVerify, cli.Request{Trace: true, TraceDirectory: directory}, "")
	if err != nil || result.Verdict != report.VerdictAssured {
		t.Fatalf("verify = %+v, %v", result, err)
	}
	if recorded == nil {
		t.Fatal("the run was handed no recorder")
	}

	events := readTrace(t, traceRun(t, directory))
	if len(events) < 3 {
		t.Fatalf("recorded events = %+v", events)
	}
	first, last := events[0], events[len(events)-1]
	if first.Type != trace.TypeRunStart || first.Schema != trace.SchemaV1 {
		t.Fatalf("first event = %+v", first)
	}
	if last.Type != trace.TypeRunEnd || last.Run == nil {
		t.Fatalf("last event = %+v", last)
	}
	if last.Run.Verdict != string(report.VerdictAssured) || last.Run.Error != "" {
		t.Fatalf("run-end = %+v", last.Run)
	}
	if last.Run.EventsDropped != 0 || last.Run.EventsEmitted != int64(len(events))-1 {
		t.Fatalf("accounting = %+v of %d events", last.Run, len(events))
	}
	progress := traceOfType(events, trace.TypeProgress)
	if len(progress) != 1 || progress[0].Progress.Kind != "snapshot" || progress[0].Progress.Detail != "captured" {
		t.Fatalf("progress events = %+v", progress)
	}
	for index, event := range events {
		if event.Seq != int64(index)+1 {
			t.Fatalf("event %d has sequence %d", index, event.Seq)
		}
	}
}

func TestTraceRecordsTheErrorThatEndedTheRun(t *testing.T) {
	t.Parallel()
	directory := filepath.Join(t.TempDir(), "trace")
	sentinel := errors.New("mutation workspace failed")
	service := app.Service{
		Root: t.TempDir(),
		Run: func(context.Context, assure.Options) (report.Report, error) {
			return report.Report{}, sentinel
		},
	}
	if _, err := service.Execute(t.Context(), cli.CommandVerify, cli.Request{Trace: true, TraceDirectory: directory}, ""); !errors.Is(err, sentinel) {
		t.Fatalf("verify error = %v", err)
	}
	events := readTrace(t, traceRun(t, directory))
	last := events[len(events)-1]
	if last.Type != trace.TypeRunEnd || last.Run == nil || last.Run.Error != sentinel.Error() {
		t.Fatalf("run-end = %+v", last)
	}
}

func TestARunThatReachedNoVerdictIsTracedWithHowItEnded(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name    string
		runErr  error
		verdict string
	}{
		{name: "canceled", runErr: context.Canceled, verdict: "INTERRUPTED"},
		{name: "deadline", runErr: context.DeadlineExceeded, verdict: "INTERRUPTED"},
		{name: "failed", runErr: errors.New("mutation workspace failed"), verdict: string(report.VerdictError)},
		{name: "silent", verdict: "UNKNOWN"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			directory := filepath.Join(t.TempDir(), "trace")
			service := app.Service{
				Root: t.TempDir(),
				Run: func(context.Context, assure.Options) (report.Report, error) {
					// A runner that stops early answers with the report it had,
					// which on the interrupted path is no report at all.
					return report.Report{}, testCase.runErr
				},
			}
			_, err := service.Execute(t.Context(), cli.CommandVerify, cli.Request{Trace: true, TraceDirectory: directory}, "")
			if testCase.runErr != nil && !errors.Is(err, testCase.runErr) {
				t.Fatalf("verify error = %v, want %v", err, testCase.runErr)
			}

			events := readTrace(t, traceRun(t, directory))
			last := events[len(events)-1]
			if last.Type != trace.TypeRunEnd || last.Run == nil {
				t.Fatalf("last event = %+v, want a run-end", last)
			}
			// A recording says how the run it recorded ended. An empty verdict
			// says nothing, and an interrupted run leaves no report to say it
			// elsewhere.
			if last.Run.Verdict != testCase.verdict {
				t.Fatalf("run-end verdict = %q, want %q", last.Run.Verdict, testCase.verdict)
			}
			if testCase.runErr != nil && last.Run.Error != testCase.runErr.Error() {
				t.Fatalf("run-end error = %q, want %q", last.Run.Error, testCase.runErr)
			}
		})
	}
}

func TestUnrequestedTraceRecordsNothingAtAll(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	traced := true
	service := app.Service{
		Root: root,
		Run: func(_ context.Context, options assure.Options) (report.Report, error) {
			traced = options.Trace != nil
			return report.Report{Schema: report.SchemaV1, Verdict: report.VerdictAssured}, nil
		},
	}
	if _, err := service.Execute(t.Context(), cli.CommandVerify, cli.Request{}, ""); err != nil {
		t.Fatal(err)
	}
	if traced {
		t.Fatal("an unrequested trace handed the run a recorder")
	}
	if _, err := os.Stat(filepath.Join(root, ".goatest", "trace")); !os.IsNotExist(err) {
		t.Fatalf("an unrequested trace created a directory: %v", err)
	}
}

func TestDefaultTraceDirectoryIsNamedForTheMomentAndTheProcess(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	service := app.Service{
		Root:      root,
		Now:       func() time.Time { return time.Date(2026, 9, 1, 10, 11, 12, 0, time.UTC) },
		ProcessID: func() int { return 4242 },
		Run: func(context.Context, assure.Options) (report.Report, error) {
			return report.Report{Schema: report.SchemaV1, Verdict: report.VerdictAssured}, nil
		},
	}
	if _, err := service.Execute(t.Context(), cli.CommandVerify, cli.Request{Trace: true}, ""); err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(root, ".goatest", "trace", "20260901T101112Z-4242")
	if events := readTrace(t, directory); len(events) < 2 || events[0].Type != trace.TypeRunStart {
		t.Fatalf("default trace directory %s recorded %+v", directory, events)
	}
}

func TestATraceThatCannotBeWrittenWarnsAndLeavesTheRunAlone(t *testing.T) {
	t.Parallel()
	blocked := filepath.Join(t.TempDir(), "occupied")
	if err := os.WriteFile(blocked, []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, testCase := range []struct {
		name      string
		directory func(root string) string
		want      string
	}{
		{name: "unopenable", directory: func(string) string { return filepath.Join(blocked, "trace") }, want: "trace directory"},
		{name: "inside-repository", directory: func(root string) string { return filepath.Join(root, "trace") }, want: "inside the repository"},
		{name: "repository-root", directory: func(root string) string { return root }, want: "inside the repository"},
		{name: "relative-inside-repository", directory: func(string) string { return "trace" }, want: "inside the repository"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			var progress bytes.Buffer
			traced := true
			service := app.Service{
				Root: root, Progress: &progress,
				Run: func(_ context.Context, options assure.Options) (report.Report, error) {
					traced = options.Trace != nil
					return report.Report{Schema: report.SchemaV1, Verdict: report.VerdictAssured, Contract: "standard-v1"}, nil
				},
			}
			result, err := service.Execute(t.Context(), cli.CommandVerify, cli.Request{Trace: true, TraceDirectory: testCase.directory(root)}, "")
			if err != nil || result.Verdict != report.VerdictAssured {
				t.Fatalf("verify = %+v, %v", result, err)
			}
			if traced {
				t.Fatal("a trace that cannot be written handed the run a recorder")
			}
			warning := progress.String()
			if !strings.Contains(warning, "trace-unavailable") || !strings.Contains(warning, testCase.want) {
				t.Fatalf("warning = %q", warning)
			}
			if strings.Count(warning, "\n") != 1 {
				t.Fatalf("warning is not one line: %q", warning)
			}
		})
	}
}

func TestATraceMayBeWrittenUnderTheRepositoryReportDirectory(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	var progress bytes.Buffer
	service := app.Service{
		Root: root, Progress: &progress,
		Run: func(context.Context, assure.Options) (report.Report, error) {
			return report.Report{Schema: report.SchemaV1, Verdict: report.VerdictAssured}, nil
		},
	}
	if _, err := service.Execute(t.Context(), cli.CommandVerify, cli.Request{
		Trace: true, TraceDirectory: filepath.Join(".goatest", "chosen"),
	}, ""); err != nil {
		t.Fatal(err)
	}
	if events := readTrace(t, traceRun(t, filepath.Join(root, ".goatest", "chosen"))); len(events) < 2 {
		t.Fatalf("recorded events = %+v", events)
	}
	if progress.Len() != 0 {
		t.Fatalf("progress = %q", progress.String())
	}
}

func TestASecondRunTracedToOneDirectoryKeepsTheRecordingOfTheFirst(t *testing.T) {
	t.Parallel()
	directory := filepath.Join(t.TempDir(), "traces")
	var progress bytes.Buffer
	moment := time.Date(2026, 9, 1, 10, 11, 12, 0, time.UTC)
	service := app.Service{
		Root: t.TempDir(), Progress: &progress,
		Now:       func() time.Time { return moment },
		ProcessID: func() int { return 4242 },
		Run: func(_ context.Context, options assure.Options) (report.Report, error) {
			options.Trace.Progress("snapshot", moment.Format(time.RFC3339))
			return report.Report{Schema: report.SchemaV1, Verdict: report.VerdictAssured}, nil
		},
	}
	// The same directory every time is what a developer does with --trace=DIR,
	// and each run of it is a recording of its own: the second must not append
	// to the stream of the first, whose events number from one and whose
	// preserved output is named after those numbers.
	for range 2 {
		if _, err := service.Execute(t.Context(), cli.CommandVerify, cli.Request{Trace: true, TraceDirectory: directory}, ""); err != nil {
			t.Fatal(err)
		}
		moment = moment.Add(time.Minute)
	}

	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("%s holds %d recordings, want one for each run", directory, len(entries))
	}
	for _, entry := range entries {
		events := readTrace(t, filepath.Join(directory, entry.Name()))
		if len(events) < 3 || events[0].Type != trace.TypeRunStart || events[0].Seq != 1 {
			t.Fatalf("recording %s = %+v", entry.Name(), events)
		}
		last := events[len(events)-1]
		if last.Type != trace.TypeRunEnd || last.Run == nil || last.Run.EventsDropped != 0 {
			t.Fatalf("recording %s ended with %+v", entry.Name(), last)
		}
	}
	if progress.Len() != 0 {
		t.Fatalf("progress = %q", progress.String())
	}
}
