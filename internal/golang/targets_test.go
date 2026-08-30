// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package golang_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
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
//goatest:resources redis
func FuzzExisting(f *testing.F) { f.Add([]byte{}); f.Fuzz(func(t *testing.T, b []byte) {}) }
func TestWrapped(t *testing.T) { gt.Run(t, gt.Integration(" postgres ", "redis", "postgres"), func(t *gt.T) {}) }
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
	if !slices.Equal(names, []string{"FuzzExisting", "TestExisting", "TestWrapped"}) {
		t.Fatalf("targets = %v", names)
	}
	for _, target := range targets {
		if target.Name == "TestWrapped" && (target.Kind != gotest.KindTest || target.Capability != "postgres" ||
			!slices.Equal(target.Capabilities, []string{"postgres", "redis"})) {
			t.Errorf("wrapped integration = %+v", target)
		}
		if target.Name == "FuzzExisting" && (target.Kind != gotest.KindFuzz || !slices.Equal(target.Capabilities, []string{"redis"})) {
			t.Errorf("native fuzz = %+v", target)
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

func TestDiscoverTargetsCanonicalizesPackagesAndFiltersNonTargets(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeGo(t, root, "a/a_test.go", `package a
import std "testing"
var declaration = 1
type suite struct{}
func (suite) TestMethod(t *std.T) {}
func TestDeclaration(t *std.T)
func TestSame(t *std.T) {}
func Testlower(t *std.T) {}
func TestWrong(t std.T) {}
`)
	writeGo(t, root, "a/looks.go", "package a\nimport \"testing\"\nfunc TestHidden(t *testing.T) {}\n")
	writeGo(t, root, "a/ignored_test.go/nested_test.go", "package ignored\nimport \"testing\"\nfunc TestHidden(t *testing.T) {}\n")
	writeGo(t, root, "b/b_test.go", "package b\nimport \"testing\"\nfunc TestSame(t *testing.T) {}\n")

	packages := []gotest.Package{
		{ImportPath: "example.com/sample/b", RelativeDir: "b", Dependencies: []string{"dep/b"}},
		{ImportPath: "example.com/sample/a", RelativeDir: "a", Dependencies: []string{"dep/a"}},
	}
	targets, err := gotest.DiscoverTargets(root, packages)
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 2 {
		t.Fatalf("targets = %+v", targets)
	}
	for index, wantPackage := range []string{"example.com/sample/a", "example.com/sample/b"} {
		target := targets[index]
		wantPath := string('a'+rune(index)) + "/" + string('a'+rune(index)) + "_test.go"
		if target.Name != "TestSame" || target.Kind != gotest.KindTest || target.Package != wantPackage || target.RelativeDir != string('a'+rune(index)) || target.Path != wantPath || target.Line <= 0 {
			t.Errorf("target[%d] = %+v", index, target)
		}
		if target.ID != gotest.TargetID(target.Package, target.Name, target.Kind, target.Path, target.Line) {
			t.Errorf("target[%d] ID = %q", index, target.ID)
		}
	}
	packages[0].Dependencies[0] = "mutated"
	if !slices.Equal(targets[1].Dependencies, []string{"dep/b"}) {
		t.Fatalf("target dependencies alias input: %v", targets[1].Dependencies)
	}
}

func TestDiscoverTargetsTreatsEmptyRelativeDirectoryAsRoot(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeGo(t, root, "root_test.go", "package root\nimport \"testing\"\nfunc TestRoot(t *testing.T) {}\n")
	targets, err := gotest.DiscoverTargets(root, []gotest.Package{{ImportPath: "example.com/sample", RelativeDir: ""}})
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 1 || targets[0].Path != "root_test.go" {
		t.Fatalf("targets = %+v", targets)
	}
}

func TestDiscoverTargetsReportsDirectoryAndParseFailures(t *testing.T) {
	t.Parallel()
	t.Run("directory", func(t *testing.T) {
		root := t.TempDir()
		_, err := gotest.DiscoverTargets(root, []gotest.Package{{ImportPath: "example.com/missing", RelativeDir: "missing"}})
		if err == nil || !strings.HasPrefix(err.Error(), "goatest: read package example.com/missing: ") {
			t.Fatalf("DiscoverTargets error = %v", err)
		}
	})
	t.Run("parse", func(t *testing.T) {
		root := t.TempDir()
		path := filepath.Join(root, "broken_test.go")
		writeGo(t, root, "broken_test.go", "package broken\nfunc TestBroken(")
		_, err := gotest.DiscoverTargets(root, []gotest.Package{{ImportPath: "example.com/broken", RelativeDir: "."}})
		if err == nil || !strings.HasPrefix(err.Error(), "goatest: parse "+path+": ") {
			t.Fatalf("DiscoverTargets error = %v", err)
		}
	})
}
