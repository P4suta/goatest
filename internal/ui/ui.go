// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package ui renders the progress of one run for the selected --ui mode.
//
// Every renderer consumes the same progress notes the assurance layer emits,
// and a traced run records the same kind and detail under its own progress
// events, so one run reads the same wherever it is watched; what differs is
// only the shape on the wire. Progress is diagnostic exhaust, never evidence:
// nothing here takes part in a verdict or in the identity a cached result is
// keyed on.
package ui

// Notes renders the progress notes of one run. Note may be called from
// concurrent workers; Close ends the rendering and releases whatever the
// renderer holds, and nothing calls Note afterwards.
type Notes interface {
	Note(kind, detail string)
	Close()
}
