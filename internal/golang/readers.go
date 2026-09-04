// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package golang

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
)

// repositoryReaderCalls are the standard-library calls through which a package
// reads a directory it computes rather than a file it names, by the import
// path the call belongs to and the names of the functions in it.
//
// The list is a deliberate over-approximation of "this package's verdict can
// depend on a file no closure of its own describes": a call to any of them may
// read the whole tree, and none of them says which part it read. It may only
// grow. Removing an entry would silently widen what a recorded verdict is
// reused across, and adding one only costs a package the reuse of verdicts it
// might not have deserved.
var repositoryReaderCalls = map[string][]string{
	"os":            {"ReadDir", "DirFS", "OpenRoot"},
	"path/filepath": {"Walk", "WalkDir", "Glob"},
	"io/fs":         {"WalkDir", "ReadDir", "Glob", "Sub"},
}

// RepositoryReaders names the packages whose sources read the repository as
// data, so that a caller can key such a package's targets on the whole tree
// instead of on the closure their test binaries link.
//
// A package answers for both halves of its own directory: its test files,
// which are where a golden reader or a repository-wide gate usually lives, and
// its own files, because a test calls into them. Detection is by syntax — a
// call whose selector names one of the functions above on an identifier bound
// to that function's import path — so a method of the package's own that
// happens to share a name is not one of them, and an aliased import of the
// same package is.
//
// Every failure is answered on the conservative side. A directory that cannot
// be listed and a file that cannot be parsed leave the question open, and an
// open question about what a test reads is answered with the whole tree rather
// than with nothing; that is also why nothing here returns an error. Shadowing
// is not resolved for the same reason: an identifier that names the import
// somewhere in the file is treated as naming it everywhere, which can only
// mark a package that does not deserve it.
func RepositoryReaders(root string, packages []Package) map[string]bool {
	readers := make(map[string]bool, len(packages))
	for _, pkg := range packages {
		if packageReadsRepository(filepath.Join(root, filepath.FromSlash(pkg.RelativeDir))) {
			readers[pkg.ImportPath] = true
		}
	}
	return readers
}

// packageReadsRepository reports whether any Go file directly in one package
// directory reads a path it computes. A directory below it is another
// package's, and answers for itself.
func packageReadsRepository(directory string) bool {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return true
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") {
			continue
		}
		if fileReadsRepository(filepath.Join(directory, entry.Name())) {
			return true
		}
	}
	return false
}

// fileReadsRepository reports whether one Go file calls one of the listed
// functions on the package it belongs to.
func fileReadsRepository(path string) bool {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		return true
	}
	selectors, unqualified := repositoryReaderSelectors(file)
	if unqualified {
		return true
	}
	if len(selectors) == 0 {
		return false
	}
	found := false
	ast.Inspect(file, func(node ast.Node) bool {
		if found {
			return false
		}
		call, isCall := node.(*ast.CallExpr)
		if !isCall {
			return true
		}
		selector, isSelector := call.Fun.(*ast.SelectorExpr)
		if !isSelector {
			return true
		}
		qualifier, isIdent := selector.X.(*ast.Ident)
		if !isIdent {
			return true
		}
		for _, name := range selectors[qualifier.Name] {
			if name == selector.Sel.Name {
				found = true
				return false
			}
		}
		return true
	})
	return found
}

// repositoryReaderSelectors maps the name one file refers to each listed
// package by — its alias when it declares one, the last element of the import
// path otherwise — to the functions of that package the rule names. A file
// that imports none of them has nothing to inspect.
//
// A dot import binds the functions to no qualifier at all, so no selector
// could name them; it is reported instead, and the file is treated as reading
// the repository without looking any further. A blank import binds nothing
// that can be called and is passed over.
func repositoryReaderSelectors(file *ast.File) (map[string][]string, bool) {
	var selectors map[string][]string
	for _, imported := range file.Imports {
		path := strings.Trim(imported.Path.Value, `"`)
		calls, listed := repositoryReaderCalls[path]
		if !listed {
			continue
		}
		name := path[strings.LastIndex(path, "/")+1:]
		if imported.Name != nil {
			if imported.Name.Name == "." {
				return nil, true
			}
			if imported.Name.Name == "_" {
				continue
			}
			name = imported.Name.Name
		}
		if selectors == nil {
			selectors = make(map[string][]string, len(repositoryReaderCalls))
		}
		selectors[name] = append(selectors[name], calls...)
	}
	return selectors, false
}
