// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package assure

import (
	"context"
	"os"

	"github.com/P4suta/goatest/internal/cache"
	"github.com/P4suta/goatest/internal/config"
	"github.com/P4suta/goatest/internal/evidence"
	goanalysis "github.com/P4suta/goatest/internal/golang"
	"github.com/P4suta/goatest/internal/mutationbridge"
	"github.com/P4suta/goatest/internal/report"
	"github.com/P4suta/goatest/internal/resource"
)

type productionRunResourceManager struct{ manager *resource.Manager }

func (manager productionRunResourceManager) AcquireEnvironment(ctx context.Context, name string) ([]string, error) {
	return acquireProductionResource(ctx, manager.manager, name)
}

func (manager productionRunResourceManager) Close() error {
	return closeProductionResources(manager.manager)
}

var (
	acquireProductionResource = func(ctx context.Context, manager *resource.Manager, name string) ([]string, error) {
		lease, err := manager.Acquire(ctx, name)
		if err != nil {
			return nil, err
		}
		return lease.Environment(), nil
	}
	closeProductionResources = func(manager *resource.Manager) error { return manager.Close() }
	closeProductionWorkspace = func(workspace *mutationbridge.Workspace) error { return workspace.Close() }
	newRunResourceManager    = func(specs map[string]resource.Spec) runResourceManager {
		return productionRunResourceManager{manager: resource.New(specs)}
	}
)

func productionRunDependencies() runDependencies {
	return runDependencies{
		repositoryRoot: repositoryRoot,
		loadConfig:     config.Load,
		newCache: func(path string, policy config.Cache) runCache {
			return cache.NewWithPolicy(path, policy.MaxBytes, policy.TTL)
		},
		openWorkspace: mutationbridge.Open,
		closeWorkspace: func(workspace *mutationbridge.Workspace) error {
			return closeProductionWorkspace(workspace)
		},
		inspectWorkspace: inspectWorkspace,
		assuranceInputs:  assuranceInputs,
		digestInputs:     evidence.Digest,
		discoverTargets:  goanalysis.DiscoverTargets,
		selectImpact:     selectImpact,
		acquireResources: func(ctx context.Context, loaded config.Config, targets []goanalysis.Target, baseEnvironment []string) (runRoundCloser, []BaselineTarget, []report.Evidence, []string, error) {
			manager, baseline, evidenceItems, environment, err := acquireResources(ctx, loaded, targets, baseEnvironment)
			return manager, baseline, evidenceItems, environment, err
		},
		makeBaselineScratch:    os.MkdirTemp,
		removeBaselineScratch:  os.RemoveAll,
		collectBaseline:        CollectBaseline,
		concurrencyPackages:    goanalysis.ConcurrencyPackages,
		relevantRacePackages:   RelevantRacePackages,
		collectRace:            CollectRace,
		collectRaceWithOptions: CollectRaceWithOptions,
		prepareSession: func(ctx context.Context, workspace *mutationbridge.Workspace, options mutationbridge.PrepareOptions) (MutationSession, error) {
			return workspace.Prepare(ctx, options)
		},
		evaluateMutations: EvaluateMutations,
		attemptRepairs:    AttemptGeneratedRepairs,
		buildGraph:        buildGraph,
		mergeGraph:        mergeGraph,
		saveGraph:         evidence.SaveGraph,
	}
}
