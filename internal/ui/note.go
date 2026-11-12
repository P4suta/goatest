// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package ui

import "github.com/P4suta/goatest/internal/report"

// NoteDetail is what a renderer prints for a note.
//
// A note's detail is very often an error, and every error this tool builds
// begins with "goatest: ". A renderer that then prefixes the line itself -
// which all three do, because a note is printed as `goatest: <kind> <detail>` -
// produces "goatest: build-cache-unavailable      goatest: create the layer:
// permission denied". Thirty-two call sites pass an error straight through, and
// each was one place to remember something.
//
// The command line already normalised this for the one line it prints to
// standard error, and its tests pin the behaviour. The note path never got it,
// which is the shape of the problem rather than an oversight about it: a rule
// applied at the call sites is a rule the thirty-third call site does not know.
// So it is applied here, where every note passes whatever it came from.
//
// Only the leading run is removed. A prefix in the middle of a wrapped chain is
// a different problem - two packages disagreeing about whose job the prefix is -
// and hiding it here would hide the disagreement.
func NoteDetail(detail string) string {
	return report.WithoutToolPrefix(detail)
}
