// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build windows

package ui

import (
	"io"
	"os"

	"golang.org/x/sys/windows"
)

type virtualTerminalOperations struct {
	get func(windows.Handle, *uint32) error
	set func(windows.Handle, uint32) error
}

// EnableVirtualTerminal turns on ANSI escape processing for a console writer
// and reports whether in-place rendering can work there. Windows Terminal
// processes escapes as it is; a legacy console needs the mode set, and one
// that refuses it gets deterministic plain lines instead of escape litter.
func EnableVirtualTerminal(writer io.Writer, injected ...virtualTerminalOperations) bool {
	operations := virtualTerminalOperations{get: windows.GetConsoleMode, set: windows.SetConsoleMode}
	if len(injected) > 0 {
		operations = injected[0]
	}
	file, ok := writer.(*os.File)
	if !ok {
		return true
	}
	handle := windows.Handle(file.Fd())
	var mode uint32
	if err := operations.get(handle, &mode); err != nil {
		// Not a console of this process; whatever reads the stream owns its
		// own escape processing.
		return true
	}
	if mode&windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING != 0 {
		return true
	}
	return operations.set(handle, mode|windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING) == nil
}
