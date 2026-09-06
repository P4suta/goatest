// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package ui

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestPlainProgressCarriesAnEstimateOnceItHasOne(t *testing.T) {
	t.Parallel()
	var buffer bytes.Buffer
	moment := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	renderer := &plain{writer: &buffer, now: func() time.Time { return moment }}

	renderer.Note("mutation-progress", "100/1000")
	moment = moment.Add(time.Minute)
	renderer.Note("mutation-progress", "200/1000")

	const progressLines = 2
	lines := strings.Split(strings.TrimSpace(buffer.String()), "\n")
	if len(lines) != progressLines {
		t.Fatalf("output = %q", buffer.String())
	}
	if strings.Contains(lines[0], "eta") {
		t.Fatalf("the first progress line already estimates: %q", lines[0])
	}
	if !strings.Contains(lines[1], "eta 04:00") {
		t.Fatalf("second progress line = %q, want a four-minute estimate", lines[1])
	}
}

func TestPlainProgressForgetsItsEstimateWhenAPhaseRestarts(t *testing.T) {
	t.Parallel()
	var buffer bytes.Buffer
	moment := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	renderer := &plain{writer: &buffer, now: func() time.Time { return moment }}

	renderer.Note("mutation-progress", "100/1000")
	moment = moment.Add(time.Minute)
	renderer.Note("mutation-target", "1000 mutants")
	renderer.Note("mutation-progress", "100/1000")

	if strings.Contains(buffer.String(), "eta") {
		t.Fatalf("a restarted phase kept its old estimate: %q", buffer.String())
	}
}
