// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package assure

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	gomutants "github.com/P4suta/go-mutants"
	goanalysis "github.com/P4suta/goatest/internal/golang"
	"github.com/P4suta/goatest/internal/report"
)

type RaceResult struct {
	Evidence []report.Evidence
	Findings []report.Finding
}

type RaceOptions struct {
	Environment []string
	TestArgs    []string
	BuildTags   []string
}

const raceVerificationTimeout = 30 * time.Minute

// RelevantRacePackages returns the packages whose top-level tests exercise a
// package containing concurrency. Running the concurrent dependency's own
// tests alone can miss a race that is only driven through a caller.
func RelevantRacePackages(model goanalysis.Model, concurrentPackages []string, targets []TargetEvidence) []string {
	concurrent := make(map[string]struct{}, len(concurrentPackages))
	for _, importPath := range concurrentPackages {
		concurrent[importPath] = struct{}{}
	}
	selected := make(map[string]struct{})
	for _, target := range targets {
		if reachesConcurrency(model, concurrent, target) {
			selected[target.Target.Package] = struct{}{}
		}
	}
	result := make([]string, 0, len(selected))
	for importPath := range selected {
		result = append(result, importPath)
	}
	slices.Sort(result)
	return result
}

func reachesConcurrency(model goanalysis.Model, concurrent map[string]struct{}, target TargetEvidence) bool {
	if _, ok := concurrent[target.Target.Package]; ok {
		return true
	}
	for _, dependency := range target.Target.Dependencies {
		if _, ok := concurrent[dependency]; ok {
			return true
		}
	}
	for _, covered := range target.CoveredFiles {
		if _, ok := concurrent[packageForFile(model.Packages, covered)]; ok {
			return true
		}
	}
	return false
}

func packageForFile(packages []goanalysis.Package, path string) string {
	path = strings.TrimPrefix(strings.ReplaceAll(path, `\`, "/"), "./")
	bestPath, bestPackage := "", ""
	for _, pkg := range packages {
		directory := strings.Trim(strings.ReplaceAll(pkg.RelativeDir, `\`, "/"), "/")
		if directory == "." {
			directory = ""
		}
		inside := !strings.Contains(path, "/")
		if directory != "" {
			inside = strings.HasPrefix(path, directory+"/")
		}
		if inside && (bestPackage == "" || len(directory) > len(bestPath)) {
			bestPath, bestPackage = directory, pkg.ImportPath
		}
	}
	return bestPackage
}

func CollectRace(ctx context.Context, workspace CommandWorkspace, model goanalysis.Model, concurrentPackages []string, contract string, environment []string) (RaceResult, error) {
	return CollectRaceWithOptions(ctx, workspace, model, concurrentPackages, contract, RaceOptions{Environment: environment})
}

func CollectRaceWithOptions(ctx context.Context, workspace CommandWorkspace, model goanalysis.Model, concurrentPackages []string, contract string, options RaceOptions) (RaceResult, error) {
	packages := slices.Clone(concurrentPackages)
	if contract == "deep-v1" {
		packages = packages[:0]
		for _, pkg := range model.Packages {
			packages = append(packages, pkg.ImportPath)
		}
	}
	slices.Sort(packages)
	packages = slices.Compact(packages)
	if len(packages) == 0 {
		return RaceResult{Evidence: []report.Evidence{{Kind: "race", ID: "packages", Status: "not-applicable"}}}, nil
	}
	if workspace == nil {
		return RaceResult{}, fmt.Errorf("goatest: nil race workspace")
	}
	argv := []string{"go", "test", "-race", "-count=1"}
	if len(options.BuildTags) != 0 {
		argv = append(argv, "-tags="+strings.Join(options.BuildTags, ","))
	}
	argv = append(argv, packages...)
	if len(options.TestArgs) != 0 {
		argv = append(argv, "-args")
		argv = append(argv, options.TestArgs...)
	}
	replay := strings.Join(argv, " ")
	run, err := workspace.Exec(ctx, gomutants.Command{
		Argv: argv, Env: slices.Clone(options.Environment), Timeout: raceVerificationTimeout,
	})
	if err != nil {
		return RaceResult{}, fmt.Errorf("goatest: race verification: %w", err)
	}
	if run.TimedOut {
		return RaceResult{}, fmt.Errorf("goatest: race verification timed out")
	}
	if run.ExitCode != 0 {
		output := string(run.Output)
		if strings.Contains(output, "WARNING: DATA RACE") {
			id := report.FindingID("data-race", strings.Join(packages, "\x00"))
			return RaceResult{Findings: []report.Finding{{
				ID: id, Kind: "data-race", Summary: "Go race detector reproduced a data race: " + summarize(run.Output),
				Replay: replay,
			}}}, nil
		}
		if strings.Contains(output, "--- FAIL:") || strings.Contains(output, "\npanic:") {
			id := report.FindingID("race-test-failure", strings.Join(packages, "\x00"))
			return RaceResult{Findings: []report.Finding{{
				ID: id, Kind: "race-test-failure", Summary: "a test failed under the Go race detector: " + summarize(run.Output),
				Replay: replay,
			}}}, nil
		}
		return RaceResult{}, fmt.Errorf("goatest: race verification failed (exit=%d): %s", run.ExitCode, summarize(run.Output))
	}
	return RaceResult{Evidence: []report.Evidence{{
		Kind: "race", ID: strings.Join(packages, ","), Status: "passed",
	}}}, nil
}
