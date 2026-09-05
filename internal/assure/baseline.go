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
	"sync"
	"time"

	gomutants "github.com/P4suta/go-mutants"
	"github.com/P4suta/goatest/internal/checkpoint"
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
	ArtifactDirectory string
	Contract          string
	CommandTimeout    time.Duration
	TargetTimeout     time.Duration
	// Jobs bounds target controls that run concurrently inside one package.
	// Zero preserves the direct API's historical serial execution.
	Jobs int
	// PackageSuites measures one whole-suite coverage control for every
	// package that owns a selected target. The control closes the gap between
	// isolated top-level targets and the exact fallback mutation execution.
	PackageSuites bool
	// SuiteEnvironment is the merged environment of every acquired resource.
	// A whole-package command may run targets with different requirements, so
	// it receives their union rather than one arbitrary target's overlay.
	SuiteEnvironment     []string
	Packages             []string
	BuildTags            []string
	TestArgs             []string
	UseTest2JSON         bool
	ClassifyUserFailures bool
	Resume               *checkpoint.Baseline
	Checkpoint           func(checkpoint.Baseline)
	RepositoryObserver   *RepositoryObserver
}

// BaselineResult is everything one baseline round established. Instrumented is
// the union of every coverage block the round compiled instrumentation for,
// whether or not a target ran it, which is what tells routing the difference
// between a position no test reached and a position no profile knows about.
type BaselineResult struct {
	Evidence     []report.Evidence
	Findings     []report.Finding
	Targets      []TargetEvidence
	Suites       map[string]PackageSuiteCoverage
	Instrumented []goanalysis.FileCoverage
	Inventory    []report.TargetDisposition
	Executed     int
	Skipped      int
}

// PackageSuiteCoverage is one passing execution of a package's complete test
// suite using the same coverage binary as its isolated target controls.
// Covered and Instrumented decide whether the fallback suite reached an exact
// mutant position; Duration calibrates the mutant execution only after the
// control passed, and WholeTree selects the matching evidence-key variant.
type PackageSuiteCoverage struct {
	Covered      []goanalysis.FileCoverage
	Instrumented []goanalysis.FileCoverage
	Duration     time.Duration
	WholeTree    bool
}

const (
	defaultBaselineTimeout     = 10 * time.Minute
	maximumSummaryRunes        = 512
	packageSuiteCoveragePrefix = "package-suite-coverage:"
)

