// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package goatest

import (
	"crypto/sha256"
	"encoding/binary"
	"slices"
	"testing"

	"github.com/P4suta/goatest/gen"
)

const replayPrefix = "goatest-replay-v1:"

// ScopeKind identifies the execution resources a test definition needs.
type ScopeKind string

const (
	// ScopeUnit requires no externally managed capability.
	ScopeUnit ScopeKind = "unit"
	// ScopeIntegration declares one externally managed capability.
	ScopeIntegration ScopeKind = "integration"
)

// TestScope is immutable metadata attached to one Run or Check definition.
type TestScope struct {
	Kind       ScopeKind
	Capability string
}

// Unit declares a test that requires no managed resource.
func Unit() TestScope { return TestScope{Kind: ScopeUnit} }

// Integration declares a test that requires capability. Ordinary go test runs
// it against the environment the caller already supplied; the goatest CLI can
// arrange that environment through a configured resource provider.
func Integration(capability string) TestScope {
	return TestScope{Kind: ScopeIntegration, Capability: capability}
}

// T embeds the original testing.T and carries deterministic draw state.
type T struct {
	*testing.T

	scope   TestScope
	raw     []byte
	offset  int
	draws   []string
	classes []string
}

// Scope returns the metadata supplied to Run or Check.
func (t *T) Scope() TestScope {
	if t == nil {
		return TestScope{}
	}
	return t.scope
}

// Uint64 implements gen.Source. Bytes from the standard fuzz input are
// consumed first; deterministic SHA-256 expansion supplies any remaining
// choices without consulting a process-global random source.
func (t *T) Uint64() uint64 {
	if t == nil {
		return 0
	}
	if t.offset < len(t.raw) {
		var block [8]byte
		copied := copy(block[:], t.raw[t.offset:])
		t.offset += copied
		return binary.LittleEndian.Uint64(block[:])
	}
	var counter [8]byte
	binary.LittleEndian.PutUint64(counter[:], uint64(t.offset))
	hash := sha256.New()
	_, _ = hash.Write([]byte("goatest-draw-v1\x00"))
	_, _ = hash.Write(t.raw)
	_, _ = hash.Write(counter[:])
	t.offset += 8
	return binary.LittleEndian.Uint64(hash.Sum(nil)[:8])
}

// Classify records label when condition holds. Classifications are included in
// replay tokens and reports; they never alter generated values.
func (t *T) Classify(label string, condition bool) {
	if t == nil || !condition {
		return
	}
	t.classes = append(t.classes, label)
}

// ReplayToken returns a versioned, deterministic encoding of the fuzz bytes,
// draw labels, and classifications consumed by this execution.
func (t *T) ReplayToken() string {
	if t == nil {
		return encodeReplay(Replay{})
	}
	return encodeReplay(Replay{
		Input:           slices.Clone(t.raw),
		Draws:           slices.Clone(t.draws),
		Classifications: slices.Clone(t.classes),
	})
}

// Run executes body as an ordinary testing.T test with deterministic draw
// state derived from the standard test name.
func Run(t *testing.T, scope TestScope, body func(*T)) {
	t.Helper()
	seed := sha256.Sum256([]byte("goatest-run-v1\x00" + t.Name()))
	body(newT(t, scope, seed[:]))
}

// Check exposes one []byte input to Go's standard fuzz engine. The same input
// drives every typed Draw in body and therefore works unchanged as a normal
// seed-corpus test, a coverage-guided fuzz target, or a mutation-guided target.
func Check(f *testing.F, scope TestScope, body func(*T)) {
	f.Helper()
	for _, seed := range [][]byte{
		{},
		[]byte("goatest-v1"),
		{0},
		{0xff},
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, input []byte) {
		body(newT(t, scope, input))
	})
}

// Draw obtains one typed value and records its stable label in the replay
// trace. Generator logic lives in package gen and depends only on gen.Source.
func Draw[V any](t *T, label string, generator gen.Generator[V]) V {
	t.Helper()
	t.draws = append(t.draws, label)
	return generator.Generate(t)
}

func newT(t *testing.T, scope TestScope, raw []byte) *T {
	return &T{T: t, scope: scope, raw: slices.Clone(raw)}
}
