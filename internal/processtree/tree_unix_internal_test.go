// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build !windows

package processtree

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
	"testing"
)

func TestPrepareUnixCreatesDedicatedProcessGroup(t *testing.T) {
	command := &exec.Cmd{}
	prepare(command)
	if command.SysProcAttr == nil || !command.SysProcAttr.Setpgid {
		t.Fatalf("SysProcAttr = %+v", command.SysProcAttr)
	}
	if _, err := attach(command); err != nil {
		t.Fatalf("attach = %v", err)
	}
}

func TestKillUnixSignalsGroupThenProcessAndNormalizesGone(t *testing.T) {
	for _, test := range []struct {
		name        string
		groupErr    error
		processErr  error
		wantErr     error
		wantProcess bool
	}{
		{name: "success", wantProcess: true},
		{name: "group gone", groupErr: syscall.ESRCH, wantProcess: true},
		{name: "group failure", groupErr: errors.New("group failed")},
		{name: "process done", processErr: os.ErrProcessDone, wantProcess: true},
		{name: "process failure", processErr: errors.New("process failed"), wantProcess: true},
	} {
		if test.name == "group failure" {
			test.wantErr = test.groupErr
		}
		if test.name == "process failure" {
			test.wantErr = test.processErr
		}
		t.Run(test.name, func(t *testing.T) {
			preserveUnixHooks(t)
			process := &os.Process{Pid: 321}
			groupCalls, processCalls := 0, 0
			killUnixGroup = func(pid int, signal syscall.Signal) error {
				groupCalls++
				if pid != -process.Pid || signal != syscall.SIGKILL {
					t.Fatalf("group kill args = (%d, %v)", pid, signal)
				}
				return test.groupErr
			}
			killUnixProcess = func(got *os.Process) error {
				processCalls++
				if got != process {
					t.Fatalf("process = %p, want %p", got, process)
				}
				return test.processErr
			}
			err := kill(&exec.Cmd{Process: process}, platformHandle{})
			if !errors.Is(err, test.wantErr) || (test.wantErr == nil && err != nil) {
				t.Fatalf("kill error = %v, want %v", err, test.wantErr)
			}
			if groupCalls != 1 || processCalls != boolIntUnix(test.wantProcess) {
				t.Fatalf("calls = group %d process %d", groupCalls, processCalls)
			}
		})
	}

	preserveUnixHooks(t)
	calls := 0
	killUnixGroup = func(int, syscall.Signal) error { calls++; return nil }
	killUnixProcess = func(*os.Process) error { calls++; return nil }
	if err := kill(&exec.Cmd{}, platformHandle{}); err != nil || calls != 0 {
		t.Fatalf("kill(nil process) = %v, calls=%d", err, calls)
	}
}

func TestCloseTreeUnixSignalsRemainingGroupAndNormalizesGone(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
		want error
	}{
		{name: "success"},
		{name: "gone", err: syscall.ESRCH},
		{name: "failure", err: errors.New("close failed")},
	} {
		if test.name == "failure" {
			test.want = test.err
		}
		t.Run(test.name, func(t *testing.T) {
			preserveUnixHooks(t)
			process := &os.Process{Pid: 321}
			calls := 0
			killUnixGroup = func(pid int, signal syscall.Signal) error {
				calls++
				if pid != -process.Pid || signal != syscall.SIGKILL {
					t.Fatalf("group kill args = (%d, %v)", pid, signal)
				}
				return test.err
			}
			err := closeTree(&exec.Cmd{Process: process}, platformHandle{})
			if !errors.Is(err, test.want) || (test.want == nil && err != nil) || calls != 1 {
				t.Fatalf("closeTree = %v, calls=%d, want %v", err, calls, test.want)
			}
		})
	}

	preserveUnixHooks(t)
	calls := 0
	killUnixGroup = func(int, syscall.Signal) error { calls++; return nil }
	for _, command := range []*exec.Cmd{nil, {}} {
		if err := closeTree(command, platformHandle{}); err != nil {
			t.Fatalf("closeTree(nil-like) = %v", err)
		}
	}
	if calls != 0 {
		t.Fatalf("group calls for nil-like commands = %d", calls)
	}
}

func preserveUnixHooks(t *testing.T) {
	t.Helper()
	groupHook, processHook := killUnixGroup, killUnixProcess
	t.Cleanup(func() { killUnixGroup, killUnixProcess = groupHook, processHook })
}

func boolIntUnix(value bool) int {
	if value {
		return 1
	}
	return 0
}
