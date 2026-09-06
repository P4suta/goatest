// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package golang

import (
	"encoding/json"
	"fmt"
	"io"
	"path"
	"path/filepath"
	"slices"
	"strings"
)

type Package struct {
	ImportPath   string   `json:"import_path"`
	RelativeDir  string   `json:"relative_dir"`
	Dependencies []string `json:"dependencies"`
	EmbedFiles   []string `json:"embed_files,omitempty"`
}

type Model struct {
	ModulePath string

	ModuleDir string
	Packages  []Package
}

type listedPackage struct {
	ImportPath      string
	Dir             string
	Deps            []string
	TestImports     []string
	XTestImports    []string
	EmbedFiles      []string
	TestEmbedFiles  []string
	XTestEmbedFiles []string
	Module          *struct {
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
	model := Model{ModulePath: modulePath, ModuleDir: moduleDir}
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
			ImportPath: item.ImportPath, RelativeDir: relative,
			Dependencies: testBinaryClosure(item, listedDeps), EmbedFiles: embeddedFiles(item, relative),
		})
	}
	slices.SortFunc(model.Packages, func(a, b Package) int { return strings.Compare(a.ImportPath, b.ImportPath) })
	return model, nil
}

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

func embeddedFiles(item listedPackage, relative string) []string {
	embedded := slices.Concat(item.EmbedFiles, item.TestEmbedFiles, item.XTestEmbedFiles)
	if len(embedded) == 0 {
		return nil
	}
	files := make([]string, 0, len(embedded))
	for _, file := range embedded {
		files = append(files, path.Join(filepath.ToSlash(relative), filepath.ToSlash(file)))
	}
	slices.Sort(files)
	return slices.Compact(files)
}
