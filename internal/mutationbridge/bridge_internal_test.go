// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package mutationbridge

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	gomutants "github.com/P4suta/go-mutants"
	"github.com/P4suta/goatest/internal/trace"
)

func TestOpenMapsOptionsWithoutAliasingAndWrapsFailure(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		original := openMutationWorkspace
		t.Cleanup(func() { openMutationWorkspace = original })
		engine := &fakeMutationWorkspace{}
		var gotRoot string
		var gotOptions gomutants.OpenOptions
		openMutationWorkspace = func(_ context.Context, root string, options gomutants.OpenOptions) (mutationWorkspace, error) {
			gotRoot, gotOptions = root, options
			return engine, nil
		}
		environment := []string{"A=1", "B=2"}
		exclusions := []string{"reports", "dist"}
		workspace, err := Open(context.Background(), "repository", Options{
			GoBinary: "custom-go", TempDirectory: "temporary", ReportDirectory: "reports",
			SnapshotExclude: exclusions, Environment: environment,
		})
		if err != nil || workspace == nil || workspace.inner != engine {
			t.Fatalf("Open = (%+v, %v)", workspace, err)
		}
		environment[0] = "MUTATED=1"
		exclusions[0] = "mutated"
		if gotRoot != "repository" || gotOptions.GoBinary != "custom-go" || gotOptions.TempDirectory != "temporary" ||
			gotOptions.ReportDirectory != "reports" || !slices.Equal(gotOptions.SnapshotExclude, []string{"reports", "dist"}) ||
			!slices.Equal(gotOptions.Env, []string{"A=1", "B=2"}) {
			t.Fatalf("Open arguments = %q %+v", gotRoot, gotOptions)
		}
	})
	t.Run("failure", func(t *testing.T) {
		original := openMutationWorkspace
		t.Cleanup(func() { openMutationWorkspace = original })
		sentinel := errors.New("open failed")
		openMutationWorkspace = func(context.Context, string, gomutants.OpenOptions) (mutationWorkspace, error) {
			return nil, sentinel
		}
		workspace, err := Open(context.Background(), "repository", Options{})
		if workspace != nil || !errors.Is(err, sentinel) || err.Error() != "goatest: open mutation workspace: open failed" {
			t.Fatalf("Open = (%+v, %v)", workspace, err)
		}
	})
}

func TestWorkspaceNilMethodsFailClosed(t *testing.T) {
	t.Parallel()
	for _, workspace := range []*Workspace{nil, {}} {
		if version := workspace.ToolchainVersion(); version != "" {
			t.Errorf("ToolchainVersion = %q, want empty", version)
		}
		if _, err := workspace.Exec(context.Background(), gomutants.Command{}); err == nil || err.Error() != "goatest: nil mutation workspace" {
			t.Errorf("Exec error = %v", err)
		}
		if _, err := workspace.Prepare(context.Background(), PrepareOptions{Contract: "standard-v1"}); err == nil || err.Error() != "goatest: nil mutation workspace" {
			t.Errorf("Prepare error = %v", err)
		}
		if err := workspace.Close(); err != nil {
			t.Errorf("Close error = %v", err)
		}
	}
}

func TestWorkspaceToolchainVersionComesFromTheOpenedEngine(t *testing.T) {
	workspace := &Workspace{inner: &fakeMutationWorkspace{}}
	if got := workspace.ToolchainVersion(); got != "go version go1.26.6 test/arch" {
		t.Fatalf("ToolchainVersion = %q", got)
	}
}

func TestWorkspaceExecForwardsResultAndError(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("exec failed")
	want := gomutants.CommandResult{ExitCode: 17, TimedOut: true, Duration: 2 * time.Second, Output: []byte("combined output")}
	engine := &fakeMutationWorkspace{execResult: want, execErr: sentinel}
	workspace := &Workspace{inner: engine}
	command := gomutants.Command{Argv: []string{"go", "test"}, Dir: "pkg", Env: []string{"A=1"}, Timeout: time.Second}
	got, err := workspace.Exec(context.Background(), command)
	if !errors.Is(err, sentinel) || got.ExitCode != want.ExitCode || got.TimedOut != want.TimedOut || got.Duration != want.Duration || !slices.Equal(got.Output, want.Output) {
		t.Fatalf("Exec = (%+v, %v)", got, err)
	}
	if !slices.Equal(engine.command.Argv, command.Argv) || engine.command.Dir != command.Dir || !slices.Equal(engine.command.Env, command.Env) || engine.command.Timeout != command.Timeout {
		t.Fatalf("forwarded command = %+v", engine.command)
	}
}

