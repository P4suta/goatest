// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"slices"
	"strings"
	"sync/atomic"
	"syscall"

	"github.com/P4suta/goatest/internal/app"
	"github.com/P4suta/goatest/internal/assure"
	"github.com/P4suta/goatest/internal/buildcache"
	"github.com/P4suta/goatest/internal/cli"
	"github.com/P4suta/goatest/internal/ui"
)

// The variables an environment that cannot pass a flag asks with, such as a job
// wrapping an existing command line. They are read here and nowhere else: every
// layer below the command line is configured through options alone.
const (
	traceEnvironmentVariable    = "GOATEST_TRACE"
	keepTempEnvironmentVariable = "GOATEST_KEEP_TEMP"
)

func main() {
	os.Exit(realMain(os.Args[1:]))
}

func realMain(arguments []string) int {
	return realMainStreams(arguments, os.Stdin, os.Stdout, os.Stderr, app.Service{
		Root: ".", Progress: os.Stderr, Output: os.Stdout, Interactive: interactiveTerminal,
		Executable: goatestExecutable(),
	})
}

// goatestExecutable names this binary so that a go command started by a run can
// re-execute it as the build cache program. It is resolved here and nowhere
// else: this is the one place that knows the running process is a goatest
// binary and not a test binary or an application that embedded the service. A
// process that cannot name itself gets no build cache, which costs time and
// decides nothing.
func goatestExecutable() string {
	path, err := os.Executable()
	if err != nil {
		return ""
	}
	return path
}

// interactiveTerminal reports whether the progress stream can carry the
// in-place dashboard. The environment is read here and nowhere else: TERM=dumb
// and NO_COLOR both ask for deterministic plain lines, a writer that is no
// terminal cannot render in place, and a console that refuses ANSI escape
// processing would show litter instead of a dashboard.
func interactiveTerminal(writer io.Writer) bool {
	if os.Getenv("TERM") == "dumb" || os.Getenv("NO_COLOR") != "" {
		return false
	}
	return ui.IsTerminalWriter(writer) && ui.EnableVirtualTerminal(writer)
}

func realMainWith(arguments []string, stdout, stderr io.Writer, service cli.Service) int {
	return realMainStreams(arguments, strings.NewReader(""), stdout, stderr, service)
}

// cacheProgramCommand is the hidden subcommand a go command starts to reach
// goatest's build cache. It is dispatched here, ahead of everything: it speaks
// a binary protocol on stdin and stdout, so it must not pass through the flag
// parsing, the environment-to-flag rendering, or the progress rendering that
// every command a person types goes through. It is not in the help text
// because nobody types it.
const cacheProgramCommand = "cacheprog"

func realMainStreams(arguments []string, stdin io.Reader, stdout, stderr io.Writer, service cli.Service) int {
	if len(arguments) != 0 && arguments[0] == cacheProgramCommand {
		return buildcache.Main(arguments[1:], stdin, stdout, stderr)
	}
	if len(arguments) == 1 && arguments[0] == "--version" {
		_, _ = fmt.Fprintf(stdout, "goatest %s\n", assure.ResolvedGoatestVersion())
		return 0
	}
	arguments = withTraceEnvironment(arguments, os.Getenv(traceEnvironmentVariable))
	arguments = withKeepTempEnvironment(arguments, os.Getenv(keepTempEnvironmentVariable))
	return runWithServiceWriters(arguments, service, stdout, stderr)
}

// withTraceEnvironment renders a trace asked for by the environment as the flag
// the command layer parses.
func withTraceEnvironment(arguments []string, value string) []string {
	flag, requested := traceFlag(value)
	return withEnvironmentFlag(arguments, flag, requested)
}

// withKeepTempEnvironment renders temporary directories asked for by the
// environment as the flag the command layer parses.
func withKeepTempEnvironment(arguments []string, value string) []string {
	flag, requested := keepTempFlag(value)
	return withEnvironmentFlag(arguments, flag, requested)
}

// withEnvironmentFlag inserts the flag an environment variable asked for, which
// is what keeps the environment out of every layer below this one.
//
// An explicit flag always wins, an argument list that only asks for the help
// text or the version is left exactly as it is, and the flag is inserted ahead
// of the test-binary separator so that it reaches goatest rather than a test
// binary. An empty argument list asks for the help text, so it stays empty
// rather than becoming a run nobody asked for.
func withEnvironmentFlag(arguments []string, flag string, requested bool) []string {
	if !requested || len(arguments) == 0 {
		return arguments
	}
	name, _, _ := strings.Cut(flag, "=")
	for _, argument := range arguments {
		if argument == "--" {
			break
		}
		switch {
		case argument == name, strings.HasPrefix(argument, name+"="),
			argument == "--help", argument == "-h", argument == "--version":
			return arguments
		}
	}
	if separator := slices.Index(arguments, "--"); separator >= 0 {
		return slices.Insert(slices.Clone(arguments), separator, flag)
	}
	return append(slices.Clone(arguments), flag)
}

// traceFlag is the flag a GOATEST_TRACE value asks for: the default trace
// location for the enabled forms, a named directory for anything else, and no
// trace at all when the variable is unset, empty, or explicitly disabled, so
// that a job can neutralize a setting it inherited.
func traceFlag(value string) (string, bool) {
	switch value {
	case "", "0", "false":
		return "", false
	case "1", "true":
		return "--trace", true
	default:
		return "--trace=" + value, true
	}
}

// keepTempFlag is the flag a GOATEST_KEEP_TEMP value asks for. Keeping
// temporary directories is asked for and never configured, so an unrecognized
// value becomes a flag carrying it: the command layer is the one authority on
// what a flag may say, and it refuses that one rather than guessing which of
// keeping and removing a job meant.
func keepTempFlag(value string) (string, bool) {
	switch value {
	case "", "0", "false":
		return "", false
	case "1", "true":
		return "--keep-temp", true
	default:
		return "--keep-temp=" + value, true
	}
}

func runWithService(arguments []string, service cli.Service) int {
	return runWithServiceWriters(arguments, service, os.Stdout, os.Stderr)
}

func runWithServiceWriters(arguments []string, service cli.Service, stdout, stderr io.Writer) int {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)
	return runWithSignals(arguments, service, signals, stdout, stderr)
}

func runWithSignals(arguments []string, service cli.Service, signals <-chan os.Signal, stdout, stderr io.Writer) int {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var received atomic.Int32
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case value := <-signals:
			if signalValue, ok := value.(syscall.Signal); ok {
				received.Store(int32(signalValue))
			}
			cancel()
		case <-done:
		}
	}()
	code := cli.Run(ctx, arguments, stdout, stderr, service)
	var receivedSignal os.Signal
	if value := received.Load(); value != 0 {
		receivedSignal = syscall.Signal(value)
	}
	return interruptedExit(code, receivedSignal)
}

func interruptedExit(code int, received os.Signal) int {
	if code == cli.ExitInterrupted && received == syscall.SIGTERM {
		return cli.ExitTerminated
	}
	return code
}
