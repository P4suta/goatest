// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package devgates

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

const (
	modulePath = "github.com/P4suta/goatest"

	testkitPath = modulePath + "/internal/testkit"

	allowlistPath = "internal/devgates/seam_allowlist.txt"
)

type seam struct {
	pkg string

	name string
}

func (declaration seam) String() string {
	return declaration.pkg + " " + declaration.name
}

func compareSeams(first, second seam) int {
	if order := strings.Compare(first.pkg, second.pkg); order != 0 {
		return order
	}
	return strings.Compare(first.name, second.name)
}

func TestPackageLevelSeamsMatchAllowlist(t *testing.T) {
	t.Parallel()
	root := repositoryRoot(t)
	found, err := scanSeams(root)
	if err != nil {
		t.Fatalf("scan the repository for seams: %v", err)
	}
	allowed, err := readAllowlist(filepath.Join(root, filepath.FromSlash(allowlistPath)))
	if err != nil {
		t.Fatalf("read %s: %v", allowlistPath, err)
	}
	if unexpected := missingFrom(found, allowed); len(unexpected) > 0 {
		t.Errorf("%d package-level seam(s) are not recorded in %s:\n%s\n\n"+
			"A package-level variable a test overwrites forces every test in the "+
			"package to run alone. Split the work into an internal xxxWithHooks("+
			"args, hooks) instead: production calls it with an immutable default, "+
			"and a test passes its hooks as an argument. Adding a line to the "+
			"ledger is a reviewed exception, not the fix.",
			len(unexpected), allowlistPath, formatSeams(unexpected))
	}
	if removed := missingFrom(allowed, found); len(removed) > 0 {
		t.Errorf("%d recorded seam(s) no longer exist:\n%s\n\n"+
			"The ledger is a ratchet: it may shrink and never grow. Delete those "+
			"lines from %s in the commit that removed the seams, so the shrinking "+
			"is reviewed with the change that earned it.",
			len(removed), formatSeams(removed), allowlistPath)
	}
}

func TestSeamAllowlistIsSortedAndFreeOfDuplicates(t *testing.T) {
	t.Parallel()
	root := repositoryRoot(t)
	allowed, err := readAllowlist(filepath.Join(root, filepath.FromSlash(allowlistPath)))
	if err != nil {
		t.Fatalf("read %s: %v", allowlistPath, err)
	}
	if !slices.IsSortedFunc(allowed, compareSeams) {
		t.Errorf("%s is not sorted; sort the entries by package, then by name", allowlistPath)
	}
	for index := 1; index < len(allowed); index++ {
		if allowed[index] == allowed[index-1] {
			t.Errorf("%s records %q twice", allowlistPath, allowed[index])
		}
	}
}

func TestProductionCodeDoesNotImportTestkit(t *testing.T) {
	t.Parallel()
	root := repositoryRoot(t)
	files, err := parseProduction(root)
	if err != nil {
		t.Fatalf("parse the production sources: %v", err)
	}
	var offenders []string
	for _, parsed := range files {
		for _, imported := range parsed.file.Imports {
			path, err := strconv.Unquote(imported.Path.Value)
			if err != nil {
				t.Fatalf("read the import path in %s: %v", parsed.path, err)
			}
			if path == testkitPath || strings.HasPrefix(path, testkitPath+"/") {
				offenders = append(offenders, parsed.path)
			}
		}
	}
	if len(offenders) > 0 {
		t.Errorf("%d production file(s) import %s:\n%s\n\n"+
			"internal/testkit is test-only support. Move the helper the file "+
			"needs into a production package, or move the caller into a test.",
			len(offenders), testkitPath, strings.Join(offenders, "\n"))
	}
}

func formatSeams(seams []seam) string {
	return strings.Join(formatSeamLines(seams), "\n")
}

func formatSeamLines(seams []seam) []string {
	lines := make([]string, 0, len(seams))
	for _, declaration := range seams {
		lines = append(lines, declaration.String())
	}
	return lines
}

func missingFrom(first, second []seam) []seam {
	present := make(map[seam]struct{}, len(second))
	for _, declaration := range second {
		present[declaration] = struct{}{}
	}
	var difference []seam
	for _, declaration := range first {
		if _, ok := present[declaration]; !ok {
			difference = append(difference, declaration)
		}
	}
	return difference
}

func readAllowlist(path string) ([]seam, error) {
	const seamFieldCount = 2
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var allowed []seam
	for number, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		fields := strings.Fields(trimmed)
		if len(fields) != seamFieldCount {
			return nil, fmt.Errorf("%s:%d: want \"package-path name\", got %q", path, number+1, trimmed)
		}
		allowed = append(allowed, seam{pkg: fields[0], name: fields[1]})
	}
	return allowed, nil
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	directory, err := os.Getwd()
	if err != nil {
		t.Fatalf("read the working directory: %v", err)
	}
	for {
		data, err := os.ReadFile(filepath.Join(directory, "go.mod"))
		if err == nil && strings.Contains(string(data), "module "+modulePath) {
			return directory
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			t.Fatalf("no go.mod declaring module %s above the working directory", modulePath)
		}
		directory = parent
	}
}

type parsedFile struct {
	pkg string

	path string

	file *ast.File
}

func skipDirectory(relative, name string) bool {
	switch relative {
	case "dist", "reports":
		return true
	}
	if name == "testdata" || name == "vendor" {
		return true
	}
	return strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_")
}

