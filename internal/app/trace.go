// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/P4suta/goatest/internal/cli"
	"github.com/P4suta/goatest/internal/report"
	"github.com/P4suta/goatest/internal/trace"
)

const traceDirectoryTimeFormat = "20060102T150405Z"

const traceUnavailable = "trace-unavailable"

const alwaysOnTraceEvents = 4096

const (
	traceVerdictInterrupted = "INTERRUPTED"
	traceVerdictUnknown     = "UNKNOWN"
)

type traceRecording struct {
	recorder *trace.Recorder

	sink trace.Sink

	ring *trace.MemorySink

	directory string
}

func (recording traceRecording) Events() []trace.Event {
	if recording.ring == nil {
		return nil
	}
	return recording.ring.Events()
}

func (service Service) startTrace(root string, request cli.Request) (traceRecording, func(report.Report, error)) {
	recording := service.openRecording(root, request)
	return recording, func(result report.Report, runErr error) {
		recording.recorder.RunEnd(traceVerdict(result, runErr), runErr)
		if closeErr := recording.sink.Close(); closeErr != nil {
			service.note(traceUnavailable, closeErr.Error())
		}
	}
}

func (service Service) openRecording(root string, request cli.Request) traceRecording {
	if !request.Trace {
		return service.recordInMemory()
	}
	directory, err := service.traceDirectory(root, request)
	if err != nil {
		service.note(traceUnavailable, err.Error())
		return service.recordInMemory()
	}
	sink, err := trace.NewDirSink(directory, service.traceName(), service.TraceFilesystem)
	if err != nil {
		service.note(traceUnavailable, err.Error())
		return service.recordInMemory()
	}
	return traceRecording{recorder: trace.New(sink, service.Now), sink: sink, directory: sink.Directory()}
}

func (service Service) recordInMemory() traceRecording {
	ring := trace.NewMemorySink(alwaysOnTraceEvents)
	sink := digestedSink{ring: ring}
	return traceRecording{recorder: trace.New(sink, service.Now), sink: sink, ring: ring}
}

type digestedSink struct{ ring *trace.MemorySink }

func (sink digestedSink) Emit(event trace.Event) error {
	if event.Exec != nil && len(event.Exec.Output) > 0 {
		record := *event.Exec
		record.Output = nil
		event.Exec = &record
	}
	return sink.ring.Emit(event)
}

func (sink digestedSink) Close() error { return sink.ring.Close() }

func (sink digestedSink) Dropped() int64 { return sink.ring.Dropped() }

func traceVerdict(result report.Report, runErr error) string {
	switch {
	case result.Verdict != "":
		return string(result.Verdict)
	case errors.Is(runErr, context.Canceled), errors.Is(runErr, context.DeadlineExceeded):
		return traceVerdictInterrupted
	case runErr != nil:
		return string(report.VerdictError)
	default:
		return traceVerdictUnknown
	}
}

func (service Service) traceDirectory(root string, request cli.Request) (string, error) {
	directory := strings.TrimSpace(request.TraceDirectory)
	if directory == "" {
		return filepath.Join(root, ".goatest", "trace"), nil
	}
	if !filepath.IsAbs(directory) {
		directory = filepath.Join(root, directory)
	}
	if inspectedAsSource(root, directory) {
		return "", fmt.Errorf(
			"goatest: trace directory %s is inside the repository and would be read as source; write it outside the repository or under .goatest",
			directory)
	}
	return directory, nil
}

func (service Service) traceName() string {
	processID := os.Getpid
	if service.ProcessID != nil {
		processID = service.ProcessID
	}
	return service.clock()().UTC().Format(traceDirectoryTimeFormat) + "-" + strconv.Itoa(processID())
}

func inspectedAsSource(root, directory string) bool {
	return underRepository(root, directory) ||
		underRepository(existingPathOf(root), existingPathOf(directory))
}

func underRepository(root, directory string) bool {
	relative, err := filepath.Rel(root, directory)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return false
	}
	first, _, _ := strings.Cut(filepath.ToSlash(relative), "/")
	return first != ".goatest"
}

func existingPathOf(path string) string {
	cleaned := filepath.Clean(path)
	remainder := ""
	for current := cleaned; ; {
		if resolved, err := filepath.EvalSymlinks(current); err == nil {
			return filepath.Join(resolved, remainder)
		}
		parent := filepath.Dir(current)
		if parent == current {
			return cleaned
		}
		remainder = filepath.Join(filepath.Base(current), remainder)
		current = parent
	}
}
