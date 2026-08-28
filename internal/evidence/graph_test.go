// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package evidence_test

import (
	"slices"
	"testing"

	"github.com/P4suta/goatest/internal/evidence"
)

func TestGraphNarrowsKnownChangesAndBroadensUnknownOnes(t *testing.T) {
	graph := evidence.Graph{
		FilePackages: map[string]string{
			"a/a.go": "example/a",
			"b/b.go": "example/b",
		},
		Targets: []evidence.Target{
			{ID: "TestA", Package: "example/a", Dependencies: []string{"example/b"}, CoveredFiles: []string{"a/a.go", "b/b.go"}},
			{ID: "TestB", Package: "example/b", CoveredFiles: []string{"b/b.go"}},
			{ID: "TestC", Package: "example/c", CoveredFiles: []string{"c/c.go"}},
		},
	}
	known := graph.Affected([]string{"a/a.go"})
	if known.Broad || !slices.Equal(known.Targets, []string{"TestA"}) {
		t.Errorf("known = %+v", known)
	}
	dependency := graph.Affected([]string{"b/b.go"})
	if dependency.Broad || !slices.Equal(dependency.Targets, []string{"TestA", "TestB"}) {
		t.Errorf("dependency = %+v", dependency)
	}
	unknown := graph.Affected([]string{"generated/unknown.go"})
	if !unknown.Broad || !slices.Equal(unknown.Targets, []string{"TestA", "TestB", "TestC"}) {
		t.Errorf("unknown = %+v", unknown)
	}
}

func TestGraphBytesAreCanonical(t *testing.T) {
	graph := evidence.Graph{
		FilePackages: map[string]string{"z.go": "z", "a.go": "a"},
		Targets:      []evidence.Target{{ID: "z"}, {ID: "a"}},
	}
	one, err := graph.JSON()
	if err != nil {
		t.Fatal(err)
	}
	two, err := graph.JSON()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(one, two) {
		t.Fatal("graph JSON is not deterministic")
	}
}
