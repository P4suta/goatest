// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package trace_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/P4suta/goatest/internal/trace"
)

// recordingSink is a scripted sink: it keeps what it was handed and answers
// with the scripted error, so a test can drive the failure paths of the sinks
// that compose it. It deliberately does not report drops, which is how a
// recorder falls back to counting the errors it saw.
type recordingSink struct {
	mutex    sync.Mutex
	events   []trace.Event
	emitErr  error
	closeErr error
	closes   int
}

func (sink *recordingSink) Emit(event trace.Event) error {
	sink.mutex.Lock()
	defer sink.mutex.Unlock()
	sink.events = append(sink.events, event)
	return sink.emitErr
}

func (sink *recordingSink) Close() error {
	sink.mutex.Lock()
	defer sink.mutex.Unlock()
	sink.closes++
	return sink.closeErr
}

func (sink *recordingSink) recorded() []trace.Event {
	sink.mutex.Lock()
	defer sink.mutex.Unlock()
	return append([]trace.Event(nil), sink.events...)
}

// failingFile is a trace file whose writes always fail, the disk the trace
// cannot be written to.
type failingFile struct{ err error }

func (file failingFile) Write([]byte) (int, error) { return 0, file.err }
func (file failingFile) Sync() error               { return nil }
func (file failingFile) Close() error              { return nil }

// sampleEvent is one minimal event, enough to drive a sink.
func sampleEvent(seq int64) trace.Event {
	return trace.Event{
		Seq:       seq,
		Type:      trace.TypeProgress,
		Timestamp: traceOrigin.Format("2006-01-02T15:04:05Z07:00"),
		Progress:  &trace.ProgressRecord{Kind: "mutation-progress", Detail: strconv.FormatInt(seq, 10)},
	}
}

func TestDirSinkCreatesItsDirectoryAndAppendsJSONL(t *testing.T) {
	t.Parallel()
	directory := filepath.Join(t.TempDir(), "trace", "20260102T030405Z-1234")
	sink, err := trace.NewDirSink(directory, trace.Filesystem{})
	if err != nil {
		t.Fatalf("NewDirSink = %v", err)
	}
	for seq := int64(1); seq <= 3; seq++ {
		if err := sink.Emit(sampleEvent(seq)); err != nil {
			t.Fatalf("Emit(%d) = %v", seq, err)
		}
	}
	if err := sink.Close(); err != nil {
		t.Fatalf("Close = %v", err)
	}

	data, err := os.ReadFile(filepath.Join(directory, trace.FileName))
	if err != nil {
		t.Fatal(err)
	}
	lines := jsonLines(t, data)
	if len(lines) != 3 {
		t.Fatalf("trace file holds %d lines, want 3", len(lines))
	}
	for index, line := range lines {
		encoded, err := json.Marshal(sampleEvent(int64(index + 1)))
		if err != nil {
			t.Fatal(err)
		}
		if line != string(encoded) {
			t.Errorf("line %d\n got %s\nwant %s", index+1, line, encoded)
		}
	}
	if sink.Dropped() != 0 {
		t.Errorf("Dropped = %d, want 0", sink.Dropped())
	}
}

func TestDirSinkMakesEachEventReadableBeforeClose(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	sink, err := trace.NewDirSink(directory, trace.Filesystem{})
	if err != nil {
		t.Fatalf("NewDirSink = %v", err)
	}
	t.Cleanup(func() { _ = sink.Close() })
	if err := sink.Emit(sampleEvent(1)); err != nil {
		t.Fatalf("Emit = %v", err)
	}
	data, err := os.ReadFile(filepath.Join(directory, trace.FileName))
	if err != nil {
		t.Fatal(err)
	}
	if len(jsonLines(t, data)) != 1 {
		t.Fatal("an emitted event was not on disk before Close; a hung run must still leave its trace")
	}
}

