// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package cli implements goatest's deterministic command and exit-code layer.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/P4suta/goatest/internal/report"
	"github.com/P4suta/goatest/internal/testargs"
)

const (
	ExitAssured      = 0
	ExitDefect       = 1
	ExitInsufficient = 2
	ExitError        = 3
	ExitInterrupted  = 130
	ExitTerminated   = 143
)

type Command string

const (
	CommandVerify  Command = "verify"
	CommandInit    Command = "init"
	CommandExplain Command = "explain"
	CommandReplay  Command = "replay"
	CommandAccept  Command = "accept"
	CommandReport  Command = "report"
	CommandPlan    Command = "plan"
	CommandDoctor  Command = "doctor"
	CommandFix     Command = "fix"
	CommandCache   Command = "cache"
	CommandTrace   Command = "trace"
)

type UI string

const (
	UIAuto  UI = "auto"
	UIPlain UI = "plain"
	UIJSONL UI = "jsonl"
)

type Request struct {
	Changed          bool
	ChangedRef       string
	Contract         string
	JSON             bool
	UI               UI
	Packages         []string
	TestArgs         []string
	Trace            bool
	TraceDirectory   string
	ReportLatestFull bool
	ReportRunID      string
	IDs              []string
	Apply            bool
	Reason           string
	Expires          string
	Owner            string
	Ticket           string
	ReplayFindingID  string
	ReplayMutantID   string
	KeepTemp         bool
}

type Service interface {
	Execute(context.Context, Command, Request, string) (report.Report, error)
}

const help = `Usage:
	goatest verify [packages...] [--changed[=REF]] [--contract=standard-v1|deep-v1] [--trace[=DIR]] [--keep-temp] [-- test-binary-args...]
	goatest plan [packages...] [--changed[=REF]] [--contract=standard-v1|deep-v1]
	goatest doctor
	goatest init
	goatest explain ID
	goatest replay ID [--trace[=DIR]] [--keep-temp]
	goatest accept ID --reason=TEXT --expires=RFC3339 [--owner=NAME] [--ticket=ID]
	goatest fix [ID...] [--apply]
	goatest report [--latest-full|--run=ID]
	goatest cache status|gc
	goatest trace summary [RUN]
	goatest trace diff RUN-A RUN-B
	goatest help [command]
Every command accepts --ui=auto|plain|jsonl and --json for its output; --version prints the version. 'goatest help COMMAND' or 'goatest COMMAND --help' explains one command; flags without a command run verify.
Exit codes: 0 assured, 1 defect, 2 insufficient, 3 error, 130 interrupted, 143 terminated.
Tracing: --trace collects diagnostic exhaust in DIR, or under .goatest/trace by default, one directory per run; GOATEST_TRACE=1|DIR asks for the same. A trace is never evidence.
Keeping temporaries: --keep-temp leaves the run's temporary directories on disk and records each kept path in the trace and in .goatest/kept-temp-v1.json; GOATEST_KEEP_TEMP=1 asks for the same. 'cache status' lists them and 'cache gc' removes them once they are older than the [cache] ttl.
`

