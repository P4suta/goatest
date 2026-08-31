// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package testkit

import (
	"slices"

	"github.com/P4suta/goatest/internal/assure"
)

// The event assertions are the one part of testkit that depends on the
// assurance package. Only assure's external test package may use them; an
// internal test would close an import cycle.

// HasEvent reports whether the progress stream contains an event of kind.
func HasEvent(events []assure.Event, kind string) bool {
	return slices.ContainsFunc(events, func(event assure.Event) bool { return event.Kind == kind })
}

// CountEvent counts the events of kind, the assertion that separates a
// progress stream that reported work once from one that repeated it.
func CountEvent(events []assure.Event, kind string) int {
	count := 0
	for _, event := range events {
		if event.Kind == kind {
			count++
		}
	}
	return count
}

// EventDetails collects the details of every event of kind in stream order,
// returning nil when no event matches.
func EventDetails(events []assure.Event, kind string) []string {
	var details []string
	for _, event := range events {
		if event.Kind == kind {
			details = append(details, event.Detail)
		}
	}
	return details
}
