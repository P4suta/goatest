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

// TestPlainShowsWhatTheProbePassReported pins that the notes of the probe pass
// reach a plain run. What a phase measured is only worth measuring if the
// person watching the run is told.
func TestPlainShowsWhatTheProbePassReported(t *testing.T) {
	var buffer bytes.Buffer
	notes := ui.NewPlain(&buffer)
	notes.Note("probe-target", "42 targets")
	notes.Note("probe-progress", "21/42")
	notes.Note("probe-summary", "40 measured, 2 without facts")
	notes.Close()
	got := buffer.String()
	for _, want := range []string{
		"goatest: probe-target       42 targets\n",
		"goatest: probe-progress     21/42\n",
		"goatest: probe-summary      40 measured, 2 without facts\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("plain output omitted %q: %q", want, got)
		}
	}
}

func TestPlainWithoutAWriterRendersNothing(t *testing.T) {
	notes := ui.NewPlain(nil)
	notes.Note("snapshot", "captured")
	notes.Close()
}
