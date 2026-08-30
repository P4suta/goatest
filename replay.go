// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package goatest

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
)

// Replay is the stable decoded form of a replay-v1 token.
type Replay struct {
	Input           []byte   `json:"input"`
	Draws           []string `json:"draws"`
	Classifications []string `json:"classifications,omitempty"`
}

// ParseReplayToken decodes a replay-v1 token and rejects other versions,
// malformed encodings, trailing JSON, and unknown fields.
func ParseReplayToken(token string) (Replay, error) {
	payload, ok := strings.CutPrefix(token, replayPrefix)
	if !ok {
		return Replay{}, errors.New("goatest: replay token has an unknown version")
	}
	encoded, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return Replay{}, fmt.Errorf("goatest: replay token encoding: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var replay Replay
	if err := decoder.Decode(&replay); err != nil {
		return Replay{}, fmt.Errorf("goatest: replay token payload: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Replay{}, errors.New("goatest: replay token has trailing data")
	}
	replay.Input = slices.Clone(replay.Input)
	replay.Draws = slices.Clone(replay.Draws)
	replay.Classifications = slices.Clone(replay.Classifications)
	return replay, nil
}

// ShrinkReplayToken returns deterministic, strictly shorter input traces while
// preserving draw labels and classifications. It is the byte-trace shrinking
// layer shared by property and mutation-guided execution.
func ShrinkReplayToken(token string) ([]string, error) {
	replay, err := ParseReplayToken(token)
	if err != nil {
		return nil, err
	}
	if len(replay.Input) == 0 {
		return nil, nil
	}
	lengths := []int{0, len(replay.Input) / 2, len(replay.Input) - 1}
	seen := make(map[int]bool, len(lengths))
	tokens := make([]string, 0, len(lengths))
	for _, length := range lengths {
		if seen[length] {
			continue
		}
		seen[length] = true
		candidate := replay
		candidate.Input = slices.Clone(replay.Input[:length])
		tokens = append(tokens, encodeReplay(candidate))
	}
	return tokens, nil
}

func encodeReplay(replay Replay) string {
	payload, _ := json.Marshal(replay)
	return replayPrefix + base64.RawURLEncoding.EncodeToString(payload)
}
