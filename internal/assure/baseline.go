// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package assure

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"time"

	gomutants "github.com/P4suta/go-mutants"
	goanalysis "github.com/P4suta/goatest/internal/golang"
	"github.com/P4suta/goatest/internal/report"
)

type CommandWorkspace interface {
	Exec(context.Context, gomutants.Command) (gomutants.CommandResult, error)
}

type BaselineTarget struct {
	Target      goanalysis.Target
	Environment []string
}

type BaselineOptions struct {
	ArtifactDirectory    string
	CommandTimeout       time.Duration
	TargetTimeout        time.Duration
	Packages             []string
	BuildTags            []string
	TestArgs             []string
	UseTest2JSON         bool
	ClassifyUserFailures bool
}

type BaselineResult struct {
	Evidence []report.Evidence
	Findings []report.Finding
	Targets  []TargetEvidence
	Executed int
	Skipped  int
}

const (
	defaultBaselineTimeout = 10 * time.Minute
	maximumSummaryRunes    = 512
)

// CollectBaseline validates build/vet, compiles exactly one baseline test
// binary for each package that owns a target, and executes every top-level
// TestX/FuzzX independently to produce an exact coverage graph.
func CollectBaseline(ctx context.Context, workspace CommandWorkspace, model goanalysis.Model, targets []BaselineTarget, options BaselineOptions) (BaselineResult, error) {
	if workspace == nil {
		return BaselineResult{}, fmt.Errorf("goatest: nil baseline workspace")
	}
	if options.ArtifactDirectory == "" {
		return BaselineResult{}, fmt.Errorf("goatest: baseline requires an artifact directory")
	}
	if err := os.MkdirAll(options.ArtifactDirectory, 0o755); err != nil {
		return BaselineResult{}, fmt.Errorf("goatest: create baseline artifact directory: %w", err)
	}
	commandTimeout := options.CommandTimeout
	if commandTimeout <= 0 {
		commandTimeout = defaultBaselineTimeout
	}
	targetTimeout := options.TargetTimeout
	if targetTimeout <= 0 {
		targetTimeout = defaultBaselineTimeout
	}
	var result BaselineResult
	patterns := slices.Clone(options.Packages)
	if len(patterns) == 0 {
		patterns = []string{"./..."}
	}
	for _, check := range []struct {
		name string
		argv []string
	}{
		{name: "go vet", argv: baselineGoCommand("vet", options.BuildTags, patterns)},
		{name: "go build", argv: baselineGoCommand("build", options.BuildTags, patterns)},
	} {
		run, err := workspace.Exec(ctx, gomutants.Command{Argv: check.argv, Timeout: commandTimeout})
		if err != nil {
			return BaselineResult{}, fmt.Errorf("goatest: %s: %w", check.name, err)
		}
		if run.TimedOut {
			return BaselineResult{}, fmt.Errorf("goatest: %s failed (exit=%d timeout=%t): %s", check.name, run.ExitCode, run.TimedOut, summarize(run.Output))
		}
		if run.ExitCode != 0 {
			if !options.ClassifyUserFailures {
				return BaselineResult{}, fmt.Errorf("goatest: %s failed (exit=%d timeout=%t): %s", check.name, run.ExitCode, run.TimedOut, summarize(run.Output))
			}
			kind := strings.ReplaceAll(check.name, "go ", "") + "-failure"
			result.Findings = append(result.Findings, report.Finding{
				ID: report.FindingID("baseline", kind), Kind: kind,
				Summary: check.name + " rejected the project: " + summarize(run.Output),
			})
			markTargetsNotRun(&result, targets, check.name+" failed")
			return result, nil
		}
		result.Evidence = append(result.Evidence, report.Evidence{Kind: "baseline", ID: check.name, Status: "passed"})
	}

	packageTargets := make(map[string][]BaselineTarget)
	for _, target := range targets {
		packageTargets[target.Target.Package] = append(packageTargets[target.Target.Package], target)
	}
	packageByImport := make(map[string]goanalysis.Package, len(model.Packages))
	for _, pkg := range model.Packages {
		packageByImport[pkg.ImportPath] = pkg
	}
	imports := make([]string, 0, len(packageTargets))
	for importPath := range packageTargets {
		imports = append(imports, importPath)
	}
	slices.Sort(imports)
	for _, importPath := range imports {
		pkg, ok := packageByImport[importPath]
		if !ok {
			return BaselineResult{}, fmt.Errorf("goatest: target package %s was absent from go list", importPath)
		}
		binary := filepath.Join(options.ArtifactDirectory, binaryName(importPath))
		compile := gomutants.Command{
			Argv:    baselineCompileCommand(model.ModulePath, importPath, binary, options.BuildTags),
			Timeout: commandTimeout,
		}
		compiled, err := workspace.Exec(ctx, compile)
		if err != nil {
			return BaselineResult{}, fmt.Errorf("goatest: compile test binary for %s: %w", importPath, err)
		}
		if compiled.TimedOut {
			return BaselineResult{}, fmt.Errorf("goatest: compile test binary for %s failed (exit=%d timeout=%t): %s", importPath, compiled.ExitCode, compiled.TimedOut, summarize(compiled.Output))
		}
		if compiled.ExitCode != 0 {
			if !options.ClassifyUserFailures {
				return BaselineResult{}, fmt.Errorf("goatest: compile test binary for %s failed (exit=%d timeout=%t): %s", importPath, compiled.ExitCode, compiled.TimedOut, summarize(compiled.Output))
			}
			result.Findings = append(result.Findings, report.Finding{
				ID: report.FindingID("test-binary-build", importPath), Kind: "test-binary-build-failure",
				Summary: "the package test binary did not compile: " + summarize(compiled.Output),
			})
			markTargetsNotRun(&result, packageTargets[importPath], "test binary did not compile")
			continue
		}
		for _, target := range packageTargets[importPath] {
			profile := filepath.Join(options.ArtifactDirectory, target.Target.ID+".cover")
			command := targetCommand(binary, profile, pkg.RelativeDir, target, targetTimeout)
			command.Argv = append(command.Argv, options.TestArgs...)
			if options.UseTest2JSON {
				command = test2JSONCommand(importPath, command)
			}
			first, err := workspace.Exec(ctx, command)
			if err != nil {
				return BaselineResult{}, fmt.Errorf("goatest: baseline target %s: %w", target.Target.Name, err)
			}
			skipped, skipKind, skipSummary, eventErr := classifyTest2JSON(target.Target.Name, first.Output)
			if eventErr != nil && options.UseTest2JSON {
				return BaselineResult{}, fmt.Errorf("goatest: decode test2json for %s: %w", target.Target.Name, eventErr)
			}
			if skipped {
				result.Skipped++
				result.Evidence = append(result.Evidence, report.Evidence{
					Kind: "target", ID: target.Target.ID, Status: "skipped", Detail: target.Target.Name,
				})
				result.Findings = append(result.Findings, targetFinding(target.Target, skipKind, skipSummary))
				continue
			}
			if first.TimedOut || first.ExitCode != 0 {
				attempts := []gomutants.CommandResult{first}
				for range 2 {
					retry, retryErr := workspace.Exec(ctx, command)
					if retryErr != nil {
						return BaselineResult{}, fmt.Errorf("goatest: repeat baseline target %s: %w", target.Target.Name, retryErr)
					}
					attempts = append(attempts, retry)
				}
				kind, summary := classifyTargetFailure(attempts)
				result.Findings = append(result.Findings, targetFinding(target.Target, kind, summary))
				result.Executed++
				continue
			}
			profileData, err := os.ReadFile(profile)
			if err != nil {
				return BaselineResult{}, fmt.Errorf("goatest: read coverage for %s: %w", target.Target.Name, err)
			}
			covered, err := goanalysis.CoverageFiles(profileData, model.ModulePath)
			if err != nil {
				return BaselineResult{}, fmt.Errorf("goatest: coverage for %s: %w", target.Target.Name, err)
			}
			result.Targets = append(result.Targets, TargetEvidence{
				Target: target.Target, CoveredFiles: covered, Environment: slices.Clone(target.Environment), Duration: first.Duration,
			})
			result.Evidence = append(result.Evidence, report.Evidence{
				Kind: "target", ID: target.Target.ID, Status: "passed", Detail: target.Target.Name,
			})
			result.Executed++
		}
	}
	return result, nil
}

