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

	gomutants "github.com/P4suta/go-mutants"
	"github.com/P4suta/goatest/internal/checkpoint"
	"github.com/P4suta/goatest/internal/config"
	envselect "github.com/P4suta/goatest/internal/environment"
	"github.com/P4suta/goatest/internal/evidence"
	goanalysis "github.com/P4suta/goatest/internal/golang"
	"github.com/P4suta/goatest/internal/mutationbridge"
	"runtime/debug"

	"github.com/P4suta/goatest/internal/provider"
	"github.com/P4suta/goatest/internal/repair"
	"github.com/P4suta/goatest/internal/report"
	"github.com/P4suta/goatest/internal/resource"
	"github.com/P4suta/goatest/internal/testargs"
	"github.com/P4suta/goatest/internal/trace"
)

const maximumRounds = 3

// goMutantsModulePath is the module the mutation bridge freezes; its version
// is part of every audited identity.
const goMutantsModulePath = "github.com/P4suta/go-mutants"

// goMutantsFallbackVersion answers for the one binary shape that records no
// dependency modules in its build info: a test binary. It must match go.mod,
// and TestGoMutantsEvidenceVersionMatchesPinnedModule holds it there; every
// built binary reports the version its build info actually linked instead, so
// a shipped identity can never drift behind this constant.
//
// The pin is a pseudo-version of go-mutants' main branch: the branch proof
// goatest routes by is only there, and the tagged releases sit on another
// branch, which is why the pseudo-version reads as v0.0.0 while being ahead.
const goMutantsFallbackVersion = "v0.0.0-20260902214351-fb36fecf91a7"

// GoMutantsVersion resolves the go-mutants version linked into this binary
// from its build info, honoring a replace directive because the replacement
// is what actually ran. A binary without build info fails closed rather than
// attest a version nothing linked.
func GoMutantsVersion() (string, error) {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "", errors.New("goatest: build info is unavailable; the go-mutants version cannot be audited")
	}
	return goMutantsVersionFrom(info)
}

// goMutantsVersionFrom picks the go-mutants module out of one build info.
func goMutantsVersionFrom(info *debug.BuildInfo) (string, error) {
	for _, dependency := range info.Deps {
		if dependency.Path != goMutantsModulePath {
			continue
		}
		module := dependency
		if dependency.Replace != nil {
			module = dependency.Replace
		}
		if module.Version == "" {
			return "", fmt.Errorf("goatest: %s carries no version in build info", goMutantsModulePath)
		}
		return module.Version, nil
	}
	if len(info.Deps) == 0 {
		return goMutantsFallbackVersion, nil
	}
	return "", fmt.Errorf("goatest: %s is absent from build info", goMutantsModulePath)
}

// goatestDevelVersion is the unstamped default of GoatestVersion. A binary
// still carrying it was not built by the release pipeline, so the module
// version its build info records - what `go install module@version` stamps -
// is the truthful identity wherever one exists.
const goatestDevelVersion = "v0.1.0-dev"

// GoatestVersion is stamped by release builds and participates in evidence
// cache identity. Readers resolve it through ResolvedGoatestVersion.
var GoatestVersion = goatestDevelVersion

// ResolvedGoatestVersion reports the goatest version this binary carries: the
// release-stamped value when one was stamped, otherwise the module version of
// a `go install` build, and the development default for a checkout build or a
// test binary.
func ResolvedGoatestVersion() string {
	info, _ := debug.ReadBuildInfo()
	return resolvedGoatestVersionFrom(GoatestVersion, info)
}

// resolvedGoatestVersionFrom settles the version from what a binary knows
// about itself.
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
	FuzzExecutions         int
	MutationJobs           int
	Generate               func(context.Context, provider.Request) (provider.Response, error)
	Validator              repair.Validator
	AllowedGenerationPaths []string
	Progress               func(Event)
	// Trace records the diagnostic exhaust of the run: the phases it passed
	// through, the commands it ran, the mutants it executed, and how it routed
	// them. A nil recorder is a run that records nothing, which is what a
	// caller that asked for no trace gets. A trace is never evidence, so it
	// takes no part in the identity a cached result is keyed on.
	Trace *trace.Recorder
	// KeepTemp keeps the temporary directories a run would otherwise remove:
	// the scratch directory a round collects its baseline in, and the isolated
	// tree a generated candidate is validated in. What a run keeps it records
	// as an artifact of the recording, because a directory left behind and
	// never named is litter rather than something a developer can find.
	//
	// Keeping a directory is a debugging aid and never evidence, so it takes no
	// part in the identity a cached result is keyed on.
	KeepTemp bool
	Now      func() time.Time
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
	inspectWorkspace       func(context.Context, CommandWorkspace) (roundMetadata, error)
	assuranceInputs        func(string, string, Options, config.Config, roundMetadata) (evidence.Inputs, string, error)
	digestInputs           func(evidence.Inputs) string
	discoverTargets        func(string, []goanalysis.Package) ([]goanalysis.Target, error)
	selectImpact           func(context.Context, string, goanalysis.Model, []goanalysis.Target, Options) impactSelection
	acquireResources       func(context.Context, config.Config, []goanalysis.Target, []string) (runRoundCloser, []BaselineTarget, []report.Evidence, []string, error)
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

