// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package cache

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/P4suta/goatest/internal/filemode"
)

const (
	cacheLockHelperDeadline = 10 * time.Second
	cacheLockPollInterval   = 20 * time.Millisecond
	cacheLockReadyDeadline  = 5 * time.Second
)

func TestCacheLockHelper(t *testing.T) {
	root := os.Getenv("GOATEST_CACHE_LOCK_HELPER_ROOT")
	if root == "" {
		return
	}
	lease, err := Acquire(context.Background(), root, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lease.Release() }()
	if err := os.WriteFile(filepath.Join(root, "helper-ready"), []byte("ready"), filemode.PrivateFile); err != nil {
		t.Fatal(err)
	}
	deadline := time.NewTimer(cacheLockHelperDeadline)
	defer deadline.Stop()
	ticker := time.NewTicker(cacheLockPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-deadline.C:
			t.Fatal("parent never released helper")
		case <-ticker.C:
			if _, err := os.Stat(filepath.Join(root, "helper-release")); err == nil {
				return
			}
		}
	}
}

func TestAcquireStateMachine(t *testing.T) {
	t.Parallel()
	failure := errors.New("operation failure")
	panicOpen := func(string, int, os.FileMode) (*os.File, error) { panic("unexpected open") }
	panicTry := func(*os.File) (bool, error) { panic("unexpected try") }
	panicWait := func(context.Context) error { panic("unexpected wait") }

	t.Run("directory failure", func(t *testing.T) {
		_, err := acquire(t.Context(), t.TempDir(), nil, lockOperations{
			mkdirAll: func(string, os.FileMode) error { return failure },
			openFile: panicOpen,
			try:      panicTry,
			wait:     panicWait,
		})
		if !errors.Is(err, failure) {
			t.Fatalf("Acquire error = %v, want %v", err, failure)
		}
	})

	t.Run("open contract and failure", func(t *testing.T) {
		root := t.TempDir()
		var path string
		var flags int
		var mode os.FileMode
		_, err := acquire(t.Context(), root, nil, lockOperations{
			mkdirAll: func(string, os.FileMode) error { return nil },
			openFile: func(gotPath string, gotFlags int, gotMode os.FileMode) (*os.File, error) {
				path, flags, mode = gotPath, gotFlags, gotMode
				return nil, failure
			},
			try:  panicTry,
			wait: panicWait,
		})
		if !errors.Is(err, failure) || path != filepath.Join(root, lockFileName) || flags != os.O_CREATE|os.O_RDWR || mode != filemode.ReadableFile {
			t.Fatalf("Acquire = (%v, %q, %d, %v)", err, path, flags, mode)
		}
	})

	t.Run("lock failure", func(t *testing.T) {
		file, err := os.CreateTemp(t.TempDir(), "lock")
		if err != nil {
			t.Fatal(err)
		}
		_, err = acquire(t.Context(), t.TempDir(), nil, lockOperations{
			mkdirAll: func(string, os.FileMode) error { return nil },
			openFile: func(string, int, os.FileMode) (*os.File, error) { return file, nil },
			try:      func(*os.File) (bool, error) { return false, failure },
			wait:     panicWait,
		})
		if !errors.Is(err, failure) {
			t.Fatalf("Acquire error = %v, want %v", err, failure)
		}
		if _, err := file.Stat(); !errors.Is(err, os.ErrClosed) {
			t.Fatalf("lock file remained open: %v", err)
		}
	})

	t.Run("lock success", func(t *testing.T) {
		file, err := os.CreateTemp(t.TempDir(), "lock")
		if err != nil {
			t.Fatal(err)
		}
		lease, err := acquire(t.Context(), t.TempDir(), func() { panic("unexpected callback") }, lockOperations{
			mkdirAll: func(string, os.FileMode) error { return nil },
			openFile: func(string, int, os.FileMode) (*os.File, error) { return file, nil },
			try:      func(*os.File) (bool, error) { return true, nil },
			wait:     panicWait,
		})
		if err != nil || lease == nil || lease.file != file {
			t.Fatalf("Acquire = (%v, %v), want the opened file", lease, err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("contention", func(t *testing.T) {
		file, err := os.CreateTemp(t.TempDir(), "lock")
		if err != nil {
			t.Fatal(err)
		}
		waited := false
		terminated := false
		announced := false
		lease, err := acquire(t.Context(), t.TempDir(), func() {
			if announced {
				panic("repeated wait announcement")
			}
			announced = true
		}, lockOperations{
			mkdirAll: func(string, os.FileMode) error { return nil },
			openFile: func(string, int, os.FileMode) (*os.File, error) { return file, nil },
			try:      func(*os.File) (bool, error) { return false, nil },
			wait: func(context.Context) error {
				if !waited {
					waited = true
					return nil
				}
				if terminated {
					panic("wait repeated after terminal result")
				}
				terminated = true
				return context.Canceled
			},
		})
		if lease != nil || !errors.Is(err, context.Canceled) || !waited || !terminated || !announced {
			t.Fatalf("Acquire = (%v, %v), waited = %t, terminated = %t, announced = %t", lease, err, waited, terminated, announced)
		}
		if _, err := file.Stat(); !errors.Is(err, os.ErrClosed) {
			t.Fatalf("lock file remained open: %v", err)
		}
	})
}

func TestWaitForCacheLockReturnsTheCancellationCause(t *testing.T) {
	t.Parallel()
	failure := errors.New("stop waiting")
	ctx, cancel := context.WithCancelCause(context.Background())
	cancel(failure)
	if err := waitForCacheLock(ctx); !errors.Is(err, failure) {
		t.Fatalf("waitForCacheLock error = %v, want %v", err, failure)
	}
}

func TestCacheAdvisoryLockExcludesAnotherProcessAndWaitIsInterruptible(t *testing.T) {
	root := t.TempDir()
	command := exec.Command(os.Args[0], "-test.run=^TestCacheLockHelper$")
	command.Env = append(os.Environ(), "GOATEST_CACHE_LOCK_HELPER_ROOT="+root)
	var output bytes.Buffer
	command.Stdout, command.Stderr = &output, &output
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	ready := filepath.Join(root, "helper-ready")
	deadline := time.Now().Add(cacheLockReadyDeadline)
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		if time.Now().After(deadline) {
			_ = command.Process.Kill()
			t.Fatalf("lock helper did not become ready: %s", output.String())
		}
		time.Sleep(cacheLockPollInterval)
	}

	interrupted := errors.New("interrupt contended cache lock")
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(context.Canceled)
	waited := make(chan struct{}, 1)
	lease, err := Acquire(ctx, root, func() {
		waited <- struct{}{}
		cancel(interrupted)
	})
	if lease != nil || !errors.Is(err, interrupted) {
		t.Fatalf("contended acquire = (%v, %v)", lease, err)
	}
	select {
	case <-waited:
	default:
		t.Fatal("contended lock did not announce its wait")
	}
	if err := os.WriteFile(filepath.Join(root, "helper-release"), []byte("release"), filemode.PrivateFile); err != nil {
		t.Fatal(err)
	}
	if err := command.Wait(); err != nil {
		t.Fatalf("lock helper failed: %v\n%s", err, output.String())
	}
	lease, err = Acquire(context.Background(), root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := lease.Release(); err != nil {
		t.Fatal(err)
	}
}
