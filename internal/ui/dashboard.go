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

const defaultDashboardWidth = 100

const (
	secondsPerMinute = int(time.Minute / time.Second)
	secondsPerHour   = int(time.Hour / time.Second)
)

type DashboardOptions struct {
	Now func() time.Time

	Tick <-chan time.Time

	Width int
}

type dashboard struct {
	writer io.Writer
	now    func() time.Time
	width  int

	mutex           sync.Mutex
	started         time.Time
	phase           string
	detail          string
	baselineDone    int
	baselineTotal   int
	mutationDone    int
	mutationTotal   int
	mutationStarted time.Time
	rendered        bool
	closed          bool

	ticker *time.Ticker
	stop   chan struct{}
	done   chan struct{}
}

func NewDashboard(writer io.Writer, options DashboardOptions) Notes {
	now := options.Now
	if now == nil {
		now = time.Now
	}
	width := options.Width
	if width <= 0 {
		width = defaultDashboardWidth
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

func dashboardPhase(kind string) (string, bool) {
	switch kind {
	case "snapshot", "cache-hit", "cache-wait":
		return "snapshot", true
	case "impact-broad", "impact-targeted":
		return "impact", true
	case "baseline-progress", "resume-baseline":
		return "baseline", true
	case "race", "resume-race":
		return "race", true
	case "mutation-target", "mutation-progress":
		return "mutation", true
	case "probe-target", "probe-progress", "probe-summary":
		return "probe", true
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
		renderer.eraseLocked()
		_, _ = fmt.Fprintf(renderer.writer, noteLineFormat, report.LineText(kind), report.LineText(detail))
		renderer.renderLocked()
		return
	}
	renderer.phase = phase
	renderer.detail = detail
	if kind == "resume-baseline" {
		renderer.baselineDone, renderer.baselineTotal = 0, 0
	}
	if kind == "baseline-progress" {
		if done, total, ok := progressFraction(detail); ok {
			renderer.baselineDone, renderer.baselineTotal = done, total
			renderer.detail = ""
		}
	}
	if kind == "mutation-target" {
		renderer.mutationDone, renderer.mutationTotal = 0, 0
		renderer.mutationStarted = renderer.now()
	}
	if kind == "mutation-progress" {
		if done, total, ok := progressFraction(detail); ok {
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

func (renderer *dashboard) eraseLocked() {
	if !renderer.rendered {
		return
	}
	_, _ = io.WriteString(renderer.writer, "\r\x1b[K")
	renderer.rendered = false
}

func (renderer *dashboard) renderLocked() {
	if renderer.phase == "" {
		return
	}
	segments := []string{
		fmt.Sprintf("goatest: %-9s", renderer.phase),
		formatElapsed(renderer.now().Sub(renderer.started)),
	}
	if renderer.phase == "baseline" && renderer.baselineTotal > 0 {
		segments = append(segments, fmt.Sprintf("%d/%d", renderer.baselineDone, renderer.baselineTotal))
	}
	if renderer.phase == "mutation" && renderer.mutationTotal > 0 {
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

func progressFraction(detail string) (int, int, bool) {
	var done, total int
	_, err := fmt.Sscanf(detail, "%d/%d", &done, &total)
	return done, total, err == nil && total > 0 && done >= 0
}

func (renderer *dashboard) estimatedRemainder() (time.Duration, bool) {
	return estimatedRemainder(renderer.mutationStarted, renderer.now, renderer.mutationDone, renderer.mutationTotal)
}

func estimatedRemainder(started time.Time, now func() time.Time, done, total int) (time.Duration, bool) {
	if done <= 0 || done >= total || started.IsZero() {
		return 0, false
	}
	elapsed := now().Sub(started)
	if elapsed <= 0 {
		return 0, false
	}
	perUnit := elapsed / time.Duration(done)
	return perUnit * time.Duration(total-done), true
}

func formatElapsed(elapsed time.Duration) string {
	elapsed = max(elapsed, 0)
	total := int(elapsed.Seconds())
	if total >= secondsPerHour {
		return fmt.Sprintf("%d:%02d:%02d", total/secondsPerHour, total%secondsPerHour/secondsPerMinute, total%secondsPerMinute)
	}
	return fmt.Sprintf("%02d:%02d", total/secondsPerMinute, total%secondsPerMinute)
}

func boundedLine(line string, width int) string {
	runes := []rune(line)
	if len(runes) <= width {
		return line
	}
	return string(runes[:width-1]) + "…"
}
