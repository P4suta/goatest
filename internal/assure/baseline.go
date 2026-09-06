// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package assure

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	gomutants "github.com/P4suta/go-mutants"
	"github.com/P4suta/goatest/internal/checkpoint"
	"github.com/P4suta/goatest/internal/filemode"
	goanalysis "github.com/P4suta/goatest/internal/golang"
	"github.com/P4suta/goatest/internal/report"
	"github.com/P4suta/goatest/internal/trace"
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

	Jobs int

	PackageSuites bool

	SuiteEnvironment     []string
	Packages             []string
	BuildTags            []string
	TestArgs             []string
	UseTestFraming       bool
	ClassifyUserFailures bool
	Resume               *checkpoint.Baseline
	Checkpoint           func(checkpoint.Baseline)
	RepositoryObserver   *RepositoryObserver
	ProbeSession         MutationSession
	Progress             func(completed, total int)
	Trace                *trace.Recorder
	StopAfterChecks      bool
	probeIndices         map[uint32]bool
	probeIdentities      map[uint32]string
	coveragePackages     []goanalysis.Package
}

type BaselineResult struct {
	Evidence     []report.Evidence
	Findings     []report.Finding
	Targets      []TargetEvidence
	Suites       map[string]PackageSuiteCoverage
	ProbeSuites  map[string]PackageProbeEvidence
	Instrumented []goanalysis.FileCoverage
	Inventory    []report.TargetDisposition
	Executed     int
	Skipped      int
}

type PackageSuiteCoverage struct {
	Covered      []goanalysis.FileCoverage
	Instrumented []goanalysis.FileCoverage
	Duration     time.Duration
	WholeTree    bool
}

const (
	defaultBaselineTimeout = 10 * time.Minute
	maximumSummaryRunes    = 512
)

func packageSuiteCoverageTarget(pkg string) string { return trace.PackageSuiteCoveragePrefix + pkg }

