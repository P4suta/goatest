// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build !windows

package ui

import "io"

// EnableVirtualTerminal reports that ANSI escape processing is available: on
// every platform but Windows a terminal accepts escapes as it is.
func EnableVirtualTerminal(io.Writer) bool { return true }
