// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build !windows

package processtree

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

type platformHandle struct{}

var (
	killUnixGroup   = syscall.Kill
	killUnixProcess = func(process *os.Process) error { return process.Kill() }
)

func prepare(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func attach(*exec.Cmd) (platformHandle, error) { return platformHandle{}, nil }

func kill(command *exec.Cmd, _ platformHandle) error {
	if command.Process == nil {
		return nil
	}
	err := killUnixGroup(-command.Process.Pid, syscall.SIGKILL)
	if err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	if err := killUnixProcess(command.Process); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}
	return nil
}

func closeTree(command *exec.Cmd, _ platformHandle) error {
	if command == nil || command.Process == nil {
		return nil
	}
	if err := killUnixGroup(-command.Process.Pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	return nil
}
