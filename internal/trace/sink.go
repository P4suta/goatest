// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package trace

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
)

// Layout of a trace directory.
const (
	// FileName is the JSON Lines stream, one event per line, in sequence order.
	FileName = "trace.jsonl"
	// OutputDirectoryName holds the command output preserved beside the stream.
	OutputDirectoryName = "output"
	// OutputFileLimit caps one preserved output file. A capture larger than the
	// limit is truncated to it; the event still digests the whole capture.
	OutputFileLimit = 1 << 20
	// TruncationMarker ends a preserved output file that did not fit.
	TruncationMarker = "..."
)

// Permissions of what a trace writes. A trace is diagnostic exhaust a developer
// reads, not a secret store.
const (
	directoryPermissions fs.FileMode = 0o755
	filePermissions      fs.FileMode = 0o644
)

// errSinkClosed is the answer of a sink that has already been closed.
var errSinkClosed = errors.New("goatest: trace sink is closed")

// Sink keeps the events of a recording. A sink that cannot keep an event
// answers with an error and counts the loss; it never fails the run.
type Sink interface {
	Emit(event Event) error
	Close() error
}

// Dropper is the optional half of a sink that counts what it lost. A sink that
// implements it is the authority on its own drops, which is how a recording
// reports loss a sink absorbed without an error, such as a full ring buffer.
type Dropper interface {
	Dropped() int64
}

// File is the part of a file a trace stream needs. It exists so a test can
// stand in for the disk without a global seam.
type File interface {
	Write(data []byte) (int, error)
	Sync() error
	Close() error
}

// Filesystem is the filesystem a DirSink writes through. Its zero value is the
// os package; a test fills in only the operation it wants to drive.
type Filesystem struct {
	MkdirAll   func(path string, perm fs.FileMode) error
	OpenAppend func(path string, perm fs.FileMode) (File, error)
	WriteFile  func(path string, data []byte, perm fs.FileMode) error
}

// resolved returns the hooks with every unset operation filled in from the os
// package.
func (hooks Filesystem) resolved() Filesystem {
	if hooks.MkdirAll == nil {
		hooks.MkdirAll = os.MkdirAll
	}
	if hooks.OpenAppend == nil {
		hooks.OpenAppend = func(name string, perm fs.FileMode) (File, error) {
			return os.OpenFile(name, os.O_CREATE|os.O_WRONLY|os.O_APPEND, perm)
		}
	}
	if hooks.WriteFile == nil {
		hooks.WriteFile = os.WriteFile
	}
	return hooks
}

// DirSink writes a recording to a trace directory: the JSON Lines stream in
// FileName, and the output of the commands that produced any in
// OutputDirectoryName.
//
// Each event is flushed as it arrives, so a run that hangs or is killed still
// leaves everything it recorded readable on disk.
type DirSink struct {
	directory string
	hooks     Filesystem

	mutex        sync.Mutex
	file         File
	closed       bool
	outputExists bool

	dropped atomic.Int64
}

// NewDirSink creates the trace directory and opens its stream for appending.
// It reports the error that stopped it rather than returning a sink that cannot
// write; a caller that cannot trace runs untraced.
func NewDirSink(directory string, hooks Filesystem) (*DirSink, error) {
	hooks = hooks.resolved()
	if err := hooks.MkdirAll(directory, directoryPermissions); err != nil {
		return nil, fmt.Errorf("goatest: create trace directory %s: %w", directory, err)
	}
	stream := filepath.Join(directory, FileName)
	file, err := hooks.OpenAppend(stream, filePermissions)
	if err != nil {
		return nil, fmt.Errorf("goatest: open trace stream %s: %w", stream, err)
	}
	return &DirSink{directory: directory, hooks: hooks, file: file}, nil
}

// Emit appends one event to the stream, preserving any captured output beside
// it first, and flushes the line before returning.
func (sink *DirSink) Emit(event Event) error {
	sink.mutex.Lock()
	defer sink.mutex.Unlock()
	if sink.closed {
		sink.dropped.Add(1)
		return errSinkClosed
	}
	event = sink.preserveOutput(event)
	encoded, err := json.Marshal(event)
	if err != nil {
		sink.dropped.Add(1)
		return fmt.Errorf("goatest: encode trace event %d: %w", event.Seq, err)
	}
	if _, err := sink.file.Write(append(encoded, '\n')); err != nil {
		sink.dropped.Add(1)
		return fmt.Errorf("goatest: write trace event %d: %w", event.Seq, err)
	}
	// The line is in the file's hands and readable; whether it reached the disk
	// is not something a diagnostic stream fails over.
	_ = sink.file.Sync()
	return nil
}

// Close closes the stream. Closing twice is not an error, so a deferred close
// and an explicit one may both run.
func (sink *DirSink) Close() error {
	sink.mutex.Lock()
	defer sink.mutex.Unlock()
	if sink.closed {
		return nil
	}
	sink.closed = true
	if err := sink.file.Close(); err != nil {
		return fmt.Errorf("goatest: close trace stream: %w", err)
	}
	return nil
}

// Dropped reports how many events the sink could not write.
func (sink *DirSink) Dropped() int64 { return sink.dropped.Load() }