// Run verifies a repository from a frozen snapshot. It repeats from a new
// snapshot after every promoted corpus repair, up to the bounded repair limit.
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
	now := time.Now
	if options.Now != nil {
		now = options.Now
	}
	accepted := activeAcceptance(loaded, now())
	acceptances := activeAcceptanceMetadata(loaded, now())
	cacheStore := dependencies.newCache(filepath.Join(root, ".goatest", "cache"), loaded.Cache)
	var appliedRepairs []report.Repair
	phases := runPhases{recorder: options.Trace}
	defer phases.leave()

	for round := 0; ; round++ {
		phases.enter(phaseSnapshot)
		emit(options, "snapshot", fmt.Sprintf("repair round %d", round+1))
		workspace, err := dependencies.openWorkspace(ctx, root, mutationbridge.Options{
			GoBinary: options.GoBinary, TempDirectory: options.TempDirectory,
			ReportDirectory: ".goatest", Environment: mutationEnvironment(options.Environment, options.BuildTags),
			Trace: options.Trace,
		})
		if err != nil {
			return report.Report{}, err
		}
		metadata, err := dependencies.inspectWorkspace(ctx, workspace)
		if err != nil {
			_ = dependencies.closeWorkspace(workspace)
			return report.Report{}, err
		}
		if !defaultPackagePatterns(options.Packages) || len(options.BuildTags) != 0 {
			selectedModel, selectErr := inspectSelectedPackages(ctx, workspace, options.Packages, options.BuildTags, options.CommandTimeout)
			if selectErr != nil {
				_ = dependencies.closeWorkspace(workspace)
				return report.Report{}, selectErr
			}
			metadata.model = selectedModel
		}
		inputs, digest, err := dependencies.assuranceInputs(root, contract, options, loaded, metadata)
		if err != nil {
			_ = dependencies.closeWorkspace(workspace)
			return report.Report{}, err
		}
		phases.enter(phaseCacheCheck)
		if round == 0 && len(loaded.Resources) == 0 {
			cached, found, cacheErr := cacheStore.Get(digest)
			if cacheErr != nil {
				_ = dependencies.closeWorkspace(workspace)
				return report.Report{}, cacheErr
			}
			if found && cachedAcceptanceValid(cached, accepted) {
				emit(options, "cache-hit", digest)
				if closeErr := dependencies.closeWorkspace(workspace); closeErr != nil {
					return report.Report{}, closeErr
				}
				return cached, nil
			}
		}

		phases.enter(phaseDiscover)
		targets, err := dependencies.discoverTargets(root, metadata.model.Packages)
		if err != nil {
			_ = dependencies.closeWorkspace(workspace)
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
				Toolchain:  report.Toolchain{Go: metadata.toolchain, Goatest: inputs.GoatestVersion, GoMutants: inputs.GoMutantsVersion, OS: runtime.GOOS, Arch: runtime.GOARCH},
				Accounting: report.Accounting{
					Targets: report.CountAccounting{Discovered: len(allTargets), Excluded: len(allTargets)},
					Race:    report.CountAccounting{Discovered: len(metadata.model.Packages), Excluded: len(metadata.model.Packages)},
				},
				Evidence:    []report.Evidence{{Kind: "changeset", ID: "changed-files", Status: "empty"}},
				Acceptances: slices.Clone(acceptances),
				Limitations: projectExcludeLimitations(loaded.Project.Exclude),
			}
			if closeErr := dependencies.closeWorkspace(workspace); closeErr != nil {
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
			_ = dependencies.closeWorkspace(workspace)
			return report.Report{}, err
		}
		var controlMutex sync.Mutex
		var controlWorkspace *mutationbridge.Workspace
		originalControl := func(controlContext context.Context, request gomutants.ExecRequest) (gomutants.CommandResult, error) {
			controlMutex.Lock()
			defer controlMutex.Unlock()
			if controlWorkspace == nil {
				opened, openErr := dependencies.openWorkspace(controlContext, root, mutationbridge.Options{
					GoBinary: options.GoBinary, TempDirectory: options.TempDirectory,
					ReportDirectory: ".goatest", Environment: mutationEnvironment(options.Environment, options.BuildTags),
					Trace: options.Trace,
				})
				if openErr != nil {
					return gomutants.CommandResult{}, openErr
				}
				controlWorkspace = opened
			}
			return runOriginalMutationControl(controlContext, controlWorkspace, request, options.BuildTags)
		}
		closeControl := func() error {
			controlMutex.Lock()
			defer controlMutex.Unlock()
			if controlWorkspace == nil {
				return nil
			}
			return dependencies.closeWorkspace(controlWorkspace)
		}
		closeRound := func() error {
			return errors.Join(closeControl(), manager.Close(), dependencies.closeWorkspace(workspace))
		}

		phases.enter(phaseBaseline)
		artifactDirectory, err := dependencies.makeBaselineScratch(options.TempDirectory, "goatest-baseline-")
		if err != nil {
			_ = closeRound()
			return report.Report{}, fmt.Errorf("goatest: create baseline scratch: %w", err)
		}
		for _, target := range targets {
			emit(options, "baseline-target", target.Name+":"+target.ID)
		}
		baseline, err := dependencies.collectBaseline(ctx, workspace, metadata.model, baselineTargets, BaselineOptions{
			ArtifactDirectory: artifactDirectory, Packages: slices.Clone(options.Packages),
			BuildTags: slices.Clone(options.BuildTags), TestArgs: slices.Clone(options.TestArgs), UseTest2JSON: true,
			ClassifyUserFailures: true,
			CommandTimeout:       options.CommandTimeout, TargetTimeout: options.TargetTimeout,
			Resume: baselineResume, Checkpoint: checkpointController.saveBaseline,
		})
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
				Code: "later-phases-not-run", Summary: "race and mutation phases were not run because baseline verification did not pass",
			})
			if closeErr := closeRound(); closeErr != nil {
				return report.Report{}, closeErr
			}
			if err := cacheStore.Put(digest, baseReport); err != nil {
				return report.Report{}, err
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
			raceResult, err = dependencies.collectRaceWithOptions(ctx, workspace, raceModel, racePackages, contract, RaceOptions{
				Environment: resourceEnv, TestArgs: slices.Clone(options.TestArgs), BuildTags: slices.Clone(options.BuildTags),
			})
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
			if closeErr := closeRound(); closeErr != nil {
				return report.Report{}, closeErr
			}
			if err := cacheStore.Put(digest, baseReport); err != nil {
				return report.Report{}, err
			}
			return baseReport, nil
		}

		phases.enter(phaseMutationPrepare)
		emit(options, "mutation-prepare", contract)
		include, packages := mutationScope(selection)
		if !defaultPackagePatterns(options.Packages) && !options.Changed {
			packages = slices.Clone(options.Packages)
			// The catalog must not reach beyond the resolved package scope:
			// Include selects mutation candidates while Packages only selects
			// test binaries, and a mutant outside every prepared binary would
			// fail its package-suite confirmation instead of being scoped out.
			include = scopedMutationInclude(metadata.model)
		}
		verifyArgv := plannedVerifyArgv(options)
		mutationJobs := mutationJobLimit(options, loaded)
		emit(options, "mutation-jobs", strconv.Itoa(mutationJobs))
		session, err := dependencies.prepareSession(ctx, workspace, mutationbridge.PrepareOptions{
			Contract:  contract,
			Operators: slices.Clone(options.MutationOperators),
			Include:   include,
			Exclude:   slices.Clone(loaded.Project.Exclude),
			Packages:  packages,
			Jobs:      mutationJobs, BuildTimeout: options.CommandTimeout, MutantTimeout: options.CommandTimeout,
			VerifyArgv: verifyArgv, VerifyEnv: resourceEnv, VerifyTimeout: options.CommandTimeout,
			// Replaying one mutant does not pay for a probe tree it would
			// measure against once: its routing is then the pre-probe one,
			// which only ever executes more.
			Probe: options.ReplayMutantID == "",
		})
		if err != nil {
			_ = closeRound()
			return report.Report{}, err
		}
		catalog := session.Catalog()
		mutationCount := mutationTargetCount(catalog, options.ReplayMutantID)
		mutationDetail := fmt.Sprintf("%d mutants", mutationCount)
		if mutationCount == 1 {
			mutationDetail = "1 mutant"
		}
		emit(options, "mutation-target", mutationDetail)

		if options.ReplayMutantID == "" {
			// The probe pass measures which mutants each target could observe
			// at all, and routing discharges a measured target that never made
			// a probed mutant's site differ. What the pass establishes is
			// recorded, so the layer is held to the proofaudit infection layer
			// on every dogfood recording rather than trusted.
			phases.enter(phaseProbe)
			probeCount := probeTargetCount(baseline.Targets)
			probeDetail := fmt.Sprintf("%d targets", probeCount)
			if probeCount == 1 {
				probeDetail = "1 target"
			}
			emit(options, "probe-target", probeDetail)
			probed, probeErr := dependencies.probeTargets(ctx, session, baseline.Targets, ProbeOptions{
				Contract: contract, Timeout: options.CommandTimeout, TestArgs: slices.Clone(options.TestArgs),
				Jobs: mutationJobs, Trace: options.Trace, Progress: probeProgress(options),
			})
			if probeErr != nil {
				_ = closeRound()
				return report.Report{}, probeErr
			}
			// The only later reader of these targets is the mutation phase; the
			// checkpoint keeps a form of its own, written while the baseline ran.
			baseline.Targets = probed.Targets
			emit(options, "probe-summary", fmt.Sprintf("%d measured, %d without facts", probed.Measured, probed.Unmeasured))
		}

		// The evidence of earlier runs is read once, here, where everything a
		// behaviour key is built from is known and before anything is routed.
		// A store that cannot be trusted is dropped rather than believed: the
		// round then executes every mutant and records what it establishes,
		// which replaces what could not be read.
		var mutationEvidence *MutationEvidence
		evidencePath := filepath.Join(root, ".goatest", "cache", mutationEvidenceFileName)
		if mutationEvidenceGuarded(round, loaded, options) {
			mutationStore, _, evidenceErr := dependencies.loadMutationEvidence(evidencePath, metadata.model.ModulePath)
			if evidenceErr != nil {
				emit(options, "mutation-evidence-rejected", evidenceErr.Error())
				mutationStore = evidence.MutationStore{}
			}
			mutationEvidence = newRunMutationEvidence(
				mutationStore,
				newTargetKeySources(inputs, metadata.model, contract, options),
				baseline.Targets, baseline.Inventory, digest,
			)
		}

		phases.enter(phaseMutation)
		mutationResume := checkpointController.mutation(catalog, root)
		mutation, err := dependencies.evaluateMutations(ctx, session, baseline.Targets, MutationOptions{
			Root: root, Snapshot: digest, Contract: contract, NoApply: options.NoApply,
			ReplayMutantID: options.ReplayMutantID,
			TestArgs:       slices.Clone(options.TestArgs),
			FuzzExecutions: options.FuzzExecutions, Timeout: options.CommandTimeout,
			Jobs: mutationJobs, Accepted: accepted,
			Progress: mutationProgress(options),
			Resume:   mutationResume, Checkpoint: checkpointController.saveMutant,
			OriginalControl: originalControl,
			Trace:           options.Trace,
			Instrumented:    baseline.Instrumented,
			Evidence:        mutationEvidence,
		})
		if err != nil {
			_ = closeRound()
			return report.Report{}, err
		}
		checkpointController.completeMutation()
		// The store is written once, now that the phase has established
		// everything it will. A run that cannot write it has still proved
		// everything it claims, so the failure is a note and the next run
		// simply starts cold.
		if mutationEvidence != nil {
			if err := dependencies.saveMutationEvidence(evidencePath, mutationEvidence.store(catalog, metadata.model.ModulePath)); err != nil {
				emit(options, "mutation-evidence-unsaved", err.Error())
			}
		}
		baseReport.Accounting.Mutants = mutation.Accounting
		baseReport.Mutants = slices.Clone(mutation.Mutants)
		baseReport.Resume = checkpointController.resumeMetadata()

		phases.enter(phaseRepair)
		generated := GenerationEvaluation{Findings: slices.Clone(mutation.Findings)}
		if !mutation.Applied {
			generated, err = dependencies.attemptRepairs(ctx, root, mutation.Findings, GenerationOptions{
				Snapshot: digest, NoApply: options.NoApply, Generate: options.Generate,
				Command:             loaded.Generation.Command,
				ProviderEnvironment: generationProviderEnvironment(options.Environment, loaded.Generation.Environment),
				AllowedPaths:        generationPaths(options, loaded), Validator: options.Validator,
				RepositoryValidator: RepositoryValidatorOptions{
					Root: root, Contract: contract, GoBinary: options.GoBinary,
					TempDirectory: options.TempDirectory, Environment: validationEnvironment(executionEnvironment(options.Environment), resourceEnv),
					MutationOperators: options.MutationOperators, Packages: options.Packages,
					BuildTags: options.BuildTags, TestArgs: options.TestArgs, Timeout: options.CommandTimeout,
					Trace: options.Trace, KeepTemp: options.KeepTemp,
				},
			})
			if err != nil {
				_ = closeRound()
				return report.Report{}, err
			}
		}
		phases.enter(phaseFinalize)
		if closeErr := closeRound(); closeErr != nil {
			return report.Report{}, closeErr
		}
		roundRepairs := append(slices.Clone(mutation.Repairs), generated.Repairs...)
		if mutation.Applied || generated.Applied {
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
		if dependencies.digestInputs(inputs) != finalDigest {
			return report.Report{}, fmt.Errorf("goatest: repository changed during verification; refusing stale evidence")
		}
		result := report.Report{
			Schema: report.SchemaV1, Contract: contract, Snapshot: finalDigest,
			Evidence: append(baseReport.Evidence, mutation.Evidence...),
			Findings: generated.Findings, Repairs: append(slices.Clone(appliedRepairs), roundRepairs...),
			Scope: baseReport.Scope, Repository: baseReport.Repository, Toolchain: baseReport.Toolchain,
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
	if hasTestArgument(arguments, "-test.fuzz") && !hasTestArgument(arguments, "-test.fuzzcachedir") {
		cacheDirectory, err := os.MkdirTemp("", "goatest-control-fuzz-")
		if err != nil {
			return gomutants.CommandResult{}, fmt.Errorf("goatest: create original-control fuzz cache: %w", err)
		}
		defer func() { _ = os.RemoveAll(cacheDirectory) }()
		arguments = append(arguments, "-test.fuzzcachedir="+cacheDirectory)
	}
	if len(arguments) != 0 {
		argv = append(argv, "-args")
		argv = append(argv, arguments...)
	}
	return workspace.Exec(ctx, gomutants.Command{
		Argv: argv, Env: slices.Clone(request.Env), Timeout: request.Timeout, OutputLimit: 32 << 20,
	})
}

func hasTestArgument(arguments []string, name string) bool {
	for _, argument := range arguments {
		if argument == name || strings.HasPrefix(argument, name+"=") {
			return true
		}
	}
	return false
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

func inspectWorkspace(ctx context.Context, workspace CommandWorkspace) (roundMetadata, error) {
	version, err := workspace.Exec(ctx, command([]string{"go", "version"}, 30*time.Second))
	if err != nil || version.ExitCode != 0 || version.TimedOut {
		return roundMetadata{}, commandError("go version", version, err)
	}
	listed, err := workspace.Exec(ctx, command([]string{"go", "list", "-json", "./..."}, 5*time.Minute))
	if err != nil || listed.ExitCode != 0 || listed.TimedOut {
		return roundMetadata{}, commandError("go list", listed, err)
	}
	model, err := goanalysis.DecodePackages(bytes.NewReader(listed.Output))
	if err != nil {
		return roundMetadata{}, err
	}
	modules, err := workspace.Exec(ctx, command([]string{"go", "list", "-m", "-json", "all"}, 5*time.Minute))
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
	return roundMetadata{model: model, toolchain: strings.TrimSpace(string(version.Output)), dependencies: dependencies}, nil
}

func inspectSelectedPackages(ctx context.Context, workspace CommandWorkspace, patterns, tags []string, timeout time.Duration) (goanalysis.Model, error) {
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
		return goanalysis.Model{}, commandError("go list selected packages", listed, err)
	}
	model, err := goanalysis.DecodePackages(bytes.NewReader(listed.Output))
	if err != nil {
		return goanalysis.Model{}, err
	}
	return model, nil
}

func defaultPackagePatterns(patterns []string) bool {
	return len(patterns) == 0 || len(patterns) == 1 && patterns[0] == "./..."
}

func command(argv []string, timeout time.Duration) gomutants.Command {
	return gomutants.Command{Argv: slices.Clone(argv), Timeout: timeout, OutputLimit: 32 << 20}
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
	goMutants, err := GoMutantsVersion()
	if err != nil {
		return evidence.Inputs{}, "", err
	}
	environment := executionEnvironment(options.Environment)
	inputs := evidence.Inputs{
		Files: files, Corpus: corpus, Dependencies: metadata.dependencies,
		Toolchain: metadata.toolchain, Platform: runtime.GOOS + "/" + runtime.GOARCH,
		Environment: selectedEnvironment(environment, loaded.Execution.Environment), Resources: resources,
		Contract: contract + modeIdentity(options), GoatestVersion: ResolvedGoatestVersion(), GoMutantsVersion: goMutants,
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
		len(options.BuildTags) != 0 || len(options.MutationOperators) != 0 || options.FuzzExecutions != 0 ||
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
		FuzzExecutions    int
		MutationJobs      int
		CommandTimeout    string
		TargetTimeout     string
	}{
		Packages: slices.Clone(options.Packages), PackageScope: options.PackageScope,
		TestArgs: slices.Clone(options.TestArgs), BuildTags: slices.Clone(options.BuildTags),
		MutationOperators: slices.Clone(options.MutationOperators),
		FuzzExecutions:    options.FuzzExecutions, MutationJobs: options.MutationJobs,
		CommandTimeout: options.CommandTimeout.String(), TargetTimeout: options.TargetTimeout.String(),
	})
	return identity + ";execution=" + string(encoded)
}