func baselineGoCommand(operation string, tags, packages []string) []string {
	argv := []string{"go", operation}
	if len(tags) != 0 {
		argv = append(argv, "-tags="+strings.Join(tags, ","))
	}
	return append(argv, packages...)
}

func baselineCompileCommand(modulePath, importPath, binary string, tags []string) []string {
	argv := []string{"go", "test", "-c"}
	if len(tags) != 0 {
		argv = append(argv, "-tags="+strings.Join(tags, ","))
	}
	argv = append(argv, "-coverpkg="+modulePath+"/...", "-o", binary, importPath)
	return argv
}

func markTargetsNotRun(result *BaselineResult, targets []BaselineTarget, reason string) {
	for _, target := range targets {
		result.Evidence = append(result.Evidence, report.Evidence{
			Kind: "target", ID: target.Target.ID, Status: "not-run", Detail: reason,
		})
		result.Skipped++
	}
}

func test2JSONCommand(importPath string, target gomutants.Command) gomutants.Command {
	arguments := slices.Clone(target.Argv[1:])
	arguments = append([]string{"go", "tool", "test2json", "-t", "-p", importPath, target.Argv[0], "-test.v=test2json"}, arguments...)
	target.Argv = arguments
	return target
}

type test2JSONEvent struct {
	Action string `json:"Action"`
	Test   string `json:"Test"`
	Output string `json:"Output"`
}

