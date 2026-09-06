// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package assure

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"runtime/debug"

	gomutants "github.com/P4suta/go-mutants"
	"github.com/P4suta/goatest/internal/checkpoint"
	"github.com/P4suta/goatest/internal/config"
	envselect "github.com/P4suta/goatest/internal/environment"
	"github.com/P4suta/goatest/internal/evidence"
	goanalysis "github.com/P4suta/goatest/internal/golang"
	"github.com/P4suta/goatest/internal/mutationbridge"

	"github.com/P4suta/goatest/internal/provider"
	"github.com/P4suta/goatest/internal/repair"
	"github.com/P4suta/goatest/internal/report"
	"github.com/P4suta/goatest/internal/resource"
	"github.com/P4suta/goatest/internal/tempowner"
	"github.com/P4suta/goatest/internal/testargs"
	"github.com/P4suta/goatest/internal/trace"
)

const (
	maximumRounds              = 3
	commandOutputLimit         = 32 << 20
	workspaceInspectionTimeout = 5 * time.Minute
	defaultMutationJobLimit    = 4
	progressDivisions          = 100
	laterPhasesNotRunCode      = "later-phases-not-run"
)

const goMutantsModulePath = "github.com/P4suta/go-mutants"

func GoMutantsVersion() (string, error) {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "", errors.New("goatest: build info is unavailable; the go-mutants version cannot be audited")
	}
	return goMutantsVersionFrom(info)
}

func goMutantsIdentity() (string, error) {
	info, _ := debug.ReadBuildInfo()
	return goMutantsIdentityFrom(info, goatestBuildIdentity)
}

func goMutantsIdentityFrom(info *debug.BuildInfo, executableIdentity func() (string, error)) (string, error) {
	version, versionErr := goMutantsVersionFrom(info)
	if versionErr == nil {
		return version, nil
	}
	identity, identityErr := executableIdentity()
	if identityErr != nil {
		return "", errors.Join(versionErr, identityErr)
	}
	if identity == "" {
		return "", errors.Join(versionErr, errors.New("goatest: running executable identity is empty"))
	}
	return "executable-sha256:" + identity, nil
}

func goMutantsVersionFrom(info *debug.BuildInfo) (string, error) {
	if info == nil {
		return "", errors.New("goatest: build info is unavailable; the go-mutants version cannot be audited")
	}
	var found *debug.Module
	for _, dependency := range info.Deps {
		if dependency == nil || dependency.Path != goMutantsModulePath {
			continue
		}
		if found != nil {
			return "", fmt.Errorf("goatest: %s appears more than once in build info", goMutantsModulePath)
		}
		found = dependency
	}
	if found != nil {
		dependency := found
		module := dependency
		if dependency.Replace != nil {
			module = dependency.Replace
		}
		if module.Version == "" || module.Version == "(devel)" {
			return "", fmt.Errorf("goatest: %s carries no auditable version in build info", goMutantsModulePath)
		}
		return module.Version, nil
	}
	return "", fmt.Errorf("goatest: %s is absent from build info", goMutantsModulePath)
}

const goatestDevelVersion = "v0.1.0-dev"

var GoatestVersion = goatestDevelVersion

func ResolvedGoatestVersion() string {
	info, _ := debug.ReadBuildInfo()
	return resolvedGoatestVersionFrom(GoatestVersion, info)
}

func resolvedGoatestVersionFrom(stamped string, info *debug.BuildInfo) string {
	if stamped != goatestDevelVersion {
		return stamped
	}
	if info == nil || info.Main.Version == "" || info.Main.Version == "(devel)" {
		return stamped
	}
	return info.Main.Version
}

var (
	absoluteRepositoryPath = filepath.Abs
	statRepositoryPath     = os.Stat
)

type Event struct {
	Kind   string
	Detail string
}

type Options struct {
	Root                   string
	Contract               string
	NoApply                bool
	Changed                bool
	ChangedRef             string
	Packages               []string
	PackageScope           bool
	TestArgs               []string
	BuildTags              []string
	CommandTimeout         time.Duration
	TargetTimeout          time.Duration
	GoBinary               string
	Environment            []string
	TempDirectory          string
	MutationOperators      []string
	ReplayFindingID        string
	ReplayMutantID         string
	MutationJobs           int
	ExecutionPinned        bool
	Generate               func(context.Context, provider.Request) (provider.Response, error)
	Validator              repair.Validator
	AllowedGenerationPaths []string
	Progress               func(Event)

	Trace *trace.Recorder

	KeepTemp bool

	BuildCacheProgram string

	BuildCacheDir          string
	BuildCacheNativeSource string
	Now                    func() time.Time
}

type roundMetadata struct {
	model        goanalysis.Model
	toolchain    string
	dependencies map[string]string
}

type runCache interface {
	Get(string) (report.Report, bool, error)
	Put(string, report.Report) error
	GetCheckpoint(string) (checkpoint.State, bool, error)
	PutCheckpoint(string, checkpoint.State) error
	DeleteCheckpoint(string) error
}

type runRoundCloser interface {
	Close() error
}

type mutationPreparationResult struct {
	session MutationSession
	catalog gomutants.Catalog
	err     error
}

type runResourceManager interface {
	runRoundCloser
	AcquireEnvironment(context.Context, string) ([]string, error)
}

type runDependencies struct {
	repositoryRoot         func(string) (string, error)
	loadConfig             func(string) (config.Config, error)
	newCache               func(string, config.Cache) runCache
	openWorkspace          func(context.Context, string, mutationbridge.Options) (*mutationbridge.Workspace, error)
	closeWorkspace         func(*mutationbridge.Workspace) error
	inspectWorkspace       func(context.Context, CommandWorkspace, string, []string, []string, time.Duration) (roundMetadata, error)
	assuranceInputs        func(string, string, Options, config.Config, roundMetadata) (evidence.Inputs, string, error)
	discoverTargets        func(string, []goanalysis.Package) ([]goanalysis.Target, error)
	selectImpact           func(context.Context, string, goanalysis.Model, []goanalysis.Target, Options) impactSelection
	acquireResources       func(context.Context, config.Config, []goanalysis.Target, []string) (runRoundCloser, []BaselineTarget, []report.Evidence, []string, error)
	makeRunScratch         func(string, string) (string, error)
	removeRunScratch       func(string) error
	sweepTemporary         func(string, []string, time.Time) (tempowner.Result, error)
	makeBaselineScratch    func(string, string) (string, error)
	removeBaselineScratch  func(string) error
	collectBaseline        func(context.Context, CommandWorkspace, goanalysis.Model, []BaselineTarget, BaselineOptions) (BaselineResult, error)
	concurrencyPackages    func(string, []goanalysis.Package) ([]string, error)
	relevantRacePackages   func(goanalysis.Model, []string, []TargetEvidence) []string
	collectRaceWithOptions func(context.Context, CommandWorkspace, goanalysis.Model, []string, string, RaceOptions) (RaceResult, error)
	prepareSession         func(context.Context, *mutationbridge.Workspace, mutationbridge.PrepareOptions) (MutationSession, error)
	probeTargets           func(context.Context, MutationSession, []TargetEvidence, ProbeOptions) (ProbeEvaluation, error)
	evaluateMutations      func(context.Context, MutationSession, []TargetEvidence, MutationOptions) (MutationEvaluation, error)
	attemptRepairs         func(context.Context, string, []report.Finding, GenerationOptions) (GenerationEvaluation, error)
	buildGraph             func(string, goanalysis.Model, []TargetEvidence) (evidence.Graph, error)
	mergeGraph             func(evidence.Graph, *evidence.GraphRecord, impactSelection) evidence.Graph
	saveGraph              func(string, evidence.GraphRecord) error
	loadMutationEvidence   func(path, modulePath string) (evidence.MutationStore, bool, error)
	saveMutationEvidence   func(path string, store evidence.MutationStore) error
}

