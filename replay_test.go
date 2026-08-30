// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package goatest_test

import (
	"encoding/base64"
	"encoding/json"
	"slices"
	"strings"
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
	for _, token := range []string{
		"other-v1:e30",
		"goatest-replay-v1:not*base64",
		replayTokenFromJSON(t, []byte(`{`)),
		replayTokenFromJSON(t, []byte(`{"input":"AQ==","draws":[],"unknown":true}`)),
		replayTokenFromJSON(t, []byte(`{} {}`)),
	} {
		if _, err := goatest.ParseReplayToken(token); err == nil {
			t.Errorf("ParseReplayToken(%q) succeeded", token)
		}
	}
	if _, err := goatest.ShrinkReplayToken("goatest-replay-v1:not*base64"); err == nil {
		t.Fatal("ShrinkReplayToken accepted an invalid token")
	}
	if _, err := goatest.ParseReplayToken("goatest-replay-v1:not*base64"); err == nil || !strings.Contains(err.Error(), "replay token encoding") {
		t.Fatalf("invalid base64 error = %v, want encoding classification", err)
	}
}

func TestReplayShrinksToExactUniqueLengthsAndPreservesMetadata(t *testing.T) {
	for _, testCase := range []struct {
		length int
		want   []int
	}{
		{length: 0, want: nil},
		{length: 1, want: []int{0}},
		{length: 2, want: []int{0, 1}},
		{length: 5, want: []int{0, 2, 4}},
	} {
		input := make([]byte, testCase.length)
		for index := range input {
			input[index] = byte(index + 1)
		}
		token := replayToken(t, goatest.Replay{Input: input, Draws: []string{"value"}, Classifications: []string{"edge"}})
		candidates, err := goatest.ShrinkReplayToken(token)
		if err != nil {
			t.Fatal(err)
		}
		got := make([]int, len(candidates))
		for index, candidate := range candidates {
			parsed, parseErr := goatest.ParseReplayToken(candidate)
			if parseErr != nil {
				t.Fatal(parseErr)
			}
			got[index] = len(parsed.Input)
			if !slices.Equal(parsed.Draws, []string{"value"}) || !slices.Equal(parsed.Classifications, []string{"edge"}) {
				t.Fatalf("metadata changed: %+v", parsed)
			}
		}
		if !slices.Equal(got, testCase.want) {
			t.Errorf("input length %d shrank to %v, want %v", testCase.length, got, testCase.want)
		}
	}
}

func TestParsedReplayOwnsIndependentSlices(t *testing.T) {
	token := replayToken(t, goatest.Replay{Input: []byte{1, 2}, Draws: []string{"draw"}, Classifications: []string{"class"}})
	first, err := goatest.ParseReplayToken(token)
	if err != nil {
		t.Fatal(err)
	}
	first.Input[0] = 9
	first.Draws[0] = "changed"
	first.Classifications[0] = "changed"
	second, err := goatest.ParseReplayToken(token)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(second.Input, []byte{1, 2}) || !slices.Equal(second.Draws, []string{"draw"}) || !slices.Equal(second.Classifications, []string{"class"}) {
		t.Fatalf("parsed replay retained caller mutations: %+v", second)
	}
}

func replayToken(t *testing.T, replay goatest.Replay) string {
	t.Helper()
	payload, err := json.Marshal(replay)
	if err != nil {
		t.Fatal(err)
	}
	return replayTokenFromJSON(t, payload)
}

func replayTokenFromJSON(t *testing.T, payload []byte) string {
	t.Helper()
	return "goatest-replay-v1:" + base64.RawURLEncoding.EncodeToString(payload)
}

func FuzzReplayParserAndShrinker(f *testing.F) {
	f.Add([]byte(`{"input":"AQID","draws":["value"],"classifications":["seed"]}`))
	f.Add([]byte(`{}`))
	f.Fuzz(func(t *testing.T, payload []byte) {
		token := "goatest-replay-v1:" + base64.RawURLEncoding.EncodeToString(payload)
		replay, err := goatest.ParseReplayToken(token)
		if err != nil {
			return
		}
		candidates, err := goatest.ShrinkReplayToken(token)
		if err != nil {
			t.Fatal(err)
		}
		for _, candidate := range candidates {
			shrunk, err := goatest.ParseReplayToken(candidate)
			if err != nil {
				t.Fatalf("generated invalid shrink: %v", err)
			}
			if len(shrunk.Input) >= len(replay.Input) {
				t.Fatalf("shrink length = %d, input length = %d", len(shrunk.Input), len(replay.Input))
			}
			if !slices.Equal(shrunk.Draws, replay.Draws) || !slices.Equal(shrunk.Classifications, replay.Classifications) {
				t.Fatalf("shrink changed trace metadata: %+v -> %+v", replay, shrunk)
			}
		}
	})
}
