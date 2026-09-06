// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package cache

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/P4suta/goatest/internal/advisorylock"
	"github.com/P4suta/goatest/internal/filemode"
)

const (
	lockFileName          = ".lock"
	lockContentionBackoff = 100 * time.Millisecond
)

type Lease struct {
	file *os.File
	once sync.Once
	err  error
}

func Acquire(ctx context.Context, root string, onWait func()) (*Lease, error) {
	return acquire(ctx, root, onWait, lockOperations{
		mkdirAll: os.MkdirAll,
		openFile: os.OpenFile,
		try:      advisorylock.Try,
		wait:     waitForCacheLock,
	})
}

type lockOperations struct {
	mkdirAll func(string, os.FileMode) error
	openFile func(string, int, os.FileMode) (*os.File, error)
	try      func(*os.File) (bool, error)
	wait     func(context.Context) error
}

func acquire(ctx context.Context, root string, onWait func(), operations lockOperations) (*Lease, error) {
	if err := operations.mkdirAll(root, filemode.ReadableDirectory); err != nil {
		return nil, fmt.Errorf("goatest: create cache lock directory: %w", err)
	}
	file, err := operations.openFile(filepath.Join(root, lockFileName), os.O_CREATE|os.O_RDWR, filemode.ReadableFile)
	if err != nil {
		return nil, fmt.Errorf("goatest: open cache lock: %w", err)
	}
	waiting := false
	for {
		locked, lockErr := operations.try(file)
		if lockErr != nil {
			_ = file.Close()
			return nil, fmt.Errorf("goatest: acquire cache lock: %w", lockErr)
		}
		if locked {
			return &Lease{file: file}, nil
		}
		if !waiting {
			waiting = true
			if onWait != nil {
				onWait()
			}
		}
		if err := operations.wait(ctx); err != nil {
			_ = file.Close()
			return nil, err
		}
	}
}

func waitForCacheLock(ctx context.Context) error {
	timer := time.NewTimer(lockContentionBackoff)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return context.Cause(ctx)
	case <-timer.C:
		return nil
	}
}

func (lease *Lease) Release() error {
	if lease == nil {
		return nil
	}
	lease.once.Do(func() {
		lease.err = advisorylock.Release(lease.file)
		if closeErr := lease.file.Close(); lease.err == nil {
			lease.err = closeErr
		}
	})
	if lease.err != nil {
		return fmt.Errorf("goatest: release cache lock: %w", lease.err)
	}
	return nil
}
