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
	goatest verify [packages...] [--changed[=REF]] [--contract=standard-v1|deep-v1] [--ui=auto|plain|jsonl] [--trace[=DIR]] [--keep-temp] [-- test-binary-args...]
	goatest plan [packages...]
	goatest doctor
	goatest init
	goatest explain ID
	goatest replay ID [--trace[=DIR]] [--keep-temp]
	goatest accept ID --reason=TEXT --expires=RFC3339 [--owner=NAME] [--ticket=ID]
	goatest fix [ID...] [--apply]
	goatest report [--latest-full|--run=ID]
	goatest cache status|gc
Exit codes: 0 assured, 1 defect, 2 insufficient, 3 error, 130 interrupted, 143 terminated.
Tracing: --trace collects diagnostic exhaust in DIR, or under .goatest/trace by default, one directory per run; GOATEST_TRACE=1|DIR asks for the same. A trace is never evidence.
Keeping temporaries: --keep-temp leaves the baseline scratch and candidate trees on disk and records each kept path in the trace; GOATEST_KEEP_TEMP=1 asks for the same. Nothing removes them afterwards.
`

func Run(ctx context.Context, args []string, stdout, stderr io.Writer, service Service) int {
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		_, _ = io.WriteString(stdout, help)
		return 0
	}
	command, request, id, err := parse(args)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "goatest: %s\n", report.LineText(err.Error()))
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
		_, _ = fmt.Fprintf(stderr, "goatest: %s\n", report.LineText(err.Error()))
		return ExitError
	}
	render(stdout, result, request)
	return exitCode(result.Verdict)
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
			return "", Request{}, "", fmt.Errorf("%s takes no ID", command)
		}
		return parsedCommand(command, request, "")
	case CommandReport:
		if len(rest) != 0 || request.ReportLatestFull && request.ReportRunID != "" {
			return "", Request{}, "", errors.New("report accepts exactly one of --latest-full or --run=ID")
		}
		return parsedCommand(command, request, "")
	case CommandExplain, CommandReplay, CommandAccept:
		if len(rest) != 1 || rest[0] == "" {
			return "", Request{}, "", fmt.Errorf("%s requires exactly one ID", command)
		}
		if command == CommandAccept && (strings.TrimSpace(request.Reason) == "" || strings.TrimSpace(request.Expires) == "") {
			return "", Request{}, "", errors.New("accept requires --reason and --expires")
		}
		return parsedCommand(command, request, rest[0])
	case CommandFix:
		request.IDs = append([]string(nil), rest...)
		return parsedCommand(command, request, "")
	case CommandCache:
		if len(rest) != 1 || rest[0] != "status" && rest[0] != "gc" {
			return "", Request{}, "", errors.New("cache requires status or gc")
		}
		return parsedCommand(command, request, rest[0])
	default:
		return "", Request{}, "", fmt.Errorf("unknown command %q", command)
	}
}

func parsedCommand(command Command, request Request, id string) (Command, Request, string, error) {
	if len(request.TestArgs) != 0 && command != CommandVerify {
		return "", Request{}, "", fmt.Errorf("%s does not accept test-binary arguments", command)
	}
	if request.Apply && command != CommandFix {
		return "", Request{}, "", errors.New("--apply is only valid with fix")
	}
	if request.Changed && command != CommandVerify && command != CommandPlan {
		return "", Request{}, "", errors.New("--changed is only valid with verify or plan")
	}
	if request.Contract != "" && command != CommandVerify && command != CommandPlan {
		return "", Request{}, "", errors.New("--contract is only valid with verify or plan")
	}
	if request.Trace && command != CommandVerify && command != CommandReplay {
		return "", Request{}, "", errors.New("--trace is only valid with verify or replay")
	}
	// A kept directory is accounted for by the recording that names it, so the
	// commands that keep one are the commands that open a recording.
	if request.KeepTemp && command != CommandVerify && command != CommandReplay {
		return "", Request{}, "", errors.New("--keep-temp is only valid with verify or replay")
	}
	if (request.ReportLatestFull || request.ReportRunID != "") && command != CommandReport {
		return "", Request{}, "", errors.New("--latest-full and --run are only valid with report")
	}
	if request.Reason != "" || request.Expires != "" || request.Owner != "" || request.Ticket != "" {
		if command != CommandAccept {
			return "", Request{}, "", errors.New("acceptance metadata flags are only valid with accept")
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
