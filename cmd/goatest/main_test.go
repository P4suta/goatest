// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"os"
	"syscall"
	"testing"

	"github.com/P4suta/goatest/internal/cli"
)

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
