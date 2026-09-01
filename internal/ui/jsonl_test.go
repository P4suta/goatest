// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package ui_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/P4suta/goatest/internal/ui"
)

// steppingClock returns a clock that starts at a fixed instant and advances by
// step on every reading after the first.
func steppingClock(step time.Duration) func() time.Time {
	current := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	first := true
	return func() time.Time {
		if first {
			first = false
			return current
		}
		current = current.Add(step)
		return current
	}
}

func TestJSONLStreamsOneEventPerNote(t *testing.T) {
	var buffer bytes.Buffer
	notes := ui.NewJSONL(&buffer, steppingClock(1500*time.Millisecond))
	notes.Note("snapshot", "captured")
	notes.Note("mutation-progress", "3/9")
	notes.Close()
	want := `{"type":"progress","kind":"snapshot","detail":"captured","elapsed_ms":1500}` + "\n" +
		`{"type":"progress","kind":"mutation-progress","detail":"3/9","elapsed_ms":3000}` + "\n"
	if got := buffer.String(); got != want {
		t.Fatalf("stream = %q, want %q", got, want)
	}
}

func TestJSONLKeepsForgedNotesOnOnePhysicalLine(t *testing.T) {
	var buffer bytes.Buffer
	notes := ui.NewJSONL(&buffer, nil)
	notes.Note("phase\nforged", "detail\x1b[31m\"quoted\"")
	got := buffer.String()
	if strings.Count(got, "\n") != 1 {
		t.Fatalf("event spans several physical lines: %q", got)
	}
	var event struct {
		Type      string `json:"type"`
		Kind      string `json:"kind"`
		Detail    string `json:"detail"`
		ElapsedMS int64  `json:"elapsed_ms"`
	}
	if err := json.Unmarshal([]byte(got), &event); err != nil {
		t.Fatalf("event is not one JSON object: %v\n%q", err, got)
	}
	if event.Type != "progress" || event.Kind != "phase\nforged" || event.Detail != "detail\x1b[31m\"quoted\"" || event.ElapsedMS < 0 {
		t.Fatalf("event = %+v", event)
	}
}