func parseProduction(root string) ([]parsedFile, error) {
	fileSet := token.NewFileSet()
	var parsed []parsedFile
	walk := func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if entry.IsDir() {
			if relative == "." {
				return nil
			}
			if skipDirectory(relative, entry.Name()) {
				return fs.SkipDir
			}
			return nil
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(fileSet, path, nil, parser.ParseComments|parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		parsed = append(parsed, parsedFile{
			pkg:  filepath.ToSlash(filepath.Dir(relative)),
			path: relative,
			file: file,
		})
		return nil
	}
	if err := filepath.WalkDir(root, walk); err != nil {
		return nil, err
	}
	return parsed, nil
}

func scanSeams(root string) ([]seam, error) {
	files, err := parseProduction(root)
	if err != nil {
		return nil, err
	}
	packages := collectPackageFacts(files)
	seen := make(map[seam]struct{})
	for _, parsed := range files {
		facts := packages[parsed.pkg]
		for _, declaration := range parsed.file.Decls {
			general, ok := declaration.(*ast.GenDecl)
			if !ok || general.Tok != token.VAR {
				continue
			}
			for _, name := range declaredSeams(facts, general) {
				seen[seam{pkg: parsed.pkg, name: name}] = struct{}{}
			}
		}
	}
	found := make([]seam, 0, len(seen))
	for declaration := range seen {
		found = append(found, declaration)
	}
	slices.SortFunc(found, compareSeams)
	return found, nil
}

type packageFacts struct {
	functions map[string]struct{}

	types map[string]ast.Expr

	declared map[string]struct{}
}

func collectPackageFacts(files []parsedFile) map[string]packageFacts {
	packages := make(map[string]packageFacts)
	for _, parsed := range files {
		facts, ok := packages[parsed.pkg]
		if !ok {
			facts = packageFacts{
				functions: make(map[string]struct{}),
				types:     make(map[string]ast.Expr),
				declared:  make(map[string]struct{}),
			}
			packages[parsed.pkg] = facts
		}
		for _, declaration := range parsed.file.Decls {
			switch typed := declaration.(type) {
			case *ast.FuncDecl:
				if typed.Recv != nil {
					continue
				}
				facts.functions[typed.Name.Name] = struct{}{}
				facts.declared[typed.Name.Name] = struct{}{}
			case *ast.GenDecl:
				for _, spec := range typed.Specs {
					switch specified := spec.(type) {
					case *ast.TypeSpec:
						facts.types[specified.Name.Name] = specified.Type
						facts.declared[specified.Name.Name] = struct{}{}
					case *ast.ValueSpec:
						for _, name := range specified.Names {
							facts.declared[name.Name] = struct{}{}
						}
					}
				}
			}
		}
	}
	return packages
}

func (facts packageFacts) isFunctionType(expression ast.Expr) bool {
	return facts.functionType(expression, make(map[string]struct{}))
}

func (facts packageFacts) functionType(expression ast.Expr, seen map[string]struct{}) bool {
	switch typed := expression.(type) {
	case *ast.FuncType:
		return true
	case *ast.ParenExpr:
		return facts.functionType(typed.X, seen)
	case *ast.Ident:
		if _, ok := seen[typed.Name]; ok {
			return false
		}
		seen[typed.Name] = struct{}{}
		underlying, ok := facts.types[typed.Name]
		if !ok {
			return false
		}
		return facts.functionType(underlying, seen)
	}
	return false
}

func (facts packageFacts) isHookStruct(name string) bool {
	underlying, ok := facts.types[name]
	if !ok {
		return false
	}
	structure, ok := underlying.(*ast.StructType)
	if !ok || structure.Fields == nil || len(structure.Fields.List) == 0 {
		return false
	}
	for _, field := range structure.Fields.List {
		if len(field.Names) == 0 || !facts.isFunctionType(field.Type) {
			return false
		}
	}
	return true
}

func (facts packageFacts) isSeamValue(expression ast.Expr) bool {
	switch typed := expression.(type) {
	case *ast.FuncLit:
		return true
	case *ast.ParenExpr:
		return facts.isSeamValue(typed.X)
	case *ast.UnaryExpr:
		return typed.Op == token.AND && facts.isSeamValue(typed.X)
	case *ast.Ident:
		_, ok := facts.functions[typed.Name]
		return ok
	case *ast.SelectorExpr:
		qualifier, ok := typed.X.(*ast.Ident)
		if !ok {
			return false
		}
		_, local := facts.declared[qualifier.Name]
		return !local
	case *ast.CompositeLit:
		name, ok := typed.Type.(*ast.Ident)
		return ok && facts.isHookStruct(name.Name)
	}
	return false
}

func declaredSeams(facts packageFacts, declaration *ast.GenDecl) []string {
	var names []string
	for _, spec := range declaration.Specs {
		value, ok := spec.(*ast.ValueSpec)
		if !ok || hasEmbedDirective(declaration.Doc) || hasEmbedDirective(value.Doc) {
			continue
		}
		for index, name := range value.Names {
			if name.Name == "_" {
				continue
			}
			switch {
			case value.Type != nil:
				if facts.isFunctionType(value.Type) {
					names = append(names, name.Name)
				}
			case index < len(value.Values):
				if facts.isSeamValue(value.Values[index]) {
					names = append(names, name.Name)
				}
			}
		}
	}
	return names
}

func hasEmbedDirective(doc *ast.CommentGroup) bool {
	if doc == nil {
		return false
	}
	for _, comment := range doc.List {
		if strings.HasPrefix(comment.Text, "//go:embed") {
			return true
		}
	}
	return false
}
