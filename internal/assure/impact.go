// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package assure

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/P4suta/goatest/internal/evidence"
	goanalysis "github.com/P4suta/goatest/internal/golang"
)

type impactSelection struct {
	targets []goanalysis.Target
	changed []string
	broad   bool
	prior   *evidence.GraphRecord
}

const changedFilesTimeout = 30 * time.Second

var (
	loadImpactGraph    = evidence.LoadGraph
	changedImpactFiles = changedFiles
	runImpactGitNames  = runGitNames
	gitNamesOutput     = func(ctx context.Context, root string, arguments []string) ([]byte, error) {
		command := exec.CommandContext(ctx, "git", arguments...)
		command.Dir = root
		command.Env = os.Environ()
		return command.Output()
	}
)

func selectImpact(ctx context.Context, root string, model goanalysis.Model, targets []goanalysis.Target, options Options) impactSelection {
	if !options.Changed {
		return impactSelection{targets: slices.Clone(targets), broad: true}
	}
	path := filepath.Join(root, ".goatest", "graph-v1.json")
	record, found, err := loadImpactGraph(path)
	if err != nil {
		return impactSelection{targets: slices.Clone(targets), broad: true}
	}
	if !found {
		return impactSelection{targets: slices.Clone(targets), broad: true}
	}
	if record.ModulePath != model.ModulePath {
		return impactSelection{targets: slices.Clone(targets), broad: true}
	}
	changed, known := changedImpactFiles(ctx, root, options.ChangedRef)
	if !known || len(changed) == 0 {
		return impactSelection{targets: slices.Clone(targets), changed: changed, broad: true, prior: &record}
	}
	impact := record.Graph.Affected(changed)
	if impact.Broad {
		return impactSelection{targets: slices.Clone(targets), changed: changed, broad: true, prior: &record}
	}
	selectedIDs := make(map[string]bool, len(impact.Targets))
	for _, id := range impact.Targets {
		selectedIDs[id] = true
	}
	changedPackages := make(map[string]bool)
	for _, path := range changed {
		if pkg := record.Graph.FilePackages[path]; pkg != "" && strings.HasSuffix(path, "_test.go") {
			changedPackages[pkg] = true
		}
	}
	var selected []goanalysis.Target
	for _, target := range targets {
		if selectedIDs[target.ID] || changedPackages[target.Package] || dependsOnChanged(target.Dependencies, changedPackages) {
			selected = append(selected, target)
		}
	}
	return impactSelection{targets: selected, changed: changed, prior: &record}
}

func dependsOnChanged(dependencies []string, changed map[string]bool) bool {
	for _, dependency := range dependencies {
		if changed[dependency] {
			return true
		}
	}
	return false
}

func changedFiles(parent context.Context, root, reference string) ([]string, bool) {
	ctx, cancel := context.WithTimeout(parent, changedFilesTimeout)
	defer cancel()
	base := reference
	if base == "" {
		base = "HEAD"
	}
	diff, ok := runImpactGitNames(ctx, root, []string{"diff", "--no-ext-diff", "--name-only", "--diff-filter=ACMRD", "-z", base, "--"})
	if !ok {
		return nil, false
	}
	untracked, ok := runImpactGitNames(ctx, root, []string{"ls-files", "--others", "--exclude-standard", "-z", "--"})
	if !ok {
		return nil, false
	}
	paths := append(diff, untracked...)
	changed := make([]string, 0, len(paths))
	for _, path := range paths {
		normalized, valid := safeChangedPath(path)
		if !valid {
			return nil, false
		}
		if generatedImpactPath(normalized) {
			continue
		}
		changed = append(changed, normalized)
	}
	slices.Sort(changed)
	return slices.Compact(changed), true
}

func generatedImpactPath(path string) bool {
	root, rest, nested := strings.Cut(path, "/")
	if !nested || rest == "" {
		return false
	}
	return root == ".goatest" || root == "reports" || root == "dist"
}

func runGitNames(ctx context.Context, root string, arguments []string) ([]string, bool) {
	output, err := gitNamesOutput(ctx, root, arguments)
	if err != nil || ctx.Err() != nil {
		return nil, false
	}
	names := bytes.Split(output, []byte{0})
	result := make([]string, 0, len(names))
	for _, name := range names {
		if len(name) != 0 {
			result = append(result, string(name))
		}
	}
	return result, true
}

func safeChangedPath(path string) (string, bool) {
	if path == "" || strings.HasPrefix(path, "/") || strings.ContainsAny(path, "\\:\x00\r\n") {
		return "", false
	}
	native := filepath.FromSlash(path)
	clean := filepath.Clean(native)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", false
	}
	return filepath.ToSlash(clean), true
}

func buildGraph(root string, model goanalysis.Model, targetEvidence []TargetEvidence) (evidence.Graph, error) {
	graph := evidence.Graph{FilePackages: make(map[string]string)}
	for _, pkg := range model.Packages {
		directory := filepath.Join(root, filepath.FromSlash(pkg.RelativeDir))
		entries, err := os.ReadDir(directory)
		if err != nil {
			return evidence.Graph{}, fmt.Errorf("goatest: build graph for %s: %w", pkg.ImportPath, err)
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") {
				continue
			}
			path := entry.Name()
			if pkg.RelativeDir != "." && pkg.RelativeDir != "" {
				path = pkg.RelativeDir + "/" + path
			}
			graph.FilePackages[path] = pkg.ImportPath
		}
	}
	for _, target := range targetEvidence {
		graph.Targets = append(graph.Targets, evidence.Target{
			ID: target.Target.ID, Package: target.Target.Package, Kind: string(target.Target.Kind),
			Dependencies: slices.Clone(target.Target.Dependencies), CoveredFiles: slices.Clone(target.CoveredFiles),
		})
	}
	return graph, nil
}

func mergeGraph(current evidence.Graph, prior *evidence.GraphRecord, selection impactSelection) evidence.Graph {
	if prior == nil || selection.broad {
		return current
	}
	updated := make(map[string]bool, len(selection.targets))
	for _, target := range selection.targets {
		updated[target.ID] = true
	}
	merged := current
	for _, target := range prior.Graph.Targets {
		if !updated[target.ID] {
			merged.Targets = append(merged.Targets, target)
		}
	}
	return merged
}

func mutationScope(selection impactSelection) (include, packages []string) {
	if selection.broad {
		return nil, nil
	}
	for _, path := range selection.changed {
		if strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, "_test.go") {
			include = append(include, path)
		}
	}
	for _, target := range selection.targets {
		directory := target.RelativeDir
		if directory == "" || directory == "." {
			directory = "."
		} else {
			directory = "./" + strings.TrimPrefix(directory, "./")
		}
		packages = append(packages, directory)
	}
	slices.Sort(include)
	include = slices.Compact(include)
	slices.Sort(packages)
	packages = slices.Compact(packages)
	return include, packages
}
