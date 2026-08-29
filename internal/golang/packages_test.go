// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package golang_test

import (
	"bytes"
	"encoding/json"
	"path/filepath"
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

func TestDecodePackagesFiltersOtherModulesAndCanonicalizesPackages(t *testing.T) {
	t.Parallel()
	moduleDir := filepath.Join(t.TempDir(), "module")
	stream := packageStream(t,
		listedPackage("example.com/sample/z", filepath.Join(moduleDir, "z"), "example.com/sample", moduleDir, "zdep", "adep", "zdep"),
		listedPackage("standard/library", moduleDir, "", ""),
		listedPackage("other.example/path", moduleDir, "other.example", moduleDir),
		listedPackage("example.com/sample/other-checkout", moduleDir, "example.com/sample", filepath.Join(moduleDir, "other")),
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