func stableEnvironment(environment []string) []string {
	result := make([]string, 0, len(environment))
	for _, entry := range environment {
		key, _, ok := strings.Cut(entry, "=")
		upper := strings.ToUpper(key)
		if !ok || upper == "TMP" || upper == "TEMP" || upper == "TMPDIR" ||
			strings.HasPrefix(upper, "GO_MUTANTS_") || ephemeralEnvironmentKey(upper) {
			continue
		}
		result = append(result, entry)
	}
	slices.Sort(result)
	return result
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
			for suffix := candidate; ; {
				if matched, _ := path.Match(remainder, suffix); matched {
					return true
				}
				_, next, found := strings.Cut(suffix, "/")
				if !found {
					break
				}
				suffix = next
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
	if len(target.Capabilities) != 0 {
		return slices.Clone(target.Capabilities)
	}
	if target.Capability != "" {
		return []string{target.Capability}
	}
	return nil
}

// mutationJobLimit decides the mutation parallelism. An exclusive resource
// serializes everything it touches, an explicit choice is respected as made,
// and only the default derived from the machine is capped, so that a wide host
// does not silently thrash the test suites it runs four of at a time.
func mutationJobLimit(options Options, loaded config.Config) int {
	for _, spec := range loaded.Resources {
		if spec.Exclusive {
			return 1
		}
	}
	if options.MutationJobs > 0 {
		return options.MutationJobs
	}
	return max(1, min(runtime.GOMAXPROCS(0), 4))
}

func mutationProgress(options Options) func(completed, total int) {
	return func(completed, total int) {
		step := max(1, (total+99)/100)
		if completed == 1 || completed == total || completed%step == 0 {
			emit(options, "mutation-progress", fmt.Sprintf("%d/%d", completed, total))
		}
	}
}

// probeProgress reports the probe pass the way the mutation phase reports
// itself, so a watcher reads one kind of progress line through both.
func probeProgress(options Options) func(completed, total int) {
	return func(completed, total int) {
		step := max(1, (total+99)/100)
		if completed == 1 || completed == total || completed%step == 0 {
			emit(options, "probe-progress", fmt.Sprintf("%d/%d", completed, total))
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
	// Snapshots exclude every .git by design and their identity is the
	// assurance digest, so VCS stamping has nothing true to stamp — while a
	// stray .git above the temporary root turns it into a hard failure for
	// every go command in the snapshot.
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

// emit reports one progress note. The caller's callback and the trace are
// independent destinations: a run records its notes whether or not anybody
// asked to be told them.
func emit(options Options, kind, detail string) {
	if options.Progress != nil {
		options.Progress(Event{Kind: kind, Detail: detail})
	}
	options.Trace.Progress(kind, detail)
}
