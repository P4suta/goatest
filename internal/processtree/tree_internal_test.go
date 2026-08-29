// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package processtree

import (
	"errors"
	"os"
	"os/exec"
	"sync"
	"testing"
)

func TestStartRejectsNilCommand(t *testing.T) {
	if tree, err := Start(nil); err == nil || tree != nil {
		t.Fatalf("Start(nil) = (%v, %v)", tree, err)
	}
}

func TestStartRunsLifecycleAndCleansUpAttachFailure(t *testing.T) {
	t.Run("start failure", func(t *testing.T) {
		preserveTreeHooks(t)
		sentinel := errors.New("start failed")
		prepared := false
		prepareTreeCommand = func(*exec.Cmd) { prepared = true }
		startTreeCommand = func(*exec.Cmd) error { return sentinel }
		attachTreeCommand = func(*exec.Cmd) (platformHandle, error) {
			t.Fatal("attach called after start failure")
			var handle platformHandle
			return handle, nil
		}
		tree, err := Start(&exec.Cmd{})
		if tree != nil || !errors.Is(err, sentinel) || !prepared {
			t.Fatalf("Start = (%v, %v), prepared=%t", tree, err, prepared)
		}
	})

	t.Run("attach failure", func(t *testing.T) {
		preserveTreeHooks(t)
		sentinel := errors.New("attach failed")
		command := &exec.Cmd{}
		prepared, killed, waited := false, false, false
		prepareTreeCommand = func(got *exec.Cmd) {
			prepared = true
			if got != command {
				t.Fatalf("prepared command = %p, want %p", got, command)
			}
		}
		startTreeCommand = func(got *exec.Cmd) error {
			got.Process = &os.Process{Pid: 1234}
			return nil
		}
		attachTreeCommand = func(got *exec.Cmd) (platformHandle, error) {
			if got != command {
				t.Fatalf("attached command = %p, want %p", got, command)
			}
			var handle platformHandle
			return handle, sentinel
		}
		killStartedCommand = func(got *exec.Cmd) error {
			killed = true
			if got != command {
				t.Fatalf("killed command = %p, want %p", got, command)
			}
			return errors.New("ignored kill failure")
		}
		waitStartedCommand = func(got *exec.Cmd) error {
			waited = true
			if got != command {
				t.Fatalf("waited command = %p, want %p", got, command)
			}
			return errors.New("ignored wait failure")
		}
		tree, err := Start(command)
		if tree != nil || !errors.Is(err, sentinel) || !prepared || !killed || !waited {
			t.Fatalf("Start = (%v, %v), prepared=%t killed=%t waited=%t", tree, err, prepared, killed, waited)
		}
	})

	t.Run("success", func(t *testing.T) {
		preserveTreeHooks(t)
		command := &exec.Cmd{}
		var handle platformHandle
		prepareTreeCommand = func(*exec.Cmd) {}
		startTreeCommand = func(got *exec.Cmd) error {
			got.Process = &os.Process{Pid: 4321}
			return nil
		}
		attachTreeCommand = func(got *exec.Cmd) (platformHandle, error) {
			if got != command {
				t.Fatalf("attached command = %p, want %p", got, command)
			}
			return handle, nil
		}
		tree, err := Start(command)
		if err != nil || tree == nil || tree.command != command || tree.handle != handle {
			t.Fatalf("Start = (%+v, %v)", tree, err)
		}
	})
}

func TestTreeKillIsFailClosedAndNormalizesFinishedProcess(t *testing.T) {
	preserveTreeHooks(t)
	calls := 0
	killTreeCommand = func(*exec.Cmd, platformHandle) error {
		calls++
		return nil
	}
	for _, tree := range []*Tree{nil, {}, {command: &exec.Cmd{}}} {
		if err := tree.Kill(); err != nil {
			t.Fatalf("nil-like Tree.Kill = %v", err)
		}
	}
	if calls != 0 {
		t.Fatalf("kill calls for nil-like trees = %d", calls)
	}

	failure := errors.New("kill failed")
	for _, test := range []struct {
		name string
		err  error
		want error
	}{
		{name: "success"},
		{name: "already done", err: os.ErrProcessDone},
		{name: "failure", err: failure, want: failure},
	} {
		t.Run(test.name, func(t *testing.T) {
			preserveTreeHooks(t)
			command := &exec.Cmd{Process: &os.Process{Pid: 1234}}
			var handle platformHandle
			killTreeCommand = func(got *exec.Cmd, gotHandle platformHandle) error {
				if got != command || gotHandle != handle {
					t.Fatalf("kill args = (%p, %v)", got, gotHandle)
				}
				return test.err
			}
			err := (&Tree{command: command, handle: handle}).Kill()
			if !errors.Is(err, test.want) || (test.want == nil && err != nil) {
				t.Fatalf("Kill error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestTreeCloseReleasesExactlyOnceAndRetainsResult(t *testing.T) {
	if err := (*Tree)(nil).Close(); err != nil {
		t.Fatalf("nil Tree.Close = %v", err)
	}
	preserveTreeHooks(t)
	sentinel := errors.New("close failed")
	command := &exec.Cmd{Process: &os.Process{Pid: 1234}}
	var handle platformHandle
	calls := 0
	closeTreeCommand = func(got *exec.Cmd, gotHandle platformHandle) error {
		calls++
		if got != command || gotHandle != handle {
			t.Fatalf("close args = (%p, %v)", got, gotHandle)
		}
		return sentinel
	}
	tree := &Tree{command: command, handle: handle}
	const goroutines = 16
	errorsSeen := make(chan error, goroutines)
	var group sync.WaitGroup
	for range goroutines {
		group.Add(1)
		go func() {
			defer group.Done()
			errorsSeen <- tree.Close()
		}()
	}
	group.Wait()
	close(errorsSeen)
	for err := range errorsSeen {
		if !errors.Is(err, sentinel) {
			t.Errorf("Close error = %v, want %v", err, sentinel)
		}
	}
	if calls != 1 {
		t.Fatalf("close calls = %d, want 1", calls)
	}
}

func preserveTreeHooks(t *testing.T) {
	t.Helper()
	prepareHook, startHook, attachHook := prepareTreeCommand, startTreeCommand, attachTreeCommand
	killStartedHook, waitStartedHook := killStartedCommand, waitStartedCommand
	killHook, closeHook := killTreeCommand, closeTreeCommand
	t.Cleanup(func() {
		prepareTreeCommand, startTreeCommand, attachTreeCommand = prepareHook, startHook, attachHook
		killStartedCommand, waitStartedCommand = killStartedHook, waitStartedHook
		killTreeCommand, closeTreeCommand = killHook, closeHook
	})
}
