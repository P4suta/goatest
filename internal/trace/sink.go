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
	"slices"
	"strconv"
	"sync"
	"sync/atomic"

	"github.com/P4suta/goatest/internal/filemode"
)

const (
	FileName = "trace.jsonl"

	OutputDirectoryName = "output"

	OutputFileLimit = 1 << 20

	TruncationMarker = "..."
)

const (
	directoryPermissions fs.FileMode = filemode.ReadableDirectory
	filePermissions      fs.FileMode = filemode.ReadableFile
)

var errSinkClosed = errors.New("goatest: trace sink is closed")

type Sink interface {
	Emit(event Event) error
	Close() error
}

type Dropper interface {
	Dropped() int64
}

type File interface {
	Write(data []byte) (int, error)
	Sync() error
	Close() error
}

type Filesystem struct {
	MkdirAll   func(path string, perm fs.FileMode) error
	Mkdir      func(path string, perm fs.FileMode) error
	OpenAppend func(path string, perm fs.FileMode) (File, error)
	WriteFile  func(path string, data []byte, perm fs.FileMode) error
}

func (hooks Filesystem) resolved() Filesystem {
	if hooks.MkdirAll == nil {
		hooks.MkdirAll = os.MkdirAll
	}
	if hooks.Mkdir == nil {
		hooks.Mkdir = os.Mkdir
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

type DirSink struct {
	directory string
	hooks     Filesystem

	mutex        sync.Mutex
	file         File
	closed       bool
	outputExists bool

	dropped atomic.Int64
}

func NewDirSink(root, run string, hooks Filesystem) (*DirSink, error) {
	hooks = hooks.resolved()
	if err := hooks.MkdirAll(root, directoryPermissions); err != nil {
		return nil, fmt.Errorf("goatest: create trace directory %s: %w", root, err)
	}
	directory := filepath.Join(root, run)
	if err := hooks.Mkdir(directory, directoryPermissions); err != nil {
		return nil, fmt.Errorf("goatest: create trace directory %s: %w", directory, err)
	}
	stream := filepath.Join(directory, FileName)
	file, err := hooks.OpenAppend(stream, filePermissions)
	if err != nil {
		return nil, fmt.Errorf("goatest: open trace stream %s: %w", stream, err)
	}
	return &DirSink{directory: directory, hooks: hooks, file: file}, nil
}

func (sink *DirSink) Directory() string { return sink.directory }

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
	return nil
}

func (sink *DirSink) Close() error {
	sink.mutex.Lock()
	defer sink.mutex.Unlock()
	if sink.closed {
		return nil
	}
	sink.closed = true
	var failures []error
	if err := sink.file.Sync(); err != nil {
		failures = append(failures, fmt.Errorf("goatest: sync trace stream: %w", err))
	}
	if err := sink.file.Close(); err != nil {
		failures = append(failures, fmt.Errorf("goatest: close trace stream: %w", err))
	}
	return errors.Join(failures...)
}

func (sink *DirSink) Dropped() int64 { return sink.dropped.Load() }

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

func limitOutput(output []byte) ([]byte, bool) {
	if len(output) <= OutputFileLimit {
		return output, false
	}
	limited := make([]byte, 0, OutputFileLimit+len(TruncationMarker))
	limited = append(limited, output[:OutputFileLimit]...)
	return append(limited, TruncationMarker...), true
}

type MemorySink struct {
	capacity int

	mutex  sync.Mutex
	events []Event
	closed bool

	dropped atomic.Int64
}

func NewMemorySink(capacity int) *MemorySink { return &MemorySink{capacity: capacity} }

func (sink *MemorySink) Emit(event Event) error {
	sink.mutex.Lock()
	defer sink.mutex.Unlock()
	if sink.closed {
		sink.dropped.Add(1)
		return errSinkClosed
	}
	sink.events = append(sink.events, cloneEvent(event))
	if sink.capacity <= 0 {
		return nil
	}
	room := sink.capacity
	if event.Type != TypeRunEnd {
		room--
	}
	if overflow := len(sink.events) - room; overflow > 0 {
		sink.events = append(sink.events[:0], sink.events[overflow:]...)
		sink.dropped.Add(int64(overflow))
	}
	return nil
}

func (sink *MemorySink) Close() error {
	sink.mutex.Lock()
	defer sink.mutex.Unlock()
	sink.closed = true
	return nil
}

func (sink *MemorySink) Dropped() int64 { return sink.dropped.Load() }

func (sink *MemorySink) Events() []Event {
	sink.mutex.Lock()
	defer sink.mutex.Unlock()
	snapshot := make([]Event, 0, len(sink.events))
	for _, event := range sink.events {
		snapshot = append(snapshot, cloneEvent(event))
	}
	return snapshot
}

func cloneEvent(event Event) Event {
	if event.Phase != nil {
		record := *event.Phase
		event.Phase = &record
	}
	if event.Prepare != nil {
		record := *event.Prepare
		if record.DurationMS != nil {
			durationMS := *record.DurationMS
			record.DurationMS = &durationMS
		}
		event.Prepare = &record
	}
	if event.Exec != nil {
		record := *event.Exec
		record.Argv = slices.Clone(record.Argv)
		record.EnvNames = slices.Clone(record.EnvNames)
		record.Output = slices.Clone(record.Output)
		event.Exec = &record
	}
	if event.Mutant != nil {
		record := *event.Mutant
		record.Args = slices.Clone(record.Args)
		event.Mutant = &record
	}
	if event.Route != nil {
		record := *event.Route
		record.ReachingTargets = slices.Clone(record.ReachingTargets)
		record.Plan = slices.Clone(record.Plan)
		record.Discharged = slices.Clone(record.Discharged)
		record.ProbeReaching = slices.Clone(record.ProbeReaching)
		event.Route = &record
	}
	if event.Probe != nil {
		record := *event.Probe
		record.Args = slices.Clone(record.Args)
		record.Infected = slices.Clone(record.Infected)
		event.Probe = &record
	}
	if event.Progress != nil {
		record := *event.Progress
		event.Progress = &record
	}
	if event.Artifact != nil {
		record := *event.Artifact
		event.Artifact = &record
	}
	if event.Run != nil {
		record := *event.Run
		event.Run = &record
	}
	return event
}

type TeeSink struct {
	sinks []Sink

	dropped atomic.Int64
}

func NewTeeSink(sinks ...Sink) *TeeSink {
	kept := make([]Sink, 0, len(sinks))
	for _, sink := range sinks {
		if sink != nil {
			kept = append(kept, sink)
		}
	}
	return &TeeSink{sinks: kept}
}

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

func (tee *TeeSink) Close() error {
	var first error
	for _, sink := range tee.sinks {
		if err := sink.Close(); err != nil && first == nil {
			first = err
		}
	}
	return first
}

func (tee *TeeSink) Dropped() int64 {
	total := tee.dropped.Load()
	for _, sink := range tee.sinks {
		if dropper, ok := sink.(Dropper); ok {
			total += dropper.Dropped()
		}
	}
	return total
}