func classifyTest2JSON(target string, output []byte) (bool, string, string, error) {
	if len(output) == 0 {
		return false, "", "", nil
	}
	scanner := bufio.NewScanner(strings.NewReader(string(output)))
	scanner.Buffer(make([]byte, 64<<10), 4<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var event test2JSONEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			continue
		}
		if event.Action != "skip" || event.Test != target && !strings.HasPrefix(event.Test, target+"/") {
			continue
		}
		if event.Test == target {
			return true, "skipped-target", "the selected top-level target called Skip", nil
		}
		return true, "skipped-subtest", "a selected subtest was skipped: " + event.Test, nil
	}
	if err := scanner.Err(); err != nil {
		return false, "", "", err
	}
	return false, "", "", nil
}

func binaryName(importPath string) string {
	sum := sha256.Sum256([]byte(importPath))
	extension := ".test"
	if runtime.GOOS == "windows" {
		extension += ".exe"
	}
	return hex.EncodeToString(sum[:8]) + extension
}

func targetCommand(binary, profile, relativeDir string, target BaselineTarget, timeout time.Duration) gomutants.Command {
	return gomutants.Command{
		Argv: []string{
			binary,
			"-test.run=^" + regexp.QuoteMeta(target.Target.Name) + "$",
			"-test.coverprofile=" + profile,
			"-test.count=1",
		},
		Dir: relativeDir, Env: slices.Clone(target.Environment), Timeout: timeout,
	}
}

func classifyTargetFailure(attempts []gomutants.CommandResult) (string, string) {
	consistent := true
	for _, attempt := range attempts[1:] {
		if attempt.TimedOut != attempts[0].TimedOut || attempt.ExitCode != attempts[0].ExitCode {
			consistent = false
		}
	}
	if !consistent {
		return "flaky-test", "baseline target produced inconsistent results across three attempts"
	}
	if attempts[0].TimedOut {
		return "baseline-timeout", "baseline target timed out in three consecutive attempts"
	}
	return "baseline-failure", "baseline target failed in three consecutive attempts: " + summarize(attempts[0].Output)
}

func targetFinding(target goanalysis.Target, kind, summary string) report.Finding {
	id := report.FindingID("target", target.ID, kind)
	return report.Finding{
		ID: id, Kind: kind, Path: target.Path, Line: target.Line, Summary: summary,
		Replay: "goatest replay " + id,
	}
}

func summarize(output []byte) string {
	trimmed := strings.TrimSpace(string(output))
	if trimmed == "" {
		return "no output"
	}
	runes := []rune(trimmed)
	if len(runes) > maximumSummaryRunes {
		return string(runes[:maximumSummaryRunes]) + "…"
	}
	return trimmed
}
