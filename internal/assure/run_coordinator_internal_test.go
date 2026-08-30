// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package assure

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	gomutants "github.com/P4suta/go-mutants"
	"github.com/P4suta/goatest/internal/config"
	"github.com/P4suta/goatest/internal/evidence"
	goanalysis "github.com/P4suta/goatest/internal/golang"
	"github.com/P4suta/goatest/internal/mutationbridge"
	"github.com/P4suta/goatest/internal/report"
)

type coordinatorCache struct {
	getReport report.Report
	found     bool
	getErr    error
	putErr    error
	gets      []string
	puts      []report.Report
}

func (cache *coordinatorCache) Get(digest string) (report.Report, bool, error) {
	cache.gets = append(cache.gets, digest)
	return cache.getReport, cache.found, cache.getErr
}

func (cache *coordinatorCache) Put(_ string, value report.Report) error {
	cache.puts = append(cache.puts, value)
	return cache.putErr
}

type coordinatorCloser struct {
	err   error
	calls int
}

func (closer *coordinatorCloser) Close() error { closer.calls++; return closer.err }

type runCoordinatorHarness struct {
	t            *testing.T
	root         string
	dependencies runDependencies
	loaded       config.Config
	cache        *coordinatorCache
	manager      *coordinatorCloser
	metadata     roundMetadata
	targets      []goanalysis.Target
	baseline     BaselineResult
	race         RaceResult
	mutation     MutationEvaluation
	generation   GenerationEvaluation
	catalog      gomutants.Catalog
	inputs       evidence.Inputs
	digest       string
	events       []Event

	openCalls       int
	workspaceCloses int
	inputCalls      int
	discoverCalls   int
	resourceCalls   int
	baselineCalls   int
	raceCalls       int
	prepareCalls    int
	mutationCalls   int
	generationCalls int
	buildGraphCalls int
	mergeGraphCalls int
	saveGraphCalls  int

	preparedOptions   mutationbridge.PrepareOptions
	mutationOptions   MutationOptions
	generationOptions GenerationOptions
	racePackages      []string
	raceOptions       RaceOptions
	baselineOptions   BaselineOptions
}

