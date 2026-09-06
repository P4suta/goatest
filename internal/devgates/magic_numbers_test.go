// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package devgates

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestGoSourcesContainNoMagicNumbers(t *testing.T) {
	t.Parallel()
	root := repositoryRoot(t)
	found, err := scanMagicNumbers(root)
	if err != nil {
		t.Fatalf("scan the repository for magic numbers: %v", err)
	}
	if len(found) != 0 {
		t.Fatalf("%d unnamed numeric literal(s):\n%s\n\nDeclare each value as a purpose-named constant.",
			len(found), strings.Join(found, "\n"))
	}
}

func scanMagicNumbers(root string) ([]string, error) {
	fileSet := token.NewFileSet()
	found := make(map[string]struct{})
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if entry.IsDir() {
			if relative != "." && skipMagicNumberDirectory(relative, entry.Name()) {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(entry.Name(), ".go") {
			return nil
		}
		file, err := parser.ParseFile(fileSet, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		constantRanges := constantDeclarationRanges(file)
		testFile := strings.HasSuffix(relative, "_test.go")
		record := func(literal *ast.BasicLit, context string) {
			if literal == nil || !numericToken(literal.Kind) || conventionalNumber(literal.Value) ||
				insideAnyRange(literal.Pos(), constantRanges) {
				return
			}
			position := fileSet.Position(literal.Pos())
			found[fmt.Sprintf("%s:%d:%d: %s (%s)", relative, position.Line, position.Column, literal.Value, context)] = struct{}{}
		}
		recordExpression := func(expression ast.Expr, context string) {
			switch value := expression.(type) {
			case *ast.BasicLit:
				record(value, context)
			case *ast.UnaryExpr:
				if literal, ok := value.X.(*ast.BasicLit); ok {
					record(literal, context)
				}
			case *ast.BinaryExpr:
				if literal, ok := value.X.(*ast.BasicLit); ok {
					record(literal, context)
				}
				if literal, ok := value.Y.(*ast.BasicLit); ok {
					record(literal, context)
				}
			}
		}
		ast.Inspect(file, func(node ast.Node) bool {
			switch value := node.(type) {
			case *ast.CallExpr:
				if ignoredNumericFunction(value.Fun) || testFile && !testControlFunction(value.Fun) {
					return true
				}
				for _, argument := range value.Args {
					recordExpression(argument, "argument to "+numericFunctionName(value.Fun))
				}
			case *ast.KeyValueExpr:
				if !testFile {
					recordExpression(value.Value, "assignment")
				}
			case *ast.AssignStmt:
				for _, expression := range value.Rhs {
					recordExpression(expression, "operation")
				}
			case *ast.CaseClause:
				for _, expression := range value.List {
					recordExpression(expression, "case")
				}
			case *ast.IfStmt:
				recordExpression(value.Cond, "condition")
			case *ast.ReturnStmt:
				for _, expression := range value.Results {
					recordExpression(expression, "return")
				}
			}
			return true
		})
		return nil
	})
	result := make([]string, 0, len(found))
	for finding := range found {
		result = append(result, finding)
	}
	slices.Sort(result)
	return result, err
}

func skipMagicNumberDirectory(relative, name string) bool {
	if relative == "dist" || relative == "reports" || name == "vendor" {
		return true
	}
	return strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_")
}

type sourceRange struct {
	start token.Pos
	end   token.Pos
}

func constantDeclarationRanges(file *ast.File) []sourceRange {
	var ranges []sourceRange
	for _, declaration := range file.Decls {
		general, ok := declaration.(*ast.GenDecl)
		if ok && general.Tok == token.CONST {
			ranges = append(ranges, sourceRange{start: general.Pos(), end: general.End()})
		}
	}
	return ranges
}

func insideAnyRange(position token.Pos, ranges []sourceRange) bool {
	return slices.ContainsFunc(ranges, func(candidate sourceRange) bool {
		return candidate.start <= position && position < candidate.end
	})
}

func numericToken(kind token.Token) bool {
	return kind == token.INT || kind == token.FLOAT
}

func conventionalNumber(literal string) bool {
	withoutSeparators := strings.ReplaceAll(literal, "_", "")
	return withoutSeparators == "0" || withoutSeparators == "0.0" ||
		withoutSeparators == "1" || withoutSeparators == "1.0"
}

func ignoredNumericFunction(function ast.Expr) bool {
	selector, ok := function.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	prefix, ok := selector.X.(*ast.Ident)
	if !ok {
		return false
	}
	if prefix.Name == "time" && selector.Sel.Name == "Date" {
		return true
	}
	return prefix.Name == "strconv" && strings.HasPrefix(selector.Sel.Name, "Parse") ||
		prefix.Name == "strconv" && strings.HasPrefix(selector.Sel.Name, "Format")
}

func numericFunctionName(function ast.Expr) string {
	switch value := function.(type) {
	case *ast.Ident:
		return value.Name
	case *ast.SelectorExpr:
		if prefix, ok := value.X.(*ast.Ident); ok {
			return prefix.Name + "." + value.Sel.Name
		}
		return value.Sel.Name
	default:
		return "call"
	}
}

func testControlFunction(function ast.Expr) bool {
	name := numericFunctionName(function)
	switch name {
	case "make", "time.After", "time.AfterFunc", "time.NewTicker", "time.Sleep",
		"context.WithDeadline", "context.WithTimeout", "strings.Repeat", "bytes.Repeat",
		"os.Exit", "Buffer", "Advance", "age":
		return true
	default:
		return false
	}
}
