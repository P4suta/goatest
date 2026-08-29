// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"context"
	"fmt"
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
	if len(arguments) == 1 && arguments[0] == "--version" {
		_, _ = fmt.Fprintf(os.Stdout, "goatest %s\n", assure.GoatestVersion)
		return cli.ExitAssured
	}
	return runWithService(arguments, app.Service{Root: ".", Progress: os.Stderr})
}

func runWithService(arguments []string, service cli.Service) int {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)
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
	code := cli.Run(ctx, arguments, os.Stdout, os.Stderr, service)
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
