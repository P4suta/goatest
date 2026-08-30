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
		workspace, err := Open(context.Background(), "repository", Options{
			GoBinary: "custom-go", TempDirectory: "temporary", ReportDirectory: "reports", Environment: environment,
		})
		if err != nil || workspace == nil || workspace.inner != engine {
			t.Fatalf("Open = (%+v, %v)", workspace, err)
		}
		environment[0] = "MUTATED=1"
		if gotRoot != "repository" || gotOptions.GoBinary != "custom-go" || gotOptions.TempDirectory != "temporary" || gotOptions.ReportDirectory != "reports" || !slices.Equal(gotOptions.Env, []string{"A=1", "B=2"}) {
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
		Exclude: []string{"generated/**"}, Changed: true, ChangedRef: "origin/main",
		Packages: []string{"./internal/..."}, Jobs: 3,
		BuildTimeout: time.Minute, MutantTimeout: 2 * time.Second,
		VerifyArgv: []string{"go", "test", "./..."}, VerifyEnv: []string{"A=1"}, VerifyTimeout: 3 * time.Minute,
	}
	got, err := workspace.Prepare(context.Background(), options)
	if err != nil || got != session {
		t.Fatalf("Prepare = (%p, %v)", got, err)
	}
	options.Operators[0], options.Include[0], options.Exclude[0], options.Packages[0] = "mutated", "mutated", "mutated", "mutated"
	options.VerifyArgv[0], options.VerifyEnv[0] = "mutated", "mutated"
	prepared := engine.prepare
	if prepared.Profile != "all" || !slices.Equal(prepared.Operators, []string{"comparison"}) || !slices.Equal(prepared.Include, []string{"internal/**"}) || !slices.Equal(prepared.Exclude, []string{"generated/**"}) || !slices.Equal(prepared.Packages, []string{"./internal/..."}) || prepared.Jobs != 3 || prepared.BuildTimeout != time.Minute || prepared.MutantTimeout != 2*time.Second || !slices.Equal(prepared.Verify.Argv, []string{"go", "test", "./..."}) || !slices.Equal(prepared.Verify.Env, []string{"A=1"}) || prepared.Verify.Timeout != 3*time.Minute {
		t.Fatalf("Prepare options = %+v", prepared)
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

func (workspace *fakeMutationWorkspace) Close() error {
	workspace.closeCalls++
	return workspace.closeErr
}