func packageSuiteCoverageTarget(pkg string) string { return packageSuiteCoveragePrefix + pkg }

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
	if options.PackageSuites {
		result.Suites = make(map[string]PackageSuiteCoverage)
	}
	completed := make(map[string]checkpoint.BaselineTarget)
	buildVetComplete := false
	if options.Resume != nil {
		buildVetComplete = options.Resume.BuildVetComplete
		result.Evidence = append(result.Evidence, options.Resume.Evidence...)
		result.Findings = append(result.Findings, options.Resume.Findings...)
		for _, unit := range options.Resume.Targets {
			completed[unit.ID] = unit
			appendBaselineUnit(&result, unit, nil)
		}
	}
	checkpointNow := func(complete bool) {
		if options.Checkpoint == nil {
			return
		}
		units := make([]checkpoint.BaselineTarget, 0, len(completed))
		for _, unit := range completed {
			units = append(units, unit)
		}
		options.Checkpoint(checkpoint.Baseline{
			BuildVetComplete: buildVetComplete, Complete: complete,
			Evidence: baselineCheckEvidence(result.Evidence), Targets: units,
		})
	}
	patterns := slices.Clone(options.Packages)
	if len(patterns) == 0 {
		patterns = []string{"./..."}
	}
	checks := []struct {
		name string
		argv []string
	}{
		{name: "go vet", argv: baselineGoCommand("vet", options.BuildTags, patterns)},
		{name: "go build", argv: baselineGoCommand("build", options.BuildTags, patterns)},
	}
	if buildVetComplete {
		checks = nil
	}
	for _, check := range checks {
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
			for _, target := range targets {
				if _, done := completed[target.Target.ID]; done {
					continue
				}
				unit := baselineClassifiedUnit(target, "not-run", check.name+" failed", 0, false, true, nil, nil, nil)
				completed[unit.ID] = unit
				appendBaselineUnit(&result, unit, nil)
			}
			return result, nil
		}
		result.Evidence = append(result.Evidence, report.Evidence{Kind: "baseline", ID: check.name, Status: "passed"})
	}
	if !buildVetComplete {
		buildVetComplete = true
		checkpointNow(false)
	}

	packageTargets := make(map[string][]BaselineTarget)
	packageSuites := make(map[string]bool)
	for _, target := range targets {
		if options.PackageSuites {
			packageSuites[target.Target.Package] = true
		}
		if _, done := completed[target.Target.ID]; done {
			continue
		}
		packageTargets[target.Target.Package] = append(packageTargets[target.Target.Package], target)
	}
	packageByImport := make(map[string]goanalysis.Package, len(model.Packages))
	for _, pkg := range model.Packages {
		packageByImport[pkg.ImportPath] = pkg
	}
	imports := make([]string, 0, len(packageTargets)+len(packageSuites))
	for importPath := range packageTargets {
		imports = append(imports, importPath)
	}
	for importPath := range packageSuites {
		if _, already := packageTargets[importPath]; !already {
			imports = append(imports, importPath)
		}
	}
	slices.Sort(imports)
	suiteControls := make([]packageSuiteControl, 0, len(packageSuites))
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
			for _, target := range packageTargets[importPath] {
				unit := baselineClassifiedUnit(target, "not-run", "test binary did not compile", 0, false, true, nil, nil, nil)
				completed[unit.ID] = unit
				appendBaselineUnit(&result, unit, nil)
				checkpointNow(false)
			}
			continue
		}
		commit := func(run baselineTargetRun) {
			completed[run.unit.ID] = run.unit
			appendBaselineUnit(&result, run.unit, run.evidence)
			result.Instrumented = goanalysis.MergeFileCoverage(result.Instrumented, run.instrumented)
			checkpointNow(false)
		}
		if err := collectPackageBaselineTargets(
			ctx, workspace, model.ModulePath, importPath, pkg.RelativeDir, binary,
			packageTargets[importPath], targetTimeout, options, commit,
		); err != nil {
			return BaselineResult{}, err
		}
		if packageSuites[importPath] {
			suiteControls = append(suiteControls, packageSuiteControl{
				importPath: importPath, relativeDir: pkg.RelativeDir, binary: binary,
			})
		}
	}
	for _, measured := range collectPackageSuiteCoverages(
		ctx, workspace, model.ModulePath, suiteControls, targetTimeout, result.Targets, options,
	) {
		if measured.err != nil {
			return BaselineResult{}, measured.err
		}
		if !measured.measured {
			continue
		}
		result.Suites[measured.importPath] = measured.suite
		result.Instrumented = goanalysis.MergeFileCoverage(result.Instrumented, measured.suite.Instrumented)
	}
	checkpointNow(true)
	return result, nil
}

// baselineTargetRun is one independently measured target, held until the
// coordinator can commit it in target order. Workers write no shared result or
// checkpoint state; that separation is what makes parallel measurement yield
// the same report bytes as serial measurement.
type baselineTargetRun struct {
	unit         checkpoint.BaselineTarget
	evidence     *TargetEvidence
	instrumented []goanalysis.FileCoverage
	err          error
}

// packageSuiteControl is the immutable input to one whole-package coverage
// control. Controls are collected after every top-level target has finished so
// their relative deadlines use the complete package measurement, then run in
// parallel across packages. That is the same package-level concurrency the Go
// command itself permits; controls of one package are never duplicated.
type packageSuiteControl struct {
	importPath  string
	relativeDir string
	binary      string
}

type packageSuiteCoverageRun struct {
	importPath string
	suite      PackageSuiteCoverage
	measured   bool
	err        error
}

// collectPackageSuiteCoverages measures independent package controls with a
// bounded worker pool and returns them in import-path order. Workers publish
// only into their indexed slot, so map insertion, coverage merging, and the
// infrastructure error selected by the caller never depend on completion
// order.
func collectPackageSuiteCoverages(
	ctx context.Context,
	workspace CommandWorkspace,
	modulePath string,
	controls []packageSuiteControl,
	targetTimeout time.Duration,
	targets []TargetEvidence,
	options BaselineOptions,
) []packageSuiteCoverageRun {
	if len(controls) == 0 {
		return nil
	}
	runs := make([]packageSuiteCoverageRun, len(controls))
	jobs := min(max(options.Jobs, 1), len(controls))
	indexes := make(chan int, len(controls))
	var workers sync.WaitGroup
	workers.Add(jobs)
	for range jobs {
		go func() {
			defer workers.Done()
			for index := range indexes {
				control := controls[index]
				suite, measured, err := collectPackageSuiteCoverage(
					ctx, workspace, modulePath, control.importPath, control.relativeDir,
					control.binary, targetTimeout, targets, options,
				)
				runs[index] = packageSuiteCoverageRun{
					importPath: control.importPath, suite: suite, measured: measured, err: err,
				}
			}
		}()
	}
	for index := range controls {
		indexes <- index
	}
	close(indexes)
	workers.Wait()
	return runs
}

