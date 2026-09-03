// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package advisorylock_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/P4suta/goatest/internal/advisorylock"
)

// open opens one more file description on path. Two descriptions of one file
// are what two processes hold, and the lock contends between them even inside
// one process, which is what lets these tests stay in this one rather than
// spawning a helper.
func open(t *testing.T, path string) *os.File {
	t.Helper()
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
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
	// A lock somebody else holds is the answer the callers act on, so it has to
	// arrive as a refusal rather than as an error.
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
	// A process that is killed mid-run never reaches Release, so the close the
	// operating system performs for it is what has to free the next caller.
	// Nothing here would ever recover otherwise.
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
	// What the second call reports differs by platform: flock re-asserts the
	// lock the description already holds and reports it taken, while a second
	// overlapping LockFileEx on one handle is a lock violation and reports it
	// refused. Only what both agree on is pinned here — it is not an error, and
	// the description still holds the lock afterwards — because pinning either
	// answer would make one platform's behaviour the contract of a package that
	// has to mean the same thing on both.
	if _, err := advisorylock.Try(holder); err != nil {
		t.Fatalf("locking a description that already holds the lock = %v, want no error", err)
	}
	if locked, err := advisorylock.Try(contender); err != nil || locked {
		t.Fatalf("lock against the still-held one = (%t, %v), want it refused without an error", locked, err)
	}
}