func TestWorkspacePrepareMapsAndClonesEveryOption(t *testing.T) {
	t.Parallel()
	session := &gomutants.Session{}
	engine := &fakeMutationWorkspace{session: session}
	workspace := &Workspace{inner: engine}
	options := PrepareOptions{
		Contract: "deep-v1", Operators: []string{"comparison"}, Include: []string{"internal/**"},
		Exclude: []string{"generated/**"}, DiscoveryPackages: []string{"./internal/codec"},
		Packages: []string{"./internal/..."}, ProbeCoverPackages: []string{"fixture.example/module/..."}, Jobs: 3,
		BuildTimeout: time.Minute, MutantTimeout: 2 * time.Second,
		VerifyArgv: []string{"go", "test", "./..."}, VerifyEnv: []string{"A=1"}, VerifyTimeout: 3 * time.Minute,
	}
	got, err := workspace.Prepare(context.Background(), options)
	if err != nil || got != session {
		t.Fatalf("Prepare = (%p, %v)", got, err)
	}
	options.Operators[0], options.Include[0], options.Exclude[0], options.DiscoveryPackages[0], options.Packages[0], options.ProbeCoverPackages[0] = "mutated", "mutated", "mutated", "mutated", "mutated", "mutated"
	options.VerifyArgv[0], options.VerifyEnv[0] = "mutated", "mutated"
	prepared := engine.prepare
	if prepared.Profile != "all" || !slices.Equal(prepared.Operators, []string{"comparison"}) || !slices.Equal(prepared.Include, []string{"internal/**"}) || !slices.Equal(prepared.Exclude, []string{"generated/**"}) || !slices.Equal(prepared.DiscoveryPackages, []string{"./internal/codec"}) || !slices.Equal(prepared.Packages, []string{"./internal/..."}) || !slices.Equal(prepared.ProbeCoverPackages, []string{"fixture.example/module/..."}) || prepared.Jobs != 3 || prepared.BuildTimeout != time.Minute || prepared.MutantTimeout != 2*time.Second || !slices.Equal(prepared.Verify.Argv, []string{"go", "test", "./..."}) || !slices.Equal(prepared.Verify.Env, []string{"A=1"}) || prepared.Verify.Timeout != 3*time.Minute || prepared.SkipVerify {
		t.Fatalf("Prepare options = %+v", prepared)
	}
}

func TestPrepareForwardsSessionFlags(t *testing.T) {
	t.Parallel()
	for _, options := range []PrepareOptions{
		{Contract: "standard-v1"},
		{Contract: "standard-v1", Probe: true},
		{Contract: "standard-v1", SkipVerify: true},
		{Contract: "standard-v1", Probe: true, SkipVerify: true},
	} {
		engine := &fakeMutationWorkspace{session: &gomutants.Session{}}
		workspace := &Workspace{inner: engine}
		if _, err := workspace.Prepare(context.Background(), options); err != nil {
			t.Fatalf("Prepare(%+v) error = %v", options, err)
		}
		if engine.prepare.Probe != options.Probe || engine.prepare.SkipVerify != options.SkipVerify {
			t.Errorf("prepared flags = probe:%t skip-verify:%t, want probe:%t skip-verify:%t", engine.prepare.Probe, engine.prepare.SkipVerify, options.Probe, options.SkipVerify)
		}
	}
}

