// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeTrace writes one recording into a temporary directory and returns its
// path.
func writeTrace(t *testing.T, stream string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "trace.jsonl")
	if err := os.WriteFile(path, []byte(stream), 0o644); err != nil {
		t.Fatalf("write the trace: %v", err)
	}
	return path
}

// soundRecording is a recording of a run whose one killer ran the block its
// mutant sits in: the shape every routing layer must keep passing.
func soundRecording(t *testing.T) (string, string) {
	t.Helper()
	stream := recordedRun(t, []string{killerTarget},
		blockRoute(2, firstMutant, 11, 4, killerTarget),
		killedBy(3, firstMutant, firstDisplay, killerTarget),
	)
	return writeTrace(t, stream), writeProfiles(t, map[string][]string{
		killerTarget: {ran(10, 2, 12, 16), linked(20, 2, 24, 3)},
	})
}

func TestRunAuditsTheNamedRecording(t *testing.T) {
	t.Parallel()
	tracePath, profiles := soundRecording(t)

	var stdout, stderr bytes.Buffer
	if code := run([]string{"-module", fixtureModule, tracePath, profiles}, &stdout, &stderr); code != exitSuccess {
		t.Fatalf("run exited %d, want %d; stderr: %s", code, exitSuccess, stderr.String())
	}
	if stderr.Len() != 0 {
		t.Errorf("run wrote %q to stderr, want nothing", stderr.String())
	}
	got := stdout.String()
	if !strings.HasPrefix(got, "trace: "+tracePath+"\n") {
		t.Errorf("the audit opens with %q, want the recording it read", firstLine(got))
	}
	for _, want := range []string{"profiles: " + profiles, "module: " + fixtureModule, "kill pairs audited"} {
		if !strings.Contains(got, want) {
			t.Errorf("the audit does not say %q:\n%s", want, got)
		}
	}
	if !strings.HasSuffix(got, "\n") {
		t.Error("the audit does not end with a newline")
	}
}

func TestRunReportsUsageWithoutTwoArguments(t *testing.T) {
	t.Parallel()
	for _, arguments := range [][]string{nil, {"trace.jsonl"}, {"trace.jsonl", "profiles", "extra"}, {"-unknown"}} {
		var stdout, stderr bytes.Buffer
		if code := run(arguments, &stdout, &stderr); code != exitUsage {
			t.Errorf("run(%q) exited %d, want %d", arguments, code, exitUsage)
		}
		if stdout.Len() != 0 {
			t.Errorf("run(%q) wrote %q to stdout, want nothing", arguments, stdout.String())
		}
		if !strings.Contains(stderr.String(), "usage:") {
			t.Errorf("run(%q) wrote %q to stderr, want the usage", arguments, stderr.String())
		}
	}
}

func TestRunReportsATraceItCannotRead(t *testing.T) {
	t.Parallel()
	_, profiles := soundRecording(t)
	missing := filepath.Join(t.TempDir(), "absent.jsonl")

	var stdout, stderr bytes.Buffer
	if code := run([]string{"-module", fixtureModule, missing, profiles}, &stdout, &stderr); code != exitFailure {
		t.Fatalf("run exited %d, want %d", code, exitFailure)
	}
	if stdout.Len() != 0 {
		t.Errorf("run wrote %q to stdout, want nothing", stdout.String())
	}
	if !strings.Contains(stderr.String(), missing) {
		t.Errorf("run wrote %q to stderr, want the path it could not read", stderr.String())
	}
}

func TestRunReportsProfilesItCannotRead(t *testing.T) {
	t.Parallel()
	tracePath, _ := soundRecording(t)
	missing := filepath.Join(t.TempDir(), "absent")

	var stdout, stderr bytes.Buffer
	if code := run([]string{"-module", fixtureModule, tracePath, missing}, &stdout, &stderr); code != exitFailure {
		t.Fatalf("run exited %d, want %d", code, exitFailure)
	}
	if stdout.Len() != 0 {
		t.Errorf("run wrote %q to stdout, want nothing", stdout.String())
	}
	if !strings.Contains(stderr.String(), missing) {
		t.Errorf("run wrote %q to stderr, want the directory it could not read", stderr.String())
	}
}

func TestRunReportsARecordingItCannotParse(t *testing.T) {
	t.Parallel()
	_, profiles := soundRecording(t)
	broken := writeTrace(t, "{\"seq\":1,\"type\":\"route\"\n{\"seq\":2}\n")

	var stdout, stderr bytes.Buffer
	if code := run([]string{"-module", fixtureModule, broken, profiles}, &stdout, &stderr); code != exitFailure {
		t.Fatalf("run exited %d, want %d", code, exitFailure)
	}
	if stdout.Len() != 0 {
		t.Errorf("run wrote %q to stdout, want nothing", stdout.String())
	}
	if !strings.Contains(stderr.String(), broken) {
		t.Errorf("run wrote %q to stderr, want the recording it refused", stderr.String())
	}
}

