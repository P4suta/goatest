// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package app

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/P4suta/goatest/internal/cli"
	"github.com/P4suta/goatest/internal/report"
	"github.com/P4suta/goatest/internal/trace"
)

func recordedExec(t *testing.T, events []trace.Event) trace.ExecRecord {
	t.Helper()
	var found []trace.ExecRecord
	for _, event := range events {
		if event.Type == trace.TypeExec && event.Exec != nil {
			found = append(found, *event.Exec)
		}
	}
	if len(found) != 1 {
		t.Fatalf("recorded %d exec events, want one: %+v", len(found), events)
	}
	return found[0]
}

func TestARunNobodyAskedToTraceRecordsIntoMemory(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	recording, finish := Service{Root: root}.startTrace(root, cli.Request{})
	if recording.recorder == nil {
		t.Fatal("a run nobody asked to trace was handed no recorder")
	}
	if recording.directory != "" {
		t.Fatalf("recording directory = %q, want a recording no file is written for", recording.directory)
	}
	recording.recorder.Progress("snapshot", "captured")
	finish(report.Report{Verdict: report.VerdictAssured}, nil)

	events := recording.Events()
	const inMemoryLifecycleEvents = 3
	if len(events) != inMemoryLifecycleEvents {
		t.Fatalf("kept events = %+v, want the run-start, the note, and the run-end", events)
	}
	if events[0].Type != trace.TypeRunStart || events[0].Schema != trace.SchemaV1 {
		t.Fatalf("first event = %+v", events[0])
	}
	if events[1].Type != trace.TypeProgress || events[1].Progress.Kind != "snapshot" {
		t.Fatalf("second event = %+v", events[1])
	}
	last := events[2]
	if last.Type != trace.TypeRunEnd || last.Run == nil {
		t.Fatalf("last event = %+v", last)
	}
	if last.Run.Verdict != string(report.VerdictAssured) || last.Run.EventsDropped != 0 {
		t.Fatalf("run-end = %+v", last.Run)
	}

	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("an unrequested recording wrote %d entries into the repository", len(entries))
	}
}

func TestARequestedTraceRecordsToItsDirectoryRatherThanMemory(t *testing.T) {
	t.Parallel()
	directory := filepath.Join(t.TempDir(), "trace")
	recording, finish := Service{}.startTrace(t.TempDir(), cli.Request{Trace: true, TraceDirectory: directory})
	if recording.directory == "" {
		t.Fatal("a requested trace recorded to no directory")
	}

	if events := recording.Events(); len(events) != 0 {
		t.Fatalf("a requested trace kept %d events in memory as well", len(events))
	}
	finish(report.Report{Verdict: report.VerdictAssured}, nil)

	stream, err := os.ReadFile(filepath.Join(recording.directory, trace.FileName))
	if err != nil {
		t.Fatal(err)
	}
	const persistedLifecycleEvents = 2
	if lines := bytes.Count(stream, []byte("\n")); lines != persistedLifecycleEvents {
		t.Fatalf("recorded stream holds %d lines, want the run-start and the run-end:\n%s", lines, stream)
	}
}

func TestTheRecordingInMemoryKeepsTheDigestOfACapturedOutputAndNotItsBytes(t *testing.T) {
	t.Parallel()
	output := []byte("--- FAIL: TestBoundary\n")
	recording, _ := Service{}.startTrace(t.TempDir(), cli.Request{})
	recording.recorder.Exec(trace.ExecRecord{Argv: []string{"go", "test"}, Output: output})

	exec := recordedExec(t, recording.Events())
	if exec.Output != nil {
		t.Fatalf("the recording in memory kept %d bytes of captured output", len(exec.Output))
	}
	if exec.OutputBytes != len(output) || exec.OutputSHA256 == "" {
		t.Fatalf("exec record = %+v, want the size and digest of the whole capture", exec)
	}
}

func TestTheRecordingInMemoryIsBoundedAndSaysWhatItDropped(t *testing.T) {
	t.Parallel()
	recording, finish := Service{}.startTrace(t.TempDir(), cli.Request{})
	for range alwaysOnTraceEvents {
		recording.recorder.Progress("mutant", "executed")
	}
	finish(report.Report{Verdict: report.VerdictAssured}, nil)

	events := recording.Events()
	if len(events) != alwaysOnTraceEvents {
		t.Fatalf("kept %d events, want the %d the ring holds", len(events), alwaysOnTraceEvents)
	}
	last := events[len(events)-1]
	if last.Type != trace.TypeRunEnd || last.Run == nil {
		t.Fatalf("last event = %+v, want the run-end the ring reserves its last slot for", last)
	}

	if last.Run.EventsDropped == 0 {
		t.Fatalf("run-end = %+v, want the events the ring could not keep", last.Run)
	}
	if events[0].Type == trace.TypeRunStart {
		t.Fatalf("first kept event = %+v, want the oldest events to have fallen out", events[0])
	}
}

func TestTheRecordingInMemoryLeavesARecordItHasNothingToShortenAlone(t *testing.T) {
	t.Parallel()
	recording, _ := Service{}.startTrace(t.TempDir(), cli.Request{})

	recording.recorder.Exec(trace.ExecRecord{Argv: []string{"go", "build", "./..."}, Output: []byte{}})

	exec := recordedExec(t, recording.Events())
	if exec.Output == nil {
		t.Fatalf("exec record = %+v, want the empty capture the command produced", exec)
	}
	if exec.OutputBytes != 0 || exec.OutputSHA256 != "" {
		t.Fatalf("exec record = %+v, want no account of an output there was none of", exec)
	}
}

func TestARecordingThatHasEndedKeepsNothingMore(t *testing.T) {
	t.Parallel()
	recording, finish := Service{}.startTrace(t.TempDir(), cli.Request{})
	recording.recorder.Progress("snapshot", "captured")
	finish(report.Report{Verdict: report.VerdictAssured}, nil)
	ended := recording.Events()

	late := trace.Event{Type: trace.TypeProgress, Progress: &trace.ProgressRecord{Kind: "late", Detail: "after the run ended"}}
	if err := recording.sink.Emit(late); err == nil {
		t.Fatal("a recording that had ended accepted another event")
	}
	if kept := recording.Events(); len(kept) != len(ended) {
		t.Fatalf("the recording holds %d events after it ended, want the %d it closed with", len(kept), len(ended))
	}
}
