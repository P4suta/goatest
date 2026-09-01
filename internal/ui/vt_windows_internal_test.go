//go:build windows

// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package ui

import (
	"bytes"
	"errors"
	"os"
	"testing"

	"golang.org/x/sys/windows"
)

func TestWindowsVirtualTerminalAcceptsOwnedRenderersAndRegularFiles(t *testing.T) {
	if !EnableVirtualTerminal(&bytes.Buffer{}) {
		t.Fatal("non-file renderer was refused")
	}
	file, err := os.CreateTemp(t.TempDir(), "regular-*")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	if !EnableVirtualTerminal(file) {
		t.Fatal("regular file's downstream escape processor was refused")
	}

	t.Run("console query failure delegates to downstream", func(t *testing.T) {
		setCalled := false
		if !EnableVirtualTerminal(file, virtualTerminalOperations{
			get: func(windows.Handle, *uint32) error { return errors.New("not a console") },
			set: func(windows.Handle, uint32) error { setCalled = true; return nil },
		}) || setCalled {
			t.Fatal("query failure did not return without changing console mode")
		}
	})

	t.Run("existing virtual terminal mode is retained", func(t *testing.T) {
		setCalled := false
		if !EnableVirtualTerminal(file, virtualTerminalOperations{
			get: func(_ windows.Handle, mode *uint32) error {
				*mode = windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING
				return nil
			},
			set: func(windows.Handle, uint32) error { setCalled = true; return nil },
		}) || setCalled {
			t.Fatal("enabled console mode was changed")
		}
	})

	t.Run("missing virtual terminal mode is enabled", func(t *testing.T) {
		initial := uint32(windows.ENABLE_PROCESSED_OUTPUT)
		var gotHandle windows.Handle
		var gotMode uint32
		if !EnableVirtualTerminal(file, virtualTerminalOperations{
			get: func(_ windows.Handle, mode *uint32) error { *mode = initial; return nil },
			set: func(handle windows.Handle, mode uint32) error { gotHandle, gotMode = handle, mode; return nil },
		}) {
			t.Fatal("settable console mode was refused")
		}
		if gotHandle != windows.Handle(file.Fd()) || gotMode != initial|windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING {
			t.Fatalf("SetConsoleMode(%d, %#x), want (%d, %#x)", gotHandle, gotMode, file.Fd(), initial|windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING)
		}
	})

	t.Run("console mode set failure is refused", func(t *testing.T) {
		if EnableVirtualTerminal(file, virtualTerminalOperations{
			get: func(windows.Handle, *uint32) error { return nil },
			set: func(windows.Handle, uint32) error { return errors.New("access denied") },
		}) {
			t.Fatal("console mode set failure was accepted")
		}
	})
}
