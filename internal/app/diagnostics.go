// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"github.com/P4suta/goatest/internal/report"
	"github.com/P4suta/goatest/internal/trace"
)

// Layout of a failure diagnostics bundle. A run that failed leaves one
// directory of its own under the tool's own directory, holding the files below.
const (
	diagnosticsDirectoryName       = "diagnostics"
	diagnosticsErrorFileName       = "error.txt"
	diagnosticsEnvironmentFileName = "environment.txt"
	diagnosticsPreservedFileName   = "preserved-paths.txt"
	// diagnosticsTraceFileName is the recording the run kept. It carries the
	// name and the format of a trace directory because it is the same stream,
	// read back from memory instead of from a file.
	diagnosticsTraceFileName = trace.FileName
)

// diagnosticsEnvironmentNamesHeading introduces the names, and says what is
// missing from them, in the file itself rather than only in the documentation
// of it.
const diagnosticsEnvironmentNamesHeading = "environment variable names, values excluded:"

// Progress notes a bundle reports itself under: where it was written, and what
// it could not write.
const (
	diagnosticsWritten     = "diagnostics"
	diagnosticsUnavailable = "diagnostics-unavailable"
)

// Permissions of what a bundle writes. A bundle is diagnostic exhaust a
// developer reads and attaches to a bug report, not a secret store.
const (
	diagnosticsDirectoryPermissions fs.FileMode = 0o755
	diagnosticsFilePermissions      fs.FileMode = 0o644
)

// DiagnosticsFilesystem is the filesystem a failure bundle is written through.
// Its zero value is the os package; a caller fills in only the operation it
// wants to answer for, because the failures a bundle must survive are not
// failures a disk produces on demand.
type DiagnosticsFilesystem struct {
	MkdirAll  func(path string, perm fs.FileMode) error
	WriteFile func(path string, data []byte, perm fs.FileMode) error
}

// resolved returns the hooks with every unset operation filled in from the os
// package.
func (hooks DiagnosticsFilesystem) resolved() DiagnosticsFilesystem {
	if hooks.MkdirAll == nil {
		hooks.MkdirAll = os.MkdirAll
	}
	if hooks.WriteFile == nil {
		hooks.WriteFile = os.WriteFile
	}
	return hooks
}

// diagnosticsFile is one file of a bundle. A file with nothing to say is not
// written: an empty file in a bundle reads as a fact about the run rather than
// as the absence of one.
type diagnosticsFile struct {
	name string
	data []byte
}

// writeDiagnostics writes the bundle of a run that failed: the recording it
// kept, the error that ended it, the environment it could see, and the paths it
// left behind. It is called with the finalized report of that run and the error
// it stopped on, which is never nil.
//
// A bundle is diagnostic exhaust and never evidence. It is written after the run
// has decided everything it decides, it takes no part in the verdict, the error,
// or the report, and what it could not write it says on the progress stream
// instead of in the outcome of the run.
func (service Service) writeDiagnostics(root string, result report.Report, recording traceRecording, runErr error) {
	hooks := service.DiagnosticsFilesystem.resolved()
	directory := filepath.Join(root, ".goatest", diagnosticsDirectoryName, service.diagnosticsName(result))
	if err := hooks.MkdirAll(directory, diagnosticsDirectoryPermissions); err != nil {
		service.note(diagnosticsUnavailable, fmt.Sprintf("create %s: %v", directory, err))
		return
	}
	events := recording.Events()
	stream, encodeErr := diagnosticsTrace(events)
	var failures []error
	if encodeErr != nil {
		failures = append(failures, encodeErr)
	}
	written := 0
	for _, file := range []diagnosticsFile{
		{name: diagnosticsTraceFileName, data: stream},
		{name: diagnosticsErrorFileName, data: diagnosticsError(result, runErr)},
		{name: diagnosticsEnvironmentFileName, data: service.diagnosticsEnvironment(result)},
		{name: diagnosticsPreservedFileName, data: diagnosticsPreservedPaths(recording.directory, events)},
	} {
		if len(file.data) == 0 {
			continue
		}
		if err := hooks.WriteFile(filepath.Join(directory, file.name), file.data, diagnosticsFilePermissions); err != nil {
			failures = append(failures, fmt.Errorf("write %s: %w", file.name, err))
			continue
		}
		written++
	}
	// One note for everything that was lost, because a bundle that reports its
	// own gaps a line at a time is a bundle whose report is skipped.
	if len(failures) != 0 {
		service.note(diagnosticsUnavailable, errors.Join(failures...).Error())
	}
	if written != 0 {
		service.note(diagnosticsWritten, "written to "+directory)
	}
}

// diagnosticsName names the bundle of a run after the run itself.
//
// A run that stopped before it had an identity is the failure a bundle is most
// needed for, so the moment and the process name that one instead — the name a
// recording of the same run takes, and one a test fixes through the same
// injected clock.
func (service Service) diagnosticsName(result report.Report) string {
	if safeRunID(result.RunID) {
		return result.RunID
	}
	return service.traceName()
}

