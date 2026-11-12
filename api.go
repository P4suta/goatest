// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package goatest

import (
	"slices"
	"strings"
	"testing"
)

// ScopeKind is which of the two kinds of evidence a test produces.
//
// There are two and there will not be a third. The line is not "fast and slow"
// or "small and large" — those are consequences — but whether the test needs
// anything the process does not already hold. A runner can schedule, sandbox
// and cache the two differently only because the distinction is about
// dependencies rather than about size.
type ScopeKind string

const (
	// ScopeUnit is a test that needs nothing outside its own process.
	//
	// It may read testdata and write to a temporary directory, because those
	// come with the test binary and the operating system. It may not start a
	// child process, open a socket, or read a tool from PATH.
	ScopeUnit ScopeKind = "unit"

	// ScopeIntegration is a test that needs something the process does not
	// hold, named by the capabilities of its [TestScope].
	ScopeIntegration ScopeKind = "integration"
)

// TestScope is what one test declares about itself.
//
// The zero value is not a valid declaration and is not a unit test: it is the
// absence of one, which is what [T.Scope] returns for a nil receiver. Build a
// scope with [Unit] or [Integration].
type TestScope struct {
	// Kind is which of the two kinds of evidence the test produces.
	Kind ScopeKind

	// capabilities is what an integration test requires, deduplicated and in
	// the order first given.
	//
	// It is unexported so that a scope cannot be edited after it is declared,
	// and read through [TestScope.Capabilities], which copies. A declaration a
	// caller could still change is a declaration a runner cannot rely on.
	capabilities []string
}

// Unit declares a test that needs nothing outside its own process.
func Unit() TestScope { return TestScope{Kind: ScopeUnit} }

// Integration declares a test that requires the named capabilities.
//
// A capability is a free-form name for something the test cannot supply
// itself — "postgres", "redis", "go", "git", "network". Whether two suites mean
// the same thing by one name is a question this package cannot answer, and
// deliberately does not try to: the names are a vocabulary the project using
// them owns.
//
// Names are trimmed and deduplicated, keeping the order first given, so that a
// declaration reads as a set rather than as a list that happens to repeat.
//
// It panics on no capabilities and on a blank one, because both are a
// declaration that says nothing while looking like it says something, and a
// runner reading it would provision nothing and report success. A test that
// truly needs nothing is [Unit], and saying so is not a special case.
func Integration(capabilities ...string) TestScope {
	if len(capabilities) == 0 {
		panic("goatest: integration requires at least one capability")
	}
	values := make([]string, 0, len(capabilities))
	seen := make(map[string]bool, len(capabilities))
	for _, capability := range capabilities {
		capability = strings.TrimSpace(capability)
		if capability == "" {
			panic("goatest: integration capability must not be blank")
		}
		if !seen[capability] {
			seen[capability] = true
			values = append(values, capability)
		}
	}
	return TestScope{Kind: ScopeIntegration, capabilities: values}
}

// Capabilities reports what the scope requires, as a copy.
//
// A [Unit] scope requires nothing and reports nil, which is a different answer
// from an empty non-nil slice: there is no such thing as an integration test
// that requires nothing, so nil is the only shape "nothing" can take here.
func (scope TestScope) Capabilities() []string {
	return slices.Clone(scope.capabilities)
}

// T is a [testing.T] that knows the scope its test declared.
//
// It embeds the original, so every method of [testing.T] is available and
// behaves exactly as it would have. Nothing is intercepted: this type adds a
// fact and takes nothing away.
type T struct {
	*testing.T

	// scope is the declaration [Run] was given, copied so that the caller's
	// value cannot be changed underneath the test.
	scope TestScope
}

// Scope reports the declaration the test was run under.
//
// A nil receiver reports the zero [TestScope] rather than panicking, because a
// helper that reads the scope of a test it was not given should be able to say
// "none" without ending the run.
func (t *T) Scope() TestScope {
	if t == nil {
		return TestScope{}
	}
	return t.scope
}

// Run runs body as a test declared under scope.
//
// It is a declaration and not a mechanism. Run starts no database, reads no
// environment, and never skips: what to do about a capability that is not
// available is the runner's decision, and a library that decided it here would
// have decided it for every runner. The goatest command reads these
// declarations; anything else may too.
//
// The scope is copied on the way in, so a caller that reuses and mutates one
// value cannot change what an already-running test declared.
//
// For a test that must carry its declaration in a comment rather than in
// code — a fuzz target, or a test a generator writes — the equivalent is
// `//goatest:resources postgres redis` on the line above the function.
func Run(t *testing.T, scope TestScope, body func(*T)) {
	t.Helper()
	body(&T{T: t, scope: TestScope{Kind: scope.Kind, capabilities: slices.Clone(scope.capabilities)}})
}
