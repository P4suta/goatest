// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package golang_test

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	gotest "github.com/P4suta/goatest/internal/golang"
)

func TestDecodePackageStreamComputesRelativeDirectories(t *testing.T) {
	stream := strings.NewReader(`{"ImportPath":"example.com/sample","Dir":"C:/snap","Module":{"Path":"example.com/sample","Dir":"C:/snap"},"Deps":["fmt"]}
{"ImportPath":"example.com/sample/sub","Dir":"C:/snap/sub","Module":{"Path":"example.com/sample","Dir":"C:/snap"},"Deps":["example.com/sample","fmt"]}
`)
	model, err := gotest.DecodePackages(stream)
	if err != nil {
		t.Fatal(err)
	}
	if model.ModulePath != "example.com/sample" || len(model.Packages) != 2 || model.Packages[0].RelativeDir != "." || model.Packages[1].RelativeDir != "sub" {
		t.Errorf("model = %+v", model)
	}
}

func TestDecodePackagesRejectsMalformedAndEmptyStreams(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name  string
		input string
		want  string
	}{
		{name: "empty", want: "goatest: go list returned no module packages"},
		{name: "malformed", input: "{", want: "goatest: decode go list package: unexpected EOF"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := gotest.DecodePackages(strings.NewReader(test.input))
			if err == nil || err.Error() != test.want {
				t.Fatalf("DecodePackages error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestDecodePackagesCanonicalizesPackages(t *testing.T) {
	t.Parallel()
	moduleDir := filepath.Join(t.TempDir(), "module")
	stream := packageStream(t,
		listedPackage("example.com/sample/z", filepath.Join(moduleDir, "z"), "example.com/sample", moduleDir, "zdep", "adep", "zdep"),
		listedPackage("standard/library", moduleDir, "", ""),
		listedPackage("example.com/sample", moduleDir, "example.com/sample", moduleDir),
	)

	model, err := gotest.DecodePackages(stream)
	if err != nil {
		t.Fatal(err)
	}
	if model.ModulePath != "example.com/sample" {
		t.Fatalf("ModulePath = %q", model.ModulePath)
	}
	if len(model.Packages) != 2 {
		t.Fatalf("Packages = %+v", model.Packages)
	}
	if model.Packages[0].ImportPath != "example.com/sample" || model.Packages[0].RelativeDir != "." {
		t.Errorf("root package = %+v", model.Packages[0])
	}
	if model.Packages[1].ImportPath != "example.com/sample/z" || model.Packages[1].RelativeDir != "z" {
		t.Errorf("sub package = %+v", model.Packages[1])
	}
	if !slices.Equal(model.Packages[1].Dependencies, []string{"adep", "zdep"}) {
		t.Errorf("Dependencies = %v", model.Packages[1].Dependencies)
	}
}

func TestDecodePackagesRejectsMultipleModuleRoots(t *testing.T) {
	t.Parallel()
	moduleDir := filepath.Join(t.TempDir(), "module")
	for _, other := range []map[string]any{
		listedPackage("other.example/path", moduleDir, "other.example", moduleDir),
		listedPackage("example.com/sample/other-checkout", moduleDir, "example.com/sample", filepath.Join(moduleDir, "other")),
	} {
		stream := packageStream(t,
			listedPackage("example.com/sample", moduleDir, "example.com/sample", moduleDir),
			other,
		)
		_, err := gotest.DecodePackages(stream)
		const want = "goatest: go list returned packages from multiple module roots; refusing partial package discovery"
		if err == nil || err.Error() != want {
			t.Fatalf("DecodePackages error = %v, want %q", err, want)
		}
	}
}

func TestDecodePackagesRejectsDirectoriesOutsideModule(t *testing.T) {
	t.Parallel()
	parent := t.TempDir()
	moduleDir := filepath.Join(parent, "module")
	for _, directory := range []string{parent, filepath.Join(parent, "sibling", "nested")} {
		stream := packageStream(t, listedPackage("example.com/sample", directory, "example.com/sample", moduleDir))
		_, err := gotest.DecodePackages(stream)
		const want = "goatest: package example.com/sample is outside module directory"
		if err == nil || err.Error() != want {
			t.Fatalf("DecodePackages(%q) error = %v, want %q", directory, err, want)
		}
	}
}

func TestDecodePackagesIncludesTestOnlyImportsInTheDependencyClosure(t *testing.T) {
	t.Parallel()
	moduleDir := filepath.Join(t.TempDir(), "module")
	model, err := gotest.DecodePackages(packageStream(t, testImportListing(moduleDir)...))
	if err != nil {
		t.Fatal(err)
	}
	if len(model.Packages) != 3 {
		t.Fatalf("Packages = %+v", model.Packages)
	}
	// The in-module test import is expanded through its own Deps, the external
	// one is recorded alone, the package itself is never its own dependency,
	// and a package whose tests import nothing keeps exactly its Deps.
	for index, want := range [][]string{
		{"example.com/m/internal/shared", "example.com/m/testutil", "fmt", "github.com/x/assert", "strings"},
		{"strings"},
		{"example.com/m/internal/shared", "strings"},
	} {
		if got := model.Packages[index].Dependencies; !slices.Equal(got, want) {
			t.Errorf("%s dependencies = %v, want %v", model.Packages[index].ImportPath, got, want)
		}
	}
}

func TestDecodePackagesDependencyClosureDoesNotDependOnListingOrder(t *testing.T) {
	t.Parallel()
	moduleDir := filepath.Join(t.TempDir(), "module")
	forward, err := gotest.DecodePackages(packageStream(t, testImportListing(moduleDir)...))
	if err != nil {
		t.Fatal(err)
	}
	backwardListing := testImportListing(moduleDir)
	slices.Reverse(backwardListing)
	backward, err := gotest.DecodePackages(packageStream(t, backwardListing...))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(backward, forward) {
		t.Fatalf("reversed listing = %+v, want %+v", backward, forward)
	}
}

func TestDecodePackagesLeavesAPackageWithoutTestImportsAsItsDeps(t *testing.T) {
	t.Parallel()
	moduleDir := filepath.Join(t.TempDir(), "module")
	absent := listedPackage("example.com/m/absent", filepath.Join(moduleDir, "absent"), "example.com/m", moduleDir, "strings", "fmt")
	empty := listedPackage("example.com/m/empty", filepath.Join(moduleDir, "empty"), "example.com/m", moduleDir, "fmt")
	empty["TestImports"] = []string{}
	empty["XTestImports"] = []string{}

	model, err := gotest.DecodePackages(packageStream(t, absent, empty))
	if err != nil {
		t.Fatal(err)
	}
	if len(model.Packages) != 2 || !slices.Equal(model.Packages[0].Dependencies, []string{"fmt", "strings"}) ||
		!slices.Equal(model.Packages[1].Dependencies, []string{"fmt"}) {
		t.Fatalf("Packages = %+v", model.Packages)
	}
}

// testImportListing is one module whose app package reaches
// example.com/m/testutil from its test files alone, and whose testutil package
// reaches example.com/m/internal/shared. The external test import and the
// self-import an external test package always carries are listed too.
func testImportListing(moduleDir string) []map[string]any {
	app := listedPackage("example.com/m/app", filepath.Join(moduleDir, "app"), "example.com/m", moduleDir, "fmt")
	app["TestImports"] = []string{"example.com/m/testutil"}
	app["XTestImports"] = []string{"example.com/m/app", "github.com/x/assert"}
	return []map[string]any{
		app,
		listedPackage("example.com/m/testutil", filepath.Join(moduleDir, "testutil"), "example.com/m", moduleDir, "example.com/m/internal/shared", "strings"),
		listedPackage("example.com/m/internal/shared", filepath.Join(moduleDir, "internal", "shared"), "example.com/m", moduleDir, "strings"),
	}
}

func listedPackage(importPath, directory, modulePath, moduleDir string, dependencies ...string) map[string]any {
	item := map[string]any{"ImportPath": importPath, "Dir": directory, "Deps": dependencies}
	if modulePath != "" || moduleDir != "" {
		item["Module"] = map[string]any{"Path": modulePath, "Dir": moduleDir}
	}
	return item
}

func packageStream(t *testing.T, packages ...map[string]any) *bytes.Reader {
	t.Helper()
	var stream bytes.Buffer
	encoder := json.NewEncoder(&stream)
	for _, item := range packages {
		if err := encoder.Encode(item); err != nil {
			t.Fatal(err)
		}
	}
	return bytes.NewReader(stream.Bytes())
}

// TestDecodePackagesRecordsEmbeddedFilesRelativeToTheModule pins the one input
// of a test binary that lives outside both the import graph and the package's
// own directory listing: `go list` reports embedded files relative to the
// package, and a consumer comparing them against a repository scan needs them
// relative to the module, once each and in a stable order.
func TestDecodePackagesRecordsEmbeddedFilesRelativeToTheModule(t *testing.T) {
	t.Parallel()
	moduleDir := filepath.Join(t.TempDir(), "module")
	embedding := listedPackage("example.com/m/app", filepath.Join(moduleDir, "app"), "example.com/m", moduleDir, "fmt")
	embedding["EmbedFiles"] = []string{"templates/page.tmpl", "static/app.css"}
	embedding["TestEmbedFiles"] = []string{"testdata/golden.txt", "templates/page.tmpl"}
	embedding["XTestEmbedFiles"] = []string{"testdata/external.txt"}
	root := listedPackage("example.com/m", moduleDir, "example.com/m", moduleDir, "fmt")
	root["EmbedFiles"] = []string{"schema.json"}
	plain := listedPackage("example.com/m/plain", filepath.Join(moduleDir, "plain"), "example.com/m", moduleDir, "fmt")

	model, err := gotest.DecodePackages(packageStream(t, embedding, root, plain))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][]string{
		"example.com/m": {"schema.json"},
		"example.com/m/app": {
			"app/static/app.css", "app/templates/page.tmpl", "app/testdata/external.txt", "app/testdata/golden.txt",
		},
		"example.com/m/plain": nil,
	}
	for _, pkg := range model.Packages {
		if !slices.Equal(pkg.EmbedFiles, want[pkg.ImportPath]) {
			t.Errorf("%s embed files = %v, want %v", pkg.ImportPath, pkg.EmbedFiles, want[pkg.ImportPath])
		}
	}
}
