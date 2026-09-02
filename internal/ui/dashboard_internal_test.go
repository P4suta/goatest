// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package ui

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestDashboardPhaseVocabularyIsExhaustive(t *testing.T) {
	for _, test := range []struct{ kind, phase string }{
		{"snapshot", "snapshot"}, {"cache-hit", "snapshot"}, {"cache-wait", "snapshot"},
		{"impact-broad", "impact"}, {"impact-targeted", "impact"},
		{"baseline-target", "baseline"}, {"resume-baseline", "baseline"},
		{"race", "race"}, {"resume-race", "race"},
		{"mutation-prepare", "mutation"}, {"mutation-target", "mutation"}, {"mutation-progress", "mutation"},
		{"probe-target", "probe"}, {"probe-progress", "probe"}, {"probe-summary", "probe"},
		{"repair-applied", "repair"},
	} {
		if phase, known := dashboardPhase(test.kind); !known || phase != test.phase {
			t.Errorf("dashboardPhase(%q) = (%q, %t), want (%q, true)", test.kind, phase, known, test.phase)
		}
	}
	if phase, known := dashboardPhase("checkpoint-warning"); known || phase != "" {
		t.Fatalf("unknown phase = (%q, %t)", phase, known)
	}
}

func TestDashboardFormattingAndEstimateBoundaries(t *testing.T) {
	for _, test := range []struct {
		input time.Duration
		want  string
	}{
		{-time.Second, "00:00"}, {0, "00:00"}, {59 * time.Second, "00:59"}, {65 * time.Second, "01:05"},
		{time.Hour, "1:00:00"},
		{time.Hour + 2*time.Minute + 3*time.Second, "1:02:03"},
	} {
		if got := formatElapsed(test.input); got != test.want {
			t.Errorf("formatElapsed(%s) = %q, want %q", test.input, got, test.want)
		}
	}
	now := time.Date(2026, 9, 1, 12, 0, 10, 0, time.UTC)
	renderer := &dashboard{now: func() time.Time { return now }, mutationTotal: 4}
	if _, ok := renderer.estimatedRemainder(); ok {
		t.Fatal("estimate existed before progress")
	}
	renderer.mutationStarted = now.Add(-time.Second)
	if _, ok := renderer.estimatedRemainder(); ok {
		t.Fatal("estimate existed at zero completed mutants")
	}
	renderer.mutationDone = 4
	renderer.mutationStarted = now.Add(-time.Second)
	if _, ok := renderer.estimatedRemainder(); ok {
		t.Fatal("estimate existed after completion")
	}
	renderer.mutationDone = 1
	renderer.mutationStarted = now
	if _, ok := renderer.estimatedRemainder(); ok {
		t.Fatal("estimate existed at zero elapsed time")
	}
	renderer.mutationStarted = now.Add(time.Second)
	if _, ok := renderer.estimatedRemainder(); ok {
		t.Fatal("estimate existed for non-positive elapsed time")
	}
	renderer.mutationStarted = now.Add(-2 * time.Second)
	if remaining, ok := renderer.estimatedRemainder(); !ok || remaining != 6*time.Second {
		t.Fatalf("estimate = (%s, %t), want (6s, true)", remaining, ok)
	}
}

func TestDashboardNoOpRenderingAndDefaultTickerLifecycle(t *testing.T) {
	var output bytes.Buffer
	renderer := &dashboard{writer: &output, now: time.Now, width: 80}
	renderer.eraseLocked()
	renderer.renderLocked()
	if output.Len() != 0 {
		t.Fatalf("empty dashboard rendered %q", output.String())
	}
	renderer.rendered = true
	renderer.eraseLocked()
	if renderer.rendered {
		t.Fatal("erase left the dashboard marked as rendered")
	}
	output.Reset()

	notes := NewDashboard(&output, DashboardOptions{})
	if notes.(*dashboard).ticker == nil {
		t.Fatal("default dashboard did not own a ticker")
	}
	notes.Note("snapshot", "default options")
	notes.Close()
	if !strings.Contains(output.String(), "default options") || strings.Contains(output.String(), "0/0") {
		t.Fatalf("default dashboard output = %q", output.String())
	}
}

func TestDashboardInvalidAndEarlyMutationProgress(t *testing.T) {
	var output bytes.Buffer
	tick := make(chan time.Time)
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	notes := NewDashboard(&output, DashboardOptions{Now: func() time.Time { return now }, Tick: tick})
	renderer := notes.(*dashboard)
	notes.Note("mutation-progress", "not-a-fraction")
	if renderer.mutationTotal != 0 || renderer.detail != "not-a-fraction" || !renderer.mutationStarted.IsZero() {
		t.Fatalf("invalid progress state = %+v", renderer)
	}
	notes.Note("mutation-progress", "1/0")
	if renderer.mutationDone != 0 || renderer.mutationTotal != 0 || renderer.detail != "1/0" || !renderer.mutationStarted.IsZero() {
		t.Fatalf("zero-total progress state = %+v", renderer)
	}
	notes.Note("mutation-progress", "0/3")
	if renderer.mutationDone != 0 || renderer.mutationTotal != 3 || renderer.detail != "" || renderer.mutationStarted != now {
		t.Fatalf("early progress state = %+v", renderer)
	}
	notes.Close()
}

func TestDashboardWatchDoesNotRenderAClosedDashboard(t *testing.T) {
	var output bytes.Buffer
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	tick := make(chan time.Time, 1)
	tick <- now
	close(tick)
	renderer := &dashboard{
		writer: &output, now: func() time.Time { return now }, width: 80,
		started: now, phase: "snapshot", closed: true,
		stop: make(chan struct{}), done: make(chan struct{}),
	}
	renderer.watch(tick)
	if output.Len() != 0 || renderer.rendered {
		t.Fatalf("closed dashboard rendered on tick: %q", output.String())
	}
}

func TestBoundedLineKeepsShortTextAndTruncatesByRunes(t *testing.T) {
	if got := boundedLine("short", 5); got != "short" {
		t.Fatalf("short line = %q", got)
	}
	if got := boundedLine("日本語abcdef", 5); got != "日本語a…" || len([]rune(got)) != 5 {
		t.Fatalf("bounded Unicode line = %q", got)
	}
}
