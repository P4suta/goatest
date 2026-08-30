// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package evidence_test

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
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

func TestGraphAffectedIsolatesTestStructureDependencyAndCoverageEdges(t *testing.T) {
	graph := evidence.Graph{
		FilePackages: map[string]string{
			"pkg/code.go":        "example/pkg",
			"pkg/code_test.go":   "example/pkg",
			"dep/dep.go":         "example/dep",
			"other/code_test.go": "example/other",
		},
		Targets: []evidence.Target{
			{ID: "z-structure", Package: "example/pkg"},
			{ID: "a-dependency", Package: "example/consumer", Dependencies: []string{"example/dep"}},
			{ID: "m-coverage", Package: "example/else", CoveredFiles: []string{"pkg/code.go"}},
			{ID: "unrelated", Package: "example/other"},
		},
	}
	for _, testCase := range []struct {
		name    string
		changed string
		want    []string
	}{
		{name: "structure", changed: `pkg\code_test.go`, want: []string{"z-structure"}},
		{name: "dependency", changed: "dep/dep.go", want: []string{"a-dependency"}},
		{name: "coverage", changed: "pkg/code.go", want: []string{"m-coverage"}},
		{name: "wrong-test-package", changed: "other/code_test.go", want: []string{"unrelated"}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			got := graph.Affected([]string{testCase.changed})
			if got.Broad || !slices.Equal(got.Targets, testCase.want) {
				t.Fatalf("Affected(%q) = %+v, want %v", testCase.changed, got, testCase.want)
			}
		})
	}
	got := graph.Affected([]string{"pkg/code.go", "dep/dep.go", "pkg/code.go"})
	if got.Broad || !slices.Equal(got.Targets, []string{"a-dependency", "m-coverage"}) {
		t.Fatalf("union = %+v", got)
	}
	if got := graph.Affected(nil); got.Broad || len(got.Targets) != 0 {
		t.Fatalf("empty change = %+v", got)
	}
}

func TestGraphBytesAreCanonical(t *testing.T) {
	graph := evidence.Graph{
		FilePackages: map[string]string{"z.go": "z", "a.go": "a"},
		Targets: []evidence.Target{
			{ID: "z", Dependencies: []string{"z", "a"}, CoveredFiles: []string{"z.go", "a.go"}},
			{ID: "a", Dependencies: []string{"d"}, CoveredFiles: []string{"d.go"}},
		},
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
	if len(one) == 0 || one[len(one)-1] != '\n' {
		t.Fatalf("graph JSON has no final newline: %q", one)
	}
	var decoded evidence.Graph
	if err := json.Unmarshal(one, &decoded); err != nil {
		t.Fatal(err)
	}
	if got := []string{decoded.Targets[0].ID, decoded.Targets[1].ID}; !slices.Equal(got, []string{"a", "z"}) {
		t.Fatalf("target order = %v", got)
	}
	if !slices.Equal(decoded.Targets[1].Dependencies, []string{"a", "z"}) || !slices.Equal(decoded.Targets[1].CoveredFiles, []string{"a.go", "z.go"}) {
		t.Fatalf("canonical target = %+v", decoded.Targets[1])
	}
	if !reflect.DeepEqual(graph.Targets[0].Dependencies, []string{"z", "a"}) || !strings.Contains(string(one), `"a.go": "a"`) {
		t.Fatalf("canonicalization mutated input or omitted map: input=%+v json=%s", graph, one)
	}
}
