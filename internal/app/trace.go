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

// traceDirectoryTimeFormat names a run's trace directory after the moment the
// run started, in the compact UTC shape run identities already use.
const traceDirectoryTimeFormat = "20060102T150405Z"

// traceUnavailable is the progress note a trace that could not be opened, or
// could not be closed, reports itself under.
const traceUnavailable = "trace-unavailable"

// Terminal verdicts of a recording whose run reached none of its own. They are
// trace vocabulary rather than report verdicts, because a run that ends this
// way writes no report: an interrupted run is abandoned before one is
// finalized, and a run that returns neither verdict nor error reached nothing
// a report could carry.
const (
	traceVerdictInterrupted = "INTERRUPTED"
	traceVerdictUnknown     = "UNKNOWN"
)

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
	sink, err := trace.NewDirSink(directory, service.traceName(), trace.Filesystem{})
	if err != nil {
		service.note(traceUnavailable, err.Error())
		return nil, untraced
	}
	recorder := trace.New(sink, service.Now)
	return recorder, func(result report.Report, runErr error) {
		recorder.RunEnd(traceVerdict(result, runErr), runErr)
		if closeErr := sink.Close(); closeErr != nil {
			service.note(traceUnavailable, closeErr.Error())
		}
	}
}

// traceVerdict names how a run ended for the recording that is closing.
//
// The runner's own verdict is the answer wherever it reached one. Where it did
// not, the error it stopped on is: a cancelled or expired context ended the run
// from outside, anything else ended it as an error, and a run that returned
// neither is an outcome a reader should see named rather than left blank. The
// service settles this rather than the recorder, because the vocabulary of a
// verdict belongs to the report layer that owns the rest of it.
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

// traceDirectory resolves the trace root a request wants its recordings
// collected in, defaulting to the repository's own. The run records into a
// directory of its own underneath it, which is what lets one directory serve
// every run a developer traces into it.
//
// A relative directory resolves against the repository, which is the root
// every other path a request names is relative to.
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

// traceName names the directory of one recording after the moment the run
// started and the process that started it, which is what keeps two runs
// tracing into one trace root out of each other's recording.
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
//
// A path is judged by its name and by where it lands, and either verdict is
// enough to refuse it. Comparing names alone would let a symbolic link outside
// the repository carry a stream into it, and comparing targets alone would
// accept a name inside the repository that points its way out — which the rest
// of goatest refuses outright rather than follows.
func inspectedAsSource(root, directory string) bool {
	return underRepository(root, directory) ||
		underRepository(existingPathOf(root), existingPathOf(directory))
}

// underRepository reports whether a path lies within the repository and outside
// the .goatest directory the snapshot never reads.
func underRepository(root, directory string) bool {
	relative, err := filepath.Rel(root, directory)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return false
	}
	first, _, _ := strings.Cut(filepath.ToSlash(relative), "/")
	return first != ".goatest"
}

// existingPathOf resolves the symbolic links of the part of a path that
// exists, keeping the part that does not as it was named. A trace directory is
// created by the sink after this check, so the deepest existing ancestor is as
// far as a name can be resolved, and that ancestor is what decides where the
// directory will land.
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
