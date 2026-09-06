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
	"strings"
)

type repositoryReadObservability bool

const (
	testLogObservable   repositoryReadObservability = false
	testLogUnobservable repositoryReadObservability = true
)

var repositoryReaderCalls = map[string]map[string]repositoryReadObservability{
	"os": {
		"Open": testLogObservable, "OpenFile": testLogObservable, "ReadFile": testLogObservable, "ReadDir": testLogObservable,
		"Stat": testLogObservable, "Lstat": testLogObservable, "Chdir": testLogObservable, "Create": testLogObservable,
		"WriteFile": testLogObservable, "DirFS": testLogObservable, "OpenRoot": testLogObservable, "OpenInRoot": testLogObservable,
		"Readlink": testLogUnobservable,
	},
	"path/filepath": {
		"Walk": testLogObservable, "WalkDir": testLogObservable, "Glob": testLogObservable, "EvalSymlinks": testLogObservable,
	},
	"io/fs": {
		"WalkDir": testLogUnobservable, "ReadDir": testLogUnobservable, "Glob": testLogUnobservable, "Sub": testLogUnobservable,
		"ReadFile": testLogUnobservable, "Stat": testLogUnobservable, "Lstat": testLogUnobservable, "ReadLink": testLogUnobservable,
	},
	"io/ioutil": {
		"ReadFile": testLogObservable, "ReadDir": testLogObservable,
	},
	"syscall": {
		"Open": testLogUnobservable, "Openat": testLogUnobservable, "Creat": testLogUnobservable,
		"Stat": testLogUnobservable, "Lstat": testLogUnobservable, "Fstatat": testLogUnobservable,
		"Chdir": testLogUnobservable, "Readlink": testLogUnobservable, "Getwd": testLogUnobservable,
		"Getdents": testLogUnobservable, "ReadDirent": testLogUnobservable, "Access": testLogUnobservable,
		"Mkdir": testLogUnobservable, "Rename": testLogUnobservable, "Unlink": testLogUnobservable,
	},
	"golang.org/x/sys/unix": {
		"Open": testLogUnobservable, "Openat": testLogUnobservable, "Openat2": testLogUnobservable,
		"Stat": testLogUnobservable, "Lstat": testLogUnobservable, "Fstatat": testLogUnobservable,
		"Statx": testLogUnobservable, "Chdir": testLogUnobservable, "Readlink": testLogUnobservable,
		"Readlinkat": testLogUnobservable, "Getwd": testLogUnobservable, "Getdents": testLogUnobservable,
		"Access": testLogUnobservable, "Faccessat": testLogUnobservable,
	},
	"golang.org/x/sys/windows": {
		"Open": testLogUnobservable, "CreateFile": testLogUnobservable, "GetFileAttributes": testLogUnobservable,
		"GetFileAttributesEx": testLogUnobservable, "FindFirstFile": testLogUnobservable,
		"FindNextFile": testLogUnobservable, "SetCurrentDirectory": testLogUnobservable,
		"GetCurrentDirectory": testLogUnobservable, "GetFinalPathNameByHandle": testLogUnobservable,
	},
	"os/exec": {
		"Command": testLogUnobservable, "CommandContext": testLogUnobservable, "LookPath": testLogUnobservable,
	},
	"plugin": {
		"Open": testLogUnobservable,
	},
}

const cgoImportPath = "C"

type RepositoryReadCandidate struct {
	Unobservable bool
}

