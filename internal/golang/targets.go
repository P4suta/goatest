// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package golang

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

type TargetKind string

const (
	KindTest TargetKind = "test"
	KindFuzz TargetKind = "fuzz"
)

type Target struct {
	ID           string
	Name         string
	Kind         TargetKind
	Package      string
	RelativeDir  string
	Path         string
	Line         int
	Capability   string
	Dependencies []string
}

func DiscoverTargets(root string, packages []Package) ([]Target, error) {
	var targets []Target
	for _, pkg := range packages {
		directory := root
		if pkg.RelativeDir != "." {
			directory = filepath.Join(root, filepath.FromSlash(pkg.RelativeDir))
		}
		entries, err := os.ReadDir(directory)
		if err != nil {
			return nil, fmt.Errorf("goatest: read package %s: %w", pkg.ImportPath, err)
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), "_test.go") {
				continue
			}
			path := filepath.Join(directory, entry.Name())
			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				return nil, fmt.Errorf("goatest: parse %s: %w", path, err)
			}
			testingAliases, goatestAliases := aliases(file)
			for _, declaration := range file.Decls {
				function, ok := declaration.(*ast.FuncDecl)
				if !ok || function.Recv != nil || function.Body == nil {
					continue
				}
				kind, ok := targetKind(function.Name.Name)
				if !ok || !testingSignature(function, testingAliases, kind) {
					continue
				}
				relativePath := entry.Name()
				if pkg.RelativeDir != "." {
					relativePath = pkg.RelativeDir + "/" + entry.Name()
				}
				line := fset.Position(function.Pos()).Line
				targets = append(targets, Target{
					ID:   TargetID(pkg.ImportPath, function.Name.Name, kind, relativePath, line),
					Name: function.Name.Name, Kind: kind, Package: pkg.ImportPath,
					RelativeDir: pkg.RelativeDir, Path: relativePath, Line: line,
					Capability:   capability(function.Body, goatestAliases),
					Dependencies: slices.Clone(pkg.Dependencies),
				})
			}
		}
	}
	slices.SortFunc(targets, func(a, b Target) int {
		if compared := strings.Compare(a.Name, b.Name); compared != 0 {
			return compared
		}
		return strings.Compare(a.Package, b.Package)
	})
	return targets, nil
}

func aliases(file *ast.File) (map[string]bool, map[string]bool) {
	testingAliases := make(map[string]bool)
	goatestAliases := make(map[string]bool)
	for _, imported := range file.Imports {
		path, err := strconv.Unquote(imported.Path.Value)
		if err != nil {
			continue
		}
		name := filepath.Base(path)
		if imported.Name != nil {
			name = imported.Name.Name
		}
		if name == "." || name == "_" {
			continue
		}
		switch path {
		case "testing":
			testingAliases[name] = true
		case "github.com/P4suta/goatest":
			goatestAliases[name] = true
		}
	}
	return testingAliases, goatestAliases
}

func targetKind(name string) (TargetKind, bool) {
	for prefix, kind := range map[string]TargetKind{"Test": KindTest, "Fuzz": KindFuzz} {
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		rest := strings.TrimPrefix(name, prefix)
		if rest == "" {
			return kind, true
		}
		r, _ := utf8.DecodeRuneInString(rest)
		if !unicode.IsLower(r) {
			return kind, true
		}
	}
	return "", false
}

func testingSignature(function *ast.FuncDecl, testingAliases map[string]bool, kind TargetKind) bool {
	if function.Type.Params == nil || len(function.Type.Params.List) != 1 || function.Type.Results != nil && len(function.Type.Results.List) != 0 {
		return false
	}
	pointer, ok := function.Type.Params.List[0].Type.(*ast.StarExpr)
	if !ok {
		return false
	}
	selector, ok := pointer.X.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	packageName, ok := selector.X.(*ast.Ident)
	if !ok || !testingAliases[packageName.Name] {
		return false
	}
	want := "T"
	if kind == KindFuzz {
		want = "F"
	}
	return selector.Sel.Name == want
}

func capability(body *ast.BlockStmt, goatestAliases map[string]bool) string {
	var found string
	ast.Inspect(body, func(node ast.Node) bool {
		if found != "" {
			return false
		}
		call, ok := node.(*ast.CallExpr)
		if !ok || len(call.Args) < 2 || !selectorIs(call.Fun, goatestAliases, "Run", "Check") {
			return true
		}
		scope, ok := call.Args[1].(*ast.CallExpr)
		if !ok || len(scope.Args) != 1 || !selectorIs(scope.Fun, goatestAliases, "Integration") {
			return true
		}
		literal, ok := scope.Args[0].(*ast.BasicLit)
		if !ok || literal.Kind != token.STRING {
			return true
		}
		value, err := strconv.Unquote(literal.Value)
		if err == nil {
			found = value
		}
		return true
	})
	return found
}

func selectorIs(expression ast.Expr, aliases map[string]bool, names ...string) bool {
	selector, ok := expression.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	identifier, ok := selector.X.(*ast.Ident)
	return ok && aliases[identifier.Name] && slices.Contains(names, selector.Sel.Name)
}

func TargetID(pkg, name string, kind TargetKind, path string, line int) string {
	hash := sha256.New()
	for _, value := range []string{"goatest-target-v1", pkg, name, string(kind), path, strconv.Itoa(line)} {
		var length [4]byte
		binary.BigEndian.PutUint32(length[:], uint32(len(value)))
		_, _ = hash.Write(length[:])
		_, _ = hash.Write([]byte(value))
	}
	return hex.EncodeToString(hash.Sum(nil))[:16]
}