func Run(ctx context.Context, options Options) (report.Report, error) {
	return runWithDependencies(ctx, options, productionRunDependencies())
}

func runWithDependencies(ctx context.Context, options Options, dependencies runDependencies) (report.Report, error) {
	root, err := dependencies.repositoryRoot(options.Root)
	if err != nil {
		return report.Report{}, err
	}
	loaded, err := dependencies.loadConfig(root)
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
	mutationJobs := mutationJobLimit(options, loaded)
	now := time.Now
	if options.Now != nil {
		now = options.Now
	}
	accepted := activeAcceptance(loaded, now())
	acceptances := activeAcceptanceMetadata(loaded, now())
	cacheStore := dependencies.newCache(filepath.Join(root, ".goatest", "cache"), loaded.Cache)

	sweepRunTemporaries(options, dependencies.sweepTemporary, now())

	scratch, err := openRunScratch(
		dependencies.makeRunScratch, dependencies.removeRunScratch, options.TempDirectory, root, now())
	if err != nil {
		emit(options, "temp-unavailable", err.Error())
		return report.Report{}, err
	}

	closeWorkspace := func(workspace *mutationbridge.Workspace) error {
		err := dependencies.closeWorkspace(workspace)
		recordTemporaryArtifacts(options, artifactMutationWorkspace, workspace.Preserved())
		return err
	}

	buildCache, err := openRunBuildCache(
		options.BuildCacheProgram, options.BuildCacheDir, options.BuildCacheNativeSource, scratch, loaded.Cache.BuildMaxBytes)
	if err != nil {
		emit(options, "build-cache-unavailable", err.Error())
	}
	defer func() {
		if detail := buildCache.summarize(); detail != "" {
			emit(options, "build-cache-summary", detail)
		}
		collectRunBuildCache(options, loaded, buildCache, now())
		if closeErr := releaseBuildCache(options, buildCache, scratch, now()); closeErr != nil {
			emit(options, "build-cache-unavailable", closeErr.Error())
		}

		releaseRunScratch(options, dependencies.removeRunScratch, scratch, now())
	}()
	var appliedRepairs []report.Repair
	phases := runPhases{recorder: options.Trace}
	defer phases.leave()

	for round := 0; ; round++ {
		phases.enter(phaseSnapshot)
		emit(options, "snapshot", fmt.Sprintf("repair round %d", round+1))
		preparationEnvironment := buildCache.preparationEnvironment()
		workspace, err := dependencies.openWorkspace(ctx, root, mutationbridge.Options{
			GoBinary: options.GoBinary, TempDirectory: scratch.dir,
			ReportDirectory: internalOutputDirectory, SnapshotExclude: assuranceSnapshotExclusions(),

			Environment: overlayEnvironment(
				mutationEnvironment(options.Environment, options.BuildTags), preparationEnvironment),
			Trace: options.Trace, KeepTemp: options.KeepTemp,
		})
		if err != nil {
			return report.Report{}, err
		}
		reportMutationSweep(options, workspace.Swept())

		commands := withBuildCache(workspace, buildCache)
		metadata, err := dependencies.inspectWorkspace(ctx, commands, workspace.ToolchainVersion(), options.Packages, options.BuildTags, options.CommandTimeout)
		if err != nil {
			_ = closeWorkspace(workspace)
			return report.Report{}, err
		}
		inputs, digest, err := dependencies.assuranceInputs(root, contract, options, loaded, metadata)
		if err != nil {
			_ = closeWorkspace(workspace)
			return report.Report{}, err
		}
		phases.enter(phaseCacheCheck)
		if round == 0 && len(loaded.Resources) == 0 {
			cached, found, cacheErr := cacheStore.Get(digest)
			if cacheErr != nil {
				_ = closeWorkspace(workspace)
				return report.Report{}, cacheErr
			}
			if found && cachedReportReusable(cached, accepted) {
				emit(options, "cache-hit", digest)
				if closeErr := closeWorkspace(workspace); closeErr != nil {
					return report.Report{}, closeErr
				}
				return cached, nil
			}
		}

		phases.enter(phaseDiscover)
		targets, err := dependencies.discoverTargets(root, metadata.model.Packages)
		if err != nil {
			_ = closeWorkspace(workspace)
			return report.Report{}, err
		}
		allTargets := slices.Clone(targets)
		targets = includedProjectTargets(targets, loaded.Project.Exclude)

		phases.enter(phaseImpact)
		selection := dependencies.selectImpact(ctx, root, metadata.model, targets, options)
		if options.Changed {
			if selection.broad {
				emit(options, "impact-broad", "dependency or prior evidence was unknown")
			} else {
				emit(options, "impact-targeted", fmt.Sprintf("%d of %d targets", len(selection.targets), len(targets)))
			}
		}
		targets = selection.targets
		if options.Changed && !selection.broad && len(selection.changed) == 0 {
			result := report.Report{
				Schema: report.SchemaV1, Verdict: report.VerdictChangeAssured, Contract: contract, Snapshot: digest,
				Scope:      reportScope(options, metadata.model, selection),
				Repository: report.Repository{Module: metadata.model.ModulePath, Packages: modelPackagePaths(metadata.model)},
				Execution:  reportExecution(options, mutationJobs),
				Toolchain:  report.Toolchain{Go: metadata.toolchain, Goatest: inputs.GoatestVersion, GoMutants: inputs.GoMutantsVersion, OS: runtime.GOOS, Arch: runtime.GOARCH},
				Accounting: report.Accounting{
					Targets: report.CountAccounting{Discovered: len(allTargets), Excluded: len(allTargets)},
					Race:    report.CountAccounting{Discovered: len(metadata.model.Packages), Excluded: len(metadata.model.Packages)},
				},
				Evidence:    []report.Evidence{{Kind: "changeset", ID: "changed-files", Status: "empty"}},
				Acceptances: slices.Clone(acceptances),
				Limitations: projectExcludeLimitations(loaded.Project.Exclude),
			}
			if closeErr := closeWorkspace(workspace); closeErr != nil {
				return report.Report{}, closeErr
			}
			if err := cacheStore.Put(digest, result); err != nil {
				return report.Report{}, err
			}
			return result, nil
		}
		checkpointController := openRunCheckpoint(cacheStore, digest, options, round == 0 && len(loaded.Resources) == 0)
		baselineResume := checkpointController.baseline(targets)
		if baselineResume != nil {
			emit(options, "resume-baseline", fmt.Sprintf("%d targets", len(baselineResume.Targets)))
		}
		phases.enter(phaseResources)
		manager, baselineTargets, resourceEvidence, resourceEnv, err := dependencies.acquireResources(ctx, loaded, targets, options.Environment)
		if err != nil {
			_ = closeWorkspace(workspace)
			return report.Report{}, err
		}
		var mutationSources targetKeySources
		var repositoryObserver *RepositoryObserver
		if mutationEvidenceGuarded(round, loaded, options) {
			candidates, readers := repositoryObservationScope(root, metadata.model.Packages)
			mutationSources = newTargetKeySources(inputs, metadata.model, contract, options, readers)
			if len(candidates) != 0 {
				observationParent, observationPrefix, observationErr := scratch.subdirectory(repositoryObservationName)
				var observationDirectory string
				if observationErr == nil {
					observationDirectory, observationErr = os.MkdirTemp(observationParent, observationPrefix)
				}
				if observationErr != nil {
					emit(options, "repository-observation-unavailable", observationErr.Error())
				} else {
					defer func() { _ = os.RemoveAll(observationDirectory) }()
				}
				repositoryObserver = newRepositoryObserver(metadata.model.ModuleDir, observationDirectory, candidates, mutationSources)
			}
		}

		var controlMutex sync.RWMutex
		var controlOpenOnce sync.Once
		var controlWorkspace *mutationbridge.Workspace
		var controlOpenErr error
		var controlClosed bool
		openControl := func(controlContext context.Context) (*mutationbridge.Workspace, error) {
			if controlClosed {
				return nil, errors.New("goatest: original-control workspace is closed")
			}
			controlOpenOnce.Do(func() {
				opened, openErr := dependencies.openWorkspace(controlContext, root, mutationbridge.Options{
					GoBinary: options.GoBinary, TempDirectory: scratch.dir,
					ReportDirectory: internalOutputDirectory, SnapshotExclude: assuranceSnapshotExclusions(),
					Environment: overlayEnvironment(mutationEnvironment(options.Environment, options.BuildTags), buildCache.environment()),
					Trace:       options.Trace, KeepTemp: options.KeepTemp,
				})
				if openErr != nil {
					controlOpenErr = openErr
					return
				}
				reportMutationSweep(options, opened.Swept())
				controlWorkspace = opened
			})
			if controlOpenErr != nil {
				return nil, controlOpenErr
			}
			return controlWorkspace, nil
		}
		originalControl := func(controlContext context.Context, request gomutants.ExecRequest) (gomutants.CommandResult, error) {
			controlMutex.RLock()
			defer controlMutex.RUnlock()
			opened, openErr := openControl(controlContext)
			if openErr != nil {
				return gomutants.CommandResult{}, openErr
			}
			return runOriginalMutationControl(controlContext, opened, request, options.BuildTags)
		}
		closeControl := func() error {
			controlMutex.Lock()
			defer controlMutex.Unlock()
			controlClosed = true
			if controlWorkspace == nil {
				return nil
			}
			err := closeWorkspace(controlWorkspace)
			controlWorkspace = nil
			return err
		}
		var closeRound func() error
		var executionSession MutationSession
		var catalog gomutants.Catalog
		var preparationDone <-chan mutationPreparationResult
		var cancelPreparation context.CancelFunc
		settlePreparation := func(cancel bool) mutationPreparationResult {
			if preparationDone == nil {
				return mutationPreparationResult{}
			}
			if cancel {
				cancelPreparation()
			}
			result := <-preparationDone
			cancelPreparation()
			preparationDone = nil
			cancelPreparation = nil
			return result
		}
		startPreparation := func() {
			include, packages := mutationScope(selection)
			if !defaultPackagePatterns(options.Packages) && !options.Changed {
				packages = slices.Clone(options.Packages)
				include = scopedMutationInclude(metadata.model)
			}
			probe := options.ReplayMutantID == ""
			var probeCoverPackages []string
			if probe {
				probeCoverPackages = []string{metadata.model.ModulePath + "/..."}
			}
			emit(options, "mutation-jobs", strconv.Itoa(mutationJobs))
			prepareContext, cancel := context.WithCancel(ctx)
			done := make(chan mutationPreparationResult, 1)
			started := make(chan struct{})
			cancelPreparation = cancel
			preparationDone = done
			prepareOptions := mutationbridge.PrepareOptions{
				Contract:           contract,
				Operators:          slices.Clone(options.MutationOperators),
				Include:            include,
				Exclude:            slices.Clone(loaded.Project.Exclude),
				DiscoveryPackages:  mutationDiscoveryPackages(include, packages),
				Packages:           packages,
				ProbeCoverPackages: probeCoverPackages,
				Jobs:               mutationJobs, BuildTimeout: options.CommandTimeout, MutantTimeout: options.CommandTimeout,
				SkipVerify: true,
				Probe:      probe,
			}
			go func() {
				close(started)
				session, prepareErr := dependencies.prepareSession(prepareContext, workspace, prepareOptions)
				result := mutationPreparationResult{session: session, err: prepareErr}
				if session != nil {
					result.catalog = session.Catalog()
				}
				done <- result
			}()
			<-started
		}
		acceptPreparation := func(result mutationPreparationResult) error {
			if result.err != nil {
				return result.err
			}
			if result.session == nil {
				return errors.New("goatest: mutation session was not prepared")
			}
			buildCache.persistPreparation()
			executionSession = withNativeBuildCache(result.session, buildCache)
			catalog = result.catalog
			return nil
		}
		closeRound = func() error {
			settlePreparation(true)
			return errors.Join(closeControl(), manager.Close(), closeWorkspace(workspace))
		}

		phases.enter(phaseBaseline)
		baselineParent, baselinePrefix, err := scratch.subdirectory(baselineScratchName)
		if err != nil {
			_ = closeRound()
			return report.Report{}, err
		}
		artifactDirectory, err := dependencies.makeBaselineScratch(baselineParent, baselinePrefix)
		if err != nil {
			_ = closeRound()
			return report.Report{}, fmt.Errorf("goatest: create baseline scratch: %w", err)
		}
		baselineState := checkpoint.Baseline{}
		if baselineResume != nil {
			baselineState = *baselineResume
		}
		baselineOptions := BaselineOptions{
			ArtifactDirectory: artifactDirectory, Contract: contract, PackageSuites: true,
			SuiteEnvironment: slices.Clone(resourceEnv),
			Packages:         slices.Clone(options.Packages),
			BuildTags:        slices.Clone(options.BuildTags), TestArgs: slices.Clone(options.TestArgs), UseTestFraming: true,
			ClassifyUserFailures: true,
			CommandTimeout:       options.CommandTimeout, TargetTimeout: options.TargetTimeout, Jobs: mutationJobs,
			Resume: baselineResume, Checkpoint: func(state checkpoint.Baseline) {
				baselineState = state
				checkpointController.saveBaseline(state)
			},
			RepositoryObserver: repositoryObserver,
			Progress:           baselineProgress(options),
			Trace:              options.Trace,
			StopAfterChecks:    options.ReplayMutantID == "",
		}
		startPreparation()
		controlMutex.RLock()
		pristine, openErr := openControl(ctx)
		controlMutex.RUnlock()
		if openErr != nil {
			settlePreparation(true)
			removeErr := releaseBaselineScratch(options, dependencies.removeBaselineScratch, artifactDirectory)
			_ = closeRound()
			return report.Report{}, errors.Join(openErr, removeErr)
		}
		baselineCommands := withBuildCache(pristine, buildCache)
		baseline, err := dependencies.collectBaseline(ctx, baselineCommands, metadata.model, baselineTargets, baselineOptions)
		phases.leave()
		if err != nil || len(baseline.Findings) != 0 {
			settlePreparation(true)
		} else if err = acceptPreparation(settlePreparation(false)); err == nil && options.ReplayMutantID == "" {
			phases.enter(phaseBaseline)
			baselineOptions.StopAfterChecks = false
			baselineOptions.ProbeSession = executionSession
			baselineOptions.Resume = &baselineState
			baseline, err = dependencies.collectBaseline(ctx, baselineCommands, metadata.model, baselineTargets, baselineOptions)
		}
		removeErr := releaseBaselineScratch(options, dependencies.removeBaselineScratch, artifactDirectory)
		if err != nil || removeErr != nil {
			_ = closeRound()
			return report.Report{}, errors.Join(err, removeErr)
		}
		baseReport := report.Report{
			Schema: report.SchemaV1, Contract: contract, Snapshot: digest,
			Evidence: append(resourceEvidence, baseline.Evidence...), Findings: baseline.Findings,
			Scope:      reportScope(options, metadata.model, selection),
			Repository: report.Repository{Module: metadata.model.ModulePath, Packages: modelPackagePaths(metadata.model)},
			Execution:  reportExecution(options, mutationJobs),
			Toolchain: report.Toolchain{
				Go: metadata.toolchain, Goatest: inputs.GoatestVersion, GoMutants: inputs.GoMutantsVersion,
				OS: runtime.GOOS, Arch: runtime.GOARCH,
			},
			Accounting: report.Accounting{Targets: report.CountAccounting{
				Discovered: len(allTargets), Selected: len(targets),
				Executed: baseline.Executed, Skipped: baseline.Skipped, Excluded: len(allTargets) - len(targets),
			}, Race: report.CountAccounting{
				Discovered: len(metadata.model.Packages), Excluded: len(metadata.model.Packages),
			}},
			Targets:     slices.Clone(baseline.Inventory),
			Resume:      checkpointController.resumeMetadata(),
			Acceptances: slices.Clone(acceptances),
		}
		baseReport.Limitations = append(baseReport.Limitations, projectExcludeLimitations(loaded.Project.Exclude)...)
		if len(loaded.Resources) != 0 {
			baseReport.Limitations = append(baseReport.Limitations, report.Limitation{
				Code: "resource-cache-disabled", Summary: "exact cache reuse is disabled because configured resources have runtime state",
			})
		}
		if len(baseline.Findings) != 0 {
			baseReport.Verdict = baselineVerdict(baseline.Findings)
			baseReport.Limitations = append(baseReport.Limitations, report.Limitation{
				Code: laterPhasesNotRunCode, Summary: "race and mutation phases were not run because baseline verification did not pass",
			})
			checkpointController.discard()
			if closeErr := closeRound(); closeErr != nil {
				return report.Report{}, closeErr
			}
			return baseReport, nil
		}
		phases.enter(phaseGraph)
		currentGraph, err := dependencies.buildGraph(root, metadata.model, baseline.Targets)
		if err != nil {
			_ = closeRound()
			return report.Report{}, err
		}
		currentGraph = dependencies.mergeGraph(currentGraph, selection.prior, selection)
		if err := dependencies.saveGraph(filepath.Join(root, ".goatest", "graph-v1.json"), evidence.GraphRecord{
			ModulePath: metadata.model.ModulePath, Graph: currentGraph,
		}); err != nil {
			_ = closeRound()
			return report.Report{}, err
		}
		phases.enter(phaseRace)
		concurrentPackages, err := dependencies.concurrencyPackages(root, metadata.model.Packages)
		if err != nil {
			_ = closeRound()
			return report.Report{}, err
		}
		raceModel := metadata.model
		racePackages := dependencies.relevantRacePackages(metadata.model, concurrentPackages, baseline.Targets)
		if contract == "deep-v1" {
			raceModel.Packages = includedProjectPackages(metadata.model.Packages, loaded.Project.Exclude)
			racePackages = modelPackagePaths(raceModel)
		} else {
			baseReport.Limitations = append(baseReport.Limitations, report.Limitation{
				Code:      "race-scope-static-estimate",
				Summary:   "standard-v1 selects race packages using static concurrency and observed reachability",
				Estimated: true,
			})
		}
		raceCount := len(racePackages)
		baseReport.Accounting.Race = report.CountAccounting{
			Discovered: len(metadata.model.Packages), Selected: raceCount,
			Executed: raceCount, Excluded: len(metadata.model.Packages) - raceCount,
		}
		emit(options, "race", fmt.Sprintf("%d packages", raceCount))
		var raceResult RaceResult
		if savedRace, reused := checkpointController.race(racePackages); reused {
			raceResult = RaceResult{Evidence: slices.Clone(savedRace.Evidence), Findings: slices.Clone(savedRace.Findings)}
			emit(options, "resume-race", fmt.Sprintf("%d packages", len(racePackages)))
		} else {
			raceOptions := RaceOptions{
				Environment: resourceEnv, TestArgs: slices.Clone(options.TestArgs), BuildTags: slices.Clone(options.BuildTags),
				PersistCompile: buildCache.needsPersistentCompile(), Timeout: options.CommandTimeout,
			}
			controlMutex.RLock()
			pristine, openErr := openControl(ctx)
			if openErr == nil {
				raceResult, err = dependencies.collectRaceWithOptions(
					ctx, withBuildCache(pristine, buildCache), raceModel, racePackages, contract, raceOptions,
				)
			} else {
				err = openErr
			}
			controlMutex.RUnlock()
			if err != nil {
				_ = closeRound()
				return report.Report{}, err
			}
			checkpointController.saveRace(racePackages, raceResult)
		}
		baseReport.Resume = checkpointController.resumeMetadata()
		baseReport.Evidence = append(baseReport.Evidence, raceResult.Evidence...)
		if len(raceResult.Findings) != 0 {
			baseReport.Verdict = report.VerdictDefect
			baseReport.Findings = raceResult.Findings
			baseReport.Limitations = append(baseReport.Limitations, report.Limitation{
				Code: laterPhasesNotRunCode, Summary: "mutation phases were not run because race verification did not pass",
			})
			checkpointController.discard()
			if closeErr := closeRound(); closeErr != nil {
				return report.Report{}, closeErr
			}
			return baseReport, nil
		}

		if executionSession == nil {
			_ = closeRound()
			return report.Report{}, errors.New("goatest: mutation session was not prepared")
		}
		mutationCount := mutationTargetCount(catalog, options.ReplayMutantID)
		mutationDetail := fmt.Sprintf("%d mutants", mutationCount)
		if mutationCount == 1 {
			mutationDetail = "1 mutant"
		}
		emit(options, "mutation-target", mutationDetail)

		mutationResume := checkpointController.mutation(catalog)

		var suiteProbes map[string]PackageProbeEvidence
		if options.ReplayMutantID == "" {
			phases.enter(phaseProbe)
			probeTargets := probeTargetCount(baseline.Targets)
			allProbeSuitePackages := neededProbeSuitePackages(
				catalog, baseline.Targets, baseline.Instrumented, baseline.Suites,
			)
			preparedSuites := make(map[string]PackageProbeEvidence)
			for _, pkg := range allProbeSuitePackages {
				if suite, ok := baseline.ProbeSuites[pkg]; ok && suite.Measured {
					preparedSuites[pkg] = suite
				}
			}
			probeSuites := len(allProbeSuitePackages) - len(preparedSuites)
			probeDetail := countedNoun(probeTargets, "target", "targets") + ", " +
				countedNoun(probeSuites, "package suite", "package suites")
			emit(options, "probe-target", probeDetail)
			probed, resumedProbe, validProbe := checkpointController.probe(
				catalog, baseline.Targets, allProbeSuitePackages,
			)
			if !validProbe {
				mutationResume = nil
			}
			if resumedProbe {
				emit(options, "resume-probe", probeDetail)
			} else {
				var probeErr error
				probed, probeErr = dependencies.probeTargets(ctx, executionSession, baseline.Targets, ProbeOptions{
					Contract: contract, Timeout: options.CommandTimeout, TestArgs: slices.Clone(options.TestArgs),
					Jobs: mutationJobs, Trace: options.Trace, Progress: probeProgress(options),
					SuitePackages: allProbeSuitePackages, Suites: preparedSuites,
					SuiteEnvironment:   slices.Clone(resourceEnv),
					SuiteCoverage:      baseline.Suites,
					RepositoryObserver: repositoryObserver,
				})
				if probeErr != nil {
					_ = closeRound()
					return report.Report{}, probeErr
				}
				checkpointController.saveProbe(catalog, probed)
			}

			baseline.Targets = probed.Targets
			suiteProbes = probed.Suites
			emit(options, "probe-summary",
				countedNoun(probed.Measured, "target", "targets")+" measured, "+
					fmt.Sprintf("%d without facts; ", probed.Unmeasured)+
					countedNoun(probed.SuitesMeasured, "package suite", "package suites")+" measured, "+
					fmt.Sprintf("%d without facts", probed.SuitesUnmeasured))
		}
		mutationOriginalControl := originalControl
		if options.ReplayMutantID == "" {
			mutationOriginalControl = preparedProbeMutationControl(executionSession, options.Trace)
		}

		var mutationEvidence *MutationEvidence
		evidencePath := filepath.Join(root, ".goatest", "cache", mutationEvidenceFileName)
		if mutationEvidenceGuarded(round, loaded, options) {
			mutationStore, _, evidenceErr := dependencies.loadMutationEvidence(evidencePath, metadata.model.ModulePath)
			if evidenceErr != nil {
				emit(options, "mutation-evidence-rejected", evidenceErr.Error())
				mutationStore = evidence.MutationStore{}
			}
			mutationEvidence = newRunMutationEvidence(
				mutationStore, mutationSources,
				baseline.Targets, baseline.Inventory, resourceEnv, digest,
			)
		}

		phases.enter(phaseMutation)
		mutation, err := dependencies.evaluateMutations(ctx, executionSession, baseline.Targets, MutationOptions{
			ReplayMutantID: options.ReplayMutantID,
			TestArgs:       slices.Clone(options.TestArgs),
			Timeout:        options.CommandTimeout,
			Jobs:           mutationJobs, Accepted: accepted,
			Progress: mutationProgress(options),
			Resume:   mutationResume, Checkpoint: checkpointController.saveMutant,
			OriginalControl:    mutationOriginalControl,
			Trace:              options.Trace,
			Instrumented:       baseline.Instrumented,
			Evidence:           mutationEvidence,
			RepositoryObserver: repositoryObserver,
			SuiteProbes:        suiteProbes,
			SuiteCoverage:      baseline.Suites,
			SuiteEnvironment:   slices.Clone(resourceEnv),
		})
		if err != nil {
			_ = closeRound()
			return report.Report{}, err
		}
		checkpointController.completeMutation()

		if mutationEvidence != nil {
			if err := dependencies.saveMutationEvidence(evidencePath, mutationEvidence.store(catalog, metadata.model.ModulePath)); err != nil {
				emit(options, "mutation-evidence-unsaved", err.Error())
			}
		}
		baseReport.Accounting.Mutants = mutation.Accounting
		baseReport.Mutants = slices.Clone(mutation.Mutants)
		baseReport.Resume = checkpointController.resumeMetadata()

		phases.enter(phaseRepair)
		var generated GenerationEvaluation
		generated, err = dependencies.attemptRepairs(ctx, root, mutation.Findings, GenerationOptions{
			Snapshot: digest, NoApply: options.NoApply, Generate: options.Generate,
			Command:             loaded.Generation.Command,
			ProviderEnvironment: generationProviderEnvironment(options.Environment, loaded.Generation.Environment),
			AllowedPaths:        generationPaths(options, loaded), Validator: options.Validator,
			RepositoryValidator: RepositoryValidatorOptions{
				Root: root, Contract: contract, GoBinary: options.GoBinary,
				TempDirectory: options.TempDirectory, scratch: &scratch,
				Environment:       validationEnvironment(executionEnvironment(options.Environment), resourceEnv),
				MutationOperators: options.MutationOperators, Packages: options.Packages,
				BuildTags: options.BuildTags, TestArgs: options.TestArgs, Timeout: options.CommandTimeout,
				Trace: options.Trace, KeepTemp: options.KeepTemp,
				BuildCacheEnvironment: buildCache.environment(),
			},
		})
		if err != nil {
			_ = closeRound()
			return report.Report{}, err
		}
		phases.enter(phaseFinalize)
		if closeErr := closeRound(); closeErr != nil {
			return report.Report{}, closeErr
		}
		roundRepairs := slices.Clone(generated.Repairs)
		if generated.Applied {
			checkpointController.discard()
			appliedRepairs = append(appliedRepairs, roundRepairs...)
			emit(options, "repair-applied", fmt.Sprintf("%d files", len(roundRepairs)))
			if round+1 == maximumRounds {
				limitFinding := report.Finding{
					ID: report.FindingID("repair-round-limit", digest), Kind: "repair-round-limit",
					Summary: "three repair rounds completed without establishing the full contract",
				}
				baseReport.Verdict = report.VerdictInsufficient
				baseReport.Findings = append(generated.Findings, limitFinding)
				baseReport.Repairs = slices.Clone(appliedRepairs)
				return baseReport, nil
			}
			continue
		}

		_, finalDigest, err := dependencies.assuranceInputs(root, contract, options, loaded, metadata)
		if err != nil {
			return report.Report{}, err
		}
		if digest != finalDigest {
			return report.Report{}, fmt.Errorf("goatest: repository changed during verification; refusing stale evidence")
		}
		result := report.Report{
			Schema: report.SchemaV1, Contract: contract, Snapshot: finalDigest,
			Evidence: append(baseReport.Evidence, mutation.Evidence...),
			Findings: generated.Findings, Repairs: append(slices.Clone(appliedRepairs), roundRepairs...),
			Scope: baseReport.Scope, Repository: baseReport.Repository, Toolchain: baseReport.Toolchain,
			Execution:  baseReport.Execution,
			Accounting: baseReport.Accounting, Mutants: slices.Clone(baseReport.Mutants),
			Targets: slices.Clone(baseReport.Targets), Resume: baseReport.Resume,
			Acceptances: slices.Clone(baseReport.Acceptances), Limitations: slices.Clone(baseReport.Limitations),
		}
		result.Accounting.Mutants = mutation.Accounting
		if len(result.Findings) == 0 {
			result.Verdict = report.VerdictAssured
		} else {
			result.Verdict = report.VerdictInsufficient
			result.Limitations = append(result.Limitations, report.Limitation{
				Code: "unresolved-mutation-gaps", Summary: "Unresolved mutation evidence gaps remain",
			})
		}
		if result.Accounting.Mutants.Unknown != 0 {
			result.Verdict = report.VerdictError
			result.Findings = append(result.Findings, report.Finding{
				ID: report.FindingID("mutation-accounting", finalDigest), Kind: "mutation-accounting",
				Summary: "one or more discovered mutants have no auditable disposition",
			})
		}
		if err := cacheStore.Put(finalDigest, result); err != nil {
			return report.Report{}, err
		}
		return result, nil
	}
}

