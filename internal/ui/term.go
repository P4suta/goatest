// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package ui

import (
	"io"
	"os"

	"golang.org/x/term"
)

// IsTerminalWriter reports whether a writer is an interactive terminal. Only
// an *os.File can be one; everything else is a pipe, a file, or a buffer, and
// renders deterministic lines.
func IsTerminalWriter(writer io.Writer) bool {
	file, ok := writer.(*os.File)
	if !ok {
		return false
	}
	return term.IsTerminal(int(file.Fd()))
}