func newRunCoordinatorHarness(t *testing.T) *runCoordinatorHarness {
	t.Helper()
	root := t.TempDir()
	target := goanalysis.Target{ID: "target-a", Name: "TestValue", Kind: goanalysis.KindTest, Package: "fixture.example/module", RelativeDir: "."}
	harness := &runCoordinatorHarness{
		t:       t,
		root:    root,
		loaded:  config.Config{Contract: "standard-v1"},
		cache:   &coordinatorCache{},
		manager: &coordinatorCloser{},
		metadata: roundMetadata{model: goanalysis.Model{
			ModulePath: "fixture.example/module",
			Packages: []goanalysis.Package{
				{ImportPath: "fixture.example/module", RelativeDir: "."},
				{ImportPath: "fixture.example/other", RelativeDir: "other"},
			},
		}, toolchain: "go version go1.26.6", dependencies: map[string]string{}},
		targets: []goanalysis.Target{target},
		baseline: BaselineResult{
			Evidence: []report.Evidence{{Kind: "baseline", ID: "baseline-a", Status: "passed"}},
			Targets:  []TargetEvidence{{Target: target, CoveredFiles: []string{"value.go"}, Duration: time.Second}},
		},
		race:       RaceResult{Evidence: []report.Evidence{{Kind: "race", ID: "race-a", Status: "passed"}}},
		mutation:   MutationEvaluation{Evidence: []report.Evidence{{Kind: "mutation", ID: "mutant-a", Status: "killed"}}},
		generation: GenerationEvaluation{},
		catalog:    gomutants.Catalog{Mutants: []gomutants.Mutant{{ID: "mutant-a", Accepted: true}}},
		inputs:     evidence.Inputs{Contract: "digest-a"},
		digest:     "digest-a",
	}
	harness.dependencies = runDependencies{
		repositoryRoot: func(root string) (string, error) {
			if root != harness.root {
				t.Fatalf("repository root input = %q", root)
			}
			return harness.root, nil
		},
		loadConfig: func(root string) (config.Config, error) {
			if root != harness.root {
				t.Fatalf("config root = %q", root)
			}
			return harness.loaded, nil
		},
		newCache: func(path string, _ config.Cache) runCache {
			if path != filepath.Join(harness.root, ".goatest", "cache") {
				t.Fatalf("cache path = %q", path)
			}
			return harness.cache
		},
		openWorkspace: func(_ context.Context, root string, options mutationbridge.Options) (*mutationbridge.Workspace, error) {
			harness.openCalls++
			if root != harness.root || options.ReportDirectory != ".goatest" {
				t.Fatalf("open workspace = %q %+v", root, options)
			}
			return nil, nil
		},
		closeWorkspace: func(*mutationbridge.Workspace) error {
			harness.workspaceCloses++
			return nil
		},
		inspectWorkspace: func(context.Context, CommandWorkspace) (roundMetadata, error) { return harness.metadata, nil },
		assuranceInputs: func(root, contract string, _ Options, _ config.Config, metadata roundMetadata) (evidence.Inputs, string, error) {
			harness.inputCalls++
			if root != harness.root || contract == "" || !reflect.DeepEqual(metadata, harness.metadata) {
				t.Fatalf("assurance input = %q %q %+v", root, contract, metadata)
			}
			return harness.inputs, harness.digest, nil
		},
		digestInputs: func(inputs evidence.Inputs) string {
			if !reflect.DeepEqual(inputs, harness.inputs) {
				t.Fatalf("digest inputs = %+v", inputs)
			}
			return harness.digest
		},
		discoverTargets: func(root string, packages []goanalysis.Package) ([]goanalysis.Target, error) {
			harness.discoverCalls++
			if root != harness.root || !reflect.DeepEqual(packages, harness.metadata.model.Packages) {
				t.Fatalf("discover targets = %q %+v", root, packages)
			}
			return slices.Clone(harness.targets), nil
		},
		selectImpact: func(_ context.Context, _ string, _ goanalysis.Model, targets []goanalysis.Target, _ Options) impactSelection {
			return impactSelection{targets: slices.Clone(targets), broad: true}
		},
		acquireResources: func(_ context.Context, _ config.Config, targets []goanalysis.Target, _ []string) (runRoundCloser, []BaselineTarget, []report.Evidence, []string, error) {
			harness.resourceCalls++
			baselineTargets := make([]BaselineTarget, len(targets))
			for index, target := range targets {
				baselineTargets[index] = BaselineTarget{Target: target, Environment: []string{"DB=ready"}}
			}
			return harness.manager, baselineTargets, []report.Evidence{{Kind: "resource", ID: "db", Status: "ready"}}, []string{"DB=ready"}, nil
		},
		makeBaselineScratch: func(parent, pattern string) (string, error) {
			if pattern != "goatest-baseline-" {
				t.Fatalf("scratch pattern = %q", pattern)
			}
			return filepath.Join(parent, "baseline-scratch"), nil
		},
		removeBaselineScratch: func(string) error { return nil },
		collectBaseline: func(_ context.Context, _ CommandWorkspace, model goanalysis.Model, targets []BaselineTarget, options BaselineOptions) (BaselineResult, error) {
			harness.baselineCalls++
			harness.baselineOptions = options
			if !reflect.DeepEqual(model, harness.metadata.model) || len(targets) != len(harness.targets) {
				t.Fatalf("baseline input = %+v %+v", model, targets)
			}
			return harness.baseline, nil
		},
		concurrencyPackages: func(root string, packages []goanalysis.Package) ([]string, error) {
			if root != harness.root || !reflect.DeepEqual(packages, harness.metadata.model.Packages) {
				t.Fatalf("concurrency input = %q %+v", root, packages)
			}
			return []string{"fixture.example/module"}, nil
		},
		relevantRacePackages: func(goanalysis.Model, []string, []TargetEvidence) []string {
			return []string{"fixture.example/module"}
		},
		collectRaceWithOptions: func(_ context.Context, _ CommandWorkspace, _ goanalysis.Model, packages []string, _ string, options RaceOptions) (RaceResult, error) {
			harness.raceCalls++
			harness.racePackages = slices.Clone(packages)
			harness.raceOptions = options
			if !slices.Equal(options.Environment, []string{"DB=ready"}) {
				t.Fatalf("race options = %+v", options)
			}
			return harness.race, nil
		},
		prepareSession: func(_ context.Context, _ *mutationbridge.Workspace, options mutationbridge.PrepareOptions) (MutationSession, error) {
			harness.prepareCalls++
			harness.preparedOptions = options
			return &mutationUnitSession{catalog: harness.catalog}, nil
		},
		evaluateMutations: func(_ context.Context, _ MutationSession, _ []TargetEvidence, options MutationOptions) (MutationEvaluation, error) {
			harness.mutationCalls++
			harness.mutationOptions = options
			return harness.mutation, nil
		},
		attemptRepairs: func(_ context.Context, _ string, _ []report.Finding, options GenerationOptions) (GenerationEvaluation, error) {
			harness.generationCalls++
			harness.generationOptions = options
			return harness.generation, nil
		},
		buildGraph: func(_ string, _ goanalysis.Model, _ []TargetEvidence) (evidence.Graph, error) {
			harness.buildGraphCalls++
			return evidence.Graph{FilePackages: map[string]string{"value.go": "fixture.example/module"}}, nil
		},
		mergeGraph: func(graph evidence.Graph, _ *evidence.GraphRecord, _ impactSelection) evidence.Graph {
			harness.mergeGraphCalls++
			return graph
		},
		saveGraph: func(path string, record evidence.GraphRecord) error {
			harness.saveGraphCalls++
			if path != filepath.Join(harness.root, ".goatest", "graph-v1.json") || record.ModulePath != harness.metadata.model.ModulePath {
				t.Fatalf("saved graph = %q %+v", path, record)
			}
			return nil
		},
	}
	return harness
}

