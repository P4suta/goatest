// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package goatest_test

import (
	"strings"
	"testing"

	"github.com/P4suta/goatest"
	"github.com/P4suta/goatest/gen"
)

func TestRunPreservesTestingTAndScope(t *testing.T) {
	called := false
	goatest.Run(t, goatest.Integration("postgres"), func(gt *goatest.T) {
		called = true
		if gt.T != t {
			t.Fatal("goatest.T did not embed the original testing.T")
		}
		if got := gt.Scope(); got.Kind != goatest.ScopeIntegration || got.Capability != "postgres" {
			t.Errorf("scope = %+v", got)
		}
	})
	if !called {
		t.Fatal("Run did not execute the test body")
	}
}

func TestDrawIsDeterministicAndLabelsTheReplayTrace(t *testing.T) {
	var firstValue, firstToken string
	goatest.Run(t, goatest.Unit(), func(gt *goatest.T) {
		firstValue = goatest.Draw(gt, "input", gen.String())
		firstToken = gt.ReplayToken()
	})
	var secondValue, secondToken string
	goatest.Run(t, goatest.Unit(), func(gt *goatest.T) {
		secondValue = goatest.Draw(gt, "input", gen.String())
		secondToken = gt.ReplayToken()
	})
	if firstValue != secondValue || firstToken != secondToken {
		t.Fatalf("draws differ: (%q, %q) != (%q, %q)", firstValue, firstToken, secondValue, secondToken)
	}
	if !strings.HasPrefix(firstToken, "goatest-replay-v1:") {
		t.Fatalf("token = %q, want versioned prefix", firstToken)
	}
}

func FuzzCheckUsesOneByteSliceInput(f *testing.F) {
	goatest.Check(f, goatest.Unit(), func(gt *goatest.T) {
		value := goatest.Draw(gt, "value", gen.String())
		if string([]byte(value)) != value {
			gt.Fatalf("string round trip changed %q", value)
		}
	})
}