// commandHelp is the help text of one command, and the fact that the command
// has one. The vocabulary lives in a function rather than a table so that no
// package-level mutable state exists for a test to reach for.
func commandHelp(command Command) (string, bool) {
	switch command {
	case CommandVerify:
		return `Usage:	goatest verify [packages...] [flags] [-- test-binary-args...]

Verify the configured assurance contract: baseline tests, race checks, and
every selected mutant, ending in a verdict written under reports/. This is the
default command: flags without a command run a verify.
Flags:
	--changed[=REF]	verify only the changeset since the merge base with REF (default HEAD)
	--contract=standard-v1|deep-v1	select the fault model (default from .goatest.toml)
	--ui=auto|plain|jsonl	select progress and report rendering
	--json	print the final report as formatted JSON
	--trace[=DIR]	record diagnostic exhaust; GOATEST_TRACE=1|DIR asks for the same
	--keep-temp	leave the run's temporary trees on disk, recorded in .goatest/kept-temp-v1.json; GOATEST_KEEP_TEMP=1 asks for the same
Arguments after -- reach the test binaries; only -short and -test.parallel are accepted, every other -test.* flag is owned by the assurance run.
`, true
	case CommandPlan:
		return `Usage:	goatest plan [packages...] [--changed[=REF]] [--contract=standard-v1|deep-v1]

Preview the work a verify would run - targets, routed tests, and mutation
waves - without executing any test.
`, true
	case CommandDoctor:
		return `Usage:	goatest doctor

Check everything a verify needs before one is run: strict configuration, the
Go toolchain, offline dependencies, the race detector, the mutation contract,
writable output directories, Git, configured providers, and disk space.
`, true
	case CommandInit:
		return `Usage:	goatest init

Write an annotated .goatest.toml describing every section, with the strict
defaults active, and never overwrite an existing file.
`, true
	case CommandExplain:
		return `Usage:	goatest explain ID

Show one finding from the latest report, with the repairs that answer it.
`, true
	case CommandReplay:
		return `Usage:	goatest replay ID [--trace[=DIR]] [--keep-temp]

Re-execute the mutation behind one finding of the latest report and answer
REPRODUCED or RESOLVED.
`, true
	case CommandAccept:
		return `Usage:	goatest accept ID --reason=TEXT --expires=RFC3339 [--owner=NAME] [--ticket=ID]

Record an explicit, expiring acceptance for one finding of the latest report
in .goatest.toml.
`, true
	case CommandFix:
		return `Usage:	goatest fix [ID...] [--apply]

Preview stored repair candidates, or with --apply revalidate them in an
isolated copy and apply the batch atomically. Only fix --apply may change the
worktree.
`, true
	case CommandReport:
		return `Usage:	goatest report [--latest-full|--run=ID]

Print the latest report, the latest full-project report, or one recorded run.
`, true
	case CommandCache:
		return `Usage:	goatest cache status|gc

Show the evidence cache policy and contents, or collect expired and
over-budget entries.
`, true
	case CommandTrace:
		return `Usage:	goatest trace summary [RUN]
	goatest trace diff RUN-A RUN-B

Read a default .goatest/trace recording without replaying it. Summary makes
missing streams, missing run-end events, sequence gaps, and dropped events
explicit; diff compares event counts and phase durations.
`, true
	default:
		return "", false
	}
}

func Run(ctx context.Context, args []string, stdout, stderr io.Writer, service Service) int {
	// A bare invocation asks what the tool can do; nothing as expensive as a
	// full verification starts without a command asking for it.
	if len(args) == 0 {
		_, _ = io.WriteString(stdout, help)
		return 0
	}
	if handled, code := runHelp(args, stdout, stderr); handled {
		return code
	}
	command, request, id, err := parse(args)
	if err != nil {
		diagnose(stderr, err.Error())
		var usage usageError
		if errors.As(err, &usage) {
			_, _ = fmt.Fprintf(stderr, "run 'goatest help %s' for usage\n", usage.command)
		} else {
			_, _ = fmt.Fprintln(stderr, "run 'goatest --help' for usage")
		}
		return ExitError
	}
	result, err := service.Execute(ctx, command, request, id)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			_, _ = fmt.Fprintln(stderr, "goatest: interrupted")
			return ExitInterrupted
		}
		if result.Verdict == report.VerdictError {
			render(stdout, result, request)
		}
		diagnose(stderr, err.Error())
		return ExitError
	}
	render(stdout, result, request)
	return exitCode(result.Verdict)
}

// diagnose writes one diagnostic line under exactly one "goatest: " prefix,
// escaped onto a single line so that nothing a run reports can forge a line of
// its own, however many times the layers below labeled their error.
func diagnose(stderr io.Writer, message string) {
	for strings.HasPrefix(message, "goatest: ") {
		message = strings.TrimPrefix(message, "goatest: ")
	}
	_, _ = fmt.Fprintf(stderr, "goatest: %s\n", report.LineText(message))
}

// usageError is a parse error raised after the command was recognized, which
// is what lets the diagnostic point at that command's own help.
type usageError struct {
	command Command
	cause   error
}

func (err usageError) Error() string { return err.cause.Error() }

