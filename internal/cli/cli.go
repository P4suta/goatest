// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package cli implements goatest's deterministic command and exit-code layer.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/P4suta/goatest/internal/report"
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
)

type Request struct {
	Changed    bool
	ChangedRef string
	Contract   string
	NoApply    bool
	JSON       bool
	NoTUI      bool
}

type Service interface {
	Execute(context.Context, Command, Request, string) (report.Report, error)
}

func Run(ctx context.Context, args []string, stdout, stderr io.Writer, service Service) int {
	command, request, id, err := parse(args)
	if err != nil {
		fmt.Fprintf(stderr, "goatest: %v\n", err)
		return ExitError
	}
	result, err := service.Execute(ctx, command, request, id)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			fmt.Fprintln(stderr, "goatest: interrupted")
			return ExitInterrupted
		}
		fmt.Fprintf(stderr, "goatest: %v\n", err)
		return ExitError
	}
	if request.JSON {
		data, encodeErr := report.JSON(result)
		if encodeErr != nil {
			fmt.Fprintf(stderr, "goatest: encode report: %v\n", encodeErr)
			return ExitError
		}
		_, _ = stdout.Write(data)
	} else {
		_, _ = io.WriteString(stdout, report.Lines(result))
	}
	return exitCode(result.Verdict)
}

func parse(args []string) (Command, Request, string, error) {
	request := Request{}
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
		case argument == "--no-apply":
			request.NoApply = true
		case argument == "--json":
			request.JSON = true
		case argument == "--no-tui":
			request.NoTUI = true
		case strings.HasPrefix(argument, "-"):
			return "", Request{}, "", fmt.Errorf("unknown flag %q", argument)
		default:
			positionals = append(positionals, argument)
		}
	}
	if request.Contract != "" && request.Contract != "standard-v1" && request.Contract != "deep-v1" {
		return "", Request{}, "", fmt.Errorf("contract %q: expected standard-v1 or deep-v1", request.Contract)
	}
	if len(positionals) == 0 {
		return CommandVerify, request, "", nil
	}
	command := Command(positionals[0])
	rest := positionals[1:]
	switch command {
	case CommandInit, CommandReport:
		if len(rest) != 0 {
			return "", Request{}, "", fmt.Errorf("%s takes no ID", command)
		}
		return command, request, "", nil
	case CommandExplain, CommandReplay, CommandAccept:
		if len(rest) != 1 || rest[0] == "" {
			return "", Request{}, "", fmt.Errorf("%s requires exactly one ID", command)
		}
		return command, request, rest[0], nil
	default:
		return "", Request{}, "", fmt.Errorf("unknown command %q", command)
	}
}

func exitCode(verdict report.Verdict) int {
	switch verdict {
	case report.VerdictAssured:
		return ExitAssured
	case report.VerdictDefect:
		return ExitDefect
	case report.VerdictInsufficient:
		return ExitInsufficient
	default:
		return ExitError
	}
}
