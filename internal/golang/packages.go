// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package golang builds the Go package, test-target, and coverage model used by
// the assurance engine.
package golang

import (
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"strings"
)

type Package struct {
	ImportPath   string   `json:"import_path"`
	RelativeDir  string   `json:"relative_dir"`
	Dependencies []string `json:"dependencies"`
}

type Model struct {
	ModulePath string
	Packages   []Package
}

type listedPackage struct {
	ImportPath   string
	Dir          string
	Deps         []string
	TestImports  []string
	XTestImports []string
	Module       *struct {
		Path string
		Dir  string
	}
}

var relativePackagePath = filepath.Rel

func DecodePackages(reader io.Reader) (Model, error) {
	decoder := json.NewDecoder(reader)
	var listed []listedPackage

decode:
	for {
		var item listedPackage
		err := decoder.Decode(&item)
		switch err {
		case nil:
		case io.EOF:
			break decode
		default:
			return Model{}, fmt.Errorf("goatest: decode go list package: %w", err)
		}
		if item.Module != nil {
			listed = append(listed, item)
		}
	}
	if len(listed) == 0 {
		return Model{}, fmt.Errorf("goatest: go list returned no module packages")
	}
	modulePath := listed[0].Module.Path
	moduleDir := listed[0].Module.Dir
	listedDeps := make(map[string][]string, len(listed))
	for _, item := range listed {
		listedDeps[item.ImportPath] = item.Deps
	}
	model := Model{ModulePath: modulePath}
	for _, item := range listed {
		if item.Module.Path != modulePath || item.Module.Dir != moduleDir {
			return Model{}, fmt.Errorf("goatest: go list returned packages from multiple module roots; refusing partial package discovery")
		}
		relative, err := relativePackagePath(moduleDir, item.Dir)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return Model{}, fmt.Errorf("goatest: package %s is outside module directory", item.ImportPath)
		}
		relative = filepath.ToSlash(relative)
		model.Packages = append(model.Packages, Package{
			ImportPath: item.ImportPath, RelativeDir: relative, Dependencies: testBinaryClosure(item, listedDeps),
		})
	}
	slices.SortFunc(model.Packages, func(a, b Package) int { return strings.Compare(a.ImportPath, b.ImportPath) })
	return model, nil
}

// testBinaryClosure is the import closure the package's test binary links: the
// transitive imports of the package itself, the imports its test files add,
// and the transitive imports of every test import the same listing resolves.
//
// One level of expansion is complete, because a listed package's Deps is
// already transitive. A test import's own test imports are not followed: a
// dependency's test files are not compiled into this test binary.
//
// A test import the listing does not resolve - another module, or an in-module
// package outside a scoped go list pattern - contributes the direct import
// alone, so the closure is complete relative to the listing: a full run lists
// ./..., and a scoped run already narrows what the report claims.
//
// The package's own import path is never a dependency of itself: Deps does not
// name it, XTestImports of package p_test always names p, and a test helper may
// import p back through its own Deps.
func testBinaryClosure(item listedPackage, listedDeps map[string][]string) []string {
	closure := slices.Clone(item.Deps)
	for _, imported := range slices.Concat(item.TestImports, item.XTestImports) {
		closure = append(closure, imported)
		closure = append(closure, listedDeps[imported]...)
	}
	slices.Sort(closure)
	closure = slices.Compact(closure)
	return slices.DeleteFunc(closure, func(dependency string) bool { return dependency == item.ImportPath })
}
