// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package testkit

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/P4suta/goatest/internal/advisorylock"
)

const (
	heavyLockRelativePath = "internal/testkit/testdata/heavy.lock"
	heavyLockRetryDelay   = 10 * time.Millisecond
)

func SerializeHeavy(t *testing.T) {
	t.Helper()
	file, err := openHeavyLock()
	if err != nil {
		t.Fatal(err)
	}
	locked, err := waitForHeavyLock(t.Context(), file)
	if err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if !locked {
		_ = file.Close()
		t.Fatal(t.Context().Err())
	}
	t.Cleanup(func() {
		releaseErr := advisorylock.Release(file)
		closeErr := file.Close()
		if err := errors.Join(releaseErr, closeErr); err != nil {
			t.Errorf("release heavy test lock: %v", err)
		}
	})
}

func openHeavyLock() (*os.File, error) {
	directory, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	for {
		path := filepath.Join(directory, filepath.FromSlash(heavyLockRelativePath))
		file, openErr := os.OpenFile(path, os.O_RDWR, 0)
		if openErr == nil {
			return file, nil
		}
		if !errors.Is(openErr, os.ErrNotExist) {
			return nil, openErr
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			return nil, openErr
		}
		directory = parent
	}
}

func waitForHeavyLock(ctx context.Context, file *os.File) (bool, error) {
	ticker := time.NewTicker(heavyLockRetryDelay)
	defer ticker.Stop()
	for {
		locked, err := advisorylock.Try(file)
		if err != nil || locked {
			return locked, err
		}
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-ticker.C:
		}
	}
}
