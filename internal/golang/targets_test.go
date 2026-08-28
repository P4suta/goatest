// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package golang_test

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	gotest "github.com/P4suta/goatest/internal/golang"
)

func TestDiscoverTargetsKeepsExistingAndWrappedTests(t *testing.T) {
	root := t.TempDir()
	source := `package sample

import (
	"testing"
	gt "github.com/P4suta/goatest"
)

func TestExisting(t *testing.T) {}
func FuzzExisting(f *testing.F) { f.Add([]byte{}); f.Fuzz(func(t *testing.T, b []byte) {}) }
func TestWrapped(t *testing.T) { gt.Run(t, gt.Integration("postgres"), func(t *gt.T) {}) }
func FuzzWrapped(f *testing.F) { gt.Check(f, gt.Unit(), func(t *gt.T) {}) }
func helper(t *testing.T) {}
`
	if err := os.WriteFile(filepath.Join(root, "sample_test.go"), []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}
	packages := []gotest.Package{{ImportPath: "example.com/sample", RelativeDir: ".", Dependencies: []string{"testing"}}}
	targets, err := gotest.DiscoverTargets(root, packages)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, len(targets))
	for i, target := range targets {
		names[i] = target.Name
		if target.ID == "" || target.Package != "example.com/sample" {
			t.Errorf("target = %+v", target)
		}
	}
	if !slices.Equal(names, []string{"FuzzExisting", "FuzzWrapped", "TestExisting", "TestWrapped"}) {
		t.Fatalf("targets = %v", names)
	}
	for _, target := range targets {
		if target.Name == "TestWrapped" && (target.Kind != gotest.KindTest || target.Capability != "postgres") {
			t.Errorf("wrapped integration = %+v", target)
		}
		if target.Name == "FuzzWrapped" && target.Kind != gotest.KindFuzz {
			t.Errorf("wrapped fuzz = %+v", target)
		}
	}
}

func TestTargetIDsMoveWithSemanticLocationNotDiscoveryOrder(t *testing.T) {
	one := gotest.TargetID("example.com/sample", "TestX", gotest.KindTest, "a_test.go", 10)
	two := gotest.TargetID("example.com/sample", "TestX", gotest.KindTest, "a_test.go", 10)
	other := gotest.TargetID("example.com/sample", "TestX", gotest.KindTest, "a_test.go", 11)
	if one != two || one == other || len(one) != 16 {
		t.Fatalf("ids = %q %q %q", one, two, other)
	}
}
