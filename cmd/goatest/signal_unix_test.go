//go:build !windows

// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"os"
	"os/exec"
	"syscall"

	"github.com/P4suta/goatest/internal/cli"
)

func processSignalCases() []processSignalCase {
	return []processSignalCase{
		{name: "interrupt", signal: os.Interrupt, want: cli.ExitInterrupted},
		{name: "termination", signal: syscall.SIGTERM, want: cli.ExitTerminated},
	}
}

func configureSignalProcess(_ *exec.Cmd) {}

func sendProcessSignal(pid int, signal os.Signal) error {
	process, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return process.Signal(signal)
}
