// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package cli

import (
	"errors"
	"testing"
)

// TestAUsageErrorReachesItsCause is the property the type did not have.
//
// It was the only error type this module declares, and errors.Is through it
// found nothing: Error returned the cause's text and Unwrap did not exist, so a
// sentinel could be built with care and then be unreachable the moment a usage
// error wrapped it.
func TestAUsageErrorReachesItsCause(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("a cause worth recognising")
	err := error(usageError{command: CommandTrace, cause: sentinel})
	if !errors.Is(err, sentinel) {
		t.Fatal("errors.Is did not reach the cause of a usage error")
	}
	if err.Error() != sentinel.Error() {
		t.Fatalf("Error() = %q, want the cause's own text", err.Error())
	}
}

// TestAUsageErrorNamesWhereToLook keeps the remedy separate from the message.
//
// A message says what happened and a remedy says what to do. An error-code table
// has a column for each, and one that mixes them has a column nobody can fill.
func TestAUsageErrorNamesWhereToLook(t *testing.T) {
	t.Parallel()
	if got, want := (usageError{command: CommandTrace}).Hint(), "run 'goatest help trace' for usage"; got != want {
		t.Errorf("Hint() = %q, want %q", got, want)
	}
	if got, want := (usageError{}).Hint(), "run 'goatest --help' for usage"; got != want {
		t.Errorf("Hint() with no command = %q, want %q", got, want)
	}
}