func TestWorkspacePrepareRecordsEnginePreparation(t *testing.T) {
	t.Parallel()
	sink := trace.NewMemorySink(0)
	recorder := trace.New(sink, func() time.Time { return time.Time{} })
	engine := &fakeMutationWorkspace{session: &gomutants.Session{}}
	workspace := &Workspace{inner: engine, trace: recorder}
	if _, err := workspace.Prepare(context.Background(), PrepareOptions{Contract: "standard-v1"}); err != nil {
		t.Fatal(err)
	}
	if engine.prepare.Trace == nil {
		t.Fatal("prepare trace was not forwarded")
	}
	engine.prepare.Trace(gomutants.PrepareEvent{
		Phase: gomutants.PreparePhaseBinaryBuild, State: gomutants.PrepareEventFinished,
		Result: gomutants.PreparePhaseSucceeded, Duration: 2 * time.Second,
	})
	var record *trace.PrepareRecord
	for _, event := range sink.Events() {
		if event.Type == trace.TypePrepare {
			record = event.Prepare
		}
	}
	if record == nil {
		t.Fatalf("events = %+v", sink.Events())
	}
	if record.Phase != trace.PreparePhaseBinaryBuild || record.State != trace.PrepareStateFinished ||
		record.Result != trace.PrepareResultSucceeded || record.DurationMS == nil || *record.DurationMS != 2_000 {
		t.Fatalf("prepare record = %+v", record)
	}
}

func TestWorkspacePrepareLeavesEngineTraceNilWithoutRecorder(t *testing.T) {
	t.Parallel()
	engine := &fakeMutationWorkspace{session: &gomutants.Session{}}
	workspace := &Workspace{inner: engine}
	if _, err := workspace.Prepare(context.Background(), PrepareOptions{Contract: "standard-v1"}); err != nil {
		t.Fatal(err)
	}
	if engine.prepare.Trace != nil {
		t.Fatal("prepare trace is non-nil")
	}
}

func TestWorkspacePrepareRejectsContractBeforeEngineAndWrapsEngineFailure(t *testing.T) {
	engine := &fakeMutationWorkspace{}
	workspace := &Workspace{inner: engine}
	if _, err := workspace.Prepare(context.Background(), PrepareOptions{Contract: "unknown"}); err == nil || err.Error() != `goatest: mutation contract "unknown" is unknown` || engine.prepareCalls != 0 {
		t.Fatalf("unknown Prepare error = %v, calls = %d", err, engine.prepareCalls)
	}
	sentinel := errors.New("prepare failed")
	engine.prepareErr = sentinel
	session, err := workspace.Prepare(context.Background(), PrepareOptions{Contract: "standard-v1"})
	if session != nil || !errors.Is(err, sentinel) || err.Error() != "goatest: prepare mutation session: prepare failed" {
		t.Fatalf("Prepare = (%+v, %v)", session, err)
	}
}

func TestWorkspaceCloseIsIdempotentAndClearsEngineOnFailure(t *testing.T) {
	sentinel := errors.New("close failed")
	engine := &fakeMutationWorkspace{closeErr: sentinel}
	workspace := &Workspace{inner: engine}
	if err := workspace.Close(); !errors.Is(err, sentinel) {
		t.Fatalf("Close error = %v", err)
	}
	if workspace.inner != nil || engine.closeCalls != 1 {
		t.Fatalf("Close state = inner %v calls %d", workspace.inner, engine.closeCalls)
	}
	if err := workspace.Close(); err != nil || engine.closeCalls != 1 {
		t.Fatalf("second Close = %v, calls %d", err, engine.closeCalls)
	}
}

type fakeMutationWorkspace struct {
	swept        gomutants.SweepResult
	preserved    []string
	command      gomutants.Command
	execResult   gomutants.CommandResult
	execErr      error
	prepare      gomutants.PrepareOptions
	prepareCalls int
	session      *gomutants.Session
	prepareErr   error
	closeCalls   int
	closeErr     error
}

func (workspace *fakeMutationWorkspace) Exec(_ context.Context, command gomutants.Command) (gomutants.CommandResult, error) {
	workspace.command = command
	return workspace.execResult, workspace.execErr
}

func (workspace *fakeMutationWorkspace) Prepare(_ context.Context, options gomutants.PrepareOptions) (*gomutants.Session, error) {
	workspace.prepareCalls++
	workspace.prepare = options
	return workspace.session, workspace.prepareErr
}

func (workspace *fakeMutationWorkspace) ToolchainVersion() string {
	return "go version go1.26.6 test/arch"
}

func (workspace *fakeMutationWorkspace) Close() error {
	workspace.closeCalls++
	return workspace.closeErr
}

func (workspace *fakeMutationWorkspace) Swept() gomutants.SweepResult { return workspace.swept }

func (workspace *fakeMutationWorkspace) Preserved() []string { return workspace.preserved }
