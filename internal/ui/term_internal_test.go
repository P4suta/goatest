// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package ui

import (
	"bytes"
	"os"
	"testing"
)

func TestTerminalProbeRejectsBuffersAndRegularFiles(t *testing.T) {
	called := false
	if IsTerminalWriter(&bytes.Buffer{}, func(int) bool { called = true; return true }) {
		t.Fatal("buffer reported as a terminal")
	}
	if called {
		t.Fatal("terminal probe was called for a non-file writer")
	}
	file, err := os.CreateTemp(t.TempDir(), "regular-*")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	if IsTerminalWriter(file) {
		t.Fatal("regular file reported as a terminal")
	}
	if !IsTerminalWriter(file, func(fd int) bool {
		if uintptr(fd) != file.Fd() {
			t.Fatalf("terminal probe descriptor = %d, want %d", fd, file.Fd())
		}
		return true
	}) {
		t.Fatal("injected terminal file was refused")
	}
	if IsTerminalWriter(file, func(int) bool { return false }) {
		t.Fatal("injected non-terminal file was accepted")
	}
}