// collectPackageBaselineTargets measures targets with bounded concurrency and
// commits the longest completed prefix in input order. Consequently result
// order, checkpoint order and the error selected from simultaneous failures do
// not depend on which process the scheduler happened to finish first.
func collectPackageBaselineTargets(
	ctx context.Context,
	workspace CommandWorkspace,
	modulePath, importPath, relativeDir, binary string,
	targets []BaselineTarget,
	targetTimeout time.Duration,
	options BaselineOptions,
	commit func(baselineTargetRun),
) error {
	if len(targets) == 0 {
		return nil
	}
	jobs := options.Jobs
	if jobs <= 0 {
		jobs = 1
	}
	jobs = min(jobs, len(targets))
	if jobs == 1 {
		for _, target := range targets {
			run := executeBaselineTarget(ctx, workspace, modulePath, importPath, relativeDir, binary, target, targetTimeout, options)
			if run.err != nil {
				return run.err
			}
			commit(run)
		}
		return nil
	}

	type indexedRun struct {
		index int
		run   baselineTargetRun
	}
	work := make(chan int)
	finished := make(chan indexedRun, len(targets))
	var workers sync.WaitGroup
	for range jobs {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for index := range work {
				finished <- indexedRun{
					index: index,
					run: executeBaselineTarget(
						ctx, workspace, modulePath, importPath, relativeDir, binary,
						targets[index], targetTimeout, options,
					),
				}
			}
		}()
	}
	go func() {
		for index := range targets {
			work <- index
		}
		close(work)
		workers.Wait()
		close(finished)
	}()

	runs := make([]baselineTargetRun, len(targets))
	ready := make([]bool, len(targets))
	next := 0
	commitBlocked := false
	for measured := range finished {
		runs[measured.index] = measured.run
		ready[measured.index] = true
		for next < len(runs) && ready[next] {
			if runs[next].err != nil {
				commitBlocked = true
			} else if !commitBlocked {
				commit(runs[next])
			}
			next++
		}
	}
	for _, run := range runs {
		if run.err != nil {
			return run.err
		}
	}
	return nil
}