// diagnosticsTrace dumps the events of a recording as the JSON Lines stream a
// trace directory holds, so that everything reading a trace reads this too.
//
// A recording a run traced to a directory answers with no events, and the
// bundle names that directory rather than copying it.
func diagnosticsTrace(events []trace.Event) ([]byte, error) {
	if len(events) == 0 {
		return nil, nil
	}
	var stream bytes.Buffer
	var failures []error
	for _, event := range events {
		encoded, err := json.Marshal(event)
		if err != nil {
			failures = append(failures, fmt.Errorf("encode trace event %d: %w", event.Seq, err))
			continue
		}
		stream.Write(encoded)
		stream.WriteByte('\n')
	}
	return stream.Bytes(), errors.Join(failures...)
}

// diagnosticsError writes the error that ended the run: the message as the run
// reported it, and then the chain behind it, because the message a wrapper
// shows is rarely the one that explains the failure.
func diagnosticsError(result report.Report, runErr error) []byte {
	var text strings.Builder
	fmt.Fprintf(&text, "run: %s\n", report.LineText(result.RunID))
	fmt.Fprintf(&text, "verdict: %s\n", report.LineText(string(result.Verdict)))
	fmt.Fprintf(&text, "\n%+v\n\nerror chain:\n", runErr)
	writeErrorChain(&text, runErr, 1)
	return []byte(text.String())
}

// writeErrorChain writes one line per error behind an error, named by its type
// and indented by how far behind the first one it is. A joined error branches,
// and every branch is followed, because a run that failed while cleaning up
// after another failure reports both and neither explains the other.
//
// Each message is escaped onto its own line, so that an error carrying the
// output of a command stays one entry of a chain a reader can count.
func writeErrorChain(text *strings.Builder, err error, depth int) {
	for ; err != nil; depth++ {
		fmt.Fprintf(text, "%s%T: %s\n", strings.Repeat("  ", depth), err, report.LineText(err.Error()))
		switch unwrapped := err.(type) {
		case interface{ Unwrap() []error }:
			for _, branch := range unwrapped.Unwrap() {
				writeErrorChain(text, branch, depth+1)
			}
			return
		case interface{ Unwrap() error }:
			err = unwrapped.Unwrap()
		default:
			return
		}
	}
}

// diagnosticsEnvironment writes what the run ran with: the toolchain that
// decided the result, and the environment its commands could see.
//
// The environment is named and never quoted, exactly as a trace names it. That
// is what makes a bundle safe to attach to a bug report from a machine holding
// real credentials.
func (service Service) diagnosticsEnvironment(result report.Report) []byte {
	goBinary := service.GoBinary
	if goBinary == "" {
		goBinary = "go"
	}
	var text strings.Builder
	for _, field := range []struct{ name, value string }{
		{"goatest", result.Toolchain.Goatest},
		{"go", result.Toolchain.Go},
		{"go-mutants", result.Toolchain.GoMutants},
		{"runtime", runtime.Version()},
		{"os", result.Toolchain.OS},
		{"arch", result.Toolchain.Arch},
		{"go-binary", goBinary},
		{"temp-directory", service.TempDirectory},
	} {
		if field.value == "" {
			continue
		}
		fmt.Fprintf(&text, "%s: %s\n", field.name, report.LineText(field.value))
	}
	text.WriteString("\n" + diagnosticsEnvironmentNamesHeading + "\n")
	for _, name := range environmentNames(service.environment()) {
		text.WriteString(name + "\n")
	}
	return []byte(text.String())
}

// environment is what the run's commands could see: the environment a caller
// injected, or the one goatest itself was started with.
func (service Service) environment() []string {
	if service.Environment != nil {
		return service.Environment
	}
	return os.Environ()
}

// environmentNames reduces environment entries to their names, sorted and
// deduplicated. An entry with no name is left out entirely, because the only
// other half of an entry is a value.
func environmentNames(environment []string) []string {
	names := make([]string, 0, len(environment))
	for _, entry := range environment {
		name, _, _ := strings.Cut(entry, "=")
		if name == "" {
			continue
		}
		names = append(names, report.LineText(name))
	}
	slices.Sort(names)
	return slices.Compact(names)
}

// diagnosticsPreservedPaths lists what the run left on the disk: the directory
// it recorded into where it recorded into one, and every artifact its recording
// named, which is where a run that was asked to keep its temporary directories
// reports them.
//
// A run that traced to a directory recorded its artifacts there, in full, and
// the bundle names that directory rather than repeating a part of it.
func diagnosticsPreservedPaths(directory string, events []trace.Event) []byte {
	var text strings.Builder
	text.WriteString("kind\tpath\n")
	preserved := 0
	if directory != "" {
		fmt.Fprintf(&text, "trace\t%s\n", report.LineText(directory))
		preserved++
	}
	for _, event := range events {
		if event.Type != trace.TypeArtifact || event.Artifact == nil {
			continue
		}
		fmt.Fprintf(&text, "%s\t%s\n", report.LineText(event.Artifact.Kind), report.LineText(event.Artifact.Path))
		preserved++
	}
	if preserved == 0 {
		text.WriteString("# this run left nothing behind\n")
	}
	return []byte(text.String())
}