func RepositoryReadCandidates(root string, packages []Package) map[string]RepositoryReadCandidate {
	scans := make(map[string]repositoryReadScan, len(packages))
	for _, pkg := range packages {
		scans[pkg.ImportPath] = packageRepositoryReadScan(filepath.Join(root, filepath.FromSlash(pkg.RelativeDir)))
	}
	productionCandidates := make(map[string]bool, len(packages))
	for _, pkg := range packages {
		productionCandidates[pkg.ImportPath] = scans[pkg.ImportPath].productionCandidate
		for _, dependency := range pkg.Dependencies {
			productionCandidates[pkg.ImportPath] = productionCandidates[pkg.ImportPath] || scans[dependency].productionCandidate
		}
	}
	productionUnobservable := make(map[string]bool, len(packages))
	for _, pkg := range packages {
		scan := scans[pkg.ImportPath]
		productionUnobservable[pkg.ImportPath] = scan.productionUnobservable
		for dependency := range scan.productionPreRunDependencies {
			productionUnobservable[pkg.ImportPath] = productionUnobservable[pkg.ImportPath] || productionCandidates[dependency]
		}
	}
	candidates := make(map[string]RepositoryReadCandidate, len(packages))
	for _, pkg := range packages {
		scan := scans[pkg.ImportPath]
		candidate, unobservable := scan.candidate, scan.unobservable
		for _, dependency := range pkg.Dependencies {
			candidate = candidate || productionCandidates[dependency]
			unobservable = unobservable || productionUnobservable[dependency]
		}
		for dependency := range scan.preRunDependencies {
			if productionCandidates[dependency] {
				candidate = true
				unobservable = true
			}
		}
		if candidate {
			candidates[pkg.ImportPath] = RepositoryReadCandidate{Unobservable: unobservable}
		}
	}
	return candidates
}

type repositoryReadScan struct {
	candidate                    bool
	unobservable                 bool
	productionCandidate          bool
	productionUnobservable       bool
	preRunDependencies           map[string]struct{}
	productionPreRunDependencies map[string]struct{}
}

func packageRepositoryReadScan(directory string) repositoryReadScan {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return repositoryReadScan{candidate: true, unobservable: true, productionCandidate: true, productionUnobservable: true}
	}
	files := make([]repositoryReadFile, 0, len(entries))
	var parseScan repositoryReadScan
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") {
			continue
		}
		fset := token.NewFileSet()
		file, parseErr := parser.ParseFile(fset, filepath.Join(directory, entry.Name()), nil, parser.SkipObjectResolution)
		production := !strings.HasSuffix(entry.Name(), "_test.go")
		if parseErr != nil {
			parseScan.candidate = true
			parseScan.unobservable = true
			if production {
				parseScan.productionCandidate = true
				parseScan.productionUnobservable = true
			}
			continue
		}
		files = append(files, repositoryReadFile{file: file, production: production})
	}
	all := analyzeRepositoryReads(files, false)
	production := analyzeRepositoryReads(files, true)
	return repositoryReadScan{
		candidate:                    parseScan.candidate || all.candidate,
		unobservable:                 parseScan.unobservable || all.unobservable,
		productionCandidate:          parseScan.productionCandidate || production.candidate,
		productionUnobservable:       parseScan.productionUnobservable || production.unobservable,
		preRunDependencies:           all.preRunDependencies,
		productionPreRunDependencies: production.preRunDependencies,
	}
}

type repositoryReadFile struct {
	file       *ast.File
	production bool
}

type repositoryReaderCall struct {
	name          string
	observability repositoryReadObservability
}

type repositoryReadAnalysis struct {
	candidate          bool
	unobservable       bool
	preRunDependencies map[string]struct{}
}

