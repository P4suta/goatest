// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package testkit

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/P4suta/goatest/internal/advisorylock"
	"github.com/P4suta/goatest/internal/filemode"
)

func TestOpenHeavyLockUsesTheCommittedCoordinationFile(t *testing.T) {
	t.Parallel()
	file, err := openHeavyLock()
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(file.Name()) != filepath.Base(heavyLockRelativePath) {
		t.Fatalf("lock path = %q", file.Name())
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestWaitForHeavyLockHonorsCancellationAndRelease(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "heavy.lock")
	holder, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, filemode.ReadableFile)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = holder.Close() })
	contender, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = contender.Close() })
	if locked, err := advisorylock.Try(holder); err != nil || !locked {
		t.Fatalf("holder lock = (%t, %v)", locked, err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if locked, err := waitForHeavyLock(ctx, contender); locked || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled contender = (%t, %v)", locked, err)
	}
	if err := advisorylock.Release(holder); err != nil {
		t.Fatal(err)
	}
	if locked, err := waitForHeavyLock(t.Context(), contender); err != nil || !locked {
		t.Fatalf("released contender = (%t, %v)", locked, err)
	}
	if err := advisorylock.Release(contender); err != nil {
		t.Fatal(err)
	}
}
