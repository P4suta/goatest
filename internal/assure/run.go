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
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	gomutants "github.com/P4suta/go-mutants"
	"github.com/P4suta/goatest/internal/config"
	"github.com/P4suta/goatest/internal/evidence"
	goanalysis "github.com/P4suta/goatest/internal/golang"
	"github.com/P4suta/goatest/internal/mutationbridge"
	"github.com/P4suta/goatest/internal/provider"
	"github.com/P4suta/goatest/internal/repair"
	"github.com/P4suta/goatest/internal/report"
	"github.com/P4suta/goatest/internal/resource"
)

const (
	GoMutantsVersion = "v0.1.3-0.20260830081807-df41b68c0c1e"
	maximumRounds    = 3
)

// GoatestVersion is stamped by release builds and participates in evidence
// cache identity.
var GoatestVersion = "v0.1.0-dev"

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
	GoBinary               string
	Environment            []string
	TempDirectory          string
	MutationOperators      []string
	FuzzExecutions         int
	MutationJobs           int
	Generate               func(context.Context, provider.Request) (provider.Response, error)
	Validator              repair.Validator
	AllowedGenerationPaths []string
	Progress               func(Event)
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
}

type runRoundCloser interface {
	Close() error
}

type runResourceManager interface {
	runRoundCloser
	AcquireEnvironment(context.Context, string) ([]string, error)
}