// preserveOutput writes the captured output of an exec event to the output
// directory and returns the event pointing at the file it wrote.
//
// It copies the record instead of amending the caller's, so a sink that shares
// an event with other sinks never sees this sink's paths. Preserving output is
// best effort: an output that cannot be written costs its path, not the event.
func (sink *DirSink) preserveOutput(event Event) Event {
	if event.Exec == nil || len(event.Exec.Output) == 0 {
		return event
	}
	data, truncated := limitOutput(event.Exec.Output)
	name := strconv.FormatInt(event.Seq, 10) + ".txt"
	if err := sink.writeOutput(name, data); err != nil {
		return event
	}
	record := *event.Exec
	record.OutputTruncated = truncated
	record.OutputPath = path.Join(OutputDirectoryName, name)
	event.Exec = &record
	return event
}

// writeOutput writes one preserved output file, creating the output directory
// the first time one is needed.
func (sink *DirSink) writeOutput(name string, data []byte) error {
	directory := filepath.Join(sink.directory, OutputDirectoryName)
	if !sink.outputExists {
		if err := sink.hooks.MkdirAll(directory, directoryPermissions); err != nil {
			return err
		}
		sink.outputExists = true
	}
	return sink.hooks.WriteFile(filepath.Join(directory, name), data, filePermissions)
}

// limitOutput caps a captured output at OutputFileLimit, marking what it cut.
func limitOutput(output []byte) ([]byte, bool) {
	if len(output) <= OutputFileLimit {
		return output, false
	}
	limited := make([]byte, 0, OutputFileLimit+len(TruncationMarker))
	limited = append(limited, output[:OutputFileLimit]...)
	return append(limited, TruncationMarker...), true
}

// MemorySink keeps the most recent events of a recording in a ring buffer. It
// is the sink of a test and of an in-process reader; a full ring drops its
// oldest event and counts it.
type MemorySink struct {
	capacity int

	mutex  sync.Mutex
	events []Event
	closed bool

	dropped atomic.Int64
}

// NewMemorySink returns a sink holding at most capacity events. A capacity of
// zero or less is unbounded.
//
// The last slot of a bounded ring belongs to the run-end event, so a run in
// progress fills capacity-1 slots. That reservation is what keeps the
// accounting honest: a recorder counts the loss before it writes the run-end,
// and a ring with no room left for that write would drop an event nothing
// could report. A ring of one therefore keeps the run-end alone.
func NewMemorySink(capacity int) *MemorySink { return &MemorySink{capacity: capacity} }

// Emit keeps one event, dropping the oldest ones the ring no longer has room
// for.
func (sink *MemorySink) Emit(event Event) error {
	sink.mutex.Lock()
	defer sink.mutex.Unlock()
	if sink.closed {
		sink.dropped.Add(1)
		return errSinkClosed
	}
	sink.events = append(sink.events, event)
	if sink.capacity <= 0 {
		return nil
	}
	room := sink.capacity
	if event.Type != TypeRunEnd {
		// The run-end has not arrived yet, and its slot is not on offer.
		room--
	}
	if overflow := len(sink.events) - room; overflow > 0 {
		sink.events = append(sink.events[:0], sink.events[overflow:]...)
		sink.dropped.Add(int64(overflow))
	}
	return nil
}

// Close stops the sink from accepting events. Closing twice is not an error.
func (sink *MemorySink) Close() error {
	sink.mutex.Lock()
	defer sink.mutex.Unlock()
	sink.closed = true
	return nil
}

// Dropped reports how many events fell out of the ring.
func (sink *MemorySink) Dropped() int64 { return sink.dropped.Load() }

// Events returns a snapshot of the kept events, oldest first.
func (sink *MemorySink) Events() []Event {
	sink.mutex.Lock()
	defer sink.mutex.Unlock()
	return append([]Event(nil), sink.events...)
}

// TeeSink delivers every event to several sinks, which is how one recording
// reaches both a trace directory and an in-process reader.
type TeeSink struct {
	sinks []Sink

	// dropped counts the failures of sinks that do not count their own.
	dropped atomic.Int64
}

// NewTeeSink returns a sink that fans out to the given sinks. Nil sinks are
// ignored, and no sinks at all is a sink that keeps nothing and loses nothing.
func NewTeeSink(sinks ...Sink) *TeeSink {
	kept := make([]Sink, 0, len(sinks))
	for _, sink := range sinks {
		if sink != nil {
			kept = append(kept, sink)
		}
	}
	return &TeeSink{sinks: kept}
}

// Emit delivers the event to every sink and reports the first failure. One sink
// failing never costs another sink the event.
func (tee *TeeSink) Emit(event Event) error {
	var first error
	for _, sink := range tee.sinks {
		err := sink.Emit(event)
		if err == nil {
			continue
		}
		if first == nil {
			first = err
		}
		if _, counts := sink.(Dropper); !counts {
			tee.dropped.Add(1)
		}
	}
	return first
}

// Close closes every sink and reports the first failure.
func (tee *TeeSink) Close() error {
	var first error
	for _, sink := range tee.sinks {
		if err := sink.Close(); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// Dropped reports the drops of every sink: their own counts where they keep
// one, and the failures observed here where they do not.
func (tee *TeeSink) Dropped() int64 {
	total := tee.dropped.Load()
	for _, sink := range tee.sinks {
		if dropper, ok := sink.(Dropper); ok {
			total += dropper.Dropped()
		}
	}
	return total
}
