// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package testkit

import (
	"flag"
	"testing"
)

// TestRegisteringTheUpdateFlagTwiceIsANoOp is the whole point of the change it
// covers: the second registration of a flag name panics, and the panic lands
// before any test runs, so a binary that links two harnesses each convinced it
// owns "update" cannot even report which two.
func TestRegisteringTheUpdateFlagTwiceIsANoOp(t *testing.T) {
	t.Parallel()
	if flag.Lookup(UpdateFlagName) == nil {
		t.Fatalf("%s is not registered, so this test cannot show a second registration being refused", UpdateFlagName)
	}
	if registerUpdateFlag() {
		t.Fatalf("registerUpdateFlag re-registered %s instead of leaving the existing flag alone", UpdateFlagName)
	}
}

// TestUpdateReadsTheFlagSetRatherThanAPointer covers the other half. Reading
// through the set is what lets this package obey a value some other package
// registered.
func TestUpdateReadsTheFlagSetRatherThanAPointer(t *testing.T) {
	registered := flag.Lookup(UpdateFlagName)
	if registered == nil {
		t.Fatalf("%s is not registered", UpdateFlagName)
	}
	previous := registered.Value.String()
	t.Cleanup(func() {
		if err := registered.Value.Set(previous); err != nil {
			t.Fatalf("restore %s: %v", UpdateFlagName, err)
		}
	})
	for _, want := range []bool{true, false} {
		value := "false"
		if want {
			value = "true"
		}
		if err := registered.Value.Set(value); err != nil {
			t.Fatalf("set %s=%s: %v", UpdateFlagName, value, err)
		}
		if got := Update(); got != want {
			t.Errorf("Update() = %t with the flag set to %s, want %t", got, value, want)
		}
	}
}