type runDependencies struct {
	repositoryRoot        func(string) (string, error)
	loadConfig            func(string) (config.Config, error)
	newCache              func(string) runCache
	openWorkspace         func(context.Context, string, mutationbridge.Options) (*mutationbridge.Workspace, error)
	closeWorkspace        func(*mutationbridge.Workspace) error
	inspectWorkspace      func(context.Context, CommandWorkspace) (roundMetadata, error)
	assuranceInputs       func(string, string, Options, config.Config, roundMetadata) (evidence.Inputs, string, error)
	digestInputs          func(evidence.Inputs) string
	discoverTargets       func(string, []goanalysis.Package) ([]goanalysis.Target, error)
	selectImpact          func(context.Context, string, goanalysis.Model, []goanalysis.Target, Options) impactSelection
	acquireResources      func(context.Context, config.Config, []goanalysis.Target) (runRoundCloser, []BaselineTarget, []report.Evidence, []string, error)
	makeBaselineScratch   func(string, string) (string, error)
	removeBaselineScratch func(string) error
	collectBaseline       func(context.Context, CommandWorkspace, goanalysis.Model, []BaselineTarget, BaselineOptions) (BaselineResult, error)
	concurrencyPackages   func(string, []goanalysis.Package) ([]string, error)
	relevantRacePackages  func(goanalysis.Model, []string, []TargetEvidence) []string
	collectRace           func(context.Context, CommandWorkspace, goanalysis.Model, []string, string, []string) (RaceResult, error)
	prepareSession        func(context.Context, *mutationbridge.Workspace, mutationbridge.PrepareOptions) (MutationSession, error)
	evaluateMutations     func(context.Context, MutationSession, []TargetEvidence, MutationOptions) (MutationEvaluation, error)
	attemptRepairs        func(context.Context, string, []report.Finding, GenerationOptions) (GenerationEvaluation, error)
	buildGraph            func(string, goanalysis.Model, []TargetEvidence) (evidence.Graph, error)
	mergeGraph            func(evidence.Graph, *evidence.GraphRecord, impactSelection) evidence.Graph
	saveGraph             func(string, evidence.GraphRecord) error
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
	contract := options.Contract
	if contract == "" {
		contract = loaded.Contract
	}
	if contract != "standard-v1" && contract != "deep-v1" {
		return report.Report{}, fmt.Errorf("goatest: contract %q is unknown", contract)
	}
	now := time.Now
	if options.Now != nil {
		now = options.Now
	}
	accepted := activeAcceptance(loaded, now())
	cacheStore := dependencies.newCache(filepath.Join(root, ".goatest", "cache"))
	var appliedRepairs []report.Repair

	for round := 0; ; round++ {
		emit(options, "snapshot", fmt.Sprintf("repair round %d", round+1))
		workspace, err := dependencies.openWorkspace(ctx, root, mutationbridge.Options{
			GoBinary: options.GoBinary, TempDirectory: options.TempDirectory,
			ReportDirectory: ".goatest", Environment: executionEnvironment(options.Environment),
		})
		if err != nil {
			return report.Report{}, err
		}
		metadata, err := dependencies.inspectWorkspace(ctx, workspace)
		if err != nil {
			_ = dependencies.closeWorkspace(workspace)
			return report.Report{}, err
		}
		inputs, digest, err := dependencies.assuranceInputs(root, contract, options, loaded, metadata)
		if err != nil {
			_ = dependencies.closeWorkspace(workspace)
			return report.Report{}, err
		}
		if round == 0 {
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

		targets, err := dependencies.discoverTargets(root, metadata.model.Packages)
		if err != nil {
			_ = dependencies.closeWorkspace(workspace)
			return report.Report{}, err
		}
		selection := dependencies.selectImpact(ctx, root, metadata.model, targets, options)
		if options.Changed {
			if selection.broad {
				emit(options, "impact-broad", "dependency or prior evidence was unknown")
			} else {
				emit(options, "impact-targeted", fmt.Sprintf("%d of %d targets", len(selection.targets), len(targets)))
			}
		}
		targets = selection.targets
		manager, baselineTargets, resourceEvidence, resourceEnv, err := dependencies.acquireResources(ctx, loaded, targets)
		if err != nil {
			_ = dependencies.closeWorkspace(workspace)
			return report.Report{}, err
		}
		closeRound := func() error { return errors.Join(manager.Close(), dependencies.closeWorkspace(workspace)) }

		artifactDirectory, err := dependencies.makeBaselineScratch(options.TempDirectory, "goatest-baseline-")
		if err != nil {
			_ = closeRound()
			return report.Report{}, fmt.Errorf("goatest: create baseline scratch: %w", err)
		}
		for _, target := range targets {
			emit(options, "baseline-target", target.Name+":"+target.ID)
		}
		baseline, err := dependencies.collectBaseline(ctx, workspace, metadata.model, baselineTargets, BaselineOptions{ArtifactDirectory: artifactDirectory})
		removeErr := dependencies.removeBaselineScratch(artifactDirectory)
		if err != nil || removeErr != nil {
			_ = closeRound()
			return report.Report{}, errors.Join(err, removeErr)
		}
		baseReport := report.Report{
			Schema: report.SchemaV1, Contract: contract, Snapshot: digest,
			Evidence: append(resourceEvidence, baseline.Evidence...), Findings: baseline.Findings,
		}
		if len(baseline.Findings) != 0 {
			baseReport.Verdict = baselineVerdict(baseline.Findings)
			if closeErr := closeRound(); closeErr != nil {
				return report.Report{}, closeErr
			}
			if err := cacheStore.Put(digest, baseReport); err != nil {
				return report.Report{}, err
			}
			return baseReport, nil
		}
		concurrentPackages, err := dependencies.concurrencyPackages(root, metadata.model.Packages)
		if err != nil {
			_ = closeRound()
			return report.Report{}, err
		}
		racePackages := dependencies.relevantRacePackages(metadata.model, concurrentPackages, baseline.Targets)
		raceCount := len(racePackages)
		if contract == "deep-v1" {
			raceCount = len(metadata.model.Packages)
		}
		emit(options, "race", fmt.Sprintf("%d packages", raceCount))
		raceResult, err := dependencies.collectRace(ctx, workspace, metadata.model, racePackages, contract, resourceEnv)
		if err != nil {
			_ = closeRound()
			return report.Report{}, err
		}
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

		emit(options, "mutation-prepare", contract)
		include, packages := mutationScope(selection)
		session, err := dependencies.prepareSession(ctx, workspace, mutationbridge.PrepareOptions{
			Contract: contract, Operators: slices.Clone(options.MutationOperators),
			Include: include, Packages: packages,
			VerifyArgv: []string{"go", "test", "-run=^$", "./..."}, VerifyEnv: resourceEnv,
		})
		if err != nil {
			_ = closeRound()
			return report.Report{}, err
		}
		emit(options, "mutation-target", fmt.Sprintf("%d mutants", len(session.Catalog().Mutants)))
		mutation, err := dependencies.evaluateMutations(ctx, session, baseline.Targets, MutationOptions{
			Root: root, Contract: contract, NoApply: options.NoApply,
			FuzzExecutions: options.FuzzExecutions, Jobs: mutationJobLimit(options, loaded), Accepted: accepted,
			Progress: mutationProgress(options),
		})
		if err != nil {
			_ = closeRound()
			return report.Report{}, err
		}
		generated := GenerationEvaluation{Findings: slices.Clone(mutation.Findings)}
		if !mutation.Applied {
			generated, err = dependencies.attemptRepairs(ctx, root, mutation.Findings, GenerationOptions{
				Snapshot: digest, NoApply: options.NoApply, Generate: options.Generate,
				Command:      loaded.Generation.Command,
				AllowedPaths: generationPaths(options, loaded), Validator: options.Validator,
				RepositoryValidator: RepositoryValidatorOptions{
					Root: root, Contract: contract, GoBinary: options.GoBinary,
					TempDirectory: options.TempDirectory, Environment: validationEnvironment(executionEnvironment(options.Environment), resourceEnv),
					MutationOperators: options.MutationOperators,
				},
			})
			if err != nil {
				_ = closeRound()
				return report.Report{}, err
			}
		}
		if closeErr := closeRound(); closeErr != nil {
			return report.Report{}, closeErr
		}
		roundRepairs := append(slices.Clone(mutation.Repairs), generated.Repairs...)
		if mutation.Applied || generated.Applied {
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

		finalInputs, finalDigest, err := dependencies.assuranceInputs(root, contract, options, loaded, metadata)
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
		}
		_ = finalInputs
		if len(result.Findings) == 0 {
			result.Verdict = report.VerdictAssured
		} else {
			result.Verdict = report.VerdictInsufficient
			result.ResidualRisks = []string{"unresolved mutation evidence gaps remain"}
		}
		if result.Verdict == report.VerdictAssured {
			currentGraph, graphErr := dependencies.buildGraph(root, metadata.model, baseline.Targets)
			if graphErr != nil {
				return report.Report{}, graphErr
			}
			currentGraph = dependencies.mergeGraph(currentGraph, selection.prior, selection)
			if graphErr := dependencies.saveGraph(filepath.Join(root, ".goatest", "graph-v1.json"), evidence.GraphRecord{
				ModulePath: metadata.model.ModulePath, Graph: currentGraph,
			}); graphErr != nil {
				return report.Report{}, graphErr
			}
		}
		if err := cacheStore.Put(finalDigest, result); err != nil {
			return report.Report{}, err
		}
		return result, nil
	}
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
	dependencies, err := dependencyDigests(modules.Output)
	if err != nil {
		return roundMetadata{}, err
	}
	return roundMetadata{model: model, toolchain: strings.TrimSpace(string(version.Output)), dependencies: dependencies}, nil
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
			Command   []string
			Timeout   string
			Shared    bool
			Exclusive bool
		}{spec.Command, spec.Timeout.String(), spec.Shared, spec.Exclusive})
		sum := sha256.Sum256(encoded)
		resources[name] = hex.EncodeToString(sum[:])
	}
	environment := executionEnvironment(options.Environment)
	inputs := evidence.Inputs{
		Files: files, Corpus: corpus, Dependencies: metadata.dependencies,
		Toolchain: metadata.toolchain, Platform: runtime.GOOS + "/" + runtime.GOARCH,
		Environment: stableEnvironment(environment), Resources: resources,
		Contract: contract + modeIdentity(options), GoatestVersion: GoatestVersion, GoMutantsVersion: GoMutantsVersion,
	}
	return inputs, evidence.Digest(inputs), nil
}

