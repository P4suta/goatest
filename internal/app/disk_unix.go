//go:build !windows

// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package app

import "golang.org/x/sys/unix"

func diskFreeBytes(path string) (uint64, error) {
	var status unix.Statfs_t
	if err := unix.Statfs(path, &status); err != nil {
		return 0, err
	}
	return status.Bavail * uint64(status.Bsize), nil
}
