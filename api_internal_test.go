// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package goatest

import (
	"crypto/sha256"
	"encoding/binary"
	"slices"
	"testing"
)

func TestTUint64ConsumesPartialRawInputThenExpandsWithStableHash(t *testing.T) {
	raw := []byte{1, 2, 3, 4, 5, 6, 7, 8, 9}
	draw := &T{raw: slices.Clone(raw)}
	if got, want := draw.Uint64(), binary.LittleEndian.Uint64(raw[:8]); got != want {
		t.Fatalf("first draw = %d, want %d", got, want)
	}
	if got := draw.Uint64(); got != 9 {
		t.Fatalf("partial draw = %d, want 9", got)
	}
	var counter [8]byte
	binary.LittleEndian.PutUint64(counter[:], uint64(len(raw)))
	hash := sha256.New()
	_, _ = hash.Write([]byte("goatest-draw-v1\x00"))
	_, _ = hash.Write(raw)
	_, _ = hash.Write(counter[:])
	wantExpanded := binary.LittleEndian.Uint64(hash.Sum(nil)[:8])
	if got := draw.Uint64(); got != wantExpanded {
		t.Fatalf("expanded draw = %d, want %d", got, wantExpanded)
	}
	if draw.offset != len(raw)+8 {
		t.Fatalf("offset = %d, want %d", draw.offset, len(raw)+8)
	}
}

func TestNilTAccessorsAreStableAndReplayable(t *testing.T) {
	var draw *T
	if scope := draw.Scope(); scope != (TestScope{}) {
		t.Fatalf("nil scope = %+v", scope)
	}
	if value := draw.Uint64(); value != 0 {
		t.Fatalf("nil draw = %d", value)
	}
	draw.Classify("ignored", true)
	token := draw.ReplayToken()
	if token == "" {
		t.Fatal("nil replay token is empty")
	}
	replay, err := ParseReplayToken(token)
	if err != nil {
		t.Fatal(err)
	}
	if len(replay.Input) != 0 || len(replay.Draws) != 0 || len(replay.Classifications) != 0 {
		t.Fatalf("nil replay = %+v", replay)
	}
}
