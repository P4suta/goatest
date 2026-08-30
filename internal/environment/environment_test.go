// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package environment_test

import (
	"slices"
	"testing"

	"github.com/P4suta/goatest/internal/environment"
)

func TestProviderSelectsLaunchAndExplicitNamesWithoutSecrets(t *testing.T) {
	input := []string{"Path=C:/tools", "TEMP=C:/temp", "TOKEN=allowed", "SECRET=hidden", "token=last"}
	got := environment.Provider(input, []string{"TOKEN"})
	want := []string{"Path=C:/tools", "TEMP=C:/temp", "token=last"}
	if !slices.Equal(got, want) {
		t.Fatalf("Provider = %v, want %v", got, want)
	}
	got[0] = "MUTATED=yes"
	if input[0] != "Path=C:/tools" {
		t.Fatal("Provider aliases caller input")
	}
}

func TestSelectDistinguishesNilAndExplicitEmptyInputs(t *testing.T) {
	t.Setenv("GOATEST_ENVIRONMENT_SELECTION", "ready")
	if got := environment.Select(nil, []string{"GOATEST_ENVIRONMENT_SELECTION"}); !slices.Equal(got, []string{"GOATEST_ENVIRONMENT_SELECTION=ready"}) {
		t.Fatalf("nil selection = %v", got)
	}
	if got := environment.Select([]string{}, []string{"GOATEST_ENVIRONMENT_SELECTION"}); got == nil || len(got) != 0 {
		t.Fatalf("explicit empty selection = %#v", got)
	}
}
