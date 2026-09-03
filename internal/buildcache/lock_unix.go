//go:build !windows

// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package buildcache

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// tryAdvisoryLock takes an exclusive lock on one open file without waiting. A
// lock somebody else holds is reported as not taken rather than as a failure:
// the caller is deciding whether to collect, and somebody else holding the lock
// is an answer.
func tryAdvisoryLock(file *os.File) (bool, error) {
	err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
		return false, nil
	}
	return false, err
}

func unlockAdvisory(file *os.File) error { return unix.Flock(int(file.Fd()), unix.LOCK_UN) }
