// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package goatest_test

import (
	"slices"
	"testing"

	"github.com/P4suta/goatest"
)

func TestRunPreservesTestingTAndScope(t *testing.T) {
	called := false
	goatest.Run(t, goatest.Unit(), func(gt *goatest.T) {
		called = true
		if gt.T != t {
			t.Fatal("goatest.T did not embed the original testing.T")
		}
		if got := gt.Scope(); got.Kind != goatest.ScopeUnit || got.Capabilities() != nil {
			t.Errorf("scope = %+v", got)
		}
	})
	if !called {
		t.Fatal("Run did not execute the test body")
	}
}

func TestIntegrationCarriesUniqueCapabilitiesWithoutAliasing(t *testing.T) {
	got := goatest.Integration("postgres", "redis", "postgres")
	if got.Kind != goatest.ScopeIntegration || !slices.Equal(got.Capabilities(), []string{"postgres", "redis"}) {
		t.Fatalf("scope = %+v", got)
	}
	capabilities := got.Capabilities()
	capabilities[0] = "mutated"
	if !slices.Equal(got.Capabilities(), []string{"postgres", "redis"}) {
		t.Fatal("Capabilities aliases internal metadata")
	}
}

func TestIntegrationRejectsBlankCapability(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("Integration accepted a blank capability")
		}
	}()
	_ = goatest.Integration(" \t")
}
