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

var (
	createWindowsJobObject   = windows.CreateJobObject
	setWindowsJobInformation = func(job windows.Handle, class uint32, information *windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION, length uint32) (int, error) {
		return windows.SetInformationJobObject(job, class, uintptr(unsafe.Pointer(information)), length)
	}
	openWindowsProcess        = windows.OpenProcess
	assignWindowsProcessToJob = windows.AssignProcessToJobObject
	closeWindowsHandle        = windows.CloseHandle
	terminateWindowsJob       = windows.TerminateJobObject
	killWindowsProcess        = func(process *os.Process) error { return process.Kill() }
)

func prepare(*exec.Cmd) {}

func attach(command *exec.Cmd) (platformHandle, error) {
	job, err := createWindowsJobObject(nil, nil)
	if err != nil {
		return 0, err
	}
	information := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	information.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := setWindowsJobInformation(
		job, windows.JobObjectExtendedLimitInformation,
		&information, uint32(unsafe.Sizeof(information)),
	); err != nil {
		_ = closeWindowsHandle(job)
		return 0, err
	}
	process, err := openWindowsProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(command.Process.Pid))
	if err != nil {
		_ = closeWindowsHandle(job)
		return 0, err
	}
	err = assignWindowsProcessToJob(job, process)
	_ = closeWindowsHandle(process)
	if err != nil {
		_ = closeWindowsHandle(job)
		return 0, err
	}
	return platformHandle(job), nil
}

func kill(command *exec.Cmd, handle platformHandle) error {
	var result error
	if handle != 0 {
		result = terminateWindowsJob(windows.Handle(handle), 1)
	}
	if command.Process != nil {
		if err := killWindowsProcess(command.Process); err != nil && !errors.Is(err, os.ErrProcessDone) {
			result = errors.Join(result, err)
		}
	}
	return result
}

func closeTree(_ *exec.Cmd, handle platformHandle) error {
	if handle == 0 {
		return nil
	}
	return closeWindowsHandle(windows.Handle(handle))
}
