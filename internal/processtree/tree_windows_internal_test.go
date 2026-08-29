// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build windows

package processtree

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestAttachWindowsConfiguresKillOnCloseAndOwnsHandles(t *testing.T) {
	preserveWindowsHooks(t)
	job := windows.Handle(101)
	process := windows.Handle(202)
	command := &exec.Cmd{Process: &os.Process{Pid: 321}}
	var configured windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	createWindowsJobObject = func(*windows.SecurityAttributes, *uint16) (windows.Handle, error) {
		return job, nil
	}
	setWindowsJobInformation = func(got windows.Handle, class uint32, information *windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION, length uint32) (int, error) {
		if got != job || class != windows.JobObjectExtendedLimitInformation || length != uint32(unsafe.Sizeof(configured)) {
			t.Fatalf("SetInformationJobObject args = (%v, %d, %d)", got, class, length)
		}
		configured = *information
		return 1, nil
	}
	openWindowsProcess = func(access uint32, inherit bool, pid uint32) (windows.Handle, error) {
		wantAccess := uint32(windows.PROCESS_SET_QUOTA | windows.PROCESS_TERMINATE)
		if access != wantAccess || inherit || pid != uint32(command.Process.Pid) {
			t.Fatalf("OpenProcess args = (%#x, %t, %d), want (%#x, false, %d)", access, inherit, pid, wantAccess, command.Process.Pid)
		}
		return process, nil
	}
	assignWindowsProcessToJob = func(gotJob, gotProcess windows.Handle) error {
		if gotJob != job || gotProcess != process {
			t.Fatalf("AssignProcessToJobObject args = (%v, %v)", gotJob, gotProcess)
		}
		return nil
	}
	var closed []windows.Handle
	closeWindowsHandle = func(handle windows.Handle) error {
		closed = append(closed, handle)
		return nil
	}

	handle, err := attach(command)
	if err != nil || handle != platformHandle(job) {
		t.Fatalf("attach = (%v, %v), want %v", handle, err, job)
	}
	if configured.BasicLimitInformation.LimitFlags != windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE {
		t.Fatalf("job limit flags = %#x", configured.BasicLimitInformation.LimitFlags)
	}
	if len(closed) != 1 || closed[0] != process {
		t.Fatalf("closed handles = %v, want process only", closed)
	}
}

func TestAttachWindowsPropagatesEveryFailureAndClosesOwnedHandles(t *testing.T) {
	for _, stage := range []string{"create", "configure", "open", "assign"} {
		t.Run(stage, func(t *testing.T) {
			preserveWindowsHooks(t)
			sentinel := errors.New(stage + " failed")
			job := windows.Handle(101)
			process := windows.Handle(202)
			createWindowsJobObject = func(*windows.SecurityAttributes, *uint16) (windows.Handle, error) {
				if stage == "create" {
					return 0, sentinel
				}
				return job, nil
			}
			setWindowsJobInformation = func(windows.Handle, uint32, *windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION, uint32) (int, error) {
				if stage == "configure" {
					return 0, sentinel
				}
				return 1, nil
			}
			openWindowsProcess = func(uint32, bool, uint32) (windows.Handle, error) {
				if stage == "open" {
					return 0, sentinel
				}
				return process, nil
			}
			assignWindowsProcessToJob = func(windows.Handle, windows.Handle) error {
				if stage == "assign" {
					return sentinel
				}
				return nil
			}
			var closed []windows.Handle
			closeWindowsHandle = func(handle windows.Handle) error {
				closed = append(closed, handle)
				return errors.New("ignored close failure")
			}

			handle, err := attach(&exec.Cmd{Process: &os.Process{Pid: 321}})
			if handle != 0 || !errors.Is(err, sentinel) {
				t.Fatalf("attach = (%v, %v), want zero and %v", handle, err, sentinel)
			}
			var wantClosed []windows.Handle
			switch stage {
			case "configure", "open":
				wantClosed = []windows.Handle{job}
			case "assign":
				wantClosed = []windows.Handle{process, job}
			}
			if fmt.Sprint(closed) != fmt.Sprint(wantClosed) {
				t.Fatalf("closed handles = %v, want %v", closed, wantClosed)
			}
		})
	}
}

func TestKillWindowsCombinesJobAndProcessFailures(t *testing.T) {
	for _, test := range []struct {
		name        string
		handle      platformHandle
		process     *os.Process
		jobErr      error
		processErr  error
		wantJob     bool
		wantProcess bool
	}{
		{name: "empty"},
		{name: "job only", handle: platformHandle(101), wantJob: true},
		{name: "process only", process: &os.Process{Pid: 321}, wantProcess: true},
		{name: "process done", process: &os.Process{Pid: 321}, processErr: os.ErrProcessDone, wantProcess: true},
		{name: "both fail", handle: platformHandle(101), process: &os.Process{Pid: 321}, jobErr: errors.New("job failed"), processErr: errors.New("process failed"), wantJob: true, wantProcess: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			preserveWindowsHooks(t)
			jobCalls, processCalls := 0, 0
			terminateWindowsJob = func(handle windows.Handle, exitCode uint32) error {
				jobCalls++
				if handle != windows.Handle(test.handle) || exitCode != 1 {
					t.Fatalf("TerminateJobObject args = (%v, %d)", handle, exitCode)
				}
				return test.jobErr
			}
			killWindowsProcess = func(process *os.Process) error {
				processCalls++
				if process != test.process {
					t.Fatalf("killed process = %p, want %p", process, test.process)
				}
				return test.processErr
			}
			err := kill(&exec.Cmd{Process: test.process}, test.handle)
			if jobCalls != boolInt(test.wantJob) || processCalls != boolInt(test.wantProcess) {
				t.Fatalf("calls = job %d process %d", jobCalls, processCalls)
			}
			if test.processErr == os.ErrProcessDone {
				if err != nil {
					t.Fatalf("process-done kill error = %v", err)
				}
				return
			}
			if !errors.Is(err, test.jobErr) || !errors.Is(err, test.processErr) {
				t.Fatalf("kill error = %v, want job=%v process=%v", err, test.jobErr, test.processErr)
			}
		})
	}
}

