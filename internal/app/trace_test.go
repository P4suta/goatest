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

	events := readTrace(t, directory)
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
	events := readTrace(t, directory)
	last := events[len(events)-1]
	if last.Type != trace.TypeRunEnd || last.Run == nil || last.Run.Error != sentinel.Error() {
		t.Fatalf("run-end = %+v", last)
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
	if events := readTrace(t, filepath.Join(root, ".goatest", "chosen")); len(events) < 2 {
		t.Fatalf("recorded events = %+v", events)
	}
	if progress.Len() != 0 {
		t.Fatalf("progress = %q", progress.String())
	}
}