func executeBaselineTarget(
	ctx context.Context,
	workspace CommandWorkspace,
	modulePath, importPath, relativeDir, binary string,
	target BaselineTarget,
	targetTimeout time.Duration,
	options BaselineOptions,
) baselineTargetRun {
	profile := filepath.Join(options.ArtifactDirectory, target.Target.ID+".cover")
	command := targetCommand(binary, profile, relativeDir, target, targetTimeout)
	command.Argv = append(command.Argv, options.TestArgs...)
	observedCommand := command
	observedArguments, finishObservation := options.RepositoryObserver.instrumentPackage(importPath, observedCommand.Argv)
	observedCommand.Argv = observedArguments
	if options.UseTest2JSON {
		observedCommand = test2JSONCommand(importPath, observedCommand)
		command = test2JSONCommand(importPath, command)
	}
	first, err := workspace.Exec(ctx, observedCommand)
	observation := finishObservation()
	if err != nil {
		return baselineTargetRun{err: fmt.Errorf("goatest: baseline target %s: %w", target.Target.Name, err)}
	}
	if repositoryTestLogFailure(string(first.Output), observedCommand.Argv) {
		first, err = workspace.Exec(ctx, command)
		observation = repositoryObservation{unknown: true}
		if err != nil {
			return baselineTargetRun{err: fmt.Errorf("goatest: repeat baseline target %s without repository observation: %w", target.Target.Name, err)}
		}
	}
	skipped, skipKind, skipSummary, eventErr := classifyTest2JSON(target.Target.Name, first.Output)
	if eventErr != nil && options.UseTest2JSON {
		return baselineTargetRun{err: fmt.Errorf("goatest: decode test2json for %s: %w", target.Target.Name, eventErr)}
	}
	if skipped {
		evidenceItem := report.Evidence{
			Kind: "target", ID: target.Target.ID, Status: "skipped", Detail: target.Target.Name,
		}
		finding := targetFinding(target.Target, skipKind, skipSummary)
		return baselineTargetRun{unit: baselineClassifiedUnit(
			target, "skipped", skipSummary, first.Duration, false, true, nil,
			[]report.Evidence{evidenceItem}, []report.Finding{finding},
		)}
	}
	if first.TimedOut || first.ExitCode != 0 {
		attempts := []gomutants.CommandResult{first}
		for range 2 {
			retry, retryErr := workspace.Exec(ctx, command)
			if retryErr != nil {
				return baselineTargetRun{err: fmt.Errorf("goatest: repeat baseline target %s: %w", target.Target.Name, retryErr)}
			}
			attempts = append(attempts, retry)
		}
		kind, summary := classifyTargetFailure(attempts)
		finding := targetFinding(target.Target, kind, summary)
		return baselineTargetRun{unit: baselineClassifiedUnit(
			target, "failed", summary, first.Duration, true, false, nil, nil, []report.Finding{finding},
		)}
	}
	profileData, err := os.ReadFile(profile)
	if err != nil {
		return baselineTargetRun{err: fmt.Errorf("goatest: read coverage for %s: %w", target.Target.Name, err)}
	}
	coverage, err := goanalysis.ParseCoverage(profileData, modulePath)
	if err != nil {
		return baselineTargetRun{err: fmt.Errorf("goatest: coverage for %s: %w", target.Target.Name, err)}
	}
	targetEvidence := TargetEvidence{
		Target: target.Target, CoveredFiles: goanalysis.CoveredPaths(coverage.Covered), Covered: coverage.Covered,
		Environment: slices.Clone(target.Environment), Duration: first.Duration,
		WholeTree: options.RepositoryObserver.wholeTree(target.Target, observation), RepositoryObserved: options.RepositoryObserver.observes(importPath),
	}
	evidenceItem := report.Evidence{
		Kind: "target", ID: target.Target.ID, Status: "passed", Detail: target.Target.Name,
	}
	unit := baselineClassifiedUnit(target, "passed", "", first.Duration, true, false, &targetEvidence, []report.Evidence{evidenceItem}, nil)
	return baselineTargetRun{unit: unit, evidence: &targetEvidence, instrumented: coverage.Instrumented}
}

// collectPackageSuiteCoverage runs the exact package-suite shape used when no
// isolated target reaches a mutant. It is an optional proof control: a failed
// or timed-out suite yields no coverage fact and therefore narrows nothing;
// an infrastructure error still stops the run. The deadline is derived from
// the sum of the package's passing isolated controls, which includes repeated
// process and TestMain overhead and is consequently a conservative first
// comparison for the one-process suite.
func collectPackageSuiteCoverage(
	ctx context.Context,
	workspace CommandWorkspace,
	modulePath, importPath, relativeDir, binary string,
	targetTimeout time.Duration,
	targets []TargetEvidence,
	options BaselineOptions,
) (PackageSuiteCoverage, bool, error) {
	profile := filepath.Join(options.ArtifactDirectory, binaryName(importPath)+".suite.cover")
	timeout := controlRelativeMutationTimeout(
		options.Contract, targetTimeout, packageSuiteControlDuration(targets, importPath),
	)
	command := gomutants.Command{
		Argv: []string{binary, "-test.coverprofile=" + profile, "-test.count=1"},
		Dir:  relativeDir, Env: slices.Clone(options.SuiteEnvironment), Timeout: timeout,
	}
	command.Argv = append(command.Argv, options.TestArgs...)
	observed := command
	observedArguments, finishObservation := options.RepositoryObserver.instrumentPackage(importPath, observed.Argv)
	observed.Argv = observedArguments
	if options.UseTest2JSON {
		observed = test2JSONCommand(importPath, observed)
		command = test2JSONCommand(importPath, command)
	}
	run, err := workspace.Exec(ctx, observed)
	observation := finishObservation()
	if err != nil {
		return PackageSuiteCoverage{}, false, fmt.Errorf("goatest: baseline package suite %s: %w", importPath, err)
	}
	if repositoryTestLogFailure(string(run.Output), observed.Argv) {
		run, err = workspace.Exec(ctx, command)
		observation = repositoryObservation{unknown: true}
		if err != nil {
			return PackageSuiteCoverage{}, false, fmt.Errorf("goatest: repeat baseline package suite %s without repository observation: %w", importPath, err)
		}
	}
	if run.TimedOut || run.ExitCode != 0 {
		return PackageSuiteCoverage{}, false, nil
	}
	profileData, err := os.ReadFile(profile)
	if err != nil {
		return PackageSuiteCoverage{}, false, fmt.Errorf("goatest: read package-suite coverage for %s: %w", importPath, err)
	}
	coverage, err := goanalysis.ParseCoverage(profileData, modulePath)
	if err != nil {
		return PackageSuiteCoverage{}, false, fmt.Errorf("goatest: package-suite coverage for %s: %w", importPath, err)
	}
	return PackageSuiteCoverage{
		Covered: coverage.Covered, Instrumented: coverage.Instrumented,
		Duration:  run.Duration,
		WholeTree: options.RepositoryObserver.wholeTreeSuite(importPath, observation),
	}, true, nil
}

