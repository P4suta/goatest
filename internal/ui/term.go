// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package ui

import (
	"io"
	"os"

	"golang.org/x/term"
)

func IsTerminalWriter(writer io.Writer, terminal ...func(int) bool) bool {
	probe := term.IsTerminal
	if len(terminal) > 0 {
		probe = terminal[0]
	}
	file, ok := writer.(*os.File)
	if !ok {
		return false
	}
	return probe(int(file.Fd()))
}
