// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package ui_test

import (
	"bytes"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/P4suta/goatest/internal/ui"
)

const (
	dashboardFixtureWidth      = 40
	longStatusSegmentCount     = 20
	concurrentDashboardWriters = 4
	dashboardTickEveryNotes    = 16
	concurrentDashboardNotes   = 64
	dashboardWriteDeadline     = 5 * time.Second
)

type lockedBuffer struct {
	mutex  sync.Mutex
	buffer bytes.Buffer
	write  chan struct{}
}

func (buffer *lockedBuffer) Write(data []byte) (int, error) {
	buffer.mutex.Lock()
	defer buffer.mutex.Unlock()
	written, err := buffer.buffer.Write(data)
	if buffer.write != nil {
		select {
		case buffer.write <- struct{}{}:
		default:
		}
	}
	return written, err
}

func (buffer *lockedBuffer) String() string {
	buffer.mutex.Lock()
	defer buffer.mutex.Unlock()
	return buffer.buffer.String()
}

type fixedClock struct {
	mutex sync.Mutex
	now   time.Time
}

func newFixedClock() *fixedClock {
	return &fixedClock{now: time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)}
}

func (clock *fixedClock) Now() time.Time {
	clock.mutex.Lock()
	defer clock.mutex.Unlock()
	return clock.now
}

func (clock *fixedClock) Advance(delta time.Duration) {
	clock.mutex.Lock()
	defer clock.mutex.Unlock()
	clock.now = clock.now.Add(delta)
}

func TestDashboardRendersPhaseElapsedAndMutationEstimate(t *testing.T) {
	buffer := &lockedBuffer{}
	clock := newFixedClock()
	tick := make(chan time.Time, 1)
	notes := ui.NewDashboard(buffer, ui.DashboardOptions{Now: clock.Now, Tick: tick})
	notes.Note("snapshot", "captured")
	if got := buffer.String(); !strings.Contains(got, "\r\x1b[K") || !strings.Contains(got, "snapshot") || !strings.Contains(got, "00:00") || !strings.Contains(got, "captured") {
		t.Fatalf("first frame = %q", got)
	}
	clock.Advance(12 * time.Second)
	notes.Note("baseline-progress", "0/3")
	if got := buffer.String(); !strings.Contains(got, "baseline") || !strings.Contains(got, "00:12") || !strings.Contains(got, "0/3") {
		t.Fatalf("baseline frame = %q", got)
	}
	notes.Note("mutation-target", "120 mutants")
	clock.Advance(40 * time.Second)
	notes.Note("mutation-progress", "40/120")
	got := buffer.String()

	if !strings.Contains(got, "mutation") || !strings.Contains(got, "40/120") || !strings.Contains(got, "eta 01:20") {
		t.Fatalf("mutation frame = %q", got)
	}
	notes.Note("probe-progress", "1/2")
	got = buffer.String()
	frame := got[strings.LastIndex(got, "\x1b[K")+len("\x1b[K"):]
	if !strings.Contains(frame, "probe") || !strings.Contains(frame, "1/2") || strings.Contains(frame, "40/120") || strings.Contains(frame, "eta ") {
		t.Fatalf("probe frame = %q", frame)
	}
	notes.Close()
	if got := buffer.String(); !strings.HasSuffix(got, "\r\x1b[K") {
		t.Fatalf("close left the status line behind: %q", got)
	}
}

func TestDashboardPrintsUnknownKindsAsPermanentLines(t *testing.T) {
	buffer := &lockedBuffer{}
	clock := newFixedClock()
	tick := make(chan time.Time)
	notes := ui.NewDashboard(buffer, ui.DashboardOptions{Now: clock.Now, Tick: tick})
	defer notes.Close()
	notes.Note("snapshot", "captured")
	notes.Note("trace-unavailable", "disk full")
	if got := buffer.String(); !strings.Contains(got, "goatest: trace-unavailable  disk full\n") {
		t.Fatalf("permanent line missing: %q", got)
	}

	if got := buffer.String(); !strings.HasSuffix(got[strings.LastIndex(got, "\n")+1:], "captured") {
		t.Fatalf("status line was not redrawn: %q", got)
	}
}

func TestDashboardTicksKeepTheElapsedTimeMoving(t *testing.T) {
	buffer := &lockedBuffer{write: make(chan struct{}, 1)}
	clock := newFixedClock()
	tick := make(chan time.Time, 1)
	notes := ui.NewDashboard(buffer, ui.DashboardOptions{Now: clock.Now, Tick: tick})
	defer notes.Close()
	notes.Note("race", "3 packages")
	<-buffer.write
	clock.Advance(65 * time.Second)
	tick <- clock.Now()
	select {
	case <-buffer.write:
	case <-time.After(dashboardWriteDeadline):
		t.Fatalf("tick did not redraw: %q", buffer.String())
	}
	if got := buffer.String(); !strings.Contains(got, "01:05") {
		t.Fatalf("tick frame = %q", got)
	}
}

func TestDashboardBoundsTheStatusLineWidth(t *testing.T) {
	buffer := &lockedBuffer{}
	tick := make(chan time.Time)
	notes := ui.NewDashboard(buffer, ui.DashboardOptions{Now: newFixedClock().Now, Tick: tick, Width: dashboardFixtureWidth})
	defer notes.Close()
	notes.Note("baseline-progress", strings.Repeat("long-package-name/", longStatusSegmentCount))
	frame := buffer.String()
	last := frame[strings.LastIndex(frame, "\x1b[K")+len("\x1b[K"):]
	if length := len([]rune(last)); length > dashboardFixtureWidth {
		t.Fatalf("status line spans %d runes: %q", length, last)
	}
}

func TestDashboardStopsWatchingWhenTheTickStreamCloses(t *testing.T) {
	buffer := &lockedBuffer{}
	tick := make(chan time.Time)
	notes := ui.NewDashboard(buffer, ui.DashboardOptions{Now: newFixedClock().Now, Tick: tick})
	notes.Note("snapshot", "captured")
	close(tick)
	notes.Close()
	before := buffer.String()
	notes.Note("snapshot", "after close")
	if after := buffer.String(); after != before {
		t.Fatalf("closed dashboard changed: %q -> %q", before, after)
	}
}

func TestDashboardSurvivesConcurrentNotesTicksAndClose(t *testing.T) {
	buffer := &lockedBuffer{}
	clock := newFixedClock()
	tick := make(chan time.Time)
	notes := ui.NewDashboard(buffer, ui.DashboardOptions{Now: clock.Now, Tick: tick})
	var writers sync.WaitGroup
	for range concurrentDashboardWriters {
		writers.Add(1)
		go func() {
			defer writers.Done()
			for index := range concurrentDashboardNotes {
				notes.Note("mutation-progress", "1/2")
				if index%dashboardTickEveryNotes == 0 {
					select {
					case tick <- clock.Now():
					default:
					}
				}
			}
		}()
	}
	writers.Wait()
	notes.Close()
	notes.Close()
	notes.Note("snapshot", "after close")
	if got := buffer.String(); strings.Contains(got, "after close") {
		t.Fatalf("note after close was rendered: %q", got)
	}
}