func TestCloseTreeWindowsHandlesZeroAndPropagatesClose(t *testing.T) {
	preserveWindowsHooks(t)
	calls := 0
	closeWindowsHandle = func(windows.Handle) error {
		calls++
		return nil
	}
	if err := closeTree(nil, 0); err != nil || calls != 0 {
		t.Fatalf("closeTree(zero) = %v, calls=%d", err, calls)
	}
	sentinel := errors.New("close failed")
	closeWindowsHandle = func(handle windows.Handle) error {
		calls++
		if handle != 101 {
			t.Fatalf("closed handle = %v", handle)
		}
		return sentinel
	}
	if err := closeTree(nil, platformHandle(101)); !errors.Is(err, sentinel) || calls != 1 {
		t.Fatalf("closeTree = %v, calls=%d", err, calls)
	}
}

const processTreeHelperEnvironment = "GOATEST_PROCESS_TREE_HELPER"

func TestCloseTreeTerminatesDescendantAfterParentExits(t *testing.T) {
	if mode := os.Getenv(processTreeHelperEnvironment); mode != "" {
		runProcessTreeHelper(t, mode)
		return
	}

	command := exec.Command(os.Args[0], "-test.run=^TestCloseTreeTerminatesDescendantAfterParentExits$", "-test.timeout=0")
	command.Env = helperEnvironment("parent")
	stdin, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	tree, err := Start(command)
	if err != nil {
		t.Fatal(err)
	}
	parentWaited := false
	t.Cleanup(func() {
		_ = tree.Kill()
		_ = tree.Close()
		if !parentWaited {
			_ = command.Wait()
		}
	})

	pidResult := make(chan int, 1)
	readError := make(chan error, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		if !scanner.Scan() {
			readError <- fmt.Errorf("read child pid: %w", scanner.Err())
			return
		}
		pid, conversionErr := strconv.Atoi(strings.TrimSpace(scanner.Text()))
		if conversionErr != nil {
			readError <- conversionErr
			return
		}
		pidResult <- pid
		for scanner.Scan() {
		}
	}()
	if _, err := stdin.Write([]byte{1}); err != nil {
		t.Fatal(err)
	}
	var childPID int
	select {
	case childPID = <-pidResult:
	case err := <-readError:
		t.Fatal(err)
	case <-time.After(15 * time.Second):
		t.Fatal("timed out waiting for helper child pid")
	}
	childHandle, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_TERMINATE, false, uint32(childPID))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = windows.TerminateProcess(childHandle, 1)
		_ = windows.CloseHandle(childHandle)
	})
	if err := stdin.Close(); err != nil {
		t.Fatal(err)
	}
	if err := command.Wait(); err != nil {
		t.Fatalf("parent wait: %v", err)
	}
	parentWaited = true
	if event, err := windows.WaitForSingleObject(childHandle, 0); err != nil || event != uint32(windows.WAIT_TIMEOUT) {
		t.Fatalf("child before Close = event %#x, err %v", event, err)
	}
	if err := tree.Close(); err != nil {
		t.Fatal(err)
	}
	if event, err := windows.WaitForSingleObject(childHandle, 2_000); err != nil || event != uint32(windows.WAIT_OBJECT_0) {
		t.Fatalf("child after Close = event %#x, err %v", event, err)
	}
}

func runProcessTreeHelper(t *testing.T, mode string) {
	t.Helper()
	if mode == "child" {
		for {
			time.Sleep(time.Hour)
		}
	}
	if mode != "parent" {
		t.Fatalf("unknown helper mode %q", mode)
	}
	var start [1]byte
	if _, err := os.Stdin.Read(start[:]); err != nil {
		t.Fatal(err)
	}
	child := exec.Command(os.Args[0], "-test.run=^TestCloseTreeTerminatesDescendantAfterParentExits$", "-test.timeout=0")
	child.Env = helperEnvironment("child")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	fmt.Println(child.Process.Pid)
	_ = child.Process.Release()
	var release [1]byte
	_, _ = os.Stdin.Read(release[:])
}

func helperEnvironment(mode string) []string {
	prefix := processTreeHelperEnvironment + "="
	environment := make([]string, 0, len(os.Environ())+1)
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(strings.ToUpper(entry), strings.ToUpper(prefix)) {
			environment = append(environment, entry)
		}
	}
	return append(environment, prefix+mode)
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func preserveWindowsHooks(t *testing.T) {
	t.Helper()
	createHook, setHook := createWindowsJobObject, setWindowsJobInformation
	openHook, assignHook := openWindowsProcess, assignWindowsProcessToJob
	closeHook, terminateHook, killHook := closeWindowsHandle, terminateWindowsJob, killWindowsProcess
	t.Cleanup(func() {
		createWindowsJobObject, setWindowsJobInformation = createHook, setHook
		openWindowsProcess, assignWindowsProcessToJob = openHook, assignHook
		closeWindowsHandle, terminateWindowsJob, killWindowsProcess = closeHook, terminateHook, killHook
	})
}