func analyzeRepositoryReads(files []repositoryReadFile, productionOnly bool) repositoryReadAnalysis {
	graph := make(map[string]map[string]struct{})
	dependencies := make(map[string]map[string]struct{})
	readers := make(map[string]bool)
	roots := make(map[string]struct{})
	candidate, unobservable := false, false
	synthetic := 0
	for _, parsed := range files {
		if productionOnly && !parsed.production {
			continue
		}
		selectors, opaque := repositoryReaderSelectors(parsed.file)
		imports := repositoryImports(parsed.file)
		if opaque {
			candidate, unobservable = true, true
		}
		dotImports := repositoryDotImports(parsed.file)
		for _, declaration := range parsed.file.Decls {
			switch typed := declaration.(type) {
			case *ast.FuncDecl:
				name := typed.Name.Name
				mergeRepositoryReferences(graph, name, repositoryReferences(typed.Body, imports))
				mergeRepositoryReferences(dependencies, name, repositoryDependencyReferences(typed.Body, imports, dotImports))
				found, cannotObserve := repositoryCallsIn(typed.Body, selectors)
				candidate = candidate || found
				unobservable = unobservable || cannotObserve
				readers[name] = readers[name] || found
				if name == "init" || name == "TestMain" {
					roots[name] = struct{}{}
				}
			case *ast.GenDecl:
				if typed.Tok != token.VAR {
					continue
				}
				for _, spec := range typed.Specs {
					value, ok := spec.(*ast.ValueSpec)
					if !ok {
						continue
					}
					found, cannotObserve := repositoryCallsIn(value, selectors)
					candidate = candidate || found
					unobservable = unobservable || cannotObserve
					references := repositoryReferences(value, imports)
					dependencyReferences := repositoryDependencyReferences(value, imports, dotImports)
					for _, name := range value.Names {
						mergeRepositoryReferences(graph, name.Name, references)
						mergeRepositoryReferences(dependencies, name.Name, dependencyReferences)
						readers[name.Name] = readers[name.Name] || found
					}
					if initializerCalls(value) {
						synthetic++
						name := fmt.Sprintf("#initializer-%d", synthetic)
						mergeRepositoryReferences(graph, name, initializerReferences(value, imports))
						mergeRepositoryReferences(dependencies, name, initializerDependencyReferences(value, imports, dotImports))
						readers[name] = found
						roots[name] = struct{}{}
					}
				}
			}
		}
	}
	seen := make(map[string]struct{}, len(roots))
	queue := make([]string, 0, len(roots))
	preRunDependencies := make(map[string]struct{})
	for root := range roots {
		queue = append(queue, root)
	}
	for len(queue) != 0 {
		name := queue[0]
		queue = queue[1:]
		if _, visited := seen[name]; visited {
			continue
		}
		seen[name] = struct{}{}
		if readers[name] {
			unobservable = true
		}
		for dependency := range dependencies[name] {
			preRunDependencies[dependency] = struct{}{}
		}
		for reference := range graph[name] {
			if _, known := graph[reference]; known {
				queue = append(queue, reference)
			}
		}
	}
	return repositoryReadAnalysis{candidate: candidate, unobservable: unobservable, preRunDependencies: preRunDependencies}
}

func repositoryCallsIn(node ast.Node, selectors map[string][]repositoryReaderCall) (bool, bool) {
	found, unobservable := false, false
	ast.Inspect(node, func(node ast.Node) bool {
		selector, ok := node.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		qualifier, ok := selector.X.(*ast.Ident)
		if !ok {
			return true
		}
		for _, call := range selectors[qualifier.Name] {
			if call.name == selector.Sel.Name {
				found = true
				unobservable = unobservable || call.observability == testLogUnobservable
				break
			}
		}
		return true
	})
	return found, unobservable
}

func repositoryReferences(node ast.Node, imports map[string]string) map[string]struct{} {
	references := make(map[string]struct{})
	selectorNames := make(map[*ast.Ident]struct{})
	ast.Inspect(node, func(node ast.Node) bool {
		selector, ok := node.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		selectorNames[selector.Sel] = struct{}{}
		if qualifier, qualified := selector.X.(*ast.Ident); !qualified {
			references[selector.Sel.Name] = struct{}{}
		} else if _, imported := imports[qualifier.Name]; !imported {
			references[selector.Sel.Name] = struct{}{}
		}
		return true
	})
	ast.Inspect(node, func(node ast.Node) bool {
		identifier, ok := node.(*ast.Ident)
		if !ok {
			return true
		}
		if _, selectorName := selectorNames[identifier]; !selectorName {
			references[identifier.Name] = struct{}{}
		}
		return true
	})
	return references
}