func runOriginalMutationControl(ctx context.Context, workspace CommandWorkspace, request gomutants.ExecRequest, buildTags []string) (gomutants.CommandResult, error) {
	argv := []string{"go", "test", "-count=1"}
	if len(buildTags) != 0 {
		argv = append(argv, "-tags="+strings.Join(buildTags, ","))
	}
	if request.Package == "" {
		argv = append(argv, "./...")
	} else {
		argv = append(argv, request.Package)
	}
	arguments := slices.Clone(request.Args)
	if len(arguments) != 0 {
		argv = append(argv, "-args")
		argv = append(argv, arguments...)
	}
	return workspace.Exec(ctx, gomutants.Command{
		Argv: argv, Env: slices.Clone(request.Env), Timeout: request.Timeout, OutputLimit: commandOutputLimit,
	})
}

func preparedProbeMutationControl(session MutationSession, recorder *trace.Recorder) func(context.Context, gomutants.ExecRequest) (gomutants.CommandResult, error) {
	return func(ctx context.Context, request gomutants.ExecRequest) (gomutants.CommandResult, error) {
		probeRequest := gomutants.ProbeRequest{
			Package: request.Package, Args: slices.Clone(request.Args),
			Env: slices.Clone(request.Env), Timeout: request.Timeout,
		}
		record := trace.ProbeRecord{
			Target: mutationControlProbeTarget(request.Package), Package: request.Package,
			Args: slices.Clone(request.Args), TimeoutMS: traceMilliseconds(request.Timeout), Control: true,
		}
		result, err := session.Probe(ctx, probeRequest)
		if err != nil {
			record.Error = err.Error()
			recorder.ProbeExec(record)
			return gomutants.CommandResult{}, fmt.Errorf("goatest: prepared original control: %w", err)
		}
		record.ExitCode = result.ExitCode
		record.DurationMS = traceMilliseconds(result.Duration)
		switch {
		case result.Duration < 0:
			err = fmt.Errorf("goatest: prepared original control returned negative duration %s", result.Duration)
		case result.Outcome == gomutants.ProbeMeasured:
			if result.ExitCode != 0 {
				err = fmt.Errorf("goatest: prepared original control returned measured with exit code %d", result.ExitCode)
			}
		case result.Outcome == gomutants.ProbeTestFailed || result.Outcome == gomutants.ProbeUnavailable:
			if result.ExitCode == 0 {
				err = fmt.Errorf("goatest: prepared original control returned %s with exit code 0", result.Outcome)
			}
		case result.Outcome == gomutants.ProbeTimedOut:
		default:
			err = fmt.Errorf("goatest: prepared original control returned unknown outcome %q", result.Outcome)
		}
		if err != nil {
			record.Error = err.Error()
			recorder.ProbeExec(record)
			return gomutants.CommandResult{}, err
		}
		record.Outcome = string(result.Outcome)
		recorder.ProbeExec(record)
		return gomutants.CommandResult{
			ExitCode: result.ExitCode, TimedOut: result.Outcome == gomutants.ProbeTimedOut,
			Duration: result.Duration, Output: slices.Clone(result.Output),
		}, nil
	}
}

