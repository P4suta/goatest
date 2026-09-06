// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package assure

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"slices"

	"github.com/P4suta/goatest/internal/config"
	goanalysis "github.com/P4suta/goatest/internal/golang"
	"github.com/P4suta/goatest/internal/mutationbridge"
	"github.com/P4suta/goatest/internal/report"
	"github.com/P4suta/goatest/internal/tempowner"
	"github.com/P4suta/goatest/internal/testargs"
)

func Plan(ctx context.Context, options Options) (result report.Report, resultErr error) {
	goMutants, err := goMutantsIdentity()
	if err != nil {
		return report.Report{}, err
	}
	return planWithGoMutantsVersion(ctx, options, goMutants)
}

func planWithGoMutantsVersion(ctx context.Context, options Options, goMutants string) (result report.Report, resultErr error) {
	root, err := repositoryRoot(options.Root)
	if err != nil {
		return report.Report{}, err
	}
	loaded, err := config.Load(root)
	if err != nil {
		return report.Report{}, err
	}
	applyExecutionDefaults(&options, loaded)
	contract := options.Contract
	if contract == "" {
		contract = loaded.Contract
	}
	if contract != "standard-v1" && contract != "deep-v1" {
		return report.Report{}, fmt.Errorf("goatest: contract %q is unknown", contract)
	}
	normalizedTestArgs, err := testargs.Normalize(options.TestArgs)
	if err != nil {
		return report.Report{}, err
	}
	options.TestArgs = normalizedTestArgs

	options.KeepTemp = false

	sweepRunTemporaries(options, tempowner.Sweep, planMoment(options))
	scratch, err := openRunScratch(os.MkdirTemp, os.RemoveAll, options.TempDirectory, root, planMoment(options))
	if err != nil {
		emit(options, "temp-unavailable", err.Error())
		return report.Report{}, err
	}
	buildCache, err := openRunBuildCache(
		options.BuildCacheProgram, options.BuildCacheDir, options.BuildCacheNativeSource, scratch, loaded.Cache.BuildMaxBytes)
	if err != nil {
		emit(options, "build-cache-unavailable", err.Error())
	}
	defer func() {
		collectRunBuildCache(options, loaded, buildCache, planMoment(options))
		resultErr = errors.Join(resultErr, releaseBuildCache(options, buildCache, scratch, planMoment(options)))
		releaseRunScratch(options, os.RemoveAll, scratch, planMoment(options))
	}()
	workspace, err := mutationbridge.Open(ctx, root, mutationbridge.Options{
		GoBinary: options.GoBinary, TempDirectory: scratch.dir,
		ReportDirectory: internalOutputDirectory, SnapshotExclude: assuranceSnapshotExclusions(),
		Environment: overlayEnvironment(mutationEnvironment(options.Environment, options.BuildTags), buildCache.persistingEnvironment()),
		KeepTemp:    options.KeepTemp,
	})
	if err != nil {
		return report.Report{}, err
	}
	reportMutationSweep(options, workspace.Swept())
	defer func() { resultErr = errors.Join(resultErr, workspace.Close()) }()
	commands := withBuildCache(workspace, buildCache)
	metadata, err := inspectWorkspace(ctx, commands, workspace.ToolchainVersion(), options.Packages, options.BuildTags, options.CommandTimeout)
	if err != nil {
		return report.Report{}, err
	}
	targets, err := goanalysis.DiscoverTargets(root, metadata.model.Packages)
	if err != nil {
		return report.Report{}, err
	}
	targets = includedProjectTargets(targets, loaded.Project.Exclude)
	selection := selectImpact(ctx, root, metadata.model, targets, options)
	targets = selection.targets
	evidenceItems := make([]report.Evidence, 0, len(targets)+1)
	for _, target := range targets {
		evidenceItems = append(evidenceItems, report.Evidence{
			Kind: "plan-target", ID: target.ID, Status: "selected",
			Detail: fmt.Sprintf("%s %s/%s", target.Kind, target.Package, target.Name),
		})
	}
	resources, err := plannedResources(targets, loaded)
	if err != nil {
		return report.Report{}, err
	}
	for _, name := range resources {
		evidenceItems = append(evidenceItems, report.Evidence{Kind: "plan-resource", ID: name, Status: "required"})
	}

	selectedMutants := 0
	compileRejected := 0
	if !options.Changed || selection.broad || len(selection.changed) != 0 {
		include, packages := mutationScope(selection)
		if !defaultPackagePatterns(options.Packages) && !options.Changed {
			packages = slices.Clone(options.Packages)
		}
		discoveryPackages := mutationDiscoveryPackages(include, packages)
		session, prepareErr := workspace.Prepare(ctx, mutationbridge.PrepareOptions{
			Contract: contract, Operators: slices.Clone(options.MutationOperators), Include: include, Exclude: slices.Clone(loaded.Project.Exclude),
			DiscoveryPackages: discoveryPackages, Packages: packages, Jobs: mutationJobLimit(options, loaded),
			BuildTimeout: options.CommandTimeout, MutantTimeout: options.CommandTimeout, SkipVerify: true,
		})
		if prepareErr != nil {
			return report.Report{}, prepareErr
		}
		catalog := session.Catalog()
		if err := validateMutationCatalog(catalog); err != nil {
			return report.Report{}, err
		}
		rejected := make(map[string]string, len(catalog.Rejections))
		for _, rejection := range catalog.Rejections {
			rejected[rejection.ID] = rejection.Diagnostic
		}
		for _, mutant := range catalog.Mutants {
			status := "excluded"
			detail := fmt.Sprintf("%s:%d %s: %s -> %s", mutant.Path, mutant.Line, mutant.Rule, mutant.Original, mutant.Replacement)
			diagnostic, wasRejected := rejected[mutant.ID]
			switch {
			case mutant.Accepted:
				status = "selected"
				selectedMutants++
			case wasRejected:
				status = "compile-rejected"
				compileRejected++
				if diagnostic != "" {
					detail = diagnostic
				}
			}
			evidenceItems = append(evidenceItems, report.Evidence{Kind: "plan-mutant", ID: mutant.ID, Status: status, Detail: detail})
		}
	}
	jobs := mutationJobLimit(options, loaded)
	waves := 0
	if selectedMutants != 0 {
		waves = (selectedMutants + jobs - 1) / jobs
	}
	evidenceItems = append(evidenceItems, report.Evidence{
		Kind: "plan", ID: "summary", Status: "completed",
		Detail: fmt.Sprintf("targets=%d mutants=%d compile-rejected=%d resources=%d jobs=%d estimated-mutation-waves=%d", len(targets), selectedMutants, compileRejected, len(resources), jobs, waves),
	})
	return report.Report{
		Schema: report.SchemaV1, RunKind: report.RunOperation, Verdict: report.VerdictCompleted, Contract: contract,
		Scope:       reportScope(options, metadata.model, selection),
		Repository:  report.Repository{Module: metadata.model.ModulePath, Packages: modelPackagePaths(metadata.model)},
		Execution:   reportExecution(options, jobs),
		Toolchain:   report.Toolchain{Go: metadata.toolchain, Goatest: ResolvedGoatestVersion(), GoMutants: goMutants, OS: runtime.GOOS, Arch: runtime.GOARCH},
		Evidence:    evidenceItems,
		Limitations: append(projectExcludeLimitations(loaded.Project.Exclude), report.Limitation{Code: "plan-cost-estimate", Summary: "mutation waves exclude target-specific runtime, resource startup, and race", Estimated: true}),
	}, nil
}