func modeIdentity(options Options) string {
	return fmt.Sprintf(";apply=%t;changed=%t;ref=%s", !options.NoApply, options.Changed, options.ChangedRef)
}

func stableEnvironment(environment []string) []string {
	result := make([]string, 0, len(environment))
	for _, entry := range environment {
		key, _, ok := strings.Cut(entry, "=")
		upper := strings.ToUpper(key)
		if !ok || upper == "TMP" || upper == "TEMP" || upper == "TMPDIR" || strings.HasPrefix(upper, "GO_MUTANTS_") {
			continue
		}
		result = append(result, entry)
	}
	slices.Sort(result)
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

func cachedAcceptanceValid(cached report.Report, accepted map[string]bool) bool {
	for _, item := range cached.Evidence {
		if item.Kind == "mutation" && item.Status == "accepted" && !accepted[item.Detail] {
			return false
		}
	}
	return true
}

func acquireResources(ctx context.Context, loaded config.Config, targets []goanalysis.Target) (runResourceManager, []BaselineTarget, []report.Evidence, []string, error) {
	specs := make(map[string]resource.Spec, len(loaded.Resources))
	for name, spec := range loaded.Resources {
		specs[name] = resource.Spec{Command: spec.Command, Timeout: spec.Timeout, Shared: spec.Shared, Exclusive: spec.Exclusive}
	}
	manager := newRunResourceManager(specs)
	capabilities := make(map[string]struct{})
	for _, target := range targets {
		if target.Capability != "" {
			capabilities[target.Capability] = struct{}{}
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
		baseline[i] = BaselineTarget{Target: target, Environment: slices.Clone(environments[target.Capability])}
	}
	return manager, baseline, evidenceItems, allEnvironment, nil
}

func mutationJobLimit(options Options, loaded config.Config) int {
	for _, spec := range loaded.Resources {
		if spec.Exclusive {
			return 1
		}
	}
	jobs := options.MutationJobs
	if jobs <= 0 {
		jobs = runtime.GOMAXPROCS(0)
	}
	return max(1, min(jobs, 4))
}

func mutationProgress(options Options) func(completed, total int) {
	return func(completed, total int) {
		step := max(1, (total+99)/100)
		if completed == 1 || completed == total || completed%step == 0 {
			emit(options, "mutation-progress", fmt.Sprintf("%d/%d", completed, total))
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
		if finding.Kind == "baseline-failure" || finding.Kind == "baseline-timeout" {
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
		values[upper] = value
		names[upper] = key
	}
	for key, value := range map[string]string{
		"GOPROXY": "off", "GOSUMDB": "off", "GOTELEMETRY": "off", "GOTOOLCHAIN": "local",
	} {
		values[key] = value
		names[key] = key
	}
	result := make([]string, 0, len(values))
	for upper, value := range values {
		result = append(result, names[upper]+"="+value)
	}
	slices.Sort(result)
	return result
}

func emit(options Options, kind, detail string) {
	if options.Progress != nil {
		options.Progress(Event{Kind: kind, Detail: detail})
	}
}
