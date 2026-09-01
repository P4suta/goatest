// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package ui

import (
	"fmt"
	"io"

	"github.com/P4suta/goatest/internal/report"
)

// noteLineFormat is the one shape a plain progress line has. Deterministic
// consumers parse these lines, so every renderer that prints a plain line
// prints this one.
const noteLineFormat = "goatest: %-18s %s\n"

type plain struct{ writer io.Writer }

// NewPlain renders one deterministic line per note, escaped onto a single
// physical line so that nothing a run reports can forge a note of its own. A
// nil writer renders nothing.
func NewPlain(writer io.Writer) Notes { return plain{writer: writer} }

func (renderer plain) Note(kind, detail string) {
	if renderer.writer == nil {
		return
	}
	_, _ = fmt.Fprintf(renderer.writer, noteLineFormat, report.LineText(kind), report.LineText(detail))
}

func (renderer plain) Close() {}