func mutationControlProbeTarget(pkg string) string {
	if pkg == "" {
		return trace.MutationControlProbePrefix + "all"
	}
	return trace.MutationControlProbePrefix + pkg
}

func reportScope(options Options, model goanalysis.Model, selection impactSelection) report.Scope {
	kind := report.RunFull
	switch {
	case options.ReplayFindingID != "" || options.ReplayMutantID != "":
		kind = report.RunReplay
	case options.Changed:
		kind = report.RunChangeset
	case options.PackageScope:
		kind = report.RunPackage
	}
	requested := report.ScopeSpec{
		Kind: string(kind), Project: ".", Modules: []string{model.ModulePath},
		Packages: slices.Clone(options.Packages), Ref: options.ChangedRef,
	}
	resolved := requested
	if options.Changed {
		requested.Files = slices.Clone(selection.changed)
		resolved.Files = slices.Clone(selection.changed)
		if selection.broad {
			resolved.Kind = string(report.RunFull)
			resolved.Packages = modelPackagePaths(model)
			resolved.Files = nil
		}
	} else if kind == report.RunFull {
		requested.Packages = modelPackagePaths(model)
		resolved.Packages = modelPackagePaths(model)
	}
	return report.Scope{Requested: requested, Resolved: resolved}
}

func modelPackagePaths(model goanalysis.Model) []string {
	packages := make([]string, 0, len(model.Packages))
	for _, pkg := range model.Packages {
		packages = append(packages, pkg.ImportPath)
	}
	slices.Sort(packages)
	return slices.Compact(packages)
}