func CollectBaseline(ctx context.Context, workspace CommandWorkspace, model goanalysis.Model, targets []BaselineTarget, options BaselineOptions) (BaselineResult, error) {
	if workspace == nil {
		return BaselineResult{}, fmt.Errorf("goatest: nil baseline workspace")
	}
	if options.ArtifactDirectory == "" {
		return BaselineResult{}, fmt.Errorf("goatest: baseline requires an artifact directory")
	}
	if options.ProbeSession != nil {
		catalog := options.ProbeSession.Catalog()
		options.probeIndices = make(map[uint32]bool, len(catalog.Mutants))
		options.probeIdentities = probeMutantIdentities(catalog)
		for _, mutant := range catalog.Mutants {
			if mutant.Probed {
				options.probeIndices[mutant.Index] = true
			}
		}
	}
	options.coveragePackages = slices.Clone(model.Packages)
	if err := os.MkdirAll(options.ArtifactDirectory, filemode.ReadableDirectory); err != nil {
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
	var structuralFindings []report.Finding
	if options.PackageSuites {
		result.Suites = make(map[string]PackageSuiteCoverage)
		if options.ProbeSession != nil {
			result.ProbeSuites = make(map[string]PackageProbeEvidence)
		}
	}
	completed := make(map[string]checkpoint.BaselineTarget)
	completedSuites := make(map[string]checkpoint.BaselineSuite)
	measuredTargets := make(map[string]*TargetEvidence)
	checkpointInstrumentation := make(map[string]bool)
	buildVetComplete := false
	resumeRouting := false
	if options.Resume != nil {
		buildVetComplete = options.Resume.BuildVetComplete
		result.Evidence = append(result.Evidence, options.Resume.Evidence...)
		result.Findings = append(result.Findings, options.Resume.Findings...)
		structuralFindings = append(structuralFindings, options.Resume.Findings...)
		for _, unit := range options.Resume.Targets {
			if unit.Target != nil {
				resumed := restoreTargetEvidence(*unit.Target)
				resumed = restrictTargetEvidenceToPackages(resumed, options.coveragePackages)
				unit.Target = checkpointTargetEvidence(resumed)
				result.Instrumented = goanalysis.MergeFileCoverage(result.Instrumented, resumed.Instrumented)
				if unit.Target.Instrumented != nil {
					checkpointInstrumentation[unit.Target.Target.Package] = true
				}
			}
			completed[unit.ID] = unit
		}
		if options.PackageSuites {
			for _, unit := range options.Resume.Suites {
				completedSuites[unit.Package] = unit
				if unit.Measured {
					suite := restoreCheckpointBaselineSuite(unit)
					result.Suites[unit.Package] = suite
					result.Instrumented = goanalysis.MergeFileCoverage(result.Instrumented, suite.Instrumented)
				}
			}
		}
		if options.Resume.Complete && options.Resume.Routing != nil {
			resumeRouting = true
			result.Instrumented, result.Suites = restoreBaselineRouting(*options.Resume.Routing, options.PackageSuites)
		}
	}
	publishProgress := func() {
		if options.Progress != nil {
			options.Progress(len(completed), len(targets))
		}
	}
	publishProgress()
	checkpointNow := func(complete bool) {
		if options.Checkpoint == nil {
			return
		}
		units := make([]checkpoint.BaselineTarget, 0, len(completed))
		for _, id := range slices.Sorted(maps.Keys(completed)) {
			unit := completed[id]
			if complete && unit.Target != nil && unit.Target.Instrumented != nil {
				target := *unit.Target
				target.Instrumented = nil
				unit.Target = &target
			}
			units = append(units, unit)
		}
		state := checkpoint.Baseline{
			BuildVetComplete: buildVetComplete, Complete: complete,
			Evidence: baselineCheckEvidence(result.Evidence), Findings: slices.Clone(structuralFindings), Targets: units,
		}
		if complete {
			state.Routing = checkpointBaselineRouting(result.Instrumented, result.Suites)
		} else {
			state.Suites = make([]checkpoint.BaselineSuite, 0, len(completedSuites))
			for _, importPath := range slices.Sorted(maps.Keys(completedSuites)) {
				state.Suites = append(state.Suites, completedSuites[importPath])
			}
		}
		options.Checkpoint(state)
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
		{name: "go build", argv: baselineBuildCommand(options.BuildTags, patterns)},
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
			finding := report.Finding{
				ID: report.FindingID("baseline", kind), Kind: kind,
				Summary: check.name + " rejected the project: " + summarize(run.Output),
			}
			result.Findings = append(result.Findings, finding)
			structuralFindings = append(structuralFindings, finding)
			for _, target := range targets {
				if _, done := completed[target.Target.ID]; done {
					continue
				}
				unit := baselineClassifiedUnit(target, "not-run", check.name+" failed", 0, false, true, nil, nil, nil)
				completed[unit.ID] = unit
			}
			publishProgress()
			appendCompletedBaselineTargets(&result, targets, completed, nil)
			return result, nil
		}
		result.Evidence = append(result.Evidence, report.Evidence{Kind: "baseline", ID: check.name, Status: "passed"})
	}
	if !buildVetComplete {
		buildVetComplete = true
		checkpointNow(false)
	}
	if options.StopAfterChecks {
		return result, nil
	}

	packageTargets := make(map[string][]BaselineTarget)
	packageSuites := make(map[string]bool)
	for _, target := range targets {
		if options.PackageSuites && !resumeRouting {
			if _, done := completedSuites[target.Target.Package]; !done {
				packageSuites[target.Target.Package] = true
			}
		}
		if _, done := completed[target.Target.ID]; done {
			continue
		}
		packageTargets[target.Target.Package] = append(packageTargets[target.Target.Package], target)
	}
	if options.PackageSuites && options.ProbeSession != nil && !resumeRouting {
		for _, mutant := range options.ProbeSession.Catalog().Mutants {
			if !mutant.Accepted || mutant.Package == "" {
				continue
			}
			if _, done := completedSuites[mutant.Package]; !done {
				packageSuites[mutant.Package] = true
			}
		}
	}
	packageByImport := make(map[string]goanalysis.Package, len(model.Packages))
	for _, pkg := range model.Packages {
		packageByImport[pkg.ImportPath] = pkg
	}
	var imports []string
	for importPath := range packageTargets {
		imports = append(imports, importPath)
	}
	for importPath := range packageSuites {
		if _, already := packageTargets[importPath]; !already {
			imports = append(imports, importPath)
		}
	}
	slices.Sort(imports)
	packageControls := make([]packageBaselineControl, 0, len(imports))
	for _, importPath := range imports {
		pkg, ok := packageByImport[importPath]
		if !ok {
			return BaselineResult{}, fmt.Errorf("goatest: target package %s was absent from go list", importPath)
		}
		binary := ""
		if options.ProbeSession == nil {
			binary = filepath.Join(options.ArtifactDirectory, binaryName(importPath))
			compile := gomutants.Command{
				Argv:    baselineCompileCommand(baselineCoveragePackages(model.ModulePath, pkg), importPath, binary, options.BuildTags),
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
				finding := report.Finding{
					ID: report.FindingID("test-binary-build", importPath), Kind: "test-binary-build-failure",
					Summary: "the package test binary did not compile: " + summarize(compiled.Output),
				}
				result.Findings = append(result.Findings, finding)
				structuralFindings = append(structuralFindings, finding)
				for _, target := range packageTargets[importPath] {
					unit := baselineClassifiedUnit(target, "not-run", "test binary did not compile", 0, false, true, nil, nil, nil)
					completed[unit.ID] = unit
				}
				if packageSuites[importPath] {
					completedSuites[importPath] = checkpoint.BaselineSuite{Package: importPath}
				}
				publishProgress()
				checkpointNow(false)
				continue
			}
		}
		packageControls = append(packageControls, packageBaselineControl{
			importPath: importPath, relativeDir: pkg.RelativeDir, binary: binary,
			targets: packageTargets[importPath], suite: packageSuites[importPath],
		})
	}
	suiteControls := make([]packageSuiteControl, 0, len(packageSuites))
	for _, control := range packageControls {
		instrumentationAnchor := ""
		if len(control.targets) != 0 {
			instrumentationAnchor = control.targets[0].Target.ID
		}
		commit := func(run baselineTargetRun) {
			if run.evidence != nil {
				measured := *run.evidence
				measured.Instrumented = nil
				measuredTargets[run.unit.ID] = &measured
			}
			if run.unit.Target != nil {
				if checkpointInstrumentation[control.importPath] || run.unit.ID != instrumentationAnchor {
					run.unit.Target.Instrumented = nil
				}
			}
			completed[run.unit.ID] = run.unit
			result.Instrumented = goanalysis.MergeFileCoverage(result.Instrumented, run.instrumented)
			publishProgress()
		}
		collectErr := collectPackageBaselineTargets(
			ctx, workspace, model.ModulePath, control.importPath, control.relativeDir, control.binary,
			control.targets, targetTimeout, options, commit,
		)
		if len(control.targets) != 0 {
			checkpointNow(false)
		}
		if collectErr != nil {
			return BaselineResult{}, collectErr
		}
	}
	neededSuites := slices.Sorted(maps.Keys(packageSuites))
	if options.ProbeSession != nil {
		neededSuites = neededBaselineSuitePackages(
			options.ProbeSession.Catalog(), completedTargetEvidence(targets, completed), result.Instrumented,
		)
	}
	for _, control := range packageControls {
		if slices.Contains(neededSuites, control.importPath) {
			suiteControls = append(suiteControls, packageSuiteControl{
				importPath: control.importPath, relativeDir: control.relativeDir, binary: control.binary,
			})
		}
	}
	commitSuite := func(measured packageSuiteCoverageRun) {
		if measured.err != nil {
			return
		}
		completedSuites[measured.importPath] = checkpointBaselineSuite(measured)
		if measured.measured {
			result.Suites[measured.importPath] = measured.suite
			result.Instrumented = goanalysis.MergeFileCoverage(result.Instrumented, measured.suite.Instrumented)
		}
		if measured.probe != nil {
			result.ProbeSuites[measured.importPath] = *measured.probe
		}
		checkpointNow(false)
	}
	for _, measured := range collectPackageSuiteCoverages(
		ctx, workspace, model.ModulePath, suiteControls, targetTimeout,
		completedTargetEvidence(targets, completed), options, commitSuite,
	) {
		if measured.err != nil {
			return BaselineResult{}, measured.err
		}
	}
	appendCompletedBaselineTargets(&result, targets, completed, measuredTargets)
	checkpointNow(true)
	return result, nil
}