// runHelp answers an argument list that asks for help - through --help or -h
// anywhere ahead of the test-binary separator, or through the help command -
// and reports whether it did. Arguments behind the separator belong to a test
// binary and never ask this layer for anything.
func runHelp(args []string, stdout, stderr io.Writer) (bool, int) {
	before := args
	if separator := slices.Index(args, "--"); separator >= 0 {
		before = args[:separator]
	}
	asked := false
	positionals := make([]string, 0, 2)
	for _, argument := range before {
		switch {
		case argument == "--help" || argument == "-h":
			asked = true
		case strings.HasPrefix(argument, "-"):
		default:
			positionals = append(positionals, argument)
		}
	}
	if len(positionals) != 0 && positionals[0] == "help" {
		asked = true
		positionals = positionals[1:]
	}
	if !asked {
		return false, 0
	}
	if len(positionals) == 0 {
		_, _ = io.WriteString(stdout, help)
		return true, 0
	}
	text, known := commandHelp(Command(positionals[0]))
	if !known {
		diagnose(stderr, fmt.Sprintf("unknown command %q", positionals[0]))
		_, _ = fmt.Fprintln(stderr, "run 'goatest --help' for usage")
		return true, ExitError
	}
	_, _ = io.WriteString(stdout, text)
	return true, 0
}

func render(output io.Writer, result report.Report, request Request) {
	if request.UI == UIJSONL {
		event := struct {
			Type   string        `json:"type"`
			Report report.Report `json:"report"`
		}{Type: "report", Report: result}
		data, _ := json.Marshal(event)
		_, _ = output.Write(append(data, '\n'))
	} else if request.JSON {
		_, _ = output.Write(report.JSON(result))
	} else {
		_, _ = io.WriteString(output, report.Lines(result))
	}
}

func parse(args []string) (Command, Request, string, error) {
	request := Request{UI: UIAuto}
	var testArgs []string
	for index, argument := range args {
		if argument == "--" {
			testArgs = append(testArgs, args[index+1:]...)
			args = args[:index]
			break
		}
	}
	positionals := make([]string, 0, 2)
	for _, argument := range args {
		switch {
		case argument == "--changed":
			request.Changed = true
		case strings.HasPrefix(argument, "--changed="):
			request.Changed = true
			request.ChangedRef = strings.TrimPrefix(argument, "--changed=")
		case strings.HasPrefix(argument, "--contract="):
			request.Contract = strings.TrimPrefix(argument, "--contract=")
		case argument == "--apply":
			request.Apply = true
		case argument == "--json":
			request.JSON = true
		case strings.HasPrefix(argument, "--ui="):
			request.UI = UI(strings.TrimPrefix(argument, "--ui="))
		case argument == "--trace":
			request.Trace = true
		case strings.HasPrefix(argument, "--trace="):
			request.Trace = true
			request.TraceDirectory = strings.TrimPrefix(argument, "--trace=")
		case argument == "--keep-temp":
			request.KeepTemp = true
		case strings.HasPrefix(argument, "--keep-temp="):
			return "", Request{}, "", errors.New("--keep-temp takes no value")
		case argument == "--latest-full":
			request.ReportLatestFull = true
		case strings.HasPrefix(argument, "--run="):
			request.ReportRunID = strings.TrimPrefix(argument, "--run=")
		case strings.HasPrefix(argument, "--reason="):
			request.Reason = strings.TrimPrefix(argument, "--reason=")
		case strings.HasPrefix(argument, "--expires="):
			request.Expires = strings.TrimPrefix(argument, "--expires=")
		case strings.HasPrefix(argument, "--owner="):
			request.Owner = strings.TrimPrefix(argument, "--owner=")
		case strings.HasPrefix(argument, "--ticket="):
			request.Ticket = strings.TrimPrefix(argument, "--ticket=")
		case strings.HasPrefix(argument, "-"):
			return "", Request{}, "", fmt.Errorf("unknown flag %q", argument)
		default:
			positionals = append(positionals, argument)
		}
	}
	normalizedTestArgs, err := testargs.Normalize(testArgs)
	if err != nil {
		return "", Request{}, "", err
	}
	request.TestArgs = normalizedTestArgs
	if request.Contract != "" && request.Contract != "standard-v1" && request.Contract != "deep-v1" {
		return "", Request{}, "", fmt.Errorf("contract %q: expected standard-v1 or deep-v1", request.Contract)
	}
	if request.UI != UIAuto && request.UI != UIPlain && request.UI != UIJSONL {
		return "", Request{}, "", fmt.Errorf("ui %q: expected auto, plain, or jsonl", request.UI)
	}
	if request.JSON && request.UI == UIJSONL {
		return "", Request{}, "", errors.New("--json and --ui=jsonl are mutually exclusive")
	}
	if len(positionals) == 0 {
		return parsedCommand(CommandVerify, request, "")
	}
	command := Command(positionals[0])
	rest := positionals[1:]
	switch command {
	case CommandVerify, CommandPlan:
		request.Packages = append([]string(nil), rest...)
		return parsedCommand(command, request, "")
	case CommandInit, CommandDoctor:
		if len(rest) != 0 {
			return "", Request{}, "", usageError{command, fmt.Errorf("%s takes no ID", command)}
		}
		return parsedCommand(command, request, "")
	case CommandReport:
		if len(rest) != 0 || request.ReportLatestFull && request.ReportRunID != "" {
			return "", Request{}, "", usageError{command, errors.New("report accepts exactly one of --latest-full or --run=ID")}
		}
		return parsedCommand(command, request, "")
	case CommandExplain, CommandReplay, CommandAccept:
		if len(rest) != 1 || rest[0] == "" {
			return "", Request{}, "", usageError{command, fmt.Errorf("%s requires exactly one ID", command)}
		}
		if command == CommandAccept && (strings.TrimSpace(request.Reason) == "" || strings.TrimSpace(request.Expires) == "") {
			return "", Request{}, "", usageError{command, errors.New("accept requires --reason and --expires")}
		}
		return parsedCommand(command, request, rest[0])
	case CommandFix:
		request.IDs = append([]string(nil), rest...)
		return parsedCommand(command, request, "")
	case CommandCache:
		if len(rest) != 1 || rest[0] != "status" && rest[0] != "gc" {
			return "", Request{}, "", usageError{command, errors.New("cache requires status or gc")}
		}
		return parsedCommand(command, request, rest[0])
	case CommandTrace:
		if len(rest) == 0 {
			return "", Request{}, "", usageError{command, errors.New("trace requires summary or diff")}
		}
		action := rest[0]
		switch action {
		case "summary":
			if len(rest) > 2 {
				return "", Request{}, "", usageError{command, errors.New("trace summary accepts at most one run")}
			}
		case "diff":
			if len(rest) != 3 {
				return "", Request{}, "", usageError{command, errors.New("trace diff requires exactly two runs")}
			}
		default:
			return "", Request{}, "", usageError{command, errors.New("trace requires summary or diff")}
		}
		request.IDs = append([]string(nil), rest[1:]...)
		return parsedCommand(command, request, action)
	default:
		return "", Request{}, "", fmt.Errorf("unknown command %q", command)
	}
}

