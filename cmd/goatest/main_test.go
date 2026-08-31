// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"bytes"
	"context"
	"os"
	"slices"
	"syscall"
	"testing"
	"time"

	"github.com/P4suta/goatest/internal/cli"
	"github.com/P4suta/goatest/internal/report"
)

type mainServiceFunc func(context.Context, cli.Command, cli.Request, string) (report.Report, error)

func (function mainServiceFunc) Execute(ctx context.Context, command cli.Command, request cli.Request, id string) (report.Report, error) {
	return function(ctx, command, request, id)
}

type syntheticSignal string

func (signal syntheticSignal) String() string { return string(signal) }
func (syntheticSignal) Signal()               {}

func TestRealMainHandlesOnlyTheExactVersionFlagAndDelegatesHelp(t *testing.T) {
	service := mainServiceFunc(func(context.Context, cli.Command, cli.Request, string) (report.Report, error) {
		t.Fatal("version/help unexpectedly executed the service")
		return report.Report{}, nil
	})
	for _, testCase := range []struct {
		name       string
		arguments  []string
		wantExit   int
		wantOutput string
		wantError  string
	}{
		{name: "version", arguments: []string{"--version"}, wantExit: 0, wantOutput: "goatest "},
		{name: "help", arguments: []string{"--help"}, wantExit: 0, wantOutput: "Usage:"},
		{name: "version-extra", arguments: []string{"--version", "extra"}, wantExit: cli.ExitError, wantError: "unknown flag"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			exit := realMainWith(testCase.arguments, &stdout, &stderr, service)
			if exit != testCase.wantExit || !bytes.Contains(stdout.Bytes(), []byte(testCase.wantOutput)) || !bytes.Contains(stderr.Bytes(), []byte(testCase.wantError)) {
				t.Fatalf("exit=%d stdout=%q stderr=%q", exit, stdout.String(), stderr.String())
			}
		})
	}
}

func TestOperatingSystemWriterWrappersReturnDelegatedExitCodes(t *testing.T) {
	if got := realMain([]string{"--definitely-unknown"}); got != cli.ExitError {
		t.Fatalf("realMain exit = %d, want %d", got, cli.ExitError)
	}
	service := mainServiceFunc(func(context.Context, cli.Command, cli.Request, string) (report.Report, error) {
		return report.Report{Schema: report.SchemaV1, Verdict: report.VerdictDefect}, nil
	})
	if got := runWithService(nil, service); got != cli.ExitDefect {
		t.Fatalf("runWithService exit = %d, want %d", got, cli.ExitDefect)
	}
}

func TestRunWithSignalsMapsTerminationAndNonSyscallInterrupt(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		signal os.Signal
		want   int
	}{
		{name: "termination", signal: syscall.SIGTERM, want: cli.ExitTerminated},
		{name: "synthetic-interrupt", signal: syntheticSignal("interrupt"), want: cli.ExitInterrupted},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			started := make(chan struct{})
			service := mainServiceFunc(func(ctx context.Context, _ cli.Command, _ cli.Request, _ string) (report.Report, error) {
				close(started)
				<-ctx.Done()
				return report.Report{}, ctx.Err()
			})
			signals := make(chan os.Signal, 1)
			result := make(chan int, 1)
			go func() {
				result <- runWithSignals(nil, service, signals, &bytes.Buffer{}, &bytes.Buffer{})
			}()
			select {
			case <-started:
			case <-time.After(5 * time.Second):
				t.Fatal("service did not start")
			}
			signals <- testCase.signal
			select {
			case got := <-result:
				if got != testCase.want {
					t.Fatalf("exit = %d, want %d", got, testCase.want)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("signal did not stop service")
			}
		})
	}
}

func TestEnvironmentTraceBecomesTheFlagTheCommandLayerParses(t *testing.T) {
	for _, testCase := range []struct {
		name      string
		value     string
		arguments []string
		want      []string
	}{
		{name: "unset", value: "", arguments: []string{"verify"}, want: []string{"verify"}},
		{name: "disabled", value: "0", arguments: []string{"verify"}, want: []string{"verify"}},
		{name: "false", value: "false", arguments: []string{"verify"}, want: []string{"verify"}},
		{name: "enabled", value: "1", arguments: []string{"verify"}, want: []string{"verify", "--trace"}},
		{name: "true", value: "true", arguments: nil, want: []string{"--trace"}},
		{name: "directory", value: "/tmp/goatest-trace", arguments: []string{"verify"}, want: []string{"verify", "--trace=/tmp/goatest-trace"}},
		{name: "explicit-flag-wins", value: "/tmp/env", arguments: []string{"verify", "--trace=/tmp/flag"}, want: []string{"verify", "--trace=/tmp/flag"}},
		{name: "explicit-default-wins", value: "/tmp/env", arguments: []string{"verify", "--trace"}, want: []string{"verify", "--trace"}},
		{name: "before-test-arguments", value: "1", arguments: []string{"verify", "--", "-short"}, want: []string{"verify", "--trace", "--", "-short"}},
		{name: "help", value: "1", arguments: []string{"--help"}, want: []string{"--help"}},
		{name: "help-short", value: "1", arguments: []string{"-h"}, want: []string{"-h"}},
		{name: "version", value: "1", arguments: []string{"--version"}, want: []string{"--version"}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			if got := withTraceEnvironment(testCase.arguments, testCase.value); !slices.Equal(got, testCase.want) {
				t.Fatalf("arguments = %q, want %q", got, testCase.want)
			}
		})
	}
}

func TestEnvironmentTraceReachesTheServiceWithoutDisturbingVersionOrHelp(t *testing.T) {
	t.Setenv("GOATEST_TRACE", "/tmp/goatest-environment-trace")
	var requested cli.Request
	service := mainServiceFunc(func(_ context.Context, _ cli.Command, request cli.Request, _ string) (report.Report, error) {
		requested = request
		return report.Report{Schema: report.SchemaV1, Verdict: report.VerdictAssured}, nil
	})
	var stdout, stderr bytes.Buffer
	if exit := realMainWith([]string{"verify"}, &stdout, &stderr, service); exit != cli.ExitAssured {
		t.Fatalf("verify exit = %d stderr = %q", exit, stderr.String())
	}
	if !requested.Trace || requested.TraceDirectory != "/tmp/goatest-environment-trace" {
		t.Fatalf("request = %+v", requested)
	}
	stdout.Reset()
	if exit := realMainWith([]string{"--version"}, &stdout, &stderr, service); exit != 0 || !bytes.Contains(stdout.Bytes(), []byte("goatest ")) {
		t.Fatalf("version exit = %d stdout = %q", exit, stdout.String())
	}
	stdout.Reset()
	if exit := realMainWith([]string{"--help"}, &stdout, &stderr, service); exit != 0 || !bytes.Contains(stdout.Bytes(), []byte("Usage:")) {
		t.Fatalf("help exit = %d stdout = %q", exit, stdout.String())
	}
}

func TestInterruptedExitDistinguishesInterruptAndTermination(t *testing.T) {
	if got := interruptedExit(cli.ExitInterrupted, os.Interrupt); got != cli.ExitInterrupted {
		t.Fatalf("interrupt exit = %d", got)
	}
	if got := interruptedExit(cli.ExitInterrupted, syscall.SIGTERM); got != cli.ExitTerminated {
		t.Fatalf("termination exit = %d", got)
	}
	if got := interruptedExit(cli.ExitAssured, syscall.SIGTERM); got != cli.ExitAssured {
		t.Fatalf("completed exit was changed to %d", got)
	}
}
