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
	if err := os.WriteFile(filepath.Join(root, "helper-ready"), []byte("ready"), 0o600); err != nil {
		t.Fatal(err)
	}
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(20 * time.Millisecond)
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
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		if time.Now().After(deadline) {
			_ = command.Process.Kill()
			t.Fatalf("lock helper did not become ready: %s", output.String())
		}
		time.Sleep(20 * time.Millisecond)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	waited := make(chan struct{}, 1)
	lease, err := Acquire(ctx, root, func() { waited <- struct{}{} })
	if lease != nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("contended acquire = (%v, %v)", lease, err)
	}
	select {
	case <-waited:
	default:
		t.Fatal("contended lock did not announce its wait")
	}
	if err := os.WriteFile(filepath.Join(root, "helper-release"), []byte("release"), 0o600); err != nil {
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
