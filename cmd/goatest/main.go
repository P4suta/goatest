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

const (
	traceEnvironmentVariable    = "GOATEST_TRACE"
	keepTempEnvironmentVariable = "GOATEST_KEEP_TEMP"
)

func main() {
	os.Exit(realMain(os.Args[1:]))
}

func realMain(arguments []string) int {
	return realMainStreams(arguments, os.Stdin, os.Stdout, os.Stderr, cliService())
}

func cliService() app.Service {
	return app.Service{
		Root: ".", Progress: os.Stderr, Output: os.Stdout, Interactive: interactiveTerminal,
		Executable: goatestExecutable(), UserCacheDir: os.UserCacheDir, TempDirectory: os.TempDir(),
	}
}

func goatestExecutable() string {
	path, err := os.Executable()
	if err != nil {
		return ""
	}
	return path
}

func interactiveTerminal(writer io.Writer) bool {
	if os.Getenv("TERM") == "dumb" || os.Getenv("NO_COLOR") != "" {
		return false
	}
	return ui.IsTerminalWriter(writer) && ui.EnableVirtualTerminal(writer)
}

func realMainWith(arguments []string, stdout, stderr io.Writer, service cli.Service) int {
	return realMainStreams(arguments, strings.NewReader(""), stdout, stderr, service)
}

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

func withTraceEnvironment(arguments []string, value string) []string {
	flag, requested := traceFlag(value)
	return withEnvironmentFlag(arguments, flag, requested)
}

func withKeepTempEnvironment(arguments []string, value string) []string {
	flag, requested := keepTempFlag(value)
	return withEnvironmentFlag(arguments, flag, requested)
}

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
