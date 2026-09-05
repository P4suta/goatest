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

// unclosableFile is a trace file that takes every write and fails on close,
// which is how a filesystem reports the write it never actually completed.
type unclosableFile struct{ err error }

func (file unclosableFile) Write(data []byte) (int, error) { return len(data), nil }
func (file unclosableFile) Sync() error                    { return nil }
func (file unclosableFile) Close() error                   { return file.err }

// sampleEvent is one minimal event, enough to drive a sink.
func sampleEvent(seq int64) trace.Event {
	return trace.Event{
		Seq:       seq,
		Type:      trace.TypeProgress,
		Timestamp: traceOrigin.Format("2006-01-02T15:04:05Z07:00"),
		Progress:  &trace.ProgressRecord{Kind: "mutation-progress", Detail: strconv.FormatInt(seq, 10)},
	}
}

// execEvent is one exec event carrying captured output, enough to drive the
// output a sink preserves beside its stream.
func execEvent(seq int64, output []byte) trace.Event {
	digest := sha256.Sum256(output)
	return trace.Event{
		Seq:       seq,
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
}

func TestDirSinkCreatesItsDirectoryAndAppendsJSONL(t *testing.T) {
	t.Parallel()
	root := filepath.Join(t.TempDir(), "trace")
	sink, err := trace.NewDirSink(root, "20260102T030405Z-1234", trace.Filesystem{})
	if err != nil {
		t.Fatalf("NewDirSink = %v", err)
	}
	directory := sink.Directory()
	if want := filepath.Join(root, "20260102T030405Z-1234"); directory != want {
		t.Fatalf("Directory = %s, want the run's own directory %s", directory, want)
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

func TestDirSinkGivesEachRunItsOwnDirectoryUnderOneRoot(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	// The same root, twice, is the developer who passes --trace=DIR to every
	// run: a second recording joins the first rather than writing over it.
	first, err := trace.NewDirSink(root, "20260102T030405Z-1234", trace.Filesystem{})
	if err != nil {
		t.Fatalf("first NewDirSink = %v", err)
	}
	if err := first.Emit(execEvent(1, []byte("first run\n"))); err != nil {
		t.Fatalf("first Emit = %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("first Close = %v", err)
	}
	second, err := trace.NewDirSink(root, "20260102T030406Z-5678", trace.Filesystem{})
	if err != nil {
		t.Fatalf("second NewDirSink = %v", err)
	}
	if err := second.Emit(execEvent(1, []byte("second run\n"))); err != nil {
		t.Fatalf("second Emit = %v", err)
	}
	if err := second.Close(); err != nil {
		t.Fatalf("second Close = %v", err)
	}

	if first.Directory() == second.Directory() {
		t.Fatalf("both runs recorded into %s", first.Directory())
	}
	for directory, want := range map[string]string{
		first.Directory():  "first run\n",
		second.Directory(): "second run\n",
	} {
		data, err := os.ReadFile(filepath.Join(directory, trace.FileName))
		if err != nil {
			t.Fatal(err)
		}
		if lines := jsonLines(t, data); len(lines) != 1 {
			t.Fatalf("%s holds %d lines, want the single event of its own run", directory, len(lines))
		}
		// A second run restarts at sequence one, so a shared directory would
		// have written over the output the first run's event digested.
		preserved, err := os.ReadFile(filepath.Join(directory, trace.OutputDirectoryName, "1.txt"))
		if err != nil {
			t.Fatal(err)
		}
		if string(preserved) != want {
			t.Fatalf("%s preserved %q, want %q", directory, preserved, want)
		}
	}
}

func TestDirSinkRefusesADirectoryARecordingAlreadyOwns(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	first, err := trace.NewDirSink(root, "20260102T030405Z-1234", trace.Filesystem{})
	if err != nil {
		t.Fatalf("NewDirSink = %v", err)
	}
	if err := first.Emit(sampleEvent(1)); err != nil {
		t.Fatalf("Emit = %v", err)
	}
	second, err := trace.NewDirSink(root, "20260102T030405Z-1234", trace.Filesystem{})
	if err == nil {
		t.Fatal("a second recording opened the directory of the first")
	}
	if second != nil {
		t.Fatal("NewDirSink returned a sink alongside an error")
	}
	if !strings.Contains(err.Error(), "20260102T030405Z-1234") {
		t.Errorf("error = %v, want the directory it refused", err)
	}
	if err := first.Emit(sampleEvent(2)); err != nil {
		t.Fatalf("the refused recording disturbed the open one: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close = %v", err)
	}
	data, err := os.ReadFile(filepath.Join(first.Directory(), trace.FileName))
	if err != nil {
		t.Fatal(err)
	}
	if lines := jsonLines(t, data); len(lines) != 2 {
		t.Fatalf("the open recording holds %d lines, want its own two", len(lines))
	}
}

func TestDirSinkMakesEachEventReadableBeforeClose(t *testing.T) {
	t.Parallel()
	sink, err := trace.NewDirSink(t.TempDir(), "20260102T030405Z-1234", trace.Filesystem{})
	if err != nil {
		t.Fatalf("NewDirSink = %v", err)
	}
	directory := sink.Directory()
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
	sink, err := trace.NewDirSink(t.TempDir(), "20260102T030405Z-1234", trace.Filesystem{})
	if err != nil {
		t.Fatalf("NewDirSink = %v", err)
	}
	directory := sink.Directory()
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
	room := 0
	sink, err := trace.NewDirSink(t.TempDir(), "20260102T030405Z-1234", trace.Filesystem{
		WriteFile: func(name string, data []byte, perm fs.FileMode) error {
			room = cap(data)
			return os.WriteFile(name, data, perm)
		},
	})
	if err != nil {
		t.Fatalf("NewDirSink = %v", err)
	}
	directory := sink.Directory()
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
	// The capped copy is a megabyte the sink lifts out of the event, and the
	// marker it ends with is part of what it is making room for. A copy sized
	// for the limit alone would be grown and moved again by the marker.
	if want := trace.OutputFileLimit + len(trace.TruncationMarker); room != want {
		t.Errorf("the truncated copy was made with room for %d bytes, want the limit and its marker %d", room, want)
	}
}

func TestDirSinkPreservesAnOutputThatExactlyFillsTheFileLimit(t *testing.T) {
	t.Parallel()
	sink, err := trace.NewDirSink(t.TempDir(), "20260102T030405Z-1234", trace.Filesystem{})
	if err != nil {
		t.Fatalf("NewDirSink = %v", err)
	}
	directory := sink.Directory()
	// The limit is what a file may hold, not what it must stay under: an
	// output of exactly the limit is preserved whole and says nothing about
	// truncation, because nothing was cut.
	output := bytes.Repeat([]byte("x"), trace.OutputFileLimit)
	if err := sink.Emit(execEvent(3, output)); err != nil {
		t.Fatalf("Emit = %v", err)
	}
	if err := sink.Close(); err != nil {
		t.Fatalf("Close = %v", err)
	}

	record := emittedExecRecord(t, directory)
	if _, truncated := record["output_truncated"]; truncated {
		t.Errorf("an output that fits exactly was marked truncated: %v", record["output_truncated"])
	}
	preserved, err := os.ReadFile(filepath.Join(directory, trace.OutputDirectoryName, "3.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if len(preserved) != trace.OutputFileLimit {
		t.Fatalf("preserved %d bytes, want the whole %d", len(preserved), trace.OutputFileLimit)
	}
	if !bytes.Equal(preserved, output) {
		t.Fatal("an output that fits exactly was not preserved as it was captured")
	}
}

func TestDirSinkKeepsTheEventWhenPreservingOutputFails(t *testing.T) {
	t.Parallel()
	sink, err := trace.NewDirSink(t.TempDir(), "20260102T030405Z-1234", trace.Filesystem{
		WriteFile: func(string, []byte, fs.FileMode) error { return errors.New("no space left on device") },
	})
	if err != nil {
		t.Fatalf("NewDirSink = %v", err)
	}
	directory := sink.Directory()
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

func TestDirSinkKeepsTheEventWhenItsOutputDirectoryCannotBeCreated(t *testing.T) {
	t.Parallel()
	var written []string
	sink, err := trace.NewDirSink(t.TempDir(), "20260102T030405Z-1234", trace.Filesystem{
		MkdirAll: func(name string, perm fs.FileMode) error {
			if filepath.Base(name) == trace.OutputDirectoryName {
				return errors.New("permission denied")
			}
			return os.MkdirAll(name, perm)
		},
		WriteFile: func(name string, data []byte, perm fs.FileMode) error {
			written = append(written, name)
			return os.WriteFile(name, data, perm)
		},
	})
	if err != nil {
		t.Fatalf("NewDirSink = %v", err)
	}
	directory := sink.Directory()
	if err := sink.Emit(execEvent(1, []byte("captured output\n"))); err != nil {
		t.Fatalf("Emit = %v", err)
	}
	if err := sink.Close(); err != nil {
		t.Fatalf("Close = %v", err)
	}

	// An output directory that cannot be created costs the output its path,
	// never the event: the record still digests what the command printed.
	record := emittedExecRecord(t, directory)
	if _, ok := record["output_path"]; ok {
		t.Fatalf("output_path names a file no output directory could hold: %v", record)
	}
	if record["output_sha256"] == nil || record["output_bytes"] == nil {
		t.Fatalf("the event lost its own accounting of the output: %v", record)
	}
	// Nothing is written into a directory the sink knows it never created.
	if len(written) != 0 {
		t.Fatalf("preserved output was written to %v without its directory", written)
	}
	if sink.Dropped() != 0 {
		t.Errorf("Dropped = %d, want 0; the event itself was written", sink.Dropped())
	}
}

func TestDirSinkCreatesItsOutputDirectoryOnceForTheWholeRecording(t *testing.T) {
	t.Parallel()
	var created []string
	sink, err := trace.NewDirSink(t.TempDir(), "20260102T030405Z-1234", trace.Filesystem{
		MkdirAll: func(name string, perm fs.FileMode) error {
			created = append(created, name)
			return os.MkdirAll(name, perm)
		},
	})
	if err != nil {
		t.Fatalf("NewDirSink = %v", err)
	}
	directory := sink.Directory()
	for seq := int64(1); seq <= 3; seq++ {
		if err := sink.Emit(execEvent(seq, []byte("output of "+strconv.FormatInt(seq, 10)+"\n"))); err != nil {
			t.Fatalf("Emit(%d) = %v", seq, err)
		}
	}
	if err := sink.Close(); err != nil {
		t.Fatalf("Close = %v", err)
	}

	// The output directory is made when the first output needs one, and the
	// sink remembers it: preserving output is a write per event, not a
	// directory per event.
	output := filepath.Join(directory, trace.OutputDirectoryName)
	var creations int
	for _, name := range created {
		if name == output {
			creations++
		}
	}
	if creations != 1 {
		t.Fatalf("the output directory was created %d times, want once for the recording", creations)
	}
	for seq := int64(1); seq <= 3; seq++ {
		preserved, err := os.ReadFile(filepath.Join(output, strconv.FormatInt(seq, 10)+".txt"))
		if err != nil {
			t.Fatal(err)
		}
		if want := "output of " + strconv.FormatInt(seq, 10) + "\n"; string(preserved) != want {
			t.Errorf("preserved output %d = %q, want %q", seq, preserved, want)
		}
	}
}

func TestDirSinkCountsTheEventsItCouldNotWrite(t *testing.T) {
	t.Parallel()
	sink, err := trace.NewDirSink(t.TempDir(), "20260102T030405Z-1234", trace.Filesystem{
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
	sink, err := trace.NewDirSink(t.TempDir(), "20260102T030405Z-1234", trace.Filesystem{
		MkdirAll: func(string, fs.FileMode) error { return errors.New("permission denied") },
	})
	if err == nil {
		t.Fatal("NewDirSink accepted a directory it could not create")
	}
	if sink != nil {
		t.Fatal("NewDirSink returned a sink alongside an error")
	}
}

func TestDirSinkFailsToOpenWhenItsStreamCannotBeOpened(t *testing.T) {
	t.Parallel()
	sink, err := trace.NewDirSink(t.TempDir(), "20260102T030405Z-1234", trace.Filesystem{
		OpenAppend: func(string, fs.FileMode) (trace.File, error) {
			return nil, errors.New("too many open files")
		},
	})
	// A sink whose stream never opened would write every event into a file it
	// does not have. It reports the failure instead, so that the caller runs
	// untraced rather than believing it is being recorded.
	if err == nil {
		t.Fatal("NewDirSink returned a sink for a stream it could not open")
	}
	if sink != nil {
		t.Fatal("NewDirSink returned a sink alongside an error")
	}
	if !strings.Contains(err.Error(), trace.FileName) || !strings.Contains(err.Error(), "too many open files") {
		t.Errorf("error = %v, want the stream it could not open and the reason", err)
	}
}

func TestDirSinkReportsAStreamItCouldNotClose(t *testing.T) {
	t.Parallel()
	sink, err := trace.NewDirSink(t.TempDir(), "20260102T030405Z-1234", trace.Filesystem{
		OpenAppend: func(string, fs.FileMode) (trace.File, error) {
			return unclosableFile{err: errors.New("no space left on device")}, nil
		},
	})
	if err != nil {
		t.Fatalf("NewDirSink = %v", err)
	}
	if err := sink.Emit(sampleEvent(1)); err != nil {
		t.Fatalf("Emit = %v", err)
	}
	// Closing is the last moment a filesystem can report what it never wrote,
	// and a recording that hid it would claim a stream nothing can read.
	closeErr := sink.Close()
	if closeErr == nil {
		t.Fatal("Close hid the failure of the stream it was closing")
	}
	if !strings.Contains(closeErr.Error(), "no space left on device") {
		t.Errorf("Close = %v, want the failure it was answered with", closeErr)
	}
	// The recording is over either way: a stream that failed to close is not
	// reopened, and a second close is not a second failure.
	if err := sink.Close(); err != nil {
		t.Fatalf("second Close = %v, want nil", err)
	}
	if err := sink.Emit(sampleEvent(2)); err == nil {
		t.Fatal("Emit after a failed Close reported success")
	}
}

func TestDirSinkRejectsEmitAfterCloseAndClosesOnce(t *testing.T) {
	t.Parallel()
	sink, err := trace.NewDirSink(t.TempDir(), "20260102T030405Z-1234", trace.Filesystem{})
	if err != nil {
		t.Fatalf("NewDirSink = %v", err)
	}
	directory := sink.Directory()
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
	// A ring of three holds two events of a run in progress: the last slot
	// belongs to the run-end, so the event that closes the recording never
	// costs the recording an event it already accounted for.
	if !reflect.DeepEqual(got, []int64{4, 5}) {
		t.Fatalf("Events = %v, want the last two beside the room held for the run-end", got)
	}
	if sink.Dropped() != 3 {
		t.Fatalf("Dropped = %d, want 3", sink.Dropped())
	}
}

func TestMemorySinkKeepsItsLastSlotForTheRunEnd(t *testing.T) {
	t.Parallel()
	sink := trace.NewMemorySink(2)
	for seq := int64(1); seq <= 3; seq++ {
		if err := sink.Emit(sampleEvent(seq)); err != nil {
			t.Fatalf("Emit(%d) = %v", seq, err)
		}
	}
	runEnd := trace.Event{
		Seq:       4,
		Type:      trace.TypeRunEnd,
		Timestamp: traceOrigin.Format("2006-01-02T15:04:05Z07:00"),
		Run:       &trace.RunRecord{Verdict: "assured", EventsEmitted: 1, EventsDropped: 2},
	}
	dropped := sink.Dropped()
	if err := sink.Emit(runEnd); err != nil {
		t.Fatalf("Emit(run-end) = %v", err)
	}
	if sink.Dropped() != dropped {
		t.Fatalf("the run-end displaced an event: Dropped = %d, was %d", sink.Dropped(), dropped)
	}
	var got []int64
	for _, event := range sink.Events() {
		got = append(got, event.Seq)
	}
	if !reflect.DeepEqual(got, []int64{3, 4}) {
		t.Fatalf("Events = %v, want the last event of the run and the run-end", got)
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

func TestMemorySinkKeepsAPayloadItsCallersCannotAmend(t *testing.T) {
	t.Parallel()
	sink := trace.NewMemorySink(0)
	exec := &trace.ExecRecord{
		Argv:     []string{"go", "test", "./..."},
		EnvNames: []string{"GOCACHE", "GOFLAGS"},
		ExitCode: 1,
		Output:   []byte("--- FAIL: TestBoundary\n"),
	}
	route := &trace.RouteRecord{
		MutantID:        "m-0001",
		Path:            "internal/assure/run.go",
		ReachingTargets: []string{"TestRun"},
		Plan:            []string{"TestRun"},
		Reason:          trace.ReasonProbeReaching,
		Granularity:     trace.GranularityBlock,
		Discharged:      []trace.Discharge{{Target: "TestSkipped", Reason: trace.DischargeBranchNeverTaken}},
		ProbeReaching:   []string{"TestRun"},
		Probed:          true,
	}
	probe := &trace.ProbeRecord{
		Target:   "TestRun",
		Args:     []string{"-test.run=^TestRun$"},
		Outcome:  trace.ProbeOutcomeMeasured,
		Infected: []string{"m-0001"},
	}
	execEvent := trace.Event{Seq: 1, Type: trace.TypeExec, Timestamp: traceOrigin.Format("2006-01-02T15:04:05Z07:00"), Exec: exec}
	routeEvent := trace.Event{Seq: 2, Type: trace.TypeRoute, Timestamp: traceOrigin.Format("2006-01-02T15:04:05Z07:00"), Route: route}
	probeEvent := trace.Event{Seq: 3, Type: trace.TypeProbeExec, Timestamp: traceOrigin.Format("2006-01-02T15:04:05Z07:00"), Probe: probe}
	for _, event := range []trace.Event{execEvent, routeEvent, probeEvent} {
		if err := sink.Emit(event); err != nil {
			t.Fatalf("Emit(%d) = %v", event.Seq, err)
		}
	}

	// The caller amends the records it emitted. A sink whose state a caller
	// can reach past its own mutex is a sink whose events are not what was
	// recorded.
	exec.Argv[0] = "rm"
	exec.EnvNames[0] = "GOATEST_TOKEN=super-secret"
	exec.Output[0] = 'X'
	exec.ExitCode = 137
	route.Plan[0] = "TestSomethingElse"
	route.Discharged[0].Target = "TestSomethingElse"
	route.ProbeReaching[0] = "TestSomethingElse"
	route.Reason = trace.ReasonUnreached
	probe.Args[0] = "-test.run=^TestSomethingElse$"
	probe.Infected[0] = "m-0002"

	kept := sink.Events()
	if kept[0].Exec.Argv[0] != "go" || kept[0].Exec.EnvNames[0] != "GOCACHE" || kept[0].Exec.ExitCode != 1 {
		t.Fatalf("a caller amended the exec record the sink kept: %+v", kept[0].Exec)
	}
	if string(kept[0].Exec.Output) != "--- FAIL: TestBoundary\n" {
		t.Fatalf("a caller amended the output the sink kept: %q", kept[0].Exec.Output)
	}
	if kept[1].Route.Plan[0] != "TestRun" || kept[1].Route.Reason != trace.ReasonProbeReaching {
		t.Fatalf("a caller amended the route record the sink kept: %+v", kept[1].Route)
	}
	if kept[1].Route.Discharged[0].Target != "TestSkipped" {
		t.Fatalf("a caller amended the discharges the sink kept: %+v", kept[1].Route.Discharged)
	}
	if kept[1].Route.ProbeReaching[0] != "TestRun" {
		t.Fatalf("a caller amended the probe-reaching targets the sink kept: %+v", kept[1].Route.ProbeReaching)
	}
	if kept[2].Probe.Args[0] != "-test.run=^TestRun$" || kept[2].Probe.Infected[0] != "m-0001" {
		t.Fatalf("a caller amended the probe record the sink kept: %+v", kept[2].Probe)
	}

	// The snapshot is the caller's own, down to the payload it points at.
	kept[0].Exec.Argv[0] = "rm"
	kept[0].Exec.Output[0] = 'X'
	kept[1].Route.ReachingTargets[0] = "TestSomethingElse"
	kept[1].Route.ProbeReaching[0] = "TestSomethingElse"
	kept[2].Probe.Infected[0] = "m-0002"
	again := sink.Events()
	if again[0].Exec.Argv[0] != "go" || again[0].Exec.Output[0] != '-' ||
		again[1].Route.ReachingTargets[0] != "TestRun" || again[1].Route.ProbeReaching[0] != "TestRun" {
		t.Fatalf("a returned snapshot shares its payload with the sink: %+v %+v", again[0].Exec, again[1].Route)
	}
	if again[2].Probe.Infected[0] != "m-0001" {
		t.Fatalf("a returned snapshot shares its probe record with the sink: %+v", again[2].Probe)
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
	// Neither ring has seen a run-end, so each is one slot smaller than its
	// capacity: the ring of one keeps nothing, the ring of two keeps the last
	// event.
	if first.Dropped() != 4 || second.Dropped() != 3 {
		t.Fatalf("sink drops = %d and %d, want 4 and 3", first.Dropped(), second.Dropped())
	}
	if tee.Dropped() != 7 {
		t.Fatalf("Dropped = %d, want the sum 7", tee.Dropped())
	}
}

func TestTeeSinkClosesEverySinkAndReportsTheFirstFailure(t *testing.T) {
	t.Parallel()
	first := &recordingSink{closeErr: errors.New("close failed")}
	second := &recordingSink{}
	// A second failure is not a better answer than the first: the failure that
	// ended the first recording is the one a reader is told about.
	third := &recordingSink{closeErr: errors.New("a later close failed")}
	tee := trace.NewTeeSink(first, second, third)
	err := tee.Close()
	if err == nil || !strings.Contains(err.Error(), "close failed") {
		t.Fatalf("Close = %v, want the first failure", err)
	}
	if strings.Contains(err.Error(), "a later close failed") {
		t.Fatalf("Close = %v, want the first failure rather than the last", err)
	}
	if first.closes != 1 || second.closes != 1 || third.closes != 1 {
		t.Fatalf("closed %d, %d and %d times, want every sink closed once", first.closes, second.closes, third.closes)
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
	failing, err := trace.NewDirSink(t.TempDir(), "20260102T030405Z-1234", trace.Filesystem{
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