func (harness *runCoordinatorHarness) run(options Options) (report.Report, error) {
	options.Root = harness.root
	options.Progress = func(event Event) { harness.events = append(harness.events, event) }
	return runWithDependencies(harness.t.Context(), options, harness.dependencies)
}

func TestRunCoordinatorEstablishesAssuranceAndPassesExactRoundOptions(t *testing.T) {
	harness := newRunCoordinatorHarness(t)
	harness.catalog.Mutants = append(harness.catalog.Mutants, gomutants.Mutant{ID: "mutant-b", Accepted: true})
	now := time.Date(2026, 8, 30, 0, 0, 0, 0, time.UTC)
	harness.loaded.Acceptance = []config.Acceptance{{ID: "accepted-a", Expires: now.Add(time.Hour)}}
	harness.loaded.Project.Exclude = []string{"generated/**"}
	result, err := harness.run(Options{
		Now: func() time.Time { return now }, NoApply: true, GoBinary: "go-custom", TempDirectory: "scratch-parent",
		Environment: []string{"A=1"}, MutationOperators: []string{"comparison"}, FuzzExecutions: 123, MutationJobs: 3,
		CommandTimeout: 7 * time.Minute,
		Changed:        true, ChangedRef: "origin/main", ReplayMutantID: "mutant-a",
	})
	if err != nil || result.Verdict != report.VerdictAssured || result.Schema != report.SchemaV1 || result.Contract != "standard-v1" || result.Snapshot != harness.digest ||
		len(result.Evidence) != 4 || len(result.Findings) != 0 || len(harness.cache.puts) != 1 || harness.workspaceCloses != 1 || harness.manager.calls != 1 ||
		harness.openCalls != 1 || harness.inputCalls != 2 || harness.discoverCalls != 1 || harness.resourceCalls != 1 || harness.baselineCalls != 1 || harness.raceCalls != 1 ||
		harness.prepareCalls != 1 || harness.mutationCalls != 1 || harness.generationCalls != 1 || harness.buildGraphCalls != 1 || harness.mergeGraphCalls != 1 || harness.saveGraphCalls != 1 {
		t.Fatalf("result = (%+v, %v), harness=%+v", result, err, harness)
	}
	if harness.preparedOptions.Contract != "standard-v1" || !slices.Equal(harness.preparedOptions.Operators, []string{"comparison"}) ||
		!slices.Equal(harness.preparedOptions.Exclude, []string{"generated/**"}) ||
		harness.preparedOptions.Jobs != 3 || harness.preparedOptions.BuildTimeout != 7*time.Minute ||
		harness.preparedOptions.MutantTimeout != 7*time.Minute || harness.preparedOptions.VerifyTimeout != 7*time.Minute ||
		!slices.Equal(harness.preparedOptions.VerifyArgv, []string{"go", "test", "-run=^$", "./..."}) || !slices.Equal(harness.preparedOptions.VerifyEnv, []string{"DB=ready"}) {
		t.Fatalf("prepare options = %+v", harness.preparedOptions)
	}
	if harness.mutationOptions.Root != harness.root || harness.mutationOptions.Contract != "standard-v1" || !harness.mutationOptions.NoApply ||
		harness.mutationOptions.FuzzExecutions != 123 || harness.mutationOptions.Timeout != 7*time.Minute || harness.mutationOptions.Jobs != 3 || harness.mutationOptions.ReplayMutantID != "mutant-a" ||
		!harness.mutationOptions.Accepted["accepted-a"] || harness.mutationOptions.Progress == nil {
		t.Fatalf("mutation options = %+v", harness.mutationOptions)
	}
	if harness.generationOptions.Snapshot != harness.digest || !harness.generationOptions.NoApply || harness.generationOptions.RepositoryValidator.Root != harness.root ||
		harness.generationOptions.RepositoryValidator.Contract != "standard-v1" || harness.generationOptions.RepositoryValidator.GoBinary != "go-custom" {
		t.Fatalf("generation options = %+v", harness.generationOptions)
	}
	wantKinds := []string{"snapshot", "impact-broad", "baseline-target", "race", "mutation-prepare", "mutation-target"}
	gotKinds := make([]string, len(harness.events))
	for index, event := range harness.events {
		gotKinds[index] = event.Kind
	}
	if !slices.Equal(gotKinds, wantKinds) || harness.events[0].Detail != "repair round 1" || harness.events[3].Detail != "1 packages" || harness.events[5].Detail != "1 mutant" {
		t.Fatalf("events = %+v", harness.events)
	}
}

