// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package ui

import (
	"fmt"
	"io"

	"github.com/P4suta/goatest/internal/report"
)

const noteLineFormat = "goatest: %-18s %s\n"

type plain struct{ writer io.Writer }

func NewPlain(writer io.Writer) Notes { return plain{writer: writer} }

func (renderer plain) Note(kind, detail string) {
	if renderer.writer == nil {
		return
	}
	_, _ = fmt.Fprintf(renderer.writer, noteLineFormat, report.LineText(kind), report.LineText(detail))
}

func (renderer plain) Close() {}
