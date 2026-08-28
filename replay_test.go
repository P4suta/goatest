// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package goatest_test

import (
	"slices"
	"testing"

	"github.com/P4suta/goatest"
	"github.com/P4suta/goatest/gen"
)

func TestReplayTokenRoundTripsTraceAndShrinksDeterministically(t *testing.T) {
	var token string
	goatest.Run(t, goatest.Unit(), func(gt *goatest.T) {
		_ = goatest.Draw(gt, "left", gen.IntRange(0, 100))
		_ = goatest.Draw(gt, "right", gen.StringRange(1, 4))
		gt.Classify("non-empty", true)
		gt.Classify("ignored", false)
		token = gt.ReplayToken()
	})
	replay, err := goatest.ParseReplayToken(token)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(replay.Draws, []string{"left", "right"}) {
		t.Errorf("draws = %v", replay.Draws)
	}
	if !slices.Equal(replay.Classifications, []string{"non-empty"}) {
		t.Errorf("classifications = %v", replay.Classifications)
	}
	first, err := goatest.ShrinkReplayToken(token)
	if err != nil {
		t.Fatal(err)
	}
	second, err := goatest.ShrinkReplayToken(token)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(first, second) || len(first) == 0 {
		t.Fatalf("shrinks are not deterministic and non-empty: %v / %v", first, second)
	}
	for _, candidate := range first {
		parsed, parseErr := goatest.ParseReplayToken(candidate)
		if parseErr != nil {
			t.Fatalf("parsing shrink: %v", parseErr)
		}
		if len(parsed.Input) >= len(replay.Input) {
			t.Errorf("candidate did not shrink input: %d >= %d", len(parsed.Input), len(replay.Input))
		}
	}
}

func TestReplayRejectsUnknownVersionsAndMalformedPayloads(t *testing.T) {
	for _, token := range []string{"other-v1:e30", "goatest-replay-v1:not-base64"} {
		if _, err := goatest.ParseReplayToken(token); err == nil {
			t.Errorf("ParseReplayToken(%q) succeeded", token)
		}
	}
}