func TestMutationTargetCountIncludesOnlyExecutableMutants(t *testing.T) {
	t.Parallel()
	catalog := gomutants.Catalog{Mutants: []gomutants.Mutant{
		{ID: "selected-a", Accepted: true},
		{ID: "changed-out", Accepted: false},
		{ID: "selected-b", Accepted: true},
		{ID: "selected-c", Accepted: true},
	}}
	if got := mutationTargetCount(catalog, ""); got != 3 {
		t.Fatalf("mutationTargetCount = %d, want 3", got)
	}
	if got := mutationTargetCount(catalog, "selected-a"); got != 1 {
		t.Fatalf("replay mutationTargetCount = %d, want 1", got)
	}
}

func TestRunCoordinatorRejectsRootConfigAndContractFailures(t *testing.T) {
	cause := errors.New("startup failed")
	for _, test := range []struct {
		name   string
		change func(*runCoordinatorHarness, *Options)
	}{
		{name: "root", change: func(h *runCoordinatorHarness, _ *Options) {
			h.dependencies.repositoryRoot = func(string) (string, error) { return "", cause }
		}},
		{name: "config", change: func(h *runCoordinatorHarness, _ *Options) {
			h.dependencies.loadConfig = func(string) (config.Config, error) { return config.Config{}, cause }
		}},
		{name: "unknown option contract", change: func(_ *runCoordinatorHarness, options *Options) { options.Contract = "unknown" }},
		{name: "unknown config contract", change: func(h *runCoordinatorHarness, _ *Options) { h.loaded.Contract = "unknown" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			harness := newRunCoordinatorHarness(t)
			options := Options{}
			test.change(harness, &options)
			result, err := harness.run(options)
			if err == nil || !reflect.DeepEqual(result, report.Report{}) || harness.openCalls != 0 {
				t.Fatalf("run = (%+v, %v), opens=%d", result, err, harness.openCalls)
			}
			if (test.name == "root" || test.name == "config") && !errors.Is(err, cause) {
				t.Fatalf("error = %v, want cause %v", err, cause)
			}
			if strings.Contains(test.name, "contract") && !strings.Contains(err.Error(), "contract") {
				t.Fatalf("contract error = %v", err)
			}
		})
	}
	for _, contract := range []string{"standard-v1", "deep-v1"} {
		t.Run(contract, func(t *testing.T) {
			harness := newRunCoordinatorHarness(t)
			result, err := harness.run(Options{Contract: contract})
			if err != nil || result.Contract != contract {
				t.Fatalf("run = (%+v, %v)", result, err)
			}
		})
	}
}

