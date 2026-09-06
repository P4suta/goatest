// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package advisorylock_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/P4suta/goatest/internal/advisorylock"
	"github.com/P4suta/goatest/internal/filemode"
)

func open(t *testing.T, path string) *os.File {
	t.Helper()
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, filemode.ReadableFile)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	return file
}

func TestTryRefusesADescriptionWhileAnotherHoldsTheLock(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "lock")
	holder, contender := open(t, path), open(t, path)
	if locked, err := advisorylock.Try(holder); err != nil || !locked {
		t.Fatalf("first lock = (%t, %v), want it taken", locked, err)
	}

	if locked, err := advisorylock.Try(contender); err != nil || locked {
		t.Fatalf("lock against a held one = (%t, %v), want it refused without an error", locked, err)
	}
	if err := advisorylock.Release(holder); err != nil {
		t.Fatal(err)
	}
	if locked, err := advisorylock.Try(contender); err != nil || !locked {
		t.Fatalf("lock after the holder released = (%t, %v), want it taken", locked, err)
	}
	if err := advisorylock.Release(contender); err != nil {
		t.Fatal(err)
	}
}

func TestClosingTheFileReleasesTheLock(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "lock")
	holder := open(t, path)
	if locked, err := advisorylock.Try(holder); err != nil || !locked {
		t.Fatalf("first lock = (%t, %v), want it taken", locked, err)
	}

	if err := holder.Close(); err != nil {
		t.Fatal(err)
	}
	contender := open(t, path)
	if locked, err := advisorylock.Try(contender); err != nil || !locked {
		t.Fatalf("lock after the holder closed = (%t, %v), want it taken", locked, err)
	}
	if err := advisorylock.Release(contender); err != nil {
		t.Fatal(err)
	}
}

func TestTryOnADescriptionThatAlreadyHoldsTheLockKeepsIt(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "lock")
	holder, contender := open(t, path), open(t, path)
	if locked, err := advisorylock.Try(holder); err != nil || !locked {
		t.Fatalf("first lock = (%t, %v), want it taken", locked, err)
	}

	if _, err := advisorylock.Try(holder); err != nil {
		t.Fatalf("locking a description that already holds the lock = %v, want no error", err)
	}
	if locked, err := advisorylock.Try(contender); err != nil || locked {
		t.Fatalf("lock against the still-held one = (%t, %v), want it refused without an error", locked, err)
	}
}
