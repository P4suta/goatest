// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package goatest

import (
	"slices"
	"strings"
	"testing"
)

// ScopeKind identifies the execution resources a test definition needs.
type ScopeKind string

const (
	// ScopeUnit requires no externally managed capability.
	ScopeUnit ScopeKind = "unit"
	// ScopeIntegration declares one or more externally managed capabilities.
	ScopeIntegration ScopeKind = "integration"
)

// TestScope is immutable metadata attached to one Run definition.
type TestScope struct {
	Kind         ScopeKind
	capabilities []string
}

// Unit declares a test that requires no managed resource.
func Unit() TestScope { return TestScope{Kind: ScopeUnit} }

// Integration declares a test that requires one or more capabilities.
// Ordinary go test runs it against the environment the caller already
// supplied; the goatest CLI can arrange that environment through configured
// resource providers.
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

// Capabilities returns every managed resource declared by this scope.
func (scope TestScope) Capabilities() []string {
	return slices.Clone(scope.capabilities)
}

// T embeds the original testing.T and exposes immutable resource metadata.
type T struct {
	*testing.T
	scope TestScope
}

// Scope returns the metadata supplied to Run.
func (t *T) Scope() TestScope {
	if t == nil {
		return TestScope{}
	}
	return t.scope
}

// Run executes body as an ordinary testing.T test carrying resource metadata.
func Run(t *testing.T, scope TestScope, body func(*T)) {
	t.Helper()
	body(&T{T: t, scope: TestScope{Kind: scope.Kind, capabilities: slices.Clone(scope.capabilities)}})
}
