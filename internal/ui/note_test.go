// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package ui_test

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/P4suta/goatest/internal/ui"
)

func TestNoteDetailRemovesTheLeadingToolPrefixHoweverOftenItWasApplied(t *testing.T) {
	t.Parallel()
	for detail, want := range map[string]string{
		"goatest: create the layer: permission denied":          "create the layer: permission denied",
		"goatest: goatest: read latest report: file is absent":  "read latest report: file is absent",
		"create the layer: permission denied":                   "create the layer: permission denied",
		"12/48":                                                 "12/48",
		"coverage for pkg: goatest: profile has no mode header": "coverage for pkg: goatest: profile has no mode header",
	} {
		if got := ui.NoteDetail(detail); got != want {
			t.Errorf("NoteDetail(%q) = %q, want %q", detail, got, want)
		}
	}
}

// TestNoRendererPrintsTheToolPrefixTwice covers what the removal is for.
//
// The plain and dashboard renderers open the line with "goatest: " themselves,
// so a detail that arrived carrying one printed it twice. The JSON Lines
// renderer prints no prefix at all, so a detail carrying one put it inside a
// field a consumer parses. The invariant that covers both is the same: the
// prefix is the renderer's to add, and appears at most once.
func TestNoRendererPrintsTheToolPrefixTwice(t *testing.T) {
	t.Parallel()
	const detail = "goatest: create the layer: permission denied"
	for name, build := range map[string]func(*bytes.Buffer) ui.Notes{
		"plain": func(buffer *bytes.Buffer) ui.Notes { return ui.NewPlain(buffer) },
		"jsonl": func(buffer *bytes.Buffer) ui.Notes { return ui.NewJSONL(buffer, time.Now) },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var buffer bytes.Buffer
			notes := build(&buffer)
			notes.Note("build-cache-unavailable", detail)
			notes.Close()
			line := buffer.String()
			if got := strings.Count(line, "goatest: "); got > 1 {
				t.Fatalf("%q holds %d tool prefixes, want at most one", line, got)
			}
			if !strings.Contains(line, "create the layer: permission denied") {
				t.Fatalf("%q lost the detail it was given", line)
			}
		})
	}
}
