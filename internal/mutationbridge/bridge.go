// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package mutationbridge is goatest's one-way adapter to go-mutants.
package mutationbridge

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	gomutants "github.com/P4suta/go-mutants"
	"github.com/P4suta/goatest/internal/trace"
)

type Options struct {
	GoBinary        string
	TempDirectory   string
	ReportDirectory string
	Environment     []string
	// Trace records the commands the workspace runs. A nil recorder is a
	// workspace that runs untraced, which is what every caller that does not
	// ask for a trace gets.
	Trace *trace.Recorder
}

type PrepareOptions struct {
	Contract      string
	Operators     []string
	Include       []string
	Exclude       []string
	Packages      []string
	Jobs          int
	BuildTimeout  time.Duration
	MutantTimeout time.Duration
	VerifyArgv    []string
	VerifyEnv     []string
	VerifyTimeout time.Duration
	// Probe also builds the probe tree the infection pass measures against: the
	// same source with no mutant ever active, in which each site the engine has
	// a probe form for records whether the mutated value would have differed. It
	// costs a second instrumentation and a second set of test binaries, so a
	// caller that never asks the infection question does not pay for it.
	Probe bool
}

type mutationWorkspace interface {
	Exec(context.Context, gomutants.Command) (gomutants.CommandResult, error)
	Prepare(context.Context, gomutants.PrepareOptions) (*gomutants.Session, error)
	Close() error
}

type Workspace struct {
	inner mutationWorkspace
	trace *trace.Recorder
}

var openMutationWorkspace = func(ctx context.Context, root string, options gomutants.OpenOptions) (mutationWorkspace, error) {
	return gomutants.Open(ctx, root, options)
}

func Profile(contract string) (string, error) {
	switch contract {
	case "standard-v1":
		return "strong", nil
	case "deep-v1":
		return "all", nil
	default:
		return "", fmt.Errorf("goatest: mutation contract %q is unknown", contract)
	}
}

func Open(ctx context.Context, root string, options Options) (*Workspace, error) {
	inner, err := openMutationWorkspace(ctx, root, gomutants.OpenOptions{
		GoBinary:        options.GoBinary,
		TempDirectory:   options.TempDirectory,
		ReportDirectory: options.ReportDirectory,
		Env:             append([]string(nil), options.Environment...),
	})
	if err != nil {
		return nil, fmt.Errorf("goatest: open mutation workspace: %w", err)
	}
	return &Workspace{inner: inner, trace: options.Trace}, nil
}

// Trace reports the recording the workspace runs under. It is how a caller
// that wraps what the workspace produced, such as a mutation session, records
// into the same trace the workspace's own commands reach.
func (workspace *Workspace) Trace() *trace.Recorder {
	if workspace == nil {
		return nil
	}
	return workspace.trace
}

// Exec runs one command and records it. Recording happens after the engine
// answers, because what a trace reader wants is the execution and its result
// as one line; a command that never returns leaves the phase it ran in open
// instead, which says the same thing.
func (workspace *Workspace) Exec(ctx context.Context, command gomutants.Command) (gomutants.CommandResult, error) {
	if workspace == nil || workspace.inner == nil {
		return gomutants.CommandResult{}, errors.New("goatest: nil mutation workspace")
	}
	result, err := workspace.inner.Exec(ctx, command)
	workspace.trace.Exec(executionRecord(command, result, err))
	return result, err
}

// executionRecord describes one executed command for the trace.
//
// The environment is handed over whole because the recorder is what reduces it
// to names; the captured output is handed over for the same reason, digested
// into the event by the recorder and preserved beside the stream by a sink
// that can store it. The bridge writes neither.
func executionRecord(command gomutants.Command, result gomutants.CommandResult, err error) trace.ExecRecord {
	record := trace.ExecRecord{
		Argv:       slices.Clone(command.Argv),
		Dir:        command.Dir,
		EnvNames:   command.Env,
		TimeoutMS:  traceMilliseconds(command.Timeout),
		ExitCode:   result.ExitCode,
		TimedOut:   result.TimedOut,
		DurationMS: traceMilliseconds(result.Duration),
		Output:     result.Output,
	}
	if err != nil {
		record.Error = err.Error()
	}
	return record
}

// traceMilliseconds is the millisecond count a trace records for a duration. A
// negative duration, which the engine contract forbids, is recorded as none
// rather than as a nonsense measurement.
func traceMilliseconds(duration time.Duration) int64 {
	return max(duration.Milliseconds(), 0)
}

func (workspace *Workspace) Prepare(ctx context.Context, options PrepareOptions) (*gomutants.Session, error) {
	if workspace == nil || workspace.inner == nil {
		return nil, errors.New("goatest: nil mutation workspace")
	}
	profile, err := Profile(options.Contract)
	if err != nil {
		return nil, err
	}
	session, err := workspace.inner.Prepare(ctx, gomutants.PrepareOptions{
		Profile:       profile,
		Operators:     append([]string(nil), options.Operators...),
		Include:       append([]string(nil), options.Include...),
		Exclude:       append([]string(nil), options.Exclude...),
		Packages:      append([]string(nil), options.Packages...),
		Jobs:          options.Jobs,
		BuildTimeout:  options.BuildTimeout,
		MutantTimeout: options.MutantTimeout,
		Verify: gomutants.Command{
			Argv:    append([]string(nil), options.VerifyArgv...),
			Env:     append([]string(nil), options.VerifyEnv...),
			Timeout: options.VerifyTimeout,
		},
		Probe: options.Probe,
	})
	if err != nil {
		return nil, fmt.Errorf("goatest: prepare mutation session: %w", err)
	}
	return session, nil
}

func (workspace *Workspace) Close() error {
	if workspace == nil || workspace.inner == nil {
		return nil
	}
	err := workspace.inner.Close()
	workspace.inner = nil
	return err
}