func TestRunCoordinatorCacheHitRequiresCurrentAcceptanceAndClosesSnapshot(t *testing.T) {
	now := time.Date(2026, 8, 30, 0, 0, 0, 0, time.UTC)
	cached := report.Report{Schema: report.SchemaV1, Verdict: report.VerdictAssured, Snapshot: "cached"}
	t.Run("valid", func(t *testing.T) {
		harness := newRunCoordinatorHarness(t)
		harness.cache.found, harness.cache.getReport = true, cached
		result, err := harness.run(Options{Now: func() time.Time { return now }})
		if err != nil || !reflect.DeepEqual(result, cached) || harness.workspaceCloses != 1 || harness.discoverCalls != 0 ||
			!reflect.DeepEqual(harness.events, []Event{{Kind: "snapshot", Detail: "repair round 1"}, {Kind: "cache-hit", Detail: harness.digest}}) {
			t.Fatalf("cache hit = (%+v, %v), harness=%+v", result, err, harness)
		}
	})
	t.Run("expired acceptance", func(t *testing.T) {
		harness := newRunCoordinatorHarness(t)
		harness.cache.found = true
		harness.cache.getReport = report.Report{Verdict: report.VerdictAssured, Evidence: []report.Evidence{{Kind: "mutation", Status: "accepted", Detail: "finding-a"}}}
		result, err := harness.run(Options{Now: func() time.Time { return now }})
		if err != nil || result.Snapshot != harness.digest || harness.discoverCalls != 1 {
			t.Fatalf("expired cache = (%+v, %v), discovers=%d", result, err, harness.discoverCalls)
		}
	})
	t.Run("expired unused acceptance metadata", func(t *testing.T) {
		harness := newRunCoordinatorHarness(t)
		harness.cache.found = true
		harness.cache.getReport = report.Report{
			Verdict:     report.VerdictAssured,
			Acceptances: []report.Acceptance{{ID: "expired-unused", Reason: "reviewed", Expires: now.Add(-time.Hour).Format(time.RFC3339)}},
		}
		result, err := harness.run(Options{Now: func() time.Time { return now }})
		if err != nil || result.Snapshot != harness.digest || harness.discoverCalls != 1 {
			t.Fatalf("expired unused acceptance cache = (%+v, %v), discovers=%d", result, err, harness.discoverCalls)
		}
	})
	t.Run("configured resources disable cache reuse", func(t *testing.T) {
		harness := newRunCoordinatorHarness(t)
		harness.loaded.Resources = map[string]config.Resource{"db": {Command: []string{"provider"}}}
		harness.cache.found, harness.cache.getReport = true, cached
		result, err := harness.run(Options{})
		if err != nil || result.Snapshot != harness.digest || len(harness.cache.gets) != 0 || harness.discoverCalls != 1 {
			t.Fatalf("resource cache policy = (%+v, %v), harness=%+v", result, err, harness)
		}
		found := false
		for _, limitation := range result.Limitations {
			found = found || limitation.Code == "resource-cache-disabled"
		}
		if !found {
			t.Fatalf("resource cache limitation missing: %+v", result.Limitations)
		}
	})
	t.Run("get error", func(t *testing.T) {
		harness := newRunCoordinatorHarness(t)
		cause := errors.New("cache read failed")
		harness.cache.getErr = cause
		result, err := harness.run(Options{})
		if !errors.Is(err, cause) || !reflect.DeepEqual(result, report.Report{}) || harness.workspaceCloses != 1 || harness.discoverCalls != 0 {
			t.Fatalf("cache error = (%+v, %v), harness=%+v", result, err, harness)
		}
	})
	t.Run("cache hit close error", func(t *testing.T) {
		harness := newRunCoordinatorHarness(t)
		harness.cache.found, harness.cache.getReport = true, cached
		cause := errors.New("close failed")
		harness.dependencies.closeWorkspace = func(*mutationbridge.Workspace) error { harness.workspaceCloses++; return cause }
		result, err := harness.run(Options{})
		if !errors.Is(err, cause) || !reflect.DeepEqual(result, report.Report{}) || harness.workspaceCloses != 1 {
			t.Fatalf("close error = (%+v, %v), closes=%d", result, err, harness.workspaceCloses)
		}
	})
}

func TestRunCoordinatorEmitsChangedImpactModeOnlyWhenRequested(t *testing.T) {
	for _, test := range []struct {
		name    string
		changed bool
		broad   bool
		kind    string
	}{
		{name: "unchanged", broad: true},
		{name: "broad", changed: true, broad: true, kind: "impact-broad"},
		{name: "targeted", changed: true, kind: "impact-targeted"},
	} {
		t.Run(test.name, func(t *testing.T) {
			harness := newRunCoordinatorHarness(t)
			harness.dependencies.selectImpact = func(_ context.Context, _ string, _ goanalysis.Model, targets []goanalysis.Target, _ Options) impactSelection {
				return impactSelection{targets: slices.Clone(targets), broad: test.broad}
			}
			_, err := harness.run(Options{Changed: test.changed})
			if err != nil {
				t.Fatal(err)
			}
			found := ""
			for _, event := range harness.events {
				if strings.HasPrefix(event.Kind, "impact-") {
					found = event.Kind
				}
			}
			if found != test.kind {
				t.Fatalf("impact event = %q, want %q: %+v", found, test.kind, harness.events)
			}
		})
	}
}