func TestRunReadsTheModuleFromGoModAndLetsTheFlagOverrideIt(t *testing.T) {
	// The default module path comes from ./go.mod, which is the working
	// directory of the process, so this test cannot run in parallel with one
	// that depends on the working directory.
	tracePath, profiles := soundRecording(t)
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "go.mod"),
		[]byte("module "+fixtureModule+"\n\ngo 1.26.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(directory)

	var stdout, stderr bytes.Buffer
	if code := run([]string{tracePath, profiles}, &stdout, &stderr); code != exitSuccess {
		t.Fatalf("run exited %d, want %d; stderr: %s", code, exitSuccess, stderr.String())
	}
	if !strings.Contains(stdout.String(), "module: "+fixtureModule) {
		t.Errorf("the audit does not name the module it read from go.mod:\n%s", stdout.String())
	}

	// The flag decides instead when it is given, which the profiles of
	// another module prove by being refused.
	const other = "example.com/other"
	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"-module", other, tracePath, profiles}, &stdout, &stderr); code != exitFailure {
		t.Fatalf("run exited %d with an overridden module, want %d", code, exitFailure)
	}
	if !strings.Contains(stderr.String(), other) {
		t.Errorf("run wrote %q to stderr, want the module the flag named", stderr.String())
	}
}

func TestRunReportsAGoModItCannotRead(t *testing.T) {
	// See TestRunReadsTheModuleFromGoModAndLetsTheFlagOverrideIt: the working
	// directory is the process's, so this test runs on its own too.
	tracePath, profiles := soundRecording(t)
	t.Chdir(t.TempDir())

	var stdout, stderr bytes.Buffer
	if code := run([]string{tracePath, profiles}, &stdout, &stderr); code != exitFailure {
		t.Fatalf("run exited %d without a go.mod, want %d", code, exitFailure)
	}
	if stdout.Len() != 0 {
		t.Errorf("run wrote %q to stdout, want nothing", stdout.String())
	}
	if !strings.Contains(stderr.String(), goModFile) {
		t.Errorf("run wrote %q to stderr, want the file it could not read", stderr.String())
	}
}

func TestRunExitsOneOnAViolation(t *testing.T) {
	t.Parallel()
	// A violation is the finding this tool exists for, so it is reported on
	// stdout and refused with an exit code a gate can read.
	stream := recordedRun(t, []string{killerTarget},
		blockRoute(2, firstMutant, 21, 4, killerTarget),
		killedBy(3, firstMutant, firstDisplay, killerTarget),
	)
	tracePath := writeTrace(t, stream)
	profiles := writeProfiles(t, map[string][]string{
		killerTarget: {ran(10, 2, 12, 16), linked(20, 2, 24, 3)},
	})

	var stdout, stderr bytes.Buffer
	if code := run([]string{"-module", fixtureModule, tracePath, profiles}, &stdout, &stderr); code != exitFailure {
		t.Fatalf("run exited %d on a violation, want %d", code, exitFailure)
	}
	if stderr.Len() != 0 {
		t.Errorf("run wrote %q to stderr, want the violation on stdout", stderr.String())
	}
	for _, want := range []string{firstDisplay, killerTarget, reachLayerName, whyOutsideCoveredBlocks} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("the audit does not report %q:\n%s", want, stdout.String())
		}
	}
}

// soundCatalog writes a catalog for the sound recording: its one mutant, with
// a proof over a body no profile of the run instrumented. The layer refuses to
// discharge anything from a body the toolchain never measured, so the sound
// recording stays sound with the layer in the audit.
func soundCatalog(t *testing.T) string {
	t.Helper()
	return writeCatalog(t, `{"document_type": "go-mutants/catalog", "schema_version": 1, "mutants": [
	  {"id": "`+firstMutant+`", "path": "`+subjectPath+`", "line": 11, "column": 4,
	   "branch": {"direction": "decreasing", "body_start_line": 30, "body_start_column": 2,
	              "body_end_line": 32, "body_end_column": 3}}]}`)
}

