// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package ui_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/P4suta/goatest/internal/ui"
)

// The plain line is a deterministic contract: one byte of drift here breaks
// every consumer that parses progress lines.
func TestPlainRendersTheDeterministicNoteLine(t *testing.T) {
	var buffer bytes.Buffer
	notes := ui.NewPlain(&buffer)
	notes.Note("snapshot", "captured")
	notes.Close()
	if got, want := buffer.String(), "goatest: snapshot           captured\n"; got != want {
		t.Fatalf("plain line = %q, want %q", got, want)
	}
}

func TestPlainEscapesTerminalControlCharactersOntoOneLine(t *testing.T) {
	var buffer bytes.Buffer
	notes := ui.NewPlain(&buffer)
	notes.Note("phase\nforged", "detail\x1b[31m")
	got := buffer.String()
	if strings.Count(got, "\n") != 1 || strings.ContainsAny(got, "\r\x1b\t") {
		t.Fatalf("plain line retained control characters: %q", got)
	}
	for _, escaped := range []string{"phase\\nforged", "detail\\u001b[31m"} {
		if !strings.Contains(got, escaped) {
			t.Errorf("plain line omitted escaped text %q: %q", escaped, got)
		}
	}
}

func TestPlainWithoutAWriterRendersNothing(t *testing.T) {
	notes := ui.NewPlain(nil)
	notes.Note("snapshot", "captured")
	notes.Close()
}
