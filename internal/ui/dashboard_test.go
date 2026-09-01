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

// lockedBuffer synchronizes reads and writes, because the dashboard's tick
// goroutine writes concurrently with the test's assertions.
type lockedBuffer struct {
	mutex  sync.Mutex
	buffer bytes.Buffer
}

func (buffer *lockedBuffer) Write(data []byte) (int, error) {
	buffer.mutex.Lock()
	defer buffer.mutex.Unlock()
	return buffer.buffer.Write(data)
}

func (buffer *lockedBuffer) String() string {
	buffer.mutex.Lock()
	defer buffer.mutex.Unlock()
	return buffer.buffer.String()
}

// fixedClock is a clock a test moves by hand.
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
	tick := make(chan time.Time)
	notes := ui.NewDashboard(buffer, ui.DashboardOptions{Now: clock.Now, Tick: tick})
	notes.Note("snapshot", "captured")
	if got := buffer.String(); !strings.Contains(got, "\r\x1b[K") || !strings.Contains(got, "snapshot") || !strings.Contains(got, "00:00") || !strings.Contains(got, "captured") {
		t.Fatalf("first frame = %q", got)
	}
	clock.Advance(12 * time.Second)
	notes.Note("baseline-target", "internal/report:TestLines")
	if got := buffer.String(); !strings.Contains(got, "baseline") || !strings.Contains(got, "00:12") {
		t.Fatalf("baseline frame = %q", got)
	}
	notes.Note("mutation-prepare", "standard-v1")
	clock.Advance(40 * time.Second)
	notes.Note("mutation-progress", "40/120")
	got := buffer.String()
	// 40 mutants over 40 seconds leave 80 mutants at one second each.
	if !strings.Contains(got, "mutation") || !strings.Contains(got, "40/120") || !strings.Contains(got, "eta 01:20") {
		t.Fatalf("mutation frame = %q", got)
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
	// The status line is redrawn after the permanent line.
	if got := buffer.String(); !strings.HasSuffix(got[strings.LastIndex(got, "\n")+1:], "captured") {
		t.Fatalf("status line was not redrawn: %q", got)
	}
}

func TestDashboardTicksKeepTheElapsedTimeMoving(t *testing.T) {
	buffer := &lockedBuffer{}
	clock := newFixedClock()
	tick := make(chan time.Time)
	notes := ui.NewDashboard(buffer, ui.DashboardOptions{Now: clock.Now, Tick: tick})
	defer notes.Close()
	notes.Note("race", "3 packages")
	clock.Advance(65 * time.Second)
	tick <- clock.Now()
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(buffer.String(), "01:05") {
		if time.Now().After(deadline) {
			t.Fatalf("tick did not redraw: %q", buffer.String())
		}
		time.Sleep(time.Millisecond)
	}
}

func TestDashboardBoundsTheStatusLineWidth(t *testing.T) {
	buffer := &lockedBuffer{}
	tick := make(chan time.Time)
	notes := ui.NewDashboard(buffer, ui.DashboardOptions{Now: newFixedClock().Now, Tick: tick, Width: 40})
	defer notes.Close()
	notes.Note("baseline-target", strings.Repeat("long-package-name/", 20))
	frame := buffer.String()
	last := frame[strings.LastIndex(frame, "\x1b[K")+len("\x1b[K"):]
	if length := len([]rune(last)); length > 40 {
		t.Fatalf("status line spans %d runes: %q", length, last)
	}
}

// A tick stream that closes must end the watcher rather than leave a receive
// that is ready forever: the buggy shape redraws in a busy loop until Close.
func TestDashboardStopsWatchingWhenTheTickStreamCloses(t *testing.T) {
	buffer := &lockedBuffer{}
	tick := make(chan time.Time)
	notes := ui.NewDashboard(buffer, ui.DashboardOptions{Now: newFixedClock().Now, Tick: tick})
	notes.Note("snapshot", "captured")
	close(tick)
	time.Sleep(10 * time.Millisecond)
	before := len(buffer.String())
	time.Sleep(20 * time.Millisecond)
	if after := len(buffer.String()); after != before {
		t.Fatalf("watcher kept redrawing after the tick stream closed: %d -> %d bytes", before, after)
	}
	notes.Close()
}

func TestDashboardSurvivesConcurrentNotesTicksAndClose(t *testing.T) {
	buffer := &lockedBuffer{}
	clock := newFixedClock()
	tick := make(chan time.Time)
	notes := ui.NewDashboard(buffer, ui.DashboardOptions{Now: clock.Now, Tick: tick})
	var writers sync.WaitGroup
	stop := make(chan struct{})
	for range 4 {
		writers.Add(1)
		go func() {
			defer writers.Done()
			for index := 0; ; index++ {
				select {
				case <-stop:
					return
				default:
				}
				notes.Note("mutation-progress", "1/2")
				if index%16 == 0 {
					select {
					case tick <- clock.Now():
					default:
					}
				}
			}
		}()
	}
	time.Sleep(20 * time.Millisecond)
	close(stop)
	writers.Wait()
	notes.Close()
	notes.Close()
	notes.Note("snapshot", "after close")
	if got := buffer.String(); strings.Contains(got, "after close") {
		t.Fatalf("note after close was rendered: %q", got)
	}
}
