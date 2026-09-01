// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package ui

import (
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/P4suta/goatest/internal/report"
)

// DashboardOptions configure the dashboard renderer. Every hook is passed
// here rather than held in package state, so a test injects its clock and its
// tick stream through the one seam the renderer owns.
type DashboardOptions struct {
	// Now is the clock elapsed time is measured with; nil reads the wall clock.
	Now func() time.Time
	// Tick delivers the redraws that keep the elapsed time moving between
	// notes; nil starts a one-second ticker of the renderer's own.
	Tick <-chan time.Time
	// Width bounds one rendered line in runes; zero uses a conservative
	// default, because a line longer than the terminal would wrap and leave
	// rows an in-place erase can no longer reach.
	Width int
}

// dashboard renders one in-place status line on an interactive terminal: the
// current phase, the elapsed time, mutation progress with an estimated
// remainder, and the latest detail. A note whose kind it does not know is
// printed as a permanent plain line above the status line, so nothing a run
// reports is lost to the rendering. Everything here is ephemeral terminal
// output; the deterministic record of a run is its report and its trace.
type dashboard struct {
	writer io.Writer
	now    func() time.Time
	width  int

	mutex           sync.Mutex
	started         time.Time
	phase           string
	detail          string
	mutationDone    int
	mutationTotal   int
	mutationStarted time.Time
	rendered        bool
	closed          bool

	ticker *time.Ticker
	stop   chan struct{}
	done   chan struct{}
}

// NewDashboard renders notes in place on writer until Close, which erases the
// status line and stops the redraws. The caller has already established that
// the writer is an interactive terminal with ANSI escape processing; nothing
// here probes one.
func NewDashboard(writer io.Writer, options DashboardOptions) Notes {
	now := options.Now
	if now == nil {
		now = time.Now
	}
	width := options.Width
	if width <= 0 {
		width = 100
	}
	renderer := &dashboard{
		writer: writer, now: now, width: width, started: now(),
		stop: make(chan struct{}), done: make(chan struct{}),
	}
	tick := options.Tick
	if tick == nil {
		renderer.ticker = time.NewTicker(time.Second)
		tick = renderer.ticker.C
	}
	go renderer.watch(tick)
	return renderer
}

// dashboardPhase names the phase a progress note reports, and whether the
// dashboard knows the kind at all.
func dashboardPhase(kind string) (string, bool) {
	switch kind {
	case "snapshot", "cache-hit":
		return "snapshot", true
	case "impact-broad", "impact-targeted":
		return "impact", true
	case "baseline-target":
		return "baseline", true
	case "race":
		return "race", true
	case "mutation-prepare", "mutation-target", "mutation-progress":
		return "mutation", true
	case "repair-applied":
		return "repair", true
	default:
		return "", false
	}
}

func (renderer *dashboard) Note(kind, detail string) {
	renderer.mutex.Lock()
	defer renderer.mutex.Unlock()
	if renderer.closed {
		return
	}
	phase, known := dashboardPhase(kind)
	if !known {
		// A kind this renderer does not know is still worth reading after the
		// run: it becomes a permanent line above the status line.
		renderer.eraseLocked()
		_, _ = fmt.Fprintf(renderer.writer, noteLineFormat, report.LineText(kind), report.LineText(detail))
		renderer.renderLocked()
		return
	}
	renderer.phase = phase
	renderer.detail = detail
	if kind == "mutation-prepare" {
		renderer.mutationStarted = renderer.now()
	}
	if kind == "mutation-progress" {
		var done, total int
		if _, err := fmt.Sscanf(detail, "%d/%d", &done, &total); err == nil && total > 0 && done >= 0 {
			renderer.mutationDone, renderer.mutationTotal = done, total
			renderer.detail = ""
			if renderer.mutationStarted.IsZero() {
				renderer.mutationStarted = renderer.now()
			}
		}
	}
	renderer.renderLocked()
}

func (renderer *dashboard) Close() {
	renderer.mutex.Lock()
	if renderer.closed {
		renderer.mutex.Unlock()
		return
	}
	renderer.closed = true
	renderer.mutex.Unlock()
	close(renderer.stop)
	<-renderer.done
	if renderer.ticker != nil {
		renderer.ticker.Stop()
	}
	renderer.mutex.Lock()
	renderer.eraseLocked()
	renderer.mutex.Unlock()
}

// watch redraws the status line on every tick, which is what keeps the
// elapsed time moving through the phases that emit nothing for a while. A tick
// stream that closes ends the watching, because a closed channel would
// otherwise be ready forever.
func (renderer *dashboard) watch(tick <-chan time.Time) {
	defer close(renderer.done)
	for {
		select {
		case <-renderer.stop:
			return
		case _, open := <-tick:
			if !open {
				return
			}
			renderer.mutex.Lock()
			if !renderer.closed && renderer.phase != "" {
				renderer.renderLocked()
			}
			renderer.mutex.Unlock()
		}
	}
}

// eraseLocked clears the status line if one occupies the current row.
func (renderer *dashboard) eraseLocked() {
	if !renderer.rendered {
		return
	}
	_, _ = io.WriteString(renderer.writer, "\r\x1b[K")
	renderer.rendered = false
}

// renderLocked draws the status line in place. Kind and detail come from the
// run and are escaped; everything else on the line is the renderer's own.
func (renderer *dashboard) renderLocked() {
	if renderer.phase == "" {
		return
	}
	segments := []string{
		fmt.Sprintf("goatest: %-9s", renderer.phase),
		formatElapsed(renderer.now().Sub(renderer.started)),
	}
	if renderer.mutationTotal > 0 {
		segments = append(segments, fmt.Sprintf("%d/%d", renderer.mutationDone, renderer.mutationTotal))
		if remaining, ok := renderer.estimatedRemainder(); ok {
			segments = append(segments, "eta "+formatElapsed(remaining))
		}
	}
	if renderer.detail != "" {
		segments = append(segments, report.LineText(renderer.detail))
	}
	line := boundedLine(strings.Join(segments, " · "), renderer.width)
	_, _ = io.WriteString(renderer.writer, "\r\x1b[K"+line)
	renderer.rendered = true
}

// estimatedRemainder projects the time the remaining mutants will take from
// the average the executed ones took. It is a heuristic for a human watching
// the line, never a contract.
func (renderer *dashboard) estimatedRemainder() (time.Duration, bool) {
	if renderer.mutationDone <= 0 || renderer.mutationDone >= renderer.mutationTotal || renderer.mutationStarted.IsZero() {
		return 0, false
	}
	elapsed := renderer.now().Sub(renderer.mutationStarted)
	if elapsed <= 0 {
		return 0, false
	}
	perMutant := elapsed / time.Duration(renderer.mutationDone)
	return perMutant * time.Duration(renderer.mutationTotal-renderer.mutationDone), true
}

// formatElapsed renders a duration the way a human reads a stopwatch.
func formatElapsed(elapsed time.Duration) string {
	if elapsed < 0 {
		elapsed = 0
	}
	total := int(elapsed.Seconds())
	if total >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", total/3600, total%3600/60, total%60)
	}
	return fmt.Sprintf("%02d:%02d", total/60, total%60)
}

// boundedLine keeps a line inside the terminal row, because a wrapped status
// line leaves rows behind that an in-place erase can no longer reach.
func boundedLine(line string, width int) string {
	runes := []rune(line)
	if len(runes) <= width {
		return line
	}
	return string(runes[:width-1]) + "…"
}
