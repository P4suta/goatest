// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package app

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/P4suta/goatest/internal/cli"
	"github.com/P4suta/goatest/internal/report"
	"github.com/P4suta/goatest/internal/trace"
)

// traceDirectoryTimeFormat names a default trace directory after the moment
// the run started, in the compact UTC shape run identities already use.
const traceDirectoryTimeFormat = "20060102T150405Z"

// traceUnavailable is the progress note a trace that could not be opened, or
// could not be closed, reports itself under.
const traceUnavailable = "trace-unavailable"

// startTrace opens the recording a request asked for, and returns it with the
// closer that ends it.
//
// A trace is diagnostic exhaust, never evidence, so nothing about one costs
// the run: a directory that cannot be opened, or one the next snapshot would
// read as source, is reported as a single progress note and the run continues
// untraced. A request that asked for no trace pays nothing at all, because a
// nil recorder is the disabled trace every layer below records into.
func (service Service) startTrace(root string, request cli.Request) (*trace.Recorder, func(report.Report, error)) {
	untraced := func(report.Report, error) {}
	if !request.Trace {
		return nil, untraced
	}
	directory, err := service.traceDirectory(root, request)
	if err != nil {
		service.note(traceUnavailable, err.Error())
		return nil, untraced
	}
	sink, err := trace.NewDirSink(directory, trace.Filesystem{})
	if err != nil {
		service.note(traceUnavailable, err.Error())
		return nil, untraced
	}
	recorder := trace.New(sink, service.Now)
	return recorder, func(result report.Report, runErr error) {
		recorder.RunEnd(string(result.Verdict), runErr)
		if closeErr := sink.Close(); closeErr != nil {
			service.note(traceUnavailable, closeErr.Error())
		}
	}
}

// traceDirectory resolves where a request wants its trace written, defaulting
// to a directory of the repository's own trace root named for the run.
//
// A relative directory resolves against the repository, which is the root
// every other path a request names is relative to.
func (service Service) traceDirectory(root string, request cli.Request) (string, error) {
	directory := strings.TrimSpace(request.TraceDirectory)
	if directory == "" {
		return filepath.Join(root, ".goatest", "trace", service.traceName()), nil
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

// traceName names a default trace directory after the moment the run started
// and the process that started it, which is what keeps two runs of the same
// repository out of each other's recording.
func (service Service) traceName() string {
	processID := os.Getpid
	if service.ProcessID != nil {
		processID = service.ProcessID
	}
	return service.clock()().UTC().Format(traceDirectoryTimeFormat) + "-" + strconv.Itoa(processID())
}

// inspectedAsSource reports whether a trace written to a directory would be
// digested as part of the repository it traces.
//
// A trace grows while the run it records is under way, so a stream the source
// snapshot reads would make the repository change during verification and cost
// the run its evidence. Only the tool's own .goatest directory is exempt,
// because the snapshot never reads it.
func inspectedAsSource(root, directory string) bool {
	relative, err := filepath.Rel(root, directory)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return false
	}
	first, _, _ := strings.Cut(filepath.ToSlash(relative), "/")
	return first != ".goatest"
}
