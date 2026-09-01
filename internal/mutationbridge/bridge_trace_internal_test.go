// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package mutationbridge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"slices"
	"testing"
	"time"

	gomutants "github.com/P4suta/go-mutants"
	"github.com/P4suta/goatest/internal/trace"
)

// traceOrigin fixes the clock of a recording, so that nothing a bridge test
// asserts depends on when the test ran.
var traceOrigin = time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)

// newRecording returns a recording kept in memory, which is how these tests
// read what a command recorded.
func newRecording() (*trace.MemorySink, *trace.Recorder) {
	sink := trace.NewMemorySink(0)
	return sink, trace.New(sink, func() time.Time { return traceOrigin })
}

// recordedExecs returns the exec records of a recording in emission order.
func recordedExecs(sink *trace.MemorySink) []trace.ExecRecord {
	var records []trace.ExecRecord
	for _, event := range sink.Events() {
		if event.Type == trace.TypeExec && event.Exec != nil {
			records = append(records, *event.Exec)
		}
	}
	return records
}

func TestWorkspaceExecRecordsTheCommandItRan(t *testing.T) {
	t.Parallel()
	output := []byte("combined output")
	digest := sha256.Sum256(output)
	engine := &fakeMutationWorkspace{execResult: gomutants.CommandResult{Duration: 1500 * time.Millisecond, Output: output}}
	sink, recorder := newRecording()
	workspace := &Workspace{inner: engine, trace: recorder}
	argv := []string{"go", "test", "./..."}
	if _, err := workspace.Exec(t.Context(), gomutants.Command{
		Argv: argv, Dir: "internal/assure", Timeout: 2 * time.Minute,
		Env: []string{"GOFLAGS=-mod=mod", "AWS_SECRET_ACCESS_KEY=shhh", "GOFLAGS=-mod=mod"},
	}); err != nil {
		t.Fatalf("Exec error = %v", err)
	}
	argv[2] = "mutated"
	records := recordedExecs(sink)
	if len(records) != 1 {
		t.Fatalf("exec records = %+v", records)
	}
	record := records[0]
	if !slices.Equal(record.Argv, []string{"go", "test", "./..."}) || record.Dir != "internal/assure" {
		t.Fatalf("recorded command = %+v", record)
	}
	if !slices.Equal(record.EnvNames, []string{"AWS_SECRET_ACCESS_KEY", "GOFLAGS"}) {
		t.Fatalf("recorded environment = %q", record.EnvNames)
	}
	if record.TimeoutMS != 120_000 || record.DurationMS != 1500 || record.ExitCode != 0 || record.TimedOut || record.Error != "" {
		t.Fatalf("recorded execution = %+v", record)
	}
	if record.OutputBytes != len(output) || record.OutputSHA256 != hex.EncodeToString(digest[:]) {
		t.Fatalf("recorded output digest = %+v", record)
	}
	if record.OutputPath != "" || record.OutputTruncated {
		t.Fatalf("bridge preserved output itself: %+v", record)
	}
}

func TestWorkspaceExecRecordsFailedAndTimedOutCommands(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("exec failed")
	engine := &fakeMutationWorkspace{
		execResult: gomutants.CommandResult{ExitCode: 17, TimedOut: true, Duration: 3 * time.Second},
		execErr:    sentinel,
	}
	sink, recorder := newRecording()
	workspace := &Workspace{inner: engine, trace: recorder}
	if _, err := workspace.Exec(t.Context(), gomutants.Command{Argv: []string{"go", "vet", "./..."}, Timeout: -time.Second}); !errors.Is(err, sentinel) {
		t.Fatalf("Exec error = %v", err)
	}
	records := recordedExecs(sink)
	if len(records) != 1 {
		t.Fatalf("exec records = %+v", records)
	}
	record := records[0]
	if record.ExitCode != 17 || !record.TimedOut || record.DurationMS != 3000 || record.Error != "exec failed" {
		t.Fatalf("recorded failure = %+v", record)
	}
	if record.TimeoutMS != 0 || record.OutputBytes != 0 || record.OutputSHA256 != "" {
		t.Fatalf("recorded failure carries nonsense measurements: %+v", record)
	}
}

func TestWorkspaceExecRecordsOneEventPerCommand(t *testing.T) {
	t.Parallel()
	engine := &fakeMutationWorkspace{}
	sink, recorder := newRecording()
	workspace := &Workspace{inner: engine, trace: recorder}
	for _, argv := range [][]string{{"go", "list"}, {"go", "build"}} {
		if _, err := workspace.Exec(t.Context(), gomutants.Command{Argv: argv}); err != nil {
			t.Fatalf("Exec error = %v", err)
		}
	}
	records := recordedExecs(sink)
	if len(records) != 2 || !slices.Equal(records[0].Argv, []string{"go", "list"}) || !slices.Equal(records[1].Argv, []string{"go", "build"}) {
		t.Fatalf("exec records = %+v", records)
	}
}

func TestOpenCarriesTheRecorderIntoTheWorkspace(t *testing.T) {
	original := openMutationWorkspace
	t.Cleanup(func() { openMutationWorkspace = original })
	engine := &fakeMutationWorkspace{}
	openMutationWorkspace = func(context.Context, string, gomutants.OpenOptions) (mutationWorkspace, error) {
		return engine, nil
	}
	sink, recorder := newRecording()
	workspace, err := Open(t.Context(), "repository", Options{Trace: recorder})
	if err != nil {
		t.Fatalf("Open error = %v", err)
	}
	if workspace.Trace() != recorder {
		t.Fatalf("Trace = %p, want %p", workspace.Trace(), recorder)
	}
	if _, err := workspace.Exec(t.Context(), gomutants.Command{Argv: []string{"go", "list"}}); err != nil {
		t.Fatalf("Exec error = %v", err)
	}
	if records := recordedExecs(sink); len(records) != 1 {
		t.Fatalf("exec records = %+v", records)
	}
}

func TestWorkspaceWithoutRecorderRunsUntraced(t *testing.T) {
	t.Parallel()
	engine := &fakeMutationWorkspace{execResult: gomutants.CommandResult{Output: []byte("combined output")}}
	workspace := &Workspace{inner: engine}
	if workspace.Trace() != nil {
		t.Fatalf("Trace = %p, want nil", workspace.Trace())
	}
	result, err := workspace.Exec(t.Context(), gomutants.Command{Argv: []string{"go", "list"}})
	if err != nil || !slices.Equal(result.Output, []byte("combined output")) {
		t.Fatalf("Exec = (%+v, %v)", result, err)
	}
	var absent *Workspace
	if absent.Trace() != nil {
		t.Fatalf("nil workspace Trace = %p, want nil", absent.Trace())
	}
	if _, err := absent.Exec(t.Context(), gomutants.Command{}); err == nil || err.Error() != "goatest: nil mutation workspace" {
		t.Fatalf("nil workspace Exec error = %v", err)
	}
}
