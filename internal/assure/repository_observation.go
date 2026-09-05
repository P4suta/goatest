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
)

var repositoryTestLogMagic = []byte("# test log\n")

// RepositoryObserver turns Go's test action log into the one fact mutation
// evidence needs: whether an execution consulted the frozen repository beyond
// the files its ordinary behaviour key already names.
//
// It is active only for statically selected candidates. Its zero and every
// failure state are conservative: a candidate that could not be observed is
// keyed on the whole tree, while a package outside the candidate boundary is
// left under the existing closure contract.
type RepositoryObserver struct {
	root       string
	directory  string
	candidates map[string]goanalysis.RepositoryReadCandidate
	packages   map[string]goanalysis.Package
	sources    targetKeySources
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
		// internal/testlog intentionally omits names containing a newline. A
		// tree that can produce one cannot be narrowed from its action log.
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
			return arguments, func() repositoryObservation { return repositoryObservation{unknown: true} }
		}
		return arguments, func() repositoryObservation { return repositoryObservation{} }
	}
	return observer.instrument(pkg, owner.RelativeDir, arguments)
}

type repositoryObservation struct {
	unknown  bool
	accesses []repositoryAccess
}

type repositoryAccess struct {
	path      string
	directory bool
}

// instrument adds one private test-binary flag and returns the operation that
// must be called as soon as the execution ends. The log path is pre-created so
// a setup failure cannot turn a test result into a false mutation kill.
func (observer *RepositoryObserver) instrument(pkg, relativeDir string, arguments []string) ([]string, func() repositoryObservation) {
	candidate, selected := observer.candidate(pkg)
	if !selected {
		return arguments, func() repositoryObservation { return repositoryObservation{} }
	}
	if candidate.Unobservable || observer.root == "" || observer.directory == "" {
		return arguments, func() repositoryObservation { return repositoryObservation{unknown: true} }
	}
	file, err := os.CreateTemp(observer.directory, "test-action-*.log")
	if err != nil {
		return arguments, func() repositoryObservation { return repositoryObservation{unknown: true} }
	}
	name := file.Name()
	if err := file.Close(); err != nil {
		_ = os.Remove(name)
		return arguments, func() repositoryObservation { return repositoryObservation{unknown: true} }
	}
	instrumented := append(slices.Clone(arguments), "-test.testlogfile="+name)
	initialDirectory := filepath.Join(observer.root, filepath.FromSlash(relativeDir))
	return instrumented, func() repositoryObservation {
		defer func() { _ = os.Remove(name) }()
		data, err := os.ReadFile(name)
		if err != nil {
			return repositoryObservation{unknown: true}
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

func (observer *RepositoryObserver) observes(pkg string) bool {
	_, selected := observer.candidate(pkg)
	return selected
}

func (observer *RepositoryObserver) wholeTree(target goanalysis.Target, observation repositoryObservation) bool {
	candidate, selected := observer.candidate(target.Package)
	if !selected {
		return false
	}
	if candidate.Unobservable || observation.unknown {
		return true
	}
	inputs := observer.sources.narrowInputsFor(target)
	for _, access := range observation.accesses {
		if access.directory {
			return true
		}
		if _, known := inputs.Files[access.path]; known {
			continue
		}
		if _, known := inputs.Corpus[access.path]; !known {
			return true
		}
	}
	return false
}

func (observer *RepositoryObserver) wholeTreeSuite(pkg string, observation repositoryObservation) bool {
	if observer == nil {
		return false
	}
	owner, known := observer.packages[pkg]
	if !known {
		_, selected := observer.candidate(pkg)
		return selected
	}
	return observer.wholeTree(goanalysis.Target{
		Package: pkg, RelativeDir: owner.RelativeDir, Dependencies: owner.Dependencies,
	}, observation)
}

// parseRepositoryTestLog accepts the format shared by testing and cmd/go. It
// keeps only accesses lexically inside root. Any malformed input is unknown,
// which the caller turns into a whole-tree key rather than an execution error.
func parseRepositoryTestLog(data []byte, root, initialDirectory string) repositoryObservation {
	if !bytes.HasPrefix(data, repositoryTestLogMagic) || len(data) == 0 || data[len(data)-1] != '\n' {
		return repositoryObservation{unknown: true}
	}
	observation := repositoryObservation{}
	workingDirectory := initialDirectory
	for _, raw := range bytes.Split(bytes.TrimPrefix(data, repositoryTestLogMagic), []byte("\n")) {
		if len(raw) == 0 {
			continue
		}
		operation, name, found := strings.Cut(string(raw), " ")
		if !found || name == "" {
			observation.unknown = true
			continue
		}
		switch operation {
		case "getenv":
			continue
		case "chdir":
			if !filepath.IsAbs(name) {
				observation.unknown = true
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
				// A missing or unreadable repository path can still decide a
				// test through its error, and no narrow digest describes it.
				observation.accesses = append(observation.accesses, repositoryAccess{path: relative, directory: true})
				continue
			}
			observation.accesses = append(observation.accesses, repositoryAccess{path: relative, directory: info.IsDir()})
		default:
			observation.unknown = true
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

// repositoryTestLogFailure reports the diagnostic emitted when testing could
// not create or flush the private action log. Only that path-specific failure
// triggers an unobserved retry; a panic or ordinary test failure may also leave
// a truncated log, but remains the result of the test and merely gets a
// conservative whole-tree key.
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
	// Baseline output may already be a test2json event, where Windows path
	// separators are escaped. Match the JSON string contents as well as raw
	// test-binary output so an observation failure never becomes a test failure.
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