func applyExecutionDefaults(options *Options, loaded config.Config) {
	if len(options.Packages) == 0 {
		options.Packages = slices.Clone(loaded.Project.Packages)
	}
	if options.ExecutionPinned {
		return
	}
	if len(options.TestArgs) == 0 {
		options.TestArgs = slices.Clone(loaded.Execution.TestBinaryArgs)
	}
	if len(options.BuildTags) == 0 {
		options.BuildTags = slices.Clone(loaded.Execution.BuildTags)
	}
	if options.CommandTimeout <= 0 {
		options.CommandTimeout = loaded.Execution.Timeout
	}
	if options.TargetTimeout <= 0 {
		options.TargetTimeout = loaded.Execution.Timeout
	}
	if options.MutationJobs <= 0 {
		options.MutationJobs = loaded.Execution.Jobs
	}
}

func plannedResources(targets []goanalysis.Target, loaded config.Config) ([]string, error) {
	set := make(map[string]bool)
	for _, target := range targets {
		capabilities := slices.Clone(target.Capabilities)
		for _, capability := range capabilities {
			if _, configured := loaded.Resources[capability]; !configured {
				return nil, fmt.Errorf("goatest: target %s requires unconfigured resource %q", target.Name, capability)
			}
			set[capability] = true
		}
	}
	result := make([]string, 0, len(set))
	for capability := range set {
		result = append(result, capability)
	}
	slices.Sort(result)
	return result, nil
}
