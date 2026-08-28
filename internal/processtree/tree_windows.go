// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build windows

package processtree

import (
	"errors"
	"os"
	"os/exec"
	"unsafe"

	"golang.org/x/sys/windows"
)

type platformHandle windows.Handle

func prepare(*exec.Cmd) {}

func attach(command *exec.Cmd) (platformHandle, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return 0, err
	}
	information := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	information.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(
		job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&information)), uint32(unsafe.Sizeof(information)),
	); err != nil {
		_ = windows.CloseHandle(job)
		return 0, err
	}
	process, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(command.Process.Pid))
	if err != nil {
		_ = windows.CloseHandle(job)
		return 0, err
	}
	err = windows.AssignProcessToJobObject(job, process)
	_ = windows.CloseHandle(process)
	if err != nil {
		_ = windows.CloseHandle(job)
		return 0, err
	}
	return platformHandle(job), nil
}

func kill(command *exec.Cmd, handle platformHandle) error {
	var result error
	if handle != 0 {
		result = windows.TerminateJobObject(windows.Handle(handle), 1)
	}
	if command.Process != nil {
		if err := command.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			result = errors.Join(result, err)
		}
	}
	return result
}

func closeHandle(handle platformHandle) error {
	if handle == 0 {
		return nil
	}
	return windows.CloseHandle(windows.Handle(handle))
}
