// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package ui

import (
	"fmt"
	"io"
	"time"

	"github.com/P4suta/goatest/internal/report"
)

const noteLineFormat = "goatest: %-18s %s\n"

type plain struct {
	writer          io.Writer
	now             func() time.Time
	mutationStarted time.Time
}

func NewPlain(writer io.Writer) Notes { return &plain{writer: writer, now: time.Now} }

func (renderer *plain) Note(kind, detail string) {
	if renderer.writer == nil {
		return
	}
	_, _ = fmt.Fprintf(renderer.writer, noteLineFormat, report.LineText(kind), report.LineText(renderer.annotate(kind, detail)))
}

func (renderer *plain) annotate(kind, detail string) string {
	if kind == "mutation-target" {
		renderer.mutationStarted = time.Time{}
		return detail
	}
	if kind != "mutation-progress" {
		return detail
	}
	done, total, ok := progressFraction(detail)
	if !ok {
		return detail
	}
	if renderer.mutationStarted.IsZero() {
		renderer.mutationStarted = renderer.now()
		return detail
	}
	remaining, estimated := estimatedRemainder(renderer.mutationStarted, renderer.now, done, total)
	if !estimated {
		return detail
	}
	return detail + " eta " + formatElapsed(remaining)
}

func (renderer *plain) Close() {}