func mutationTargetCount(catalog gomutants.Catalog, replayMutantID string) int {
	count := 0
	for _, mutant := range catalog.Mutants {
		if mutant.Accepted && (replayMutantID == "" || mutant.ID == replayMutantID) {
			count++
		}
	}
	return count
}

func inspectWorkspace(ctx context.Context, workspace CommandWorkspace, toolchain string, patterns, tags []string, timeout time.Duration) (roundMetadata, error) {
	toolchain = strings.TrimSpace(toolchain)
	if toolchain == "" {
		return roundMetadata{}, errors.New("goatest: workspace toolchain version is empty")
	}
	argv := []string{"go", "list", "-json"}
	if len(tags) != 0 {
		argv = append(argv, "-tags="+strings.Join(tags, ","))
	}
	if len(patterns) == 0 {
		patterns = []string{"./..."}
	}
	argv = append(argv, patterns...)
	listed, err := workspace.Exec(ctx, command(argv, timeout))
	if err != nil || listed.ExitCode != 0 || listed.TimedOut {
		return roundMetadata{}, commandError("go list", listed, err)
	}
	model, err := goanalysis.DecodePackages(bytes.NewReader(listed.Output))
	if err != nil {
		return roundMetadata{}, err
	}
	modules, err := workspace.Exec(ctx, command([]string{"go", "list", "-m", "-json", "all"}, workspaceInspectionTimeout))
	if err != nil || modules.ExitCode != 0 || modules.TimedOut {
		return roundMetadata{}, commandError("go list -m", modules, err)
	}
	if err := validateWorkspaceModuleGraph(modules.Output, model.ModulePath); err != nil {
		return roundMetadata{}, err
	}
	dependencies, err := dependencyDigests(modules.Output)
	if err != nil {
		return roundMetadata{}, err
	}
	return roundMetadata{model: model, toolchain: toolchain, dependencies: dependencies}, nil
}

