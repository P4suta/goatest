// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package goatest

import (
	"slices"
	"strings"
	"testing"
)

type ScopeKind string

const (
	ScopeUnit ScopeKind = "unit"

	ScopeIntegration ScopeKind = "integration"
)

type TestScope struct {
	Kind         ScopeKind
	capabilities []string
}

func Unit() TestScope { return TestScope{Kind: ScopeUnit} }

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

func (scope TestScope) Capabilities() []string {
	return slices.Clone(scope.capabilities)
}

type T struct {
	*testing.T
	scope TestScope
}

func (t *T) Scope() TestScope {
	if t == nil {
		return TestScope{}
	}
	return t.scope
}

func Run(t *testing.T, scope TestScope, body func(*T)) {
	t.Helper()
	body(&T{T: t, scope: TestScope{Kind: scope.Kind, capabilities: slices.Clone(scope.capabilities)}})
}
