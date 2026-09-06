// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build !windows

package ui

import "io"

func EnableVirtualTerminal(io.Writer) bool { return true }