func defaultPackagePatterns(patterns []string) bool {
	return len(patterns) == 0 || len(patterns) == 1 && patterns[0] == "./..."
}

func command(argv []string, timeout time.Duration) gomutants.Command {
	return gomutants.Command{Argv: slices.Clone(argv), Timeout: timeout, OutputLimit: commandOutputLimit}
}

func commandError(name string, result gomutants.CommandResult, err error) error {
	if err != nil {
		return fmt.Errorf("goatest: %s: %w", name, err)
	}
	return fmt.Errorf("goatest: %s failed (exit=%d timeout=%t): %s", name, result.ExitCode, result.TimedOut, summarize(result.Output))
}

type listedModule struct {
	Path     string
	Version  string
	Sum      string
	GoModSum string
	Dir      string
	Main     bool
	Replace  *listedModule
}

func validateWorkspaceModuleGraph(data []byte, selectedModule string) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	var mainModules []string
	for {
		var module listedModule
		err := decoder.Decode(&module)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("goatest: decode module graph: %w", err)
		}
		if module.Path == "" {
			return errors.New("goatest: module graph contains an empty path")
		}
		if module.Main {
			mainModules = append(mainModules, module.Path)
		}
	}
	slices.Sort(mainModules)
	mainModules = slices.Compact(mainModules)
	switch len(mainModules) {
	case 0:
		return errors.New("goatest: module graph contains no main module")
	case 1:
		if mainModules[0] != selectedModule {
			return fmt.Errorf("goatest: selected package module %q does not match main module %q", selectedModule, mainModules[0])
		}
		return nil
	default:
		return fmt.Errorf("goatest: workspace has multiple main modules (%s); refusing partial assurance", strings.Join(mainModules, ", "))
	}
}

func dependencyDigests(data []byte) (map[string]string, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	result := make(map[string]string)
	for {
		var module listedModule
		err := decoder.Decode(&module)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("goatest: decode module graph: %w", err)
		}
		if module.Path == "" {
			return nil, fmt.Errorf("goatest: module graph contains an empty path")
		}
		identity := module.Version + "\x00" + module.Sum + "\x00" + module.GoModSum
		if module.Replace != nil {
			identity += "\x00replace\x00" + module.Replace.Path + "\x00" + module.Replace.Version + "\x00" + module.Replace.Sum + "\x00" + module.Replace.GoModSum
			if module.Replace.Version == "" && module.Replace.Sum == "" && module.Replace.Dir != "" {
				files, corpus, scanErr := evidence.Scan(module.Replace.Dir)
				if scanErr != nil {
					return nil, fmt.Errorf("goatest: digest local replacement %s: %w", module.Path, scanErr)
				}
				identity += "\x00content\x00" + evidence.Digest(evidence.Inputs{Files: files, Corpus: corpus})
			}
		}
		sum := sha256.Sum256([]byte(identity))
		result[module.Path] = hex.EncodeToString(sum[:])
	}
	return result, nil
}

func assuranceInputs(root, contract string, options Options, loaded config.Config, metadata roundMetadata) (evidence.Inputs, string, error) {
	goMutants, err := goMutantsIdentity()
	if err != nil {
		return evidence.Inputs{}, "", err
	}
	goatestBuild, err := goatestBuildIdentity()
	if err != nil {
		return evidence.Inputs{}, "", err
	}
	return assuranceInputsWithBuildIdentity(root, contract, options, loaded, metadata, goMutants, goatestBuild)
}

