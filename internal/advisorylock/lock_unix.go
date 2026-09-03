//go:build !windows

// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package advisorylock

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// Try takes an exclusive lock on one open file without waiting. A lock somebody
// else holds is reported as not taken rather than as a failure: the caller is
// deciding whether to go ahead, and somebody else holding the lock is an
// answer.
func Try(file *os.File) (bool, error) {
	err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
		return false, nil
	}
	return false, err
}

// Release drops the lock without closing the file, for the callers that keep
// the file open afterwards.
func Release(file *os.File) error { return unix.Flock(int(file.Fd()), unix.LOCK_UN) }
