// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package assure

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/P4suta/goatest/internal/config"
	goanalysis "github.com/P4suta/goatest/internal/golang"
	"github.com/P4suta/goatest/internal/mutationbridge"
	"github.com/P4suta/goatest/internal/report"
	"github.com/P4suta/goatest/internal/resource"
)

func TestProductionRunResourceAdapterPropagatesEnvironmentAcquireAndClose(t *testing.T) {
	previousAcquire, previousClose := acquireProductionResource, closeProductionResources
	t.Cleanup(func() { acquireProductionResource, closeProductionResources = previousAcquire, previousClose })
	cause := errors.New("resource failed")
	manager := productionRunResourceManager{}
	acquireProductionResource = func(_ context.Context, got *resource.Manager, name string) ([]string, error) {
		if got != nil || name != "postgres" {
			t.Fatalf("acquire = (%T, %q)", got, name)
		}
		return []string{"DB=ready"}, nil
	}
	environment, err := manager.AcquireEnvironment(t.Context(), "postgres")
	if err != nil || !slices.Equal(environment, []string{"DB=ready"}) {
		t.Fatalf("AcquireEnvironment = (%v, %v)", environment, err)
	}
	acquireProductionResource = func(context.Context, *resource.Manager, string) ([]string, error) { return nil, cause }
	if environment, err := manager.AcquireEnvironment(t.Context(), "postgres"); !errors.Is(err, cause) || environment != nil {
		t.Fatalf("AcquireEnvironment error = (%v, %v)", environment, err)
	}
	closeProductionResources = func(got *resource.Manager) error {
		if got != nil {
			t.Fatalf("close manager = %T", got)
		}
		return cause
	}
	if err := manager.Close(); !errors.Is(err, cause) {
		t.Fatalf("Close error = %v", err)
	}
}

func TestProductionRunDependenciesConstructCacheDelegateCloseAndPreserveResourceResults(t *testing.T) {
	dependencies := productionRunDependencies()
	if dependencies.repositoryRoot == nil || dependencies.loadConfig == nil || dependencies.newCache == nil || dependencies.openWorkspace == nil ||
		dependencies.closeWorkspace == nil || dependencies.inspectWorkspace == nil || dependencies.assuranceInputs == nil || dependencies.digestInputs == nil ||
		dependencies.discoverTargets == nil || dependencies.selectImpact == nil || dependencies.acquireResources == nil || dependencies.makeBaselineScratch == nil ||
		dependencies.removeBaselineScratch == nil || dependencies.collectBaseline == nil || dependencies.concurrencyPackages == nil || dependencies.relevantRacePackages == nil ||
		dependencies.collectRace == nil || dependencies.prepareSession == nil || dependencies.evaluateMutations == nil || dependencies.attemptRepairs == nil ||
		dependencies.buildGraph == nil || dependencies.mergeGraph == nil || dependencies.saveGraph == nil {
		t.Fatal("production dependencies contain a nil function")
	}
	store := dependencies.newCache(t.TempDir(), config.Cache{MaxBytes: 5 << 30, TTL: 30 * 24 * time.Hour})
	if store == nil {
		t.Fatal("production cache is nil")
	}
	wantReport := report.Report{Schema: report.SchemaV1, Verdict: report.VerdictAssured, Snapshot: "digest"}
	if err := store.Put("digest", wantReport); err != nil {
		t.Fatal(err)
	}
	gotReport, found, err := store.Get("digest")
	if err != nil || !found || gotReport.Schema != wantReport.Schema || gotReport.Verdict != wantReport.Verdict || gotReport.Snapshot != wantReport.Snapshot {
		t.Fatalf("cache round trip = (%+v, %t, %v)", gotReport, found, err)
	}

	previousWorkspaceClose := closeProductionWorkspace
	t.Cleanup(func() { closeProductionWorkspace = previousWorkspaceClose })
	cause := errors.New("workspace close failed")
	closeProductionWorkspace = func(workspace *mutationbridge.Workspace) error {
		if workspace != nil {
			t.Fatalf("workspace = %T", workspace)
		}
		return cause
	}
	if err := dependencies.closeWorkspace(nil); !errors.Is(err, cause) {
		t.Fatalf("close workspace = %v", err)
	}

	previousManager := newRunResourceManager
	t.Cleanup(func() { newRunResourceManager = previousManager })
	manager := &scriptedRunResourceManager{environments: map[string][]string{"postgres": {"DB=ready"}}}
	newRunResourceManager = func(map[string]resource.Spec) runResourceManager { return manager }
	closer, baseline, evidenceItems, environment, err := dependencies.acquireResources(t.Context(), config.Config{}, []goanalysis.Target{{ID: "target", Capability: "postgres"}}, nil)
	if err != nil || closer != manager || len(baseline) != 1 || baseline[0].Target.ID != "target" ||
		!reflect.DeepEqual(evidenceItems, []report.Evidence{{Kind: "resource", ID: "postgres", Status: "ready"}}) || !slices.Equal(environment, []string{"DB=ready"}) {
		t.Fatalf("resource delegation = (%T, %+v, %+v, %v, %v)", closer, baseline, evidenceItems, environment, err)
	}

	resourceCause := errors.New("acquire failed")
	manager = &scriptedRunResourceManager{errors: map[string]error{"postgres": resourceCause}}
	newRunResourceManager = func(map[string]resource.Spec) runResourceManager { return manager }
	closer, baseline, evidenceItems, environment, err = dependencies.acquireResources(t.Context(), config.Config{}, []goanalysis.Target{{Capability: "postgres"}}, nil)
	if !errors.Is(err, resourceCause) || closer != nil || baseline != nil || evidenceItems != nil || environment != nil {
		t.Fatalf("resource error delegation = (%T, %+v, %+v, %v, %v)", closer, baseline, evidenceItems, environment, err)
	}
}

var _ runRoundCloser = productionRunResourceManager{}