func assuranceInputsWithBuildIdentity(
	root, contract string,
	options Options,
	loaded config.Config,
	metadata roundMetadata,
	goMutants, goatestBuild string,
) (evidence.Inputs, string, error) {
	files, corpus, err := evidence.Scan(root)
	if err != nil {
		return evidence.Inputs{}, "", err
	}
	resources := make(map[string]string, len(loaded.Resources))
	for name, spec := range loaded.Resources {
		encoded, _ := json.Marshal(struct {
			Command             []string
			Timeout             string
			Shared              bool
			Exclusive           bool
			Environment         []string
			ProviderEnvironment []string
		}{spec.Command, spec.Timeout.String(), spec.Shared, spec.Exclusive, spec.Environment,
			envselect.Provider(options.Environment, spec.Environment)})
		sum := sha256.Sum256(encoded)
		resources[name] = hex.EncodeToString(sum[:])
	}
	environment := executionEnvironment(options.Environment)
	inputs := evidence.Inputs{
		Files: files, Corpus: corpus, Dependencies: metadata.dependencies,
		Toolchain: metadata.toolchain, Platform: runtime.GOOS + "/" + runtime.GOARCH,
		Environment: selectedEnvironment(environment, loaded.Execution.Environment), Resources: resources,
		Contract: contract + modeIdentity(options), GoatestVersion: ResolvedGoatestVersion(), GoatestBuild: goatestBuild,
		GoMutantsVersion: goMutants,
	}
	return inputs, evidence.Digest(inputs), nil
}

func modeIdentity(options Options) string {
	identity := fmt.Sprintf(";apply=%t;changed=%t;ref=%s", !options.NoApply, options.Changed, options.ChangedRef)
	if options.ReplayMutantID != "" {
		identity += ";replay=" + options.ReplayMutantID
	}
	if options.ReplayFindingID != "" {
		identity += ";replay-finding=" + options.ReplayFindingID
	}
	hasExtended := len(options.Packages) != 0 || options.PackageScope || len(options.TestArgs) != 0 ||
		len(options.BuildTags) != 0 || len(options.MutationOperators) != 0 ||
		options.MutationJobs != 0 || options.CommandTimeout != 0 || options.TargetTimeout != 0
	if !hasExtended {
		return identity
	}
	encoded, _ := json.Marshal(struct {
		Packages          []string
		PackageScope      bool
		TestArgs          []string
		BuildTags         []string
		MutationOperators []string
		MutationJobs      int
		CommandTimeout    string
		TargetTimeout     string
	}{
		Packages: slices.Clone(options.Packages), PackageScope: options.PackageScope,
		TestArgs: slices.Clone(options.TestArgs), BuildTags: slices.Clone(options.BuildTags),
		MutationOperators: slices.Clone(options.MutationOperators),
		MutationJobs:      options.MutationJobs,
		CommandTimeout:    options.CommandTimeout.String(), TargetTimeout: options.TargetTimeout.String(),
	})
	return identity + ";execution=" + string(encoded)
}

var buildEnvironmentNames = []string{
	"AR", "CC", "CGO_CFLAGS", "CGO_CPPFLAGS", "CGO_CXXFLAGS", "CGO_ENABLED", "CGO_FFLAGS", "CGO_LDFLAGS",
	"CXX", "FC", "GCCGO", "GODEBUG", "GOENV", "GOEXPERIMENT", "GOFLAGS", "GO386", "GOAMD64", "GOARM",
	"GOARM64", "GOMIPS", "GOMIPS64", "GOPPC64", "GORISCV64", "GOTOOLCHAIN", "GOWASM", "GOWORK", "PKG_CONFIG",
}

func selectedEnvironment(environment, configured []string) []string {
	return envselect.Select(environment, append(slices.Clone(buildEnvironmentNames), configured...))
}

func generationProviderEnvironment(input, configured []string) []string {
	return envselect.Provider(input, configured)
}

func includedProjectTargets(targets []goanalysis.Target, excludes []string) []goanalysis.Target {
	if len(excludes) == 0 {
		return slices.Clone(targets)
	}
	result := make([]goanalysis.Target, 0, len(targets))
	for _, target := range targets {
		if !projectPathExcluded(target.Path, excludes) {
			result = append(result, target)
		}
	}
	return result
}

func includedProjectPackages(packages []goanalysis.Package, excludes []string) []goanalysis.Package {
	result := make([]goanalysis.Package, 0, len(packages))
	for _, pkg := range packages {
		if !projectPathExcluded(pkg.RelativeDir, excludes) {
			result = append(result, pkg)
		}
	}
	return result
}

