//go:build !windows

// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package ui

import (
	"bytes"
	"testing"
)

func TestVirtualTerminalIsNativeOutsideWindows(t *testing.T) {
	if !EnableVirtualTerminal(&bytes.Buffer{}) {
		t.Fatal("non-Windows terminal refused ANSI escape processing")
	}
}