func repositoryImports(file *ast.File) map[string]string {
	names := make(map[string]string, len(file.Imports))
	for _, imported := range file.Imports {
		path := strings.Trim(imported.Path.Value, `"`)
		name := path[strings.LastIndex(path, "/")+1:]
		if imported.Name != nil {
			name = imported.Name.Name
		}
		if name != "." && name != "_" {
			names[name] = path
		}
	}
	return names
}

func repositoryDotImports(file *ast.File) []string {
	var paths []string
	for _, imported := range file.Imports {
		if imported.Name != nil && imported.Name.Name == "." {
			paths = append(paths, strings.Trim(imported.Path.Value, `"`))
		}
	}
	return paths
}

func repositoryDependencyReferences(node ast.Node, imports map[string]string, dotImports []string) map[string]struct{} {
	references := make(map[string]struct{}, len(dotImports))
	for _, dependency := range dotImports {
		references[dependency] = struct{}{}
	}
	ast.Inspect(node, func(node ast.Node) bool {
		selector, ok := node.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		qualifier, ok := selector.X.(*ast.Ident)
		if !ok {
			return true
		}
		if dependency, imported := imports[qualifier.Name]; imported {
			references[dependency] = struct{}{}
		}
		return true
	})
	return references
}

func initializerReferences(node ast.Node, imports map[string]string) map[string]struct{} {
	references := make(map[string]struct{})
	ast.Inspect(node, func(node ast.Node) bool {
		if _, literal := node.(*ast.FuncLit); literal {
			return false
		}
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		if selected, qualified := call.Fun.(*ast.SelectorExpr); qualified {
			if qualifier, identified := selected.X.(*ast.Ident); identified && imports[qualifier.Name] == "sync" &&
				(selected.Sel.Name == "OnceValue" || selected.Sel.Name == "OnceValues") {
				for reference := range repositoryReferences(call.Fun, imports) {
					references[reference] = struct{}{}
				}
				return false
			}
		}
		for reference := range repositoryReferences(call, imports) {
			references[reference] = struct{}{}
		}
		return false
	})
	return references
}

func initializerDependencyReferences(node ast.Node, imports map[string]string, dotImports []string) map[string]struct{} {
	references := make(map[string]struct{})
	ast.Inspect(node, func(node ast.Node) bool {
		if _, literal := node.(*ast.FuncLit); literal {
			return false
		}
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		if selected, qualified := call.Fun.(*ast.SelectorExpr); qualified {
			if qualifier, identified := selected.X.(*ast.Ident); identified && imports[qualifier.Name] == "sync" &&
				(selected.Sel.Name == "OnceValue" || selected.Sel.Name == "OnceValues") {
				return false
			}
		}
		for dependency := range repositoryDependencyReferences(call, imports, dotImports) {
			references[dependency] = struct{}{}
		}
		return false
	})
	return references
}

func mergeRepositoryReferences(graph map[string]map[string]struct{}, name string, references map[string]struct{}) {
	if graph[name] == nil {
		graph[name] = make(map[string]struct{}, len(references))
	}
	for reference := range references {
		graph[name][reference] = struct{}{}
	}
}

func initializerCalls(node ast.Node) bool {
	called := false
	ast.Inspect(node, func(node ast.Node) bool {
		if _, literal := node.(*ast.FuncLit); literal {
			return false
		}
		if _, call := node.(*ast.CallExpr); call {
			called = true
		}
		return !called
	})
	return called
}

func repositoryReaderSelectors(file *ast.File) (map[string][]repositoryReaderCall, bool) {
	var selectors map[string][]repositoryReaderCall
	for _, imported := range file.Imports {
		path := strings.Trim(imported.Path.Value, `"`)
		if path == cgoImportPath {
			return nil, true
		}
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
		for call, observability := range calls {
			selectors[name] = append(selectors[name], repositoryReaderCall{name: call, observability: observability})
		}
	}
	return selectors, false
}