func projectPathExcluded(candidate string, patterns []string) bool {
	candidate = strings.TrimPrefix(strings.ReplaceAll(candidate, `\`, "/"), "./")
	for _, pattern := range patterns {
		pattern = strings.TrimPrefix(pattern, "./")
		if pattern == "**" {
			return true
		}
		if strings.HasSuffix(pattern, "/**") {
			prefix := strings.TrimSuffix(pattern, "/**")
			if candidate == prefix || strings.HasPrefix(candidate, prefix+"/") {
				return true
			}
		}
		if strings.HasPrefix(pattern, "**/") {
			remainder := strings.TrimPrefix(pattern, "**/")
			components := strings.Split(candidate, "/")
			for index := range components {
				suffix := strings.Join(components[index:], "/")
				if matched, _ := path.Match(remainder, suffix); matched {
					return true
				}
			}
		}
		if matched, _ := path.Match(pattern, candidate); matched {
			return true
		}
	}
	return false
}

func projectExcludeLimitations(excludes []string) []report.Limitation {
	result := make([]report.Limitation, 0, len(excludes))
	for _, pattern := range excludes {
		result = append(result, report.Limitation{
			Code: "project-exclude", Summary: fmt.Sprintf("paths matching %q are outside the configured assurance boundary", pattern),
		})
	}
	return result
}

func activeAcceptance(loaded config.Config, now time.Time) map[string]bool {
	result := make(map[string]bool)
	for _, acceptance := range loaded.Acceptance {
		if acceptance.Expires.After(now) {
			result[acceptance.ID] = true
		}
	}
	return result
}

func activeAcceptanceMetadata(loaded config.Config, now time.Time) []report.Acceptance {
	result := make([]report.Acceptance, 0, len(loaded.Acceptance))
	for _, acceptance := range loaded.Acceptance {
		if !acceptance.Expires.After(now) {
			continue
		}
		result = append(result, report.Acceptance{
			ID: acceptance.ID, Reason: acceptance.Reason, Expires: acceptance.Expires.UTC().Format(time.RFC3339),
			Owner: acceptance.Owner, Ticket: acceptance.Ticket,
		})
	}
	slices.SortFunc(result, func(a, b report.Acceptance) int { return strings.Compare(a.ID, b.ID) })
	return result
}

func cachedAcceptanceValid(cached report.Report, accepted map[string]bool) bool {
	for _, item := range cached.Acceptances {
		if !accepted[item.ID] {
			return false
		}
	}
	for _, item := range cached.Evidence {
		if item.Kind == "mutation" && item.Status == "accepted" && !accepted[item.Detail] {
			return false
		}
	}
	return true
}

func cachedReportReusable(cached report.Report, accepted map[string]bool) bool {
	if !cachedAcceptanceValid(cached, accepted) {
		return false
	}
	return !slices.ContainsFunc(cached.Limitations, func(item report.Limitation) bool {
		return item.Code == laterPhasesNotRunCode
	})
}

func acquireResources(ctx context.Context, loaded config.Config, targets []goanalysis.Target, baseEnvironment []string) (runResourceManager, []BaselineTarget, []report.Evidence, []string, error) {
	specs := make(map[string]resource.Spec, len(loaded.Resources))
	for name, spec := range loaded.Resources {
		specs[name] = resource.Spec{
			Command: spec.Command, Timeout: spec.Timeout, Shared: spec.Shared, Exclusive: spec.Exclusive,
			Environment: envselect.Provider(baseEnvironment, spec.Environment),
		}
	}
	manager := newRunResourceManager(specs)
	capabilities := make(map[string]struct{})
	for _, target := range targets {
		for _, capability := range targetResourceCapabilities(target) {
			capabilities[capability] = struct{}{}
		}
	}
	names := make([]string, 0, len(capabilities))
	for name := range capabilities {
		names = append(names, name)
	}
	slices.Sort(names)
	environments := make(map[string][]string, len(names))
	var evidenceItems []report.Evidence
	var allEnvironment []string
	for _, name := range names {
		env, err := manager.AcquireEnvironment(ctx, name)
		if err != nil {
			_ = manager.Close()
			return nil, nil, nil, nil, err
		}
		environments[name] = env
		var mergeErr error
		allEnvironment, mergeErr = mergeEnvironment(allEnvironment, env)
		if mergeErr != nil {
			_ = manager.Close()
			return nil, nil, nil, nil, fmt.Errorf("goatest: resource %s: %w", name, mergeErr)
		}
		evidenceItems = append(evidenceItems, report.Evidence{Kind: "resource", ID: name, Status: "ready"})
	}
	baseline := make([]BaselineTarget, len(targets))
	for i, target := range targets {
		capabilities := targetResourceCapabilities(target)
		if len(capabilities) == 1 {
			baseline[i] = BaselineTarget{Target: target, Environment: slices.Clone(environments[capabilities[0]])}
			continue
		}
		var targetEnvironment []string
		for _, capability := range capabilities {
			merged, mergeErr := mergeEnvironment(targetEnvironment, environments[capability])
			if mergeErr != nil {
				_ = manager.Close()
				return nil, nil, nil, nil, fmt.Errorf("goatest: target %s resources: %w", target.Name, mergeErr)
			}
			targetEnvironment = merged
		}
		baseline[i] = BaselineTarget{Target: target, Environment: targetEnvironment}
	}
	return manager, baseline, evidenceItems, allEnvironment, nil
}

func targetResourceCapabilities(target goanalysis.Target) []string {
	return slices.Clone(target.Capabilities)
}

func mutationJobLimit(options Options, loaded config.Config) int {
	for _, spec := range loaded.Resources {
		if spec.Exclusive {
			return 1
		}
	}
	if options.MutationJobs > 0 {
		return options.MutationJobs
	}
	return max(1, min(runtime.GOMAXPROCS(0), defaultMutationJobLimit))
}

func reportExecution(options Options, mutationJobs int) report.Execution {
	return report.Execution{
		TestArgs:          slices.Clone(options.TestArgs),
		BuildTags:         slices.Clone(options.BuildTags),
		MutationOperators: slices.Clone(options.MutationOperators),
		MutationJobs:      mutationJobs,
		CommandTimeoutNS:  int64(options.CommandTimeout),
		TargetTimeoutNS:   int64(options.TargetTimeout),
	}
}

func countedNoun(count int, singular, plural string) string {
	if count == 1 {
		return "1 " + singular
	}
	return strconv.Itoa(count) + " " + plural
}

func mutationProgress(options Options) func(completed, total int) {
	return boundedProgress(options, "mutation-progress")
}

func probeProgress(options Options) func(completed, total int) {
	return boundedProgress(options, "probe-progress")
}

func baselineProgress(options Options) func(completed, total int) {
	return boundedProgress(options, "baseline-progress")
}

func boundedProgress(options Options, kind string) func(completed, total int) {
	previousCompleted, previousTotal := -1, -1
	return func(completed, total int) {
		if completed == previousCompleted && total == previousTotal {
			return
		}
		previousCompleted, previousTotal = completed, total
		step := max(1, (total+progressDivisions-1)/progressDivisions)
		if completed == 0 || completed == 1 || completed == total || completed%step == 0 {
			emit(options, kind, fmt.Sprintf("%d/%d", completed, total))
		}
	}
}

func mergeEnvironment(base, overlay []string) ([]string, error) {
	values := make(map[string]string)
	for _, entry := range append(slices.Clone(base), overlay...) {
		key, value, ok := strings.Cut(entry, "=")
		if !ok || key == "" {
			return nil, fmt.Errorf("invalid environment entry %q", entry)
		}
		upper := strings.ToUpper(key)
		if existing, exists := values[upper]; exists && existing != value {
			return nil, fmt.Errorf("conflicting values for %s", key)
		}
		values[upper] = value
	}
	result := make([]string, 0, len(values))
	for key, value := range values {
		result = append(result, key+"="+value)
	}
	slices.Sort(result)
	return result, nil
}

func validationEnvironment(base, overlay []string) []string {
	if base == nil {
		base = os.Environ()
	}
	values := make(map[string]string)
	names := make(map[string]string)
	for _, entry := range base {
		key, value, ok := strings.Cut(entry, "=")
		if ok && key != "" {
			upper := strings.ToUpper(key)
			values[upper] = value
			names[upper] = key
		}
	}
	for _, entry := range overlay {
		key, value, ok := strings.Cut(entry, "=")
		if ok && key != "" {
			upper := strings.ToUpper(key)
			values[upper] = value
			names[upper] = key
		}
	}
	result := make([]string, 0, len(values))
	for upper, value := range values {
		result = append(result, names[upper]+"="+value)
	}
	slices.Sort(result)
	return result
}

func baselineVerdict(findings []report.Finding) report.Verdict {
	for _, finding := range findings {
		if finding.Kind == "baseline-failure" || finding.Kind == "baseline-timeout" ||
			finding.Kind == "vet-failure" || finding.Kind == "build-failure" ||
			finding.Kind == "test-binary-build-failure" {
			return report.VerdictDefect
		}
	}
	return report.VerdictInsufficient
}

func repositoryRoot(root string) (string, error) {
	if root == "" {
		root = "."
	}
	absolute, err := absoluteRepositoryPath(root)
	if err != nil {
		return "", err
	}
	info, err := statRepositoryPath(absolute)
	if err != nil {
		return "", fmt.Errorf("goatest: repository root: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("goatest: repository root %s is not a directory", absolute)
	}
	return absolute, nil
}

func executionEnvironment(input []string) []string {
	if input == nil {
		input = os.Environ()
	}
	values := make(map[string]string)
	names := make(map[string]string)
	for _, entry := range input {
		key, value, ok := strings.Cut(entry, "=")
		if !ok || key == "" {
			continue
		}
		upper := strings.ToUpper(key)
		if ephemeralEnvironmentKey(upper) {
			continue
		}
		values[upper] = value
		names[upper] = key
	}
	for key, value := range map[string]string{
		"GOPROXY": "off", "GOSUMDB": "off", "GOTELEMETRY": "off", "GOTOOLCHAIN": "local",
	} {
		values[key] = value
		names[key] = key
	}

	if !strings.Contains(values["GOFLAGS"], "-buildvcs=") {
		flags := strings.TrimSpace(values["GOFLAGS"])
		if flags != "" {
			flags += " "
		}
		values["GOFLAGS"] = flags + "-buildvcs=false"
		if _, declared := names["GOFLAGS"]; !declared {
			names["GOFLAGS"] = "GOFLAGS"
		}
	}
	result := make([]string, 0, len(values))
	for upper, value := range values {
		result = append(result, names[upper]+"="+value)
	}
	slices.Sort(result)
	return result
}

func mutationEnvironment(input, buildTags []string) []string {
	environment := executionEnvironment(input)
	if len(buildTags) == 0 {
		return environment
	}
	flag := "-tags=" + strings.Join(buildTags, ",")
	for index, entry := range environment {
		key, value, ok := strings.Cut(entry, "=")
		if ok && strings.EqualFold(key, "GOFLAGS") {
			value = strings.TrimSpace(value)
			if value != "" {
				value += " "
			}
			environment[index] = key + "=" + value + flag
			slices.Sort(environment)
			return environment
		}
	}
	environment = append(environment, "GOFLAGS="+flag)
	slices.Sort(environment)
	return environment
}

func ephemeralEnvironmentKey(upper string) bool {
	return upper == "STARSHIP_SESSION_KEY" || upper == "__MISE_SESSION"
}

func emit(options Options, kind, detail string) {
	if options.Progress != nil {
		options.Progress(Event{Kind: kind, Detail: detail})
	}
	options.Trace.Progress(kind, detail)
}
