// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package mutationbridge_test

import (
	"testing"

	"github.com/P4suta/goatest/internal/mutationbridge"
)

func TestProfileMatchesAssuranceContract(t *testing.T) {
	for contract, want := range map[string]string{"standard-v1": "strong", "deep-v1": "all"} {
		got, err := mutationbridge.Profile(contract)
		if err != nil || got != want {
			t.Errorf("Profile(%q) = %q, %v; want %q", contract, got, err, want)
		}
	}
	if _, err := mutationbridge.Profile("unknown"); err == nil {
		t.Fatal("unknown contract was accepted")
	}
}