func baselineCheckEvidence(items []report.Evidence) []report.Evidence {
	result := make([]report.Evidence, 0, 2)
	for _, item := range items {
		if item.Kind == "baseline" {
			result = append(result, item)
		}
	}
	return result
}

func baselineClassifiedUnit(target BaselineTarget, status, detail string, duration time.Duration, executed, skipped bool, evidence *TargetEvidence, evidenceItems []report.Evidence, findings []report.Finding) checkpoint.BaselineTarget {
	if status == "not-run" && len(evidenceItems) == 0 {
		evidenceItems = []report.Evidence{{Kind: "target", ID: target.Target.ID, Status: status, Detail: detail}}
	}
	unit := checkpoint.BaselineTarget{
		ID: target.Target.ID, Executed: executed, Skipped: skipped,
		Evidence: slices.Clone(evidenceItems), Findings: slices.Clone(findings),
		Inventory: report.TargetDisposition{
			ID: target.Target.ID, Name: target.Target.Name, Kind: string(target.Target.Kind), Package: target.Target.Package,
			Path: target.Target.Path, Line: max(target.Target.Line, 0), Status: status,
			DurationMS: max(duration.Milliseconds(), 0), Detail: detail,
		},
	}
	if evidence != nil {
		unit.Target = checkpointTargetEvidence(*evidence)
	}
	return unit
}

// appendBaselineUnit adds one completed target to the round. A unit measured
// in this round hands over the evidence it measured, blocks included; a unit
// that comes back from a checkpoint has only the checkpoint form, which
// carries the files a target reached but not the blocks inside them.
func appendBaselineUnit(result *BaselineResult, unit checkpoint.BaselineTarget, measured *TargetEvidence) {
	result.Evidence = append(result.Evidence, unit.Evidence...)
	result.Findings = append(result.Findings, unit.Findings...)
	result.Inventory = append(result.Inventory, unit.Inventory)
	if unit.Executed {
		result.Executed++
	}
	if unit.Skipped {
		result.Skipped++
	}
	switch {
	case measured != nil:
		result.Targets = append(result.Targets, *measured)
	case unit.Target != nil:
		result.Targets = append(result.Targets, restoreTargetEvidence(*unit.Target))
	}
}

func checkpointTargetEvidence(input TargetEvidence) *checkpoint.TargetEvidence {
	return &checkpoint.TargetEvidence{
		Target: checkpoint.Target{
			ID: input.Target.ID, Name: input.Target.Name, Kind: string(input.Target.Kind), Package: input.Target.Package,
			RelativeDir: input.Target.RelativeDir, Path: input.Target.Path, Line: input.Target.Line,
			Capability: input.Target.Capability, Capabilities: slices.Clone(input.Target.Capabilities), Dependencies: slices.Clone(input.Target.Dependencies),
		},
		CoveredFiles: slices.Clone(input.CoveredFiles), Environment: slices.Clone(input.Environment), DurationNS: int64(input.Duration),
		WholeTree: input.WholeTree, RepositoryObserved: input.RepositoryObserved,
	}
}

func restoreTargetEvidence(input checkpoint.TargetEvidence) TargetEvidence {
	return TargetEvidence{
		Target: goanalysis.Target{
			ID: input.Target.ID, Name: input.Target.Name, Kind: goanalysis.TargetKind(input.Target.Kind), Package: input.Target.Package,
			RelativeDir: input.Target.RelativeDir, Path: input.Target.Path, Line: input.Target.Line,
			Capability: input.Target.Capability, Capabilities: slices.Clone(input.Target.Capabilities), Dependencies: slices.Clone(input.Target.Dependencies),
		},
		CoveredFiles: slices.Clone(input.CoveredFiles), Environment: slices.Clone(input.Environment), Duration: time.Duration(input.DurationNS),
		WholeTree: input.WholeTree, RepositoryObserved: input.RepositoryObserved,
	}
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
