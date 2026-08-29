// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"

	"github.com/P4suta/goatest/internal/cli"
)

func processSignalCases() []processSignalCase {
	return []processSignalCase{{name: "interrupt", signal: os.Interrupt, want: cli.ExitInterrupted}}
}

func configureSignalProcess(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
}

func sendProcessSignal(pid int, _ os.Signal) error {
	dll, err := syscall.LoadDLL("kernel32.dll")
	if err != nil {
		return err
	}
	defer func() { _ = dll.Release() }()
	generate, err := dll.FindProc("GenerateConsoleCtrlEvent")
	if err != nil {
		return err
	}
	result, _, callErr := generate.Call(syscall.CTRL_BREAK_EVENT, uintptr(pid))
	if result == 0 {
		return fmt.Errorf("GenerateConsoleCtrlEvent: %w", callErr)
	}
	return nil
}
