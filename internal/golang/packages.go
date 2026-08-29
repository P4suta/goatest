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
	ImportPath string
	Dir        string
	Deps       []string
	Module     *struct {
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
	model := Model{ModulePath: modulePath}
	for _, item := range listed {
		if item.Module.Path != modulePath || item.Module.Dir != moduleDir {
			continue
		}
		relative, err := relativePackagePath(moduleDir, item.Dir)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return Model{}, fmt.Errorf("goatest: package %s is outside module directory", item.ImportPath)
		}
		relative = filepath.ToSlash(relative)
		dependencies := slices.Clone(item.Deps)
		slices.Sort(dependencies)
		dependencies = slices.Compact(dependencies)
		model.Packages = append(model.Packages, Package{
			ImportPath: item.ImportPath, RelativeDir: relative, Dependencies: dependencies,
		})
	}
	slices.SortFunc(model.Packages, func(a, b Package) int { return strings.Compare(a.ImportPath, b.ImportPath) })
	return model, nil
}