func parsedCommand(command Command, request Request, id string) (Command, Request, string, error) {
	if len(request.TestArgs) != 0 && command != CommandVerify {
		return "", Request{}, "", usageError{command, fmt.Errorf("%s does not accept test-binary arguments", command)}
	}
	if request.Apply && command != CommandFix {
		return "", Request{}, "", usageError{command, errors.New("--apply is only valid with fix")}
	}
	if request.Changed && command != CommandVerify && command != CommandPlan {
		return "", Request{}, "", usageError{command, errors.New("--changed is only valid with verify or plan")}
	}
	if request.Contract != "" && command != CommandVerify && command != CommandPlan {
		return "", Request{}, "", usageError{command, errors.New("--contract is only valid with verify or plan")}
	}
	if request.Trace && command != CommandVerify && command != CommandReplay {
		return "", Request{}, "", usageError{command, errors.New("--trace is only valid with verify or replay")}
	}
	// A kept directory is accounted for by the recording that names it, so the
	// commands that keep one are the commands that open a recording.
	if request.KeepTemp && command != CommandVerify && command != CommandReplay {
		return "", Request{}, "", usageError{command, errors.New("--keep-temp is only valid with verify or replay")}
	}
	if (request.ReportLatestFull || request.ReportRunID != "") && command != CommandReport {
		return "", Request{}, "", usageError{command, errors.New("--latest-full and --run are only valid with report")}
	}
	if request.Reason != "" || request.Expires != "" || request.Owner != "" || request.Ticket != "" {
		if command != CommandAccept {
			return "", Request{}, "", usageError{command, errors.New("acceptance metadata flags are only valid with accept")}
		}
	}
	return command, request, id, nil
}

func exitCode(verdict report.Verdict) int {
	switch verdict {
	case report.VerdictAssured, report.VerdictChangeAssured, report.VerdictScopeAssured,
		report.VerdictResolved, report.VerdictCompleted:
		return 0
	case report.VerdictDefect, report.VerdictReproduced:
		return ExitDefect
	case report.VerdictInsufficient:
		return ExitInsufficient
	default:
		return ExitError
	}
}