func TestRunAuditsTheBranchLayerWhenACatalogIsGiven(t *testing.T) {
	t.Parallel()
	tracePath, profiles := soundRecording(t)

	var stdout, stderr bytes.Buffer
	code := run([]string{"-module", fixtureModule, "-catalog", soundCatalog(t), tracePath, profiles}, &stdout, &stderr)
	if code != exitSuccess {
		t.Fatalf("run exited %d, want %d; stderr: %s", code, exitSuccess, stderr.String())
	}
	got := stdout.String()
	for _, want := range []string{branchLayerName, branchDischargeHeading, "routes with a branch proof"} {
		if !strings.Contains(got, want) {
			t.Errorf("the audit does not say %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, whyBranchNotAudited) {
		t.Errorf("a run given a catalog says the layer was not audited:\n%s", got)
	}
}

func TestRunSaysTheBranchLayerWasNotAuditedWithoutACatalog(t *testing.T) {
	t.Parallel()
	// A missing row and a row of zeroes read the same to anyone skimming, so
	// the report says which of the two it is and the exit code stays what the
	// audited layers decided.
	tracePath, profiles := soundRecording(t)

	var stdout, stderr bytes.Buffer
	if code := run([]string{"-module", fixtureModule, tracePath, profiles}, &stdout, &stderr); code != exitSuccess {
		t.Fatalf("run exited %d, want %d; stderr: %s", code, exitSuccess, stderr.String())
	}
	got := stdout.String()
	if !strings.Contains(got, whyBranchNotAudited) {
		t.Errorf("the audit does not say the branch layer went unaudited:\n%s", got)
	}
	if strings.Contains(got, branchDischargeHeading) {
		t.Errorf("an unaudited layer reports what it would have saved:\n%s", got)
	}
}

func TestRunReportsACatalogItCannotRead(t *testing.T) {
	t.Parallel()
	tracePath, profiles := soundRecording(t)
	missing := filepath.Join(t.TempDir(), "absent.json")

	var stdout, stderr bytes.Buffer
	code := run([]string{"-module", fixtureModule, "-catalog", missing, tracePath, profiles}, &stdout, &stderr)
	if code != exitFailure {
		t.Fatalf("run exited %d, want %d", code, exitFailure)
	}
	if stdout.Len() != 0 {
		t.Errorf("run wrote %q to stdout, want nothing", stdout.String())
	}
	if !strings.Contains(stderr.String(), missing) {
		t.Errorf("run wrote %q to stderr, want the catalog it could not read", stderr.String())
	}
}

func TestRunRefusesACatalogItCannotBeSureOf(t *testing.T) {
	t.Parallel()
	// A document of another kind or another schema may name the same fields
	// and mean something else by them, and an audit that read one anyway would
	// print a soundness result it has no evidence for.
	tracePath, profiles := soundRecording(t)
	catalog := writeCatalog(t, `{"document_type": "go-mutants/inventory", "schema_version": 7, "mutants": []}`)

	var stdout, stderr bytes.Buffer
	code := run([]string{"-module", fixtureModule, "-catalog", catalog, tracePath, profiles}, &stdout, &stderr)
	if code != exitFailure {
		t.Fatalf("run exited %d, want %d", code, exitFailure)
	}
	if stdout.Len() != 0 {
		t.Errorf("run wrote %q to stdout, want nothing", stdout.String())
	}
	for _, want := range []string{catalog, "go-mutants/inventory", "7", catalogDocumentType} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("run wrote %q to stderr, want it to name %q", stderr.String(), want)
		}
	}
}

func TestModuleFromGoModReadsTheDirectiveAlone(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		content string
		want    string
	}{
		{name: "the directive a go.mod opens with", content: "module example.com/audited\n\ngo 1.26.0\n", want: "example.com/audited"},
		{name: "a quoted path", content: "module \"example.com/audited\"\n", want: "example.com/audited"},
		{name: "a trailing comment", content: "module example.com/audited // the module\n", want: "example.com/audited"},
		{name: "an indented directive behind a comment", content: "// a module\n\n\tmodule\texample.com/audited\n", want: "example.com/audited"},
		{name: "a word that only opens with module", content: "module example.com/audited\nmodulepath other\n", want: "example.com/audited"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), goModFile)
			if err := os.WriteFile(path, []byte(testCase.content), 0o644); err != nil {
				t.Fatal(err)
			}
			got, err := moduleFromGoMod(path)
			if err != nil {
				t.Fatalf("read the module of %q: %v", testCase.content, err)
			}
			if got != testCase.want {
				t.Errorf("moduleFromGoMod read %q, want %q", got, testCase.want)
			}
		})
	}
}

func TestModuleFromGoModReportsAFileThatNamesNoModule(t *testing.T) {
	t.Parallel()
	for _, content := range []string{"go 1.26.0\n", "// module example.com/commented\n", "module\n"} {
		path := filepath.Join(t.TempDir(), goModFile)
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := moduleFromGoMod(path)
		if err == nil {
			t.Errorf("moduleFromGoMod accepted %q, which names no module", content)
			continue
		}
		if !strings.Contains(err.Error(), path) {
			t.Errorf("the error is %q, want it to name the file it read", err)
		}
	}
}

// firstLine is the opening line of an audit, which is what a failure about the
// header should print rather than the whole report.
func firstLine(text string) string {
	line, _, _ := strings.Cut(text, "\n")
	return line
}
