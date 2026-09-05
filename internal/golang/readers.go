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
// path the selector belongs to and the names of the functions in it.
//
// The list is a deliberate over-approximation of "this package's verdict can
// depend on a file no closure of its own describes": using any of them may read
// the whole tree, and none of them says which part it read. It may only
// grow. Removing an entry would silently widen what a recorded verdict is
// reused across, and adding one only costs a package the reuse of verdicts it
// might not have deserved.
var repositoryReaderCalls = map[string][]string{
	"os":            {"ReadDir", "DirFS", "OpenRoot"},
	"path/filepath": {"Walk", "WalkDir", "Glob"},
	"io/fs":         {"WalkDir", "ReadDir", "Glob", "Sub"},
}

// RepositoryReadCandidate is the static boundary within which runtime
// observation may safely narrow the old whole-tree approximation.
//
// Unobservable is true when the syntax itself uses a reader through a route
// Go's test action log cannot account for completely. Such a package remains
// keyed on the whole tree without trying to observe it.
type RepositoryReadCandidate struct {
	Unobservable bool
}

// RepositoryReaders names the packages whose sources read the repository as
// data, so that a caller can key such a package's targets on the whole tree
// instead of on the closure their test binaries link.
//
// A package answers for both halves of its own directory: its test files,
// which are where a golden reader or a repository-wide gate usually lives, and
// its own files, because a test calls into them. Detection is by syntax — a
// selector naming one of the functions above on an identifier imported from
// that function's path — so a method of the package's own that
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
	for path := range RepositoryReadCandidates(root, packages) {
		readers[path] = true
	}
	return readers
}

// RepositoryReadCandidates describes the packages covered by the existing
// reader rule and identifies the cases runtime action logging cannot narrow.
// It deliberately has the same candidate boundary as RepositoryReaders: the
// richer answer changes only whether a candidate may be observed, never which
// package is protected by the rule.
func RepositoryReadCandidates(root string, packages []Package) map[string]RepositoryReadCandidate {
	candidates := make(map[string]RepositoryReadCandidate, len(packages))
	for _, pkg := range packages {
		candidate, unobservable := packageRepositoryReadCandidate(filepath.Join(root, filepath.FromSlash(pkg.RelativeDir)))
		if candidate {
			candidates[pkg.ImportPath] = RepositoryReadCandidate{Unobservable: unobservable}
		}
	}
	return candidates
}

func packageRepositoryReadCandidate(directory string) (bool, bool) {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return true, true
	}
	var candidate, unobservable bool
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") {
			continue
		}
		reads, cannotObserve := fileRepositoryReadCandidate(filepath.Join(directory, entry.Name()))
		candidate = candidate || reads
		unobservable = unobservable || cannotObserve
		if candidate && unobservable {
			return true, true
		}
	}
	return candidate, unobservable
}

func fileRepositoryReadCandidate(path string) (bool, bool) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		return true, true
	}
	selectors, unqualified := repositoryReaderSelectors(file)
	if unqualified {
		return true, true
	}
	if len(selectors) == 0 {
		return false, false
	}
	found, unobservable := false, false
	var stack []ast.Node
	ast.Inspect(file, func(node ast.Node) bool {
		if node == nil {
			stack = stack[:len(stack)-1]
			return true
		}
		stack = append(stack, node)
		selector, isSelector := node.(*ast.SelectorExpr)
		if !isSelector {
			return true
		}
		qualifier, isIdent := selector.X.(*ast.Ident)
		if !isIdent {
			return true
		}
		for _, named := range selectors[qualifier.Name] {
			if named.name == selector.Sel.Name {
				found = true
				// Generic io/fs calls can be backed by an implementation that
				// never reaches package os, and package/TestMain initialisation
				// runs before testing installs its action logger.
				unobservable = unobservable || named.importPath == "io/fs" || referenceRunsBeforeTestLog(stack)
				break
			}
		}
		return true
	})
	return found, unobservable
}

type repositoryReaderCall struct {
	importPath string
	name       string
}

// referenceRunsBeforeTestLog identifies reader references in execution regions
// that precede testing.M.Run's action logger. A selector used as a function
// value is included as well as a direct call, so indirection cannot leave the
// static candidate boundary merely by dropping the call's parentheses.
func referenceRunsBeforeTestLog(stack []ast.Node) bool {
	for index := len(stack) - 1; index >= 0; index-- {
		declaration, ok := stack[index].(*ast.FuncDecl)
		if !ok {
			continue
		}
		return declaration.Name.Name == "init" || declaration.Name.Name == "TestMain"
	}
	// A selector merely stored as a package-level function value performs no
	// I/O yet; its later call still reaches package os and is observable. A
	// selector below a call expression may execute as part of package
	// initialization, whether as the callee or as a callback argument.
	for _, node := range stack {
		if _, called := node.(*ast.CallExpr); called {
			return true
		}
	}
	return false
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
func repositoryReaderSelectors(file *ast.File) (map[string][]repositoryReaderCall, bool) {
	var selectors map[string][]repositoryReaderCall
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
			selectors = make(map[string][]repositoryReaderCall, len(repositoryReaderCalls))
		}
		for _, call := range calls {
			selectors[name] = append(selectors[name], repositoryReaderCall{importPath: path, name: call})
		}
	}
	return selectors, false
}