type baselineTargetRun struct {
	unit         checkpoint.BaselineTarget
	evidence     *TargetEvidence
	instrumented []goanalysis.FileCoverage
	err          error
}

type packageBaselineControl struct {
	importPath  string
	relativeDir string
	binary      string
	targets     []BaselineTarget
	suite       bool
}

type packageSuiteControl struct {
	importPath  string
	relativeDir string
	binary      string
}

type packageSuiteCoverageRun struct {
	importPath string
	suite      PackageSuiteCoverage
	probe      *PackageProbeEvidence
	measured   bool
	err        error
}

func collectPackageSuiteCoverages(
	ctx context.Context,
	workspace CommandWorkspace,
	modulePath string,
	controls []packageSuiteControl,
	targetTimeout time.Duration,
	targets []TargetEvidence,
	options BaselineOptions,
	commit func(packageSuiteCoverageRun),
) []packageSuiteCoverageRun {
	if len(controls) == 0 {
		return nil
	}
	runs := make([]packageSuiteCoverageRun, len(controls))
	jobs := baselineJobLimitFor(options.Jobs, len(controls), min(runtime.GOMAXPROCS(0), defaultMutationJobLimit))
	indexes := make(chan int, len(controls))
	finished := make(chan int, len(controls))
	var workers sync.WaitGroup
	workers.Add(jobs)
	for range jobs {
		go func() {
			defer workers.Done()
			for index := range indexes {
				control := controls[index]
				if options.ProbeSession != nil {
					runs[index] = collectPreparedPackageSuiteCoverage(
						ctx, options.ProbeSession, modulePath, control.importPath,
						targetTimeout, targets, options,
					)
					finished <- index
					continue
				}
				suite, measured, err := collectPackageSuiteCoverage(
					ctx, workspace, modulePath, control.importPath, control.relativeDir,
					control.binary, targetTimeout, targets, options,
				)
				runs[index] = packageSuiteCoverageRun{
					importPath: control.importPath, suite: suite, measured: measured, err: err,
				}
				finished <- index
			}
		}()
	}
	for index := range controls {
		indexes <- index
	}
	close(indexes)
	go func() {
		workers.Wait()
		close(finished)
	}()
	for index := range finished {
		if commit != nil {
			commit(runs[index])
		}
	}
	return runs
}

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
	jobs := baselineJobLimitFor(options.Jobs, len(targets), min(runtime.GOMAXPROCS(0), defaultMutationJobLimit))
	type indexedRun struct {
		index int
		run   baselineTargetRun
	}
	work := make(chan int, len(targets))
	finished := make(chan indexedRun, len(targets))
	var workers sync.WaitGroup
	for range jobs {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for index := range work {
				if ctx.Err() != nil {
					return
				}
				var run baselineTargetRun
				if options.ProbeSession != nil {
					run = executePreparedBaselineTarget(
						ctx, options.ProbeSession, modulePath, importPath,
						targets[index], targetTimeout, options,
					)
				} else {
					run = executeBaselineTarget(
						ctx, workspace, modulePath, importPath, relativeDir, binary,
						targets[index], targetTimeout, options,
					)
				}
				finished <- indexedRun{index: index, run: run}
			}
		}()
	}
	for index := range targets {
		work <- index
	}
	close(work)
	go func() {
		workers.Wait()
		close(finished)
	}()

	runs := make([]baselineTargetRun, len(targets))
	for measured := range finished {
		runs[measured.index] = measured.run
		if measured.run.err == nil {
			commit(measured.run)
		}
	}
	for _, run := range runs {
		if run.err != nil {
			return run.err
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}

func neededBaselineSuitePackages(catalog gomutants.Catalog, targets []TargetEvidence, instrumented []goanalysis.FileCoverage) []string {
	needed := make(map[string]bool)
	for _, mutant := range catalog.Mutants {
		if !mutant.Accepted || mutant.Package == "" {
			continue
		}
		route := routeMutant(mutant, targets, instrumented)
		if len(route.reaching) == 0 && len(route.discharged) == 0 {
			needed[mutant.Package] = true
		}
	}
	return slices.Sorted(maps.Keys(needed))
}

func baselineJobLimitFor(requested, work, automatic int) int {
	if requested <= 0 {
		requested = automatic
	}
	return min(max(requested, 1), max(work, 1))
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
	if options.UseTestFraming {
		observedCommand = testFramedCommand(observedCommand)
		command = testFramedCommand(command)
	}
	first, err := workspace.Exec(ctx, observedCommand)
	observation := finishObservation()
	if err != nil {
		return baselineTargetRun{err: fmt.Errorf("goatest: baseline target %s: %w", target.Target.Name, err)}
	}
	if repositoryTestLogFailure(string(first.Output), observedCommand.Argv) {
		return baselineTargetRun{err: fmt.Errorf("goatest: repository observation for baseline target %s failed", target.Target.Name)}
	}
	skipped, skipKind, skipSummary, framingErr := classifyTestFraming(target.Target.Name, first.Output)
	if framingErr != nil && options.UseTestFraming {
		return baselineTargetRun{err: fmt.Errorf("goatest: classify framed test output for %s: %w", target.Target.Name, framingErr)}
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
		kind, summary := classifyTargetFailure(first)
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
	coverage = restrictBaselineCoverage(coverage, options.coveragePackages)
	targetEvidence := TargetEvidence{
		Target: target.Target, CoveredFiles: goanalysis.CoveredPaths(coverage.Covered), Covered: coverage.Covered,
		Instrumented: coverage.Instrumented,
		Environment:  slices.Clone(target.Environment), Duration: first.Duration,
		WholeTree: options.RepositoryObserver.wholeTree(target.Target, observation),
	}
	evidenceItem := report.Evidence{
		Kind: "target", ID: target.Target.ID, Status: "passed", Detail: target.Target.Name,
	}
	unit := baselineClassifiedUnit(target, "passed", "", first.Duration, true, false, &targetEvidence, []report.Evidence{evidenceItem}, nil)
	return baselineTargetRun{unit: unit, evidence: &targetEvidence, instrumented: coverage.Instrumented}
}

func executePreparedBaselineTarget(
	ctx context.Context,
	session MutationSession,
	modulePath, importPath string,
	target BaselineTarget,
	targetTimeout time.Duration,
	options BaselineOptions,
) baselineTargetRun {
	profile := filepath.Join(options.ArtifactDirectory, target.Target.ID+".cover")
	request := preparedBaselineTargetRequest(importPath, profile, target, targetTimeout, options)
	observed := request
	observedArgs, finishObservation := options.RepositoryObserver.instrumentPackage(importPath, observed.Args)
	observed.Args = observedArgs
	result, err := session.Probe(ctx, observed)
	observation := finishObservation()
	if err != nil {
		record := probeRequestRecord(target.Target.ID, false, request)
		record.Error = err.Error()
		options.Trace.ProbeExec(record)
		return baselineTargetRun{err: fmt.Errorf("goatest: baseline target %s: %w", target.Target.Name, err)}
	}
	if repositoryTestLogFailure(string(result.Output), observed.Args) {
		err = fmt.Errorf("goatest: repository observation for baseline target %s failed", target.Target.Name)
		record := probeRequestRecord(target.Target.ID, false, request)
		record.Error = err.Error()
		options.Trace.ProbeExec(record)
		return baselineTargetRun{err: err}
	}
	record, _, _ := probeResultRecord(target.Target.ID, false, request, result, options.probeIdentities)
	options.Trace.ProbeExec(record)
	first := commandResultFromProbe(result)
	skipped, skipKind, skipSummary, framingErr := classifyTestFraming(target.Target.Name, first.Output)
	if framingErr != nil && options.UseTestFraming {
		return baselineTargetRun{err: fmt.Errorf("goatest: classify framed test output for %s: %w", target.Target.Name, framingErr)}
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
	if result.Outcome != gomutants.ProbeMeasured {
		kind, summary := classifyTargetFailure(first)
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
	coverage = restrictBaselineCoverage(coverage, options.coveragePackages)
	probed, infected := validatedBaselineProbe(result, options.probeIndices)
	targetEvidence := TargetEvidence{
		Target: target.Target, CoveredFiles: goanalysis.CoveredPaths(coverage.Covered), Covered: coverage.Covered,
		Instrumented: coverage.Instrumented,
		Environment:  slices.Clone(target.Environment), Duration: result.Duration,
		WholeTree: options.RepositoryObserver.wholeTree(target.Target, observation),
		Probed:    probed, Infected: infected,
	}
	evidenceItem := report.Evidence{
		Kind: "target", ID: target.Target.ID, Status: "passed", Detail: target.Target.Name,
	}
	unit := baselineClassifiedUnit(target, "passed", "", result.Duration, true, false, &targetEvidence, []report.Evidence{evidenceItem}, nil)
	return baselineTargetRun{unit: unit, evidence: &targetEvidence, instrumented: coverage.Instrumented}
}

func preparedBaselineTargetRequest(importPath, profile string, target BaselineTarget, timeout time.Duration, options BaselineOptions) gomutants.ProbeRequest {
	args := []string{
		"-test.run=^" + regexp.QuoteMeta(target.Target.Name) + "$",
		"-test.coverprofile=" + profile,
		"-test.count=1",
	}
	args = append(args, options.TestArgs...)
	if options.UseTestFraming {
		args = append([]string{"-test.v=test2json"}, args...)
	}
	return gomutants.ProbeRequest{
		Package: importPath, Args: args, Env: slices.Clone(target.Environment), Timeout: timeout,
	}
}

func commandResultFromProbe(result gomutants.ProbeResult) gomutants.CommandResult {
	return gomutants.CommandResult{
		ExitCode: result.ExitCode, TimedOut: result.Outcome == gomutants.ProbeTimedOut,
		Duration: result.Duration, Output: slices.Clone(result.Output),
	}
}

func validatedBaselineProbe(result gomutants.ProbeResult, indices map[uint32]bool) (bool, []uint32) {
	if result.Outcome != gomutants.ProbeMeasured {
		return false, nil
	}
	infected := slices.Clone(result.Infected)
	slices.Sort(infected)
	infected = slices.Compact(infected)
	for _, index := range infected {
		if !indices[index] {
			return false, nil
		}
	}
	return true, infected
}

func collectPreparedPackageSuiteCoverage(
	ctx context.Context,
	session MutationSession,
	modulePath, importPath string,
	targetTimeout time.Duration,
	targets []TargetEvidence,
	options BaselineOptions,
) packageSuiteCoverageRun {
	profile := filepath.Join(options.ArtifactDirectory, binaryName(importPath)+".suite.cover")
	timeout := controlExecutionTimeout(
		targetTimeout, packageSuiteControlDuration(targets, importPath),
	)
	request := gomutants.ProbeRequest{
		Package: importPath,
		Args: append([]string{
			"-test.coverprofile=" + profile,
			"-test.count=1",
		}, options.TestArgs...),
		Env: slices.Clone(options.SuiteEnvironment), Timeout: timeout,
	}
	observed := request
	observedArgs, finishObservation := options.RepositoryObserver.instrumentPackage(importPath, observed.Args)
	observed.Args = observedArgs
	result, err := session.Probe(ctx, observed)
	observation := finishObservation()
	if err != nil {
		record := probeRequestRecord(packageSuiteProbeTarget(importPath), true, request)
		record.Error = err.Error()
		options.Trace.ProbeExec(record)
		return packageSuiteCoverageRun{importPath: importPath, err: fmt.Errorf("goatest: baseline package suite %s: %w", importPath, err)}
	}
	if repositoryTestLogFailure(string(result.Output), observed.Args) {
		err = fmt.Errorf("goatest: repository observation for baseline package suite %s failed", importPath)
		record := probeRequestRecord(packageSuiteProbeTarget(importPath), true, request)
		record.Error = err.Error()
		options.Trace.ProbeExec(record)
		return packageSuiteCoverageRun{importPath: importPath, err: err}
	}
	wholeTree := options.RepositoryObserver.wholeTreeSuiteReason(importPath, observation)
	record, _, _ := probeResultRecord(packageSuiteProbeTarget(importPath), true, request, result, options.probeIdentities)
	record.WholeTreeReason = string(wholeTree)
	record.WholeTree = wholeTree != wholeTreeObserved
	options.Trace.ProbeExec(record)
	if result.Outcome != gomutants.ProbeMeasured {
		return packageSuiteCoverageRun{importPath: importPath}
	}
	profileData, err := os.ReadFile(profile)
	if err != nil {
		return packageSuiteCoverageRun{importPath: importPath, err: fmt.Errorf("goatest: read package-suite coverage for %s: %w", importPath, err)}
	}
	coverage, err := goanalysis.ParseCoverage(profileData, modulePath)
	if err != nil {
		return packageSuiteCoverageRun{importPath: importPath, err: fmt.Errorf("goatest: package-suite coverage for %s: %w", importPath, err)}
	}
	coverage = restrictBaselineCoverage(coverage, options.coveragePackages)
	probed, infected := validatedBaselineProbe(result, options.probeIndices)
	run := packageSuiteCoverageRun{
		importPath: importPath,
		suite: PackageSuiteCoverage{
			Covered: coverage.Covered, Instrumented: coverage.Instrumented,
			Duration: result.Duration, WholeTree: record.WholeTree,
		},
		measured: true,
	}
	if probed {
		run.probe = &PackageProbeEvidence{
			Measured: true, Infected: infected, WholeTree: record.WholeTree,
		}
	}
	return run
}

func collectPackageSuiteCoverage(
	ctx context.Context,
	workspace CommandWorkspace,
	modulePath, importPath, relativeDir, binary string,
	targetTimeout time.Duration,
	targets []TargetEvidence,
	options BaselineOptions,
) (PackageSuiteCoverage, bool, error) {
	profile := filepath.Join(options.ArtifactDirectory, binaryName(importPath)+".suite.cover")
	timeout := controlExecutionTimeout(
		targetTimeout, packageSuiteControlDuration(targets, importPath),
	)
	command := gomutants.Command{
		Argv: []string{binary, "-test.coverprofile=" + profile, "-test.count=1"},
		Dir:  relativeDir, Env: slices.Clone(options.SuiteEnvironment), Timeout: timeout,
	}
	command.Argv = append(command.Argv, options.TestArgs...)
	observed := command
	observedArguments, finishObservation := options.RepositoryObserver.instrumentPackage(importPath, observed.Argv)
	observed.Argv = observedArguments
	run, err := workspace.Exec(ctx, observed)
	observation := finishObservation()
	if err != nil {
		return PackageSuiteCoverage{}, false, fmt.Errorf("goatest: baseline package suite %s: %w", importPath, err)
	}
	if repositoryTestLogFailure(string(run.Output), observed.Argv) {
		return PackageSuiteCoverage{}, false, fmt.Errorf("goatest: repository observation for baseline package suite %s failed", importPath)
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
	coverage = restrictBaselineCoverage(coverage, options.coveragePackages)
	return PackageSuiteCoverage{
		Covered: coverage.Covered, Instrumented: coverage.Instrumented,
		Duration:  run.Duration,
		WholeTree: options.RepositoryObserver.wholeTreeSuite(importPath, observation),
	}, true, nil
}

func baselineCheckEvidence(items []report.Evidence) []report.Evidence {
	var result []report.Evidence
	for _, item := range items {
		if item.Kind == "baseline" {
			result = append(result, item)
		}
	}
	return result
}

func restrictBaselineCoverage(coverage goanalysis.Coverage, packages []goanalysis.Package) goanalysis.Coverage {
	if packages == nil {
		return coverage
	}
	return goanalysis.RestrictCoverageToPackages(coverage, packages)
}

func restrictTargetEvidenceToPackages(target TargetEvidence, packages []goanalysis.Package) TargetEvidence {
	coverage := restrictBaselineCoverage(goanalysis.Coverage{
		Covered: target.Covered, Instrumented: target.Instrumented,
	}, packages)
	target.Covered = coverage.Covered
	target.Instrumented = coverage.Instrumented
	target.CoveredFiles = goanalysis.CoveredPaths(coverage.Covered)
	return target
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
		target := *measured
		target.Instrumented = nil
		result.Targets = append(result.Targets, target)
	case unit.Target != nil:
		target := restoreTargetEvidence(*unit.Target)
		target.Instrumented = nil
		result.Targets = append(result.Targets, target)
	}
}

func appendCompletedBaselineTargets(
	result *BaselineResult,
	targets []BaselineTarget,
	completed map[string]checkpoint.BaselineTarget,
	measured map[string]*TargetEvidence,
) {
	for _, target := range targets {
		if unit, ok := completed[target.Target.ID]; ok {
			appendBaselineUnit(result, unit, measured[target.Target.ID])
		}
	}
}

func completedTargetEvidence(targets []BaselineTarget, completed map[string]checkpoint.BaselineTarget) []TargetEvidence {
	result := make([]TargetEvidence, 0, len(completed))
	for _, target := range targets {
		unit, ok := completed[target.Target.ID]
		if !ok || unit.Target == nil {
			continue
		}
		result = append(result, restoreTargetEvidence(*unit.Target))
	}
	return result
}

func checkpointTargetEvidence(input TargetEvidence) *checkpoint.TargetEvidence {
	covered := checkpointCoverageValue(input.Covered)
	instrumented := checkpointCoverageValue(input.Instrumented)
	return &checkpoint.TargetEvidence{
		Target: checkpoint.Target{
			ID: input.Target.ID, Name: input.Target.Name, Kind: string(input.Target.Kind), Package: input.Target.Package,
			RelativeDir: input.Target.RelativeDir, Path: input.Target.Path, Line: input.Target.Line,
			Capabilities: slices.Clone(input.Target.Capabilities), Dependencies: slices.Clone(input.Target.Dependencies),
		},
		CoveredFiles: slices.Clone(input.CoveredFiles), Environment: slices.Clone(input.Environment), DurationNS: int64(input.Duration),
		WholeTree: input.WholeTree,
		Coverage:  &covered, Probed: input.Probed, ProbeDurationNS: int64(input.ProbeDuration),
		Infected: slices.Clone(input.Infected), Instrumented: &instrumented,
	}
}

func restoreTargetEvidence(input checkpoint.TargetEvidence) TargetEvidence {
	return TargetEvidence{
		Target: goanalysis.Target{
			ID: input.Target.ID, Name: input.Target.Name, Kind: goanalysis.TargetKind(input.Target.Kind), Package: input.Target.Package,
			RelativeDir: input.Target.RelativeDir, Path: input.Target.Path, Line: input.Target.Line,
			Capabilities: slices.Clone(input.Target.Capabilities), Dependencies: slices.Clone(input.Target.Dependencies),
		},
		CoveredFiles: slices.Clone(input.CoveredFiles), Covered: restoreCheckpointCoverage(input.Coverage),
		Instrumented: restoreCheckpointCoverage(input.Instrumented),
		Environment:  slices.Clone(input.Environment), Duration: time.Duration(input.DurationNS),
		WholeTree: input.WholeTree, Probed: input.Probed,
		ProbeDuration: time.Duration(input.ProbeDurationNS), Infected: slices.Clone(input.Infected),
	}
}

func checkpointBaselineSuite(run packageSuiteCoverageRun) checkpoint.BaselineSuite {
	unit := checkpoint.BaselineSuite{Package: run.importPath, Measured: run.measured}
	if !run.measured {
		return unit
	}
	unit.Covered = checkpointCoverage(run.suite.Covered)
	unit.Instrumented = checkpointCoverage(run.suite.Instrumented)
	if unit.Covered == nil {
		unit.Covered = &checkpoint.Coverage{Files: []checkpoint.FileCoverage{}}
	}
	if unit.Instrumented == nil {
		unit.Instrumented = &checkpoint.Coverage{Files: []checkpoint.FileCoverage{}}
	}
	unit.DurationNS = int64(run.suite.Duration)
	unit.WholeTree = run.suite.WholeTree
	return unit
}

func restoreCheckpointBaselineSuite(input checkpoint.BaselineSuite) PackageSuiteCoverage {
	return PackageSuiteCoverage{
		Covered: restoreCheckpointCoverage(input.Covered), Instrumented: restoreCheckpointCoverage(input.Instrumented),
		Duration: time.Duration(input.DurationNS), WholeTree: input.WholeTree,
	}
}

func checkpointCoverage(input []goanalysis.FileCoverage) *checkpoint.Coverage {
	if input == nil {
		return nil
	}
	result := &checkpoint.Coverage{Files: make([]checkpoint.FileCoverage, len(input))}
	for fileIndex, file := range input {
		blocks := make([]checkpoint.CoverageBlock, len(file.Blocks))
		for blockIndex, block := range file.Blocks {
			blocks[blockIndex] = checkpoint.CoverageBlock{
				StartLine: block.StartLine, StartColumn: block.StartColumn,
				EndLine: block.EndLine, EndColumn: block.EndColumn,
			}
		}
		result.Files[fileIndex] = checkpoint.FileCoverage{Path: file.Path, Blocks: blocks}
	}
	return result
}

func restoreCheckpointCoverage(input *checkpoint.Coverage) []goanalysis.FileCoverage {
	if input == nil {
		return nil
	}
	files := make([]goanalysis.FileCoverage, len(input.Files))
	for fileIndex, file := range input.Files {
		blocks := make([]goanalysis.CoverageBlock, len(file.Blocks))
		for blockIndex, block := range file.Blocks {
			blocks[blockIndex] = goanalysis.CoverageBlock{
				StartLine: block.StartLine, StartColumn: block.StartColumn,
				EndLine: block.EndLine, EndColumn: block.EndColumn,
			}
		}
		files[fileIndex] = goanalysis.FileCoverage{Path: file.Path, Blocks: blocks}
	}

	return goanalysis.MergeFileCoverage(nil, files)
}

func checkpointBaselineRouting(instrumented []goanalysis.FileCoverage, suites map[string]PackageSuiteCoverage) *checkpoint.BaselineRouting {
	routing := &checkpoint.BaselineRouting{Instrumented: checkpointCoverageValue(instrumented)}
	packages := make([]string, 0, len(suites))
	for pkg := range suites {
		packages = append(packages, pkg)
	}
	slices.Sort(packages)
	routing.Suites = make([]checkpoint.SuiteCoverage, 0, len(packages))
	for _, pkg := range packages {
		suite := suites[pkg]
		routing.Suites = append(routing.Suites, checkpoint.SuiteCoverage{
			Package: pkg, Covered: checkpointCoverageValue(suite.Covered),
			Instrumented: checkpointCoverageValue(suite.Instrumented),
			DurationNS:   int64(suite.Duration), WholeTree: suite.WholeTree,
		})
	}
	return routing
}

func checkpointCoverageValue(input []goanalysis.FileCoverage) checkpoint.Coverage {
	if coverage := checkpointCoverage(input); coverage != nil {
		return *coverage
	}
	return checkpoint.Coverage{Files: []checkpoint.FileCoverage{}}
}

func restoreBaselineRouting(input checkpoint.BaselineRouting, packageSuites bool) ([]goanalysis.FileCoverage, map[string]PackageSuiteCoverage) {
	instrumented := restoreCheckpointCoverage(&input.Instrumented)
	var suites map[string]PackageSuiteCoverage
	if packageSuites {
		suites = make(map[string]PackageSuiteCoverage, len(input.Suites))
		for _, saved := range input.Suites {
			suites[saved.Package] = PackageSuiteCoverage{
				Covered:      restoreCheckpointCoverage(&saved.Covered),
				Instrumented: restoreCheckpointCoverage(&saved.Instrumented),
				Duration:     time.Duration(saved.DurationNS), WholeTree: saved.WholeTree,
			}
		}
	}
	return instrumented, suites
}

func baselineGoCommand(operation string, tags, packages []string) []string {
	argv := []string{"go", operation}
	if len(tags) != 0 {
		argv = append(argv, "-tags="+strings.Join(tags, ","))
	}
	return append(argv, packages...)
}

func baselineBuildCommand(tags, packages []string) []string {
	argv := baselineGoCommand("build", tags, nil)
	argv = append(argv, "-o", os.DevNull)
	return append(argv, packages...)
}

func baselineCoveragePackages(modulePath string, pkg goanalysis.Package) []string {
	packages := []string{pkg.ImportPath}
	for _, dependency := range pkg.Dependencies {
		if dependency == modulePath || strings.HasPrefix(dependency, modulePath+"/") {
			packages = append(packages, dependency)
		}
	}
	slices.Sort(packages)
	return slices.Compact(packages)
}

func baselineCompileCommand(coveragePackages []string, importPath, binary string, tags []string) []string {
	argv := baselineGoCommand("test", tags, nil)
	argv = append(argv, "-c")
	argv = append(argv, "-coverpkg="+strings.Join(coveragePackages, ","), "-o", binary, importPath)
	return argv
}

func testFramedCommand(target gomutants.Command) gomutants.Command {
	if len(target.Argv) == 0 {
		return target
	}
	arguments := []string{target.Argv[0], "-test.v=test2json"}
	arguments = append(arguments, target.Argv[1:]...)
	target.Argv = arguments
	return target
}

const (
	testFramingMarker            = byte(0x16)
	testSkipReportPrefix         = "--- SKIP: "
	commandOutputTruncatedPrefix = "[go-mutants] output truncated"
)

func classifyTestFraming(target string, output []byte) (bool, string, string, error) {
	truncated := bytes.HasPrefix(output, []byte(commandOutputTruncatedPrefix))
	for remaining := output; ; {
		marker := bytes.IndexByte(remaining, testFramingMarker)
		if marker < 0 {
			break
		}
		framed := remaining[marker+1:]
		end := len(framed)
		delimiter := byte(0)
		if newline := bytes.IndexByte(framed, '\n'); newline != -1 {
			end, delimiter = newline, '\n'
		}
		if next := bytes.IndexByte(framed, testFramingMarker); next != -1 {
			end = min(end, next)
			if end == next {
				delimiter = testFramingMarker
			}
		}
		line := bytes.TrimSuffix(framed[:end], []byte{'\r'})
		for bytes.HasPrefix(line, []byte("    ")) {
			line = line[4:]
		}
		if bytes.HasPrefix(line, []byte(testSkipReportPrefix)) {
			name := trimTestDuration(strings.TrimSpace(string(line[len(testSkipReportPrefix):])))
			switch {
			case name == target:
				return true, "skipped-target", "the selected top-level target called Skip", nil
			case strings.HasPrefix(name, target+"/"):
				return true, "skipped-subtest", "a selected subtest was skipped: " + name, nil
			}
		}
		if delimiter == 0 {
			break
		}
		remaining = framed[end:]
		if delimiter == '\n' {
			remaining = remaining[1:]
		}
	}
	if truncated {
		return false, "", "", errors.New("captured output was truncated before skip classification completed")
	}
	return false, "", "", nil
}

func trimTestDuration(name string) string {
	prefix, suffix, found := strings.Cut(name, " (")
	if !found {
		return name
	}
	seconds, found := strings.CutSuffix(suffix, "s)")
	if !found {
		return name
	}
	if _, err := strconv.ParseFloat(seconds, 64); err != nil {
		return name
	}
	return prefix
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

func classifyTargetFailure(attempt gomutants.CommandResult) (string, string) {
	if attempt.TimedOut {
		return "baseline-timeout", "baseline target exceeded its execution budget"
	}
	return "baseline-failure", "baseline target failed: " + summarize(attempt.Output)
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
