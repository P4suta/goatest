// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"

	"github.com/P4suta/goatest/internal/app"
	"github.com/P4suta/goatest/internal/assure"
	"github.com/P4suta/goatest/internal/cli"
)

func main() {
	os.Exit(realMain(os.Args[1:]))
}

func realMain(arguments []string) int {
	return realMainWith(arguments, os.Stdout, os.Stderr, app.Service{Root: ".", Progress: os.Stderr})
}

func realMainWith(arguments []string, stdout, stderr io.Writer, service cli.Service) int {
	if len(arguments) == 1 && arguments[0] == "--version" {
		_, _ = fmt.Fprintf(stdout, "goatest %s\n", assure.GoatestVersion)
		return 0
	}
	return runWithServiceWriters(arguments, service, stdout, stderr)
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
