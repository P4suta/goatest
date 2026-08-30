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
	KindTest    TargetKind = "test"
	KindFuzz    TargetKind = "fuzz"
	KindExample TargetKind = "example"
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
	Capabilities []string
	Dependencies []string
}

func DiscoverTargets(root string, packages []Package) ([]Target, error) {
	var targets []Target
	for _, pkg := range packages {
		directory := filepath.Join(root, filepath.FromSlash(pkg.RelativeDir))
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
			file, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
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
				if !ok || !testingSignature(function, testingAliases, kind) ||
					kind == KindExample && !hasExampleOutput(function, file.Comments) {
					continue
				}
				relativePath := filepath.ToSlash(filepath.Join(filepath.FromSlash(pkg.RelativeDir), entry.Name()))
				line := fset.Position(function.Pos()).Line
				capabilities := targetCapabilities(function, goatestAliases)
				firstCapability := ""
				if len(capabilities) != 0 {
					firstCapability = capabilities[0]
				}
				targets = append(targets, Target{
					ID:   TargetID(pkg.ImportPath, function.Name.Name, kind, relativePath, line),
					Name: function.Name.Name, Kind: kind, Package: pkg.ImportPath,
					RelativeDir: pkg.RelativeDir, Path: relativePath, Line: line,
					Capability: firstCapability, Capabilities: capabilities,
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
		path, _ := strconv.Unquote(imported.Path.Value)
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
	for _, candidate := range []struct {
		prefix string
		kind   TargetKind
	}{{"Test", KindTest}, {"Fuzz", KindFuzz}, {"Example", KindExample}} {
		prefix, kind := candidate.prefix, candidate.kind
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
	if kind == KindExample {
		return (function.Type.Params == nil || len(function.Type.Params.List) == 0) &&
			(function.Type.Results == nil || len(function.Type.Results.List) == 0)
	}
	if function.Type.Params == nil || len(function.Type.Params.List) != 1 || function.Type.Results != nil && len(function.Type.Results.List) != 0 {
		return false
	}
	parameter := function.Type.Params.List[0]
	if len(parameter.Names) > 1 {
		return false
	}
	pointer, ok := parameter.Type.(*ast.StarExpr)
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

func hasExampleOutput(function *ast.FuncDecl, comments []*ast.CommentGroup) bool {
	for _, group := range comments {
		if group.Pos() < function.Body.Lbrace || group.End() > function.Body.Rbrace {
			continue
		}
		for _, comment := range group.List {
			text := strings.TrimSpace(strings.TrimPrefix(comment.Text, "//"))
			lower := strings.ToLower(text)
			if strings.HasPrefix(lower, "output:") || strings.HasPrefix(lower, "unordered output:") {
				return true
			}
		}
	}
	return false
}

func targetCapabilities(function *ast.FuncDecl, goatestAliases map[string]bool) []string {
	visitor := &capabilityVisitor{aliases: goatestAliases}
	ast.Walk(visitor, function.Body)
	values := slices.Clone(visitor.values)
	if function.Doc != nil {
		for _, comment := range function.Doc.List {
			text := strings.TrimSpace(strings.TrimPrefix(comment.Text, "//"))
			if resources, ok := strings.CutPrefix(text, "goatest:resources"); ok {
				values = append(values, strings.Fields(resources)...)
			}
		}
	}
	result := make([]string, 0, len(values))
	seen := make(map[string]bool, len(values))
	for _, value := range values {
		if value != "" && !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	return result
}

// capability is kept as the single-resource parser seam used by focused
// tests; production target discovery records the complete capability set.
func capability(body *ast.BlockStmt, goatestAliases map[string]bool) string {
	result := ""
	ast.Inspect(body, func(node ast.Node) bool {
		if result != "" {
			return false
		}
		call, ok := node.(*ast.CallExpr)
		if !ok || len(call.Args) != 3 || !selectorIs(call.Fun, goatestAliases, "Run") {
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
		result, _ = strconv.Unquote(literal.Value)
		return true
	})
	return result
}

type capabilityVisitor struct {
	aliases map[string]bool
	values  []string
}

func (visitor *capabilityVisitor) Visit(node ast.Node) ast.Visitor {
	if values, ok := integrationCapabilities(node, visitor.aliases); ok {
		visitor.values = append(visitor.values, values...)
	}
	return visitor
}

func integrationCapabilities(node ast.Node, goatestAliases map[string]bool) ([]string, bool) {
	call, ok := node.(*ast.CallExpr)
	if !ok || len(call.Args) != 3 || !selectorIs(call.Fun, goatestAliases, "Run") {
		return nil, false
	}
	scope, ok := call.Args[1].(*ast.CallExpr)
	if !ok || len(scope.Args) == 0 || !selectorIs(scope.Fun, goatestAliases, "Integration") {
		return nil, false
	}
	values := make([]string, 0, len(scope.Args))
	for _, argument := range scope.Args {
		literal, ok := argument.(*ast.BasicLit)
		if !ok || literal.Kind != token.STRING {
			return nil, false
		}
		value, err := strconv.Unquote(literal.Value)
		if err != nil || strings.TrimSpace(value) == "" {
			return nil, false
		}
		values = append(values, value)
	}
	return values, true
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
