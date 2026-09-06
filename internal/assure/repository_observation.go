// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package assure

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"

	goanalysis "github.com/P4suta/goatest/internal/golang"
	"github.com/P4suta/goatest/internal/trace"
)

var repositoryTestLogMagic = []byte("# test log\n")

type RepositoryObserver struct {
	root       string
	directory  string
	candidates map[string]goanalysis.RepositoryReadCandidate
	packages   map[string]goanalysis.Package
	sources    targetKeySources
}

func repositoryObservationScope(root string, packages []goanalysis.Package) (map[string]goanalysis.RepositoryReadCandidate, map[string]bool) {
	candidates := goanalysis.RepositoryReadCandidates(root, packages)
	readers := make(map[string]bool, len(packages))
	for _, pkg := range packages {
		if _, found := candidates[pkg.ImportPath]; !found {
			candidates[pkg.ImportPath] = goanalysis.RepositoryReadCandidate{}
		}
		readers[pkg.ImportPath] = true
	}
	return candidates, readers
}

func newRepositoryObserver(root, directory string, candidates map[string]goanalysis.RepositoryReadCandidate, sources targetKeySources) *RepositoryObserver {
	packages := make(map[string]goanalysis.Package, len(sources.model.Packages))
	for _, pkg := range sources.model.Packages {
		packages[pkg.ImportPath] = pkg
	}
	absolute, err := filepath.Abs(root)
	if err != nil || root == "" {
		absolute = ""
	}
	if absolute != "" {
		absolute = filepath.Clean(absolute)
	}
	selected := make(map[string]goanalysis.RepositoryReadCandidate, len(candidates))
	for path, candidate := range candidates {
		selected[path] = candidate
	}
	if strings.ContainsRune(absolute, '\n') || slices.ContainsFunc(sources.extraFiles, func(name string) bool {
		return strings.ContainsRune(name, '\n')
	}) {
		for path, candidate := range selected {
			candidate.Unobservable = true
			selected[path] = candidate
		}
	}
	return &RepositoryObserver{
		root: absolute, directory: directory,
		candidates: selected, packages: packages, sources: sources,
	}
}

func (observer *RepositoryObserver) instrumentPackage(pkg string, arguments []string) ([]string, func() repositoryObservation) {
	if observer == nil {
		return arguments, func() repositoryObservation { return repositoryObservation{} }
	}
	owner, known := observer.packages[pkg]
	if !known {
		if _, selected := observer.candidate(pkg); selected {
			return arguments, func() repositoryObservation {
				return repositoryObservation{reason: wholeTreeStaticUnobservable}
			}
		}
		return arguments, func() repositoryObservation { return repositoryObservation{} }
	}
	return observer.instrument(pkg, owner.RelativeDir, arguments)
}

type wholeTreeReason string

const (
	wholeTreeObserved           wholeTreeReason = ""
	wholeTreeStaticUnobservable wholeTreeReason = trace.WholeTreeStaticUnobservable
	wholeTreeLogUnavailable     wholeTreeReason = trace.WholeTreeLogUnavailable
	wholeTreeLogAmbiguous       wholeTreeReason = trace.WholeTreeLogAmbiguous
	wholeTreeDirectoryAccess    wholeTreeReason = trace.WholeTreeDirectoryAccess
	wholeTreeOutsideInput       wholeTreeReason = trace.WholeTreeOutsideInput
)

type repositoryObservation struct {
	reason   wholeTreeReason
	accesses []repositoryAccess
}

type repositoryAccess struct {
	path      string
	directory bool
}

func (observer *RepositoryObserver) instrument(pkg, relativeDir string, arguments []string) ([]string, func() repositoryObservation) {
	candidate, selected := observer.candidate(pkg)
	if !selected {
		return arguments, func() repositoryObservation { return repositoryObservation{} }
	}
	if candidate.Unobservable {
		return arguments, func() repositoryObservation {
			return repositoryObservation{reason: wholeTreeStaticUnobservable}
		}
	}
	if observer.root == "" || observer.directory == "" {
		return arguments, func() repositoryObservation {
			return repositoryObservation{reason: wholeTreeLogUnavailable}
		}
	}
	file, err := os.CreateTemp(observer.directory, "test-action-*.log")
	if err != nil {
		return arguments, func() repositoryObservation {
			return repositoryObservation{reason: wholeTreeLogUnavailable}
		}
	}
	name := file.Name()
	if err := file.Close(); err != nil {
		_ = os.Remove(name)
		return arguments, func() repositoryObservation {
			return repositoryObservation{reason: wholeTreeLogUnavailable}
		}
	}
	instrumented := append(slices.Clone(arguments), "-test.testlogfile="+name)
	initialDirectory := filepath.Join(observer.root, filepath.FromSlash(relativeDir))
	return instrumented, func() repositoryObservation {
		defer func() { _ = os.Remove(name) }()
		data, err := os.ReadFile(name)
		if err != nil {
			return repositoryObservation{reason: wholeTreeLogUnavailable}
		}
		return parseRepositoryTestLog(data, observer.root, initialDirectory)
	}
}