func TestDirSinkPreservesCommandOutputBesideTheTrace(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	sink, err := trace.NewDirSink(directory, trace.Filesystem{})
	if err != nil {
		t.Fatalf("NewDirSink = %v", err)
	}
	output := []byte("--- FAIL: TestBoundary\n\tboundary_test.go:12: want 3, got 4\n")
	digest := sha256.Sum256(output)
	event := trace.Event{
		Seq:       7,
		Type:      trace.TypeExec,
		Timestamp: traceOrigin.Format("2006-01-02T15:04:05Z07:00"),
		Exec: &trace.ExecRecord{
			Argv:         []string{"go", "test", "./..."},
			ExitCode:     1,
			Output:       output,
			OutputBytes:  len(output),
			OutputSHA256: hex.EncodeToString(digest[:]),
		},
	}
	if err := sink.Emit(event); err != nil {
		t.Fatalf("Emit = %v", err)
	}
	if err := sink.Close(); err != nil {
		t.Fatalf("Close = %v", err)
	}

	record := emittedExecRecord(t, directory)
	wantPath := path.Join(trace.OutputDirectoryName, "7.txt")
	if record["output_path"] != wantPath {
		t.Fatalf("output_path = %v, want %q", record["output_path"], wantPath)
	}
	if record["output_bytes"] != float64(len(output)) {
		t.Errorf("output_bytes = %v, want %d", record["output_bytes"], len(output))
	}
	if record["output_sha256"] != hex.EncodeToString(digest[:]) {
		t.Errorf("output_sha256 = %v, want %q", record["output_sha256"], hex.EncodeToString(digest[:]))
	}
	if _, truncated := record["output_truncated"]; truncated {
		t.Errorf("a preserved output that fits was marked truncated: %v", record)
	}
	preserved, err := os.ReadFile(filepath.Join(directory, trace.OutputDirectoryName, "7.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(preserved, output) {
		t.Fatalf("preserved output = %q, want %q", preserved, output)
	}
	if event.Exec.OutputPath != "" {
		t.Error("Emit mutated the caller's record; a tee'd sink must not see another sink's paths")
	}
}

func TestDirSinkTruncatesAPreservedOutputAtTheFileLimit(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	sink, err := trace.NewDirSink(directory, trace.Filesystem{})
	if err != nil {
		t.Fatalf("NewDirSink = %v", err)
	}
	output := bytes.Repeat([]byte("x"), trace.OutputFileLimit+4096)
	digest := sha256.Sum256(output)
	if err := sink.Emit(trace.Event{
		Seq:       2,
		Type:      trace.TypeExec,
		Timestamp: traceOrigin.Format("2006-01-02T15:04:05Z07:00"),
		Exec: &trace.ExecRecord{
			Argv: []string{"go", "test"}, Output: output,
			OutputBytes: len(output), OutputSHA256: hex.EncodeToString(digest[:]),
		},
	}); err != nil {
		t.Fatalf("Emit = %v", err)
	}
	if err := sink.Close(); err != nil {
		t.Fatalf("Close = %v", err)
	}

	record := emittedExecRecord(t, directory)
	if record["output_truncated"] != true {
		t.Fatalf("output_truncated = %v, want true", record["output_truncated"])
	}
	if record["output_bytes"] != float64(len(output)) {
		t.Errorf("output_bytes = %v, want the whole captured output %d", record["output_bytes"], len(output))
	}
	if record["output_sha256"] != hex.EncodeToString(digest[:]) {
		t.Error("a truncated file must still digest the whole captured output")
	}
	preserved, err := os.ReadFile(filepath.Join(directory, trace.OutputDirectoryName, "2.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if want := trace.OutputFileLimit + len(trace.TruncationMarker); len(preserved) != want {
		t.Fatalf("preserved %d bytes, want %d", len(preserved), want)
	}
	if !bytes.HasSuffix(preserved, []byte(trace.TruncationMarker)) {
		t.Fatal("a truncated file does not say so")
	}
}

func TestDirSinkKeepsTheEventWhenPreservingOutputFails(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	sink, err := trace.NewDirSink(directory, trace.Filesystem{
		WriteFile: func(string, []byte, fs.FileMode) error { return errors.New("no space left on device") },
	})
	if err != nil {
		t.Fatalf("NewDirSink = %v", err)
	}
	if err := sink.Emit(trace.Event{
		Seq:       1,
		Type:      trace.TypeExec,
		Timestamp: traceOrigin.Format("2006-01-02T15:04:05Z07:00"),
		Exec:      &trace.ExecRecord{Argv: []string{"go", "test"}, Output: []byte("output")},
	}); err != nil {
		t.Fatalf("Emit = %v", err)
	}
	if err := sink.Close(); err != nil {
		t.Fatalf("Close = %v", err)
	}

	record := emittedExecRecord(t, directory)
	if _, ok := record["output_path"]; ok {
		t.Fatalf("output_path names a file that was never written: %v", record)
	}
	if sink.Dropped() != 0 {
		t.Errorf("Dropped = %d, want 0; the event itself was written", sink.Dropped())
	}
}

func TestDirSinkCountsTheEventsItCouldNotWrite(t *testing.T) {
	t.Parallel()
	sink, err := trace.NewDirSink(t.TempDir(), trace.Filesystem{
		OpenAppend: func(string, fs.FileMode) (trace.File, error) {
			return failingFile{err: errors.New("input/output error")}, nil
		},
	})
	if err != nil {
		t.Fatalf("NewDirSink = %v", err)
	}
	for seq := int64(1); seq <= 3; seq++ {
		if err := sink.Emit(sampleEvent(seq)); err == nil {
			t.Fatalf("Emit(%d) hid a write failure", seq)
		}
	}
	if sink.Dropped() != 3 {
		t.Fatalf("Dropped = %d, want 3", sink.Dropped())
	}
	if err := sink.Close(); err != nil {
		t.Fatalf("Close = %v", err)
	}
}

func TestDirSinkFailsToOpenWhenItsDirectoryCannotBeCreated(t *testing.T) {
	t.Parallel()
	sink, err := trace.NewDirSink(t.TempDir(), trace.Filesystem{
		MkdirAll: func(string, fs.FileMode) error { return errors.New("permission denied") },
	})
	if err == nil {
		t.Fatal("NewDirSink accepted a directory it could not create")
	}
	if sink != nil {
		t.Fatal("NewDirSink returned a sink alongside an error")
	}
}

func TestDirSinkRejectsEmitAfterCloseAndClosesOnce(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	sink, err := trace.NewDirSink(directory, trace.Filesystem{})
	if err != nil {
		t.Fatalf("NewDirSink = %v", err)
	}
	if err := sink.Close(); err != nil {
		t.Fatalf("Close = %v", err)
	}
	if err := sink.Close(); err != nil {
		t.Fatalf("second Close = %v, want nil", err)
	}
	if err := sink.Emit(sampleEvent(1)); err == nil {
		t.Fatal("Emit after Close reported success")
	}
	if sink.Dropped() != 1 {
		t.Fatalf("Dropped = %d, want 1", sink.Dropped())
	}
	data, err := os.ReadFile(filepath.Join(directory, trace.FileName))
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != 0 {
		t.Fatalf("a closed sink wrote %q", data)
	}
}

func TestMemorySinkDropsTheOldestEventsWhenItsRingIsFull(t *testing.T) {
	t.Parallel()
	sink := trace.NewMemorySink(3)
	for seq := int64(1); seq <= 5; seq++ {
		if err := sink.Emit(sampleEvent(seq)); err != nil {
			t.Fatalf("Emit(%d) = %v", seq, err)
		}
	}
	var got []int64
	for _, event := range sink.Events() {
		got = append(got, event.Seq)
	}
	if !reflect.DeepEqual(got, []int64{3, 4, 5}) {
		t.Fatalf("Events = %v, want the last three", got)
	}
	if sink.Dropped() != 2 {
		t.Fatalf("Dropped = %d, want 2", sink.Dropped())
	}
}

func TestMemorySinkIsUnboundedWithoutACapacity(t *testing.T) {
	t.Parallel()
	sink := trace.NewMemorySink(0)
	for seq := int64(1); seq <= 100; seq++ {
		if err := sink.Emit(sampleEvent(seq)); err != nil {
			t.Fatalf("Emit(%d) = %v", seq, err)
		}
	}
	if len(sink.Events()) != 100 || sink.Dropped() != 0 {
		t.Fatalf("Events = %d, Dropped = %d; want 100 and 0", len(sink.Events()), sink.Dropped())
	}
}

func TestMemorySinkEventsAreASnapshot(t *testing.T) {
	t.Parallel()
	sink := trace.NewMemorySink(0)
	if err := sink.Emit(sampleEvent(1)); err != nil {
		t.Fatal(err)
	}
	events := sink.Events()
	events[0].Seq = 99
	if sink.Events()[0].Seq != 1 {
		t.Fatal("Events shares storage with the sink")
	}
}

func TestMemorySinkRejectsEmitAfterClose(t *testing.T) {
	t.Parallel()
	sink := trace.NewMemorySink(0)
	if err := sink.Close(); err != nil {
		t.Fatalf("Close = %v", err)
	}
	if err := sink.Emit(sampleEvent(1)); err == nil {
		t.Fatal("Emit after Close reported success")
	}
	if len(sink.Events()) != 0 {
		t.Fatal("a closed sink kept an event")
	}
	if sink.Dropped() != 1 {
		t.Fatalf("Dropped = %d, want 1", sink.Dropped())
	}
}

func TestTeeSinkDeliversToEverySinkEvenAfterOneFails(t *testing.T) {
	t.Parallel()
	failing := &recordingSink{emitErr: errors.New("input/output error")}
	memory := trace.NewMemorySink(0)
	tee := trace.NewTeeSink(failing, memory)

	err := tee.Emit(sampleEvent(1))
	if err == nil {
		t.Fatal("Emit hid a sink failure")
	}
	if !strings.Contains(err.Error(), "input/output error") {
		t.Fatalf("Emit error = %v, want the failing sink's error", err)
	}
	if len(failing.recorded()) != 1 || len(memory.Events()) != 1 {
		t.Fatalf("delivered to %d and %d sinks, want both", len(failing.recorded()), len(memory.Events()))
	}
	if tee.Dropped() != 1 {
		t.Fatalf("Dropped = %d, want 1", tee.Dropped())
	}
}

func TestTeeSinkSumsTheDropsOfEverySink(t *testing.T) {
	t.Parallel()
	first := trace.NewMemorySink(1)
	second := trace.NewMemorySink(2)
	tee := trace.NewTeeSink(first, second)
	for seq := int64(1); seq <= 4; seq++ {
		if err := tee.Emit(sampleEvent(seq)); err != nil {
			t.Fatalf("Emit(%d) = %v", seq, err)
		}
	}
	if first.Dropped() != 3 || second.Dropped() != 2 {
		t.Fatalf("sink drops = %d and %d, want 3 and 2", first.Dropped(), second.Dropped())
	}
	if tee.Dropped() != 5 {
		t.Fatalf("Dropped = %d, want the sum 5", tee.Dropped())
	}
}

func TestTeeSinkClosesEverySinkAndReportsTheFirstFailure(t *testing.T) {
	t.Parallel()
	first := &recordingSink{closeErr: errors.New("close failed")}
	second := &recordingSink{}
	tee := trace.NewTeeSink(first, second)
	if err := tee.Close(); err == nil || !strings.Contains(err.Error(), "close failed") {
		t.Fatalf("Close = %v, want the first failure", err)
	}
	if first.closes != 1 || second.closes != 1 {
		t.Fatalf("closed %d and %d times, want both closed once", first.closes, second.closes)
	}
}

func TestTeeSinkWithoutSinksAcceptsEverything(t *testing.T) {
	t.Parallel()
	tee := trace.NewTeeSink()
	if err := tee.Emit(sampleEvent(1)); err != nil {
		t.Fatalf("Emit = %v", err)
	}
	if err := tee.Close(); err != nil {
		t.Fatalf("Close = %v", err)
	}
	if tee.Dropped() != 0 {
		t.Fatalf("Dropped = %d, want 0", tee.Dropped())
	}
}

func TestARecorderCountsTheDropsOfASinkThatReportsThem(t *testing.T) {
	t.Parallel()
	failing, err := trace.NewDirSink(t.TempDir(), trace.Filesystem{
		OpenAppend: func(string, fs.FileMode) (trace.File, error) {
			return failingFile{err: errors.New("input/output error")}, nil
		},
	})
	if err != nil {
		t.Fatalf("NewDirSink = %v", err)
	}
	memory := trace.NewMemorySink(0)
	recorder := trace.New(trace.NewTeeSink(failing, memory), newClock().Now)
	recorder.Progress("snapshot", "repair round 1")
	recorder.RunEnd("assured", nil)

	events := memory.Events()
	if len(events) != 3 {
		t.Fatalf("recorded %+v, want run-start, progress and run-end", events)
	}
	run := events[2].Run
	if run == nil || run.EventsEmitted != 0 || run.EventsDropped != 2 {
		t.Fatalf("run-end accounting = %+v, want 0 emitted and 2 dropped", run)
	}
}

func TestARecorderCountsEmitErrorsOfASinkThatDoesNotReportDrops(t *testing.T) {
	t.Parallel()
	sink := &recordingSink{emitErr: errors.New("input/output error")}
	recorder := trace.New(sink, newClock().Now)
	recorder.Progress("snapshot", "repair round 1")
	recorder.RunEnd("assured", nil)

	events := sink.recorded()
	if len(events) != 3 {
		t.Fatalf("recorded %+v, want run-start, progress and run-end", events)
	}
	run := events[2].Run
	if run == nil || run.EventsEmitted != 0 || run.EventsDropped != 2 {
		t.Fatalf("run-end accounting = %+v, want 0 emitted and 2 dropped", run)
	}
}

// emittedExecRecord returns the exec payload of the single exec event written
// to a trace directory.
func emittedExecRecord(t *testing.T, directory string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(directory, trace.FileName))
	if err != nil {
		t.Fatal(err)
	}
	lines := jsonLines(t, data)
	if len(lines) != 1 {
		t.Fatalf("trace file holds %d lines, want 1", len(lines))
	}
	var event map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &event); err != nil {
		t.Fatal(err)
	}
	record, ok := event["exec"].(map[string]any)
	if !ok {
		t.Fatalf("event has no exec record: %s", lines[0])
	}
	return record
}
