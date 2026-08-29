// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package golang

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// ConcurrencyPackages conservatively identifies module packages that use Go
// concurrency syntax or the standard synchronization libraries.
func ConcurrencyPackages(root string, packages []Package) ([]string, error) {
	var result []string
	for _, pkg := range packages {
		directory := filepath.Join(root, filepath.FromSlash(pkg.RelativeDir))
		entries, err := os.ReadDir(directory)
		if err != nil {
			return nil, fmt.Errorf("goatest: inspect concurrency in %s: %w", pkg.ImportPath, err)
		}
		concurrent := false
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") {
				continue
			}
			path := filepath.Join(directory, entry.Name())
			file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
			if err != nil {
				return nil, fmt.Errorf("goatest: parse %s for concurrency: %w", path, err)
			}
			if importsSynchronization(file) || hasConcurrencyNode(file) {
				concurrent = true
			}
		}
		if concurrent {
			result = append(result, pkg.ImportPath)
		}
	}
	slices.Sort(result)
	return slices.Compact(result), nil
}

func importsSynchronization(file *ast.File) bool {
	for _, imported := range file.Imports {
		path, err := strconv.Unquote(imported.Path.Value)
		if err == nil && (path == "sync" || path == "sync/atomic") {
			return true
		}
	}
	return false
}

func hasConcurrencyNode(file *ast.File) bool {
	found := false
	ast.Inspect(file, func(node ast.Node) bool {
		switch value := node.(type) {
		case *ast.GoStmt, *ast.SendStmt, *ast.ChanType, *ast.SelectStmt:
			found = true
		case *ast.UnaryExpr:
			if value.Op == token.ARROW {
				found = true
			}
		}
		return true
	})
	return found
}