func (observer *RepositoryObserver) candidate(pkg string) (goanalysis.RepositoryReadCandidate, bool) {
	if observer == nil {
		return goanalysis.RepositoryReadCandidate{}, false
	}
	candidate, selected := observer.candidates[pkg]
	return candidate, selected
}

func (observer *RepositoryObserver) wholeTree(target goanalysis.Target, observation repositoryObservation) bool {
	return observer.wholeTreeReason(target, observation) != wholeTreeObserved
}

func (observer *RepositoryObserver) wholeTreeReason(target goanalysis.Target, observation repositoryObservation) wholeTreeReason {
	candidate, selected := observer.candidate(target.Package)
	if !selected {
		return wholeTreeObserved
	}
	if candidate.Unobservable {
		return wholeTreeStaticUnobservable
	}
	if observation.reason != wholeTreeObserved {
		return observation.reason
	}
	inputs := observer.sources.narrowInputsFor(target)
	for _, access := range observation.accesses {
		if access.directory {
			return wholeTreeDirectoryAccess
		}
		if _, known := inputs.Files[access.path]; known {
			continue
		}
		if _, known := inputs.Corpus[access.path]; !known {
			return wholeTreeOutsideInput
		}
	}
	return wholeTreeObserved
}

func (observer *RepositoryObserver) wholeTreeSuite(pkg string, observation repositoryObservation) bool {
	return observer.wholeTreeSuiteReason(pkg, observation) != wholeTreeObserved
}

func (observer *RepositoryObserver) wholeTreeSuiteReason(pkg string, observation repositoryObservation) wholeTreeReason {
	if observer == nil {
		return wholeTreeObserved
	}
	owner, known := observer.packages[pkg]
	if !known {
		if _, selected := observer.candidate(pkg); selected {
			return wholeTreeStaticUnobservable
		}
		return wholeTreeObserved
	}
	return observer.wholeTreeReason(goanalysis.Target{
		Package: pkg, RelativeDir: owner.RelativeDir, Dependencies: owner.Dependencies,
	}, observation)
}

func parseRepositoryTestLog(data []byte, root, initialDirectory string) repositoryObservation {
	if !bytes.HasPrefix(data, repositoryTestLogMagic) || len(data) == 0 || data[len(data)-1] != '\n' {
		return repositoryObservation{reason: wholeTreeLogAmbiguous}
	}
	observation := repositoryObservation{}
	workingDirectory := initialDirectory
	for _, raw := range bytes.Split(bytes.TrimPrefix(data, repositoryTestLogMagic), []byte("\n")) {
		if len(raw) == 0 {
			continue
		}
		operation, name, found := strings.Cut(string(raw), " ")
		if !found || name == "" {
			observation.reason = wholeTreeLogAmbiguous
			continue
		}
		switch operation {
		case "getenv":
			continue
		case "chdir":
			if !filepath.IsAbs(name) {
				observation.reason = wholeTreeLogAmbiguous
				continue
			}
			workingDirectory = filepath.Clean(name)
			if relative, inside := repositoryRelativePath(root, workingDirectory); inside {
				observation.accesses = append(observation.accesses, repositoryAccess{path: relative, directory: true})
			}
		case "open", "stat":
			resolved := name
			if !filepath.IsAbs(resolved) {
				resolved = filepath.Join(workingDirectory, resolved)
			}
			resolved = filepath.Clean(resolved)
			relative, inside := repositoryRelativePath(root, resolved)
			if !inside {
				continue
			}
			info, err := os.Stat(resolved)
			if err != nil {
				observation.accesses = append(observation.accesses, repositoryAccess{path: relative, directory: true})
				continue
			}
			observation.accesses = append(observation.accesses, repositoryAccess{path: relative, directory: info.IsDir()})
		default:
			observation.reason = wholeTreeLogAmbiguous
		}
	}
	return observation
}

func repositoryRelativePath(root, name string) (string, bool) {
	if root == "" || name == "" {
		return "", false
	}
	relative, err := filepath.Rel(root, name)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return "", false
	}
	return filepath.ToSlash(relative), true
}

func repositoryTestLogFailure(output string, arguments []string) bool {
	path, found := repositoryTestLogPath(arguments)
	if !found {
		return false
	}
	if !strings.Contains(output, "testing:") {
		return false
	}
	if strings.Contains(output, path) {
		return true
	}

	encoded, err := json.Marshal(path)
	return err == nil && len(encoded) >= 2 && strings.Contains(output, string(encoded[1:len(encoded)-1]))
}

func repositoryTestLogPath(arguments []string) (string, bool) {
	for _, argument := range arguments {
		if path, found := strings.CutPrefix(argument, "-test.testlogfile="); found {
			return path, path != ""
		}
	}
	return "", false
}
