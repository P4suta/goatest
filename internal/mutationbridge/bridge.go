// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package mutationbridge

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	gomutants "github.com/P4suta/go-mutants"
	"github.com/P4suta/goatest/internal/trace"
)

type Options struct {
	GoBinary        string
	TempDirectory   string
	ReportDirectory string
	SnapshotExclude []string
	Environment     []string

	Trace *trace.Recorder

	KeepTemp bool
}

type PrepareOptions struct {
	Contract           string
	Operators          []string
	Include            []string
	Exclude            []string
	DiscoveryPackages  []string
	Packages           []string
	ProbeCoverPackages []string
	Jobs               int
	BuildTimeout       time.Duration
	MutantTimeout      time.Duration
	VerifyArgv         []string
	VerifyEnv          []string
	VerifyTimeout      time.Duration
	SkipVerify         bool

	Probe bool
}

type mutationWorkspace interface {
	Exec(context.Context, gomutants.Command) (gomutants.CommandResult, error)
	Prepare(context.Context, gomutants.PrepareOptions) (*gomutants.Session, error)
	ToolchainVersion() string
	Close() error
	Swept() gomutants.SweepResult
	Preserved() []string
}

type Workspace struct {
	inner mutationWorkspace
	trace *trace.Recorder

	swept     gomutants.SweepResult
	preserved []string
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
		SnapshotExclude: slices.Clone(options.SnapshotExclude),
		KeepTemp:        options.KeepTemp,
		Env:             append([]string(nil), options.Environment...),
	})
	if err != nil {
		return nil, fmt.Errorf("goatest: open mutation workspace: %w", err)
	}
	return &Workspace{inner: inner, trace: options.Trace}, nil
}

func (workspace *Workspace) Trace() *trace.Recorder {
	if workspace == nil {
		return nil
	}
	return workspace.trace
}

// GoWorkVariable and goWorkDisabled keep a command inside the module it is
// measuring.
//
// A go workspace changes what `go list ./...` answers, what a build resolves,
// and - because GOWORK is one of the names in buildEnvironmentNames - the
// identity a cached verdict is keyed on. goatest assures one main module per
// run and refuses a go.work that holds several, on purpose and in writing. A
// go.work inherited from the operator's shell would not be a second opinion
// about that; it would be the refusal firing on a repository nobody asked to
// aggregate, or worse, a silently different answer.
//
// go-mutants states the rule for consumers that run their own go commands
// through Workspace.Exec: pass GOWORK=off. This is where goatest passes it, and
// there is exactly one such place, because every workspace command in this
// module goes through here.
const (
	GoWorkVariable = "GOWORK"

	goWorkDisabled = GoWorkVariable + "=off"
)

// Exec runs one command in the frozen workspace, inside the module it is
// measuring.
func (workspace *Workspace) Exec(ctx context.Context, command gomutants.Command) (gomutants.CommandResult, error) {
	if workspace == nil || workspace.inner == nil {
		return gomutants.CommandResult{}, errors.New("goatest: nil mutation workspace")
	}
	command.Env = withoutGoWorkspace(command.Env)
	result, err := workspace.inner.Exec(ctx, command)
	workspace.trace.Exec(executionRecord(command, result, err))
	return result, err
}

// withoutGoWorkspace adds GOWORK=off unless the caller named GOWORK itself.
//
// Deferring to an explicit setting rather than overwriting it keeps this from
// being a rule no caller can opt out of. A caller that means to run under a
// workspace - and there is none today - says so and is obeyed, which is a
// decision somebody made rather than an accident of the environment.
func withoutGoWorkspace(environment []string) []string {
	for _, entry := range environment {
		if name, _, found := strings.Cut(entry, "="); found && name == GoWorkVariable {
			return environment
		}
	}
	return append(slices.Clone(environment), goWorkDisabled)
}

func executionRecord(command gomutants.Command, result gomutants.CommandResult, err error) trace.ExecRecord {
	record := trace.ExecRecord{
		Argv:       diagnosticCommandArguments(command.Argv),
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

func diagnosticCommandArguments(arguments []string) []string {
	result := slices.Clone(arguments)
	return slices.DeleteFunc(result, func(argument string) bool {
		return strings.HasPrefix(argument, "-test.testlogfile=")
	})
}

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
	var prepareTrace func(gomutants.PrepareEvent)
	if workspace.trace != nil {
		prepareTrace = func(event gomutants.PrepareEvent) {
			workspace.trace.Prepare(string(event.Phase), string(event.State), string(event.Result), event.Duration)
		}
	}
	session, err := workspace.inner.Prepare(ctx, gomutants.PrepareOptions{
		Profile:            profile,
		Operators:          append([]string(nil), options.Operators...),
		Include:            append([]string(nil), options.Include...),
		Exclude:            append([]string(nil), options.Exclude...),
		DiscoveryPackages:  append([]string(nil), options.DiscoveryPackages...),
		Packages:           append([]string(nil), options.Packages...),
		ProbeCoverPackages: append([]string(nil), options.ProbeCoverPackages...),
		Jobs:               options.Jobs,
		BuildTimeout:       options.BuildTimeout,
		MutantTimeout:      options.MutantTimeout,
		Verify: gomutants.Command{
			Argv:    append([]string(nil), options.VerifyArgv...),
			Env:     append([]string(nil), options.VerifyEnv...),
			Timeout: options.VerifyTimeout,
		},
		SkipVerify: options.SkipVerify,
		Probe:      options.Probe,
		Trace:      prepareTrace,
	})
	if err != nil {
		return nil, fmt.Errorf("goatest: prepare mutation session: %w", err)
	}
	return session, nil
}

func (workspace *Workspace) Swept() gomutants.SweepResult {
	if workspace == nil {
		return gomutants.SweepResult{}
	}
	if workspace.inner == nil {
		return workspace.swept
	}
	return workspace.inner.Swept()
}

func (workspace *Workspace) Preserved() []string {
	if workspace == nil {
		return nil
	}
	if workspace.inner == nil {
		return slices.Clone(workspace.preserved)
	}
	return workspace.inner.Preserved()
}

func (workspace *Workspace) ToolchainVersion() string {
	if workspace == nil || workspace.inner == nil {
		return ""
	}
	return workspace.inner.ToolchainVersion()
}

func (workspace *Workspace) Close() error {
	if workspace == nil || workspace.inner == nil {
		return nil
	}
	err := workspace.inner.Close()
	workspace.swept, workspace.preserved = workspace.inner.Swept(), workspace.inner.Preserved()
	workspace.inner = nil
	return err
}
