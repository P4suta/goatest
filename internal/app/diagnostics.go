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

	"github.com/P4suta/goatest/internal/filemode"
	"github.com/P4suta/goatest/internal/report"
	"github.com/P4suta/goatest/internal/trace"
)

const (
	diagnosticsDirectoryName       = "diagnostics"
	diagnosticsErrorFileName       = "error.txt"
	diagnosticsEnvironmentFileName = "environment.txt"
	diagnosticsPreservedFileName   = "preserved-paths.txt"

	diagnosticsTraceFileName = trace.FileName
)

const diagnosticsEnvironmentNamesHeading = "environment variable names, values excluded:"

const (
	diagnosticsWritten     = "diagnostics"
	diagnosticsUnavailable = "diagnostics-unavailable"
)

const (
	diagnosticsDirectoryPermissions fs.FileMode = filemode.ReadableDirectory
	diagnosticsFilePermissions      fs.FileMode = filemode.ReadableFile
)

type DiagnosticsFilesystem struct {
	MkdirAll  func(path string, perm fs.FileMode) error
	WriteFile func(path string, data []byte, perm fs.FileMode) error
}

func (hooks DiagnosticsFilesystem) resolved() DiagnosticsFilesystem {
	if hooks.MkdirAll == nil {
		hooks.MkdirAll = os.MkdirAll
	}
	if hooks.WriteFile == nil {
		hooks.WriteFile = os.WriteFile
	}
	return hooks
}

type diagnosticsFile struct {
	name string
	data []byte
}

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

	if len(failures) != 0 {
		service.note(diagnosticsUnavailable, errors.Join(failures...).Error())
	}
	if written != 0 {
		service.note(diagnosticsWritten, "written to "+directory)
	}
}

func (service Service) diagnosticsName(result report.Report) string {
	if safeRunID(result.RunID) {
		return result.RunID
	}
	return service.traceName()
}

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

func diagnosticsError(result report.Report, runErr error) []byte {
	var text strings.Builder
	fmt.Fprintf(&text, "run: %s\n", report.LineText(result.RunID))
	fmt.Fprintf(&text, "verdict: %s\n", report.LineText(string(result.Verdict)))
	fmt.Fprintf(&text, "\n%+v\n\nerror chain:\n", runErr)
	writeErrorChain(&text, runErr, 1)
	return []byte(text.String())
}

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

func (service Service) environment() []string {
	if service.Environment != nil {
		return service.Environment
	}
	return os.Environ()
}

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
