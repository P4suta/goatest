// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package testkit

import (
	"slices"

	"github.com/P4suta/goatest/internal/assure"
)

func HasEvent(events []assure.Event, kind string) bool {
	return slices.ContainsFunc(events, func(event assure.Event) bool { return event.Kind == kind })
}

func CountEvent(events []assure.Event, kind string) int {
	count := 0
	for _, event := range events {
		if event.Kind == kind {
			count++
		}
	}
	return count
}

func EventDetails(events []assure.Event, kind string) []string {
	var details []string
	for _, event := range events {
		if event.Kind == kind {
			details = append(details, event.Detail)
		}
	}
	return details
}
