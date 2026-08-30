//go:build windows

// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package app

import (
	"path/filepath"

	"golang.org/x/sys/windows"
)

func diskFreeBytes(path string) (uint64, error) {
	pointer, err := windows.UTF16PtrFromString(filepath.Clean(path))
	if err != nil {
		return 0, err
	}
	var available uint64
	if err := windows.GetDiskFreeSpaceEx(pointer, &available, nil, nil); err != nil {
		return 0, err
	}
	return available, nil
}
