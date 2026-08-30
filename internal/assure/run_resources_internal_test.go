// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package assure

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/P4suta/goatest/internal/config"
	goanalysis "github.com/P4suta/goatest/internal/golang"
	"github.com/P4suta/goatest/internal/report"
	"github.com/P4suta/goatest/internal/resource"
)

type scriptedRunResourceManager struct {
	environments map[string][]string
	errors       map[string]error
	acquired     []string
	closed       int
}

func (manager *scriptedRunResourceManager) AcquireEnvironment(_ context.Context, name string) ([]string, error) {
	manager.acquired = append(manager.acquired, name)
	return manager.environments[name], manager.errors[name]
}

func (manager *scriptedRunResourceManager) Close() error { manager.closed++; return nil }

func TestAcquireResourcesBuildsSortedUniqueCapabilitiesAndTargetEnvironments(t *testing.T) {
	previous := newRunResourceManager
	t.Cleanup(func() { newRunResourceManager = previous })
	manager := &scriptedRunResourceManager{environments: map[string][]string{
		"alpha": {"DB=ready", "COMMON=same"},
		"beta":  {"CACHE=ready", "common=same"},
	}}
	loaded := config.Config{Resources: map[string]config.Resource{
		"alpha":  {Command: []string{"provider", "alpha"}, Timeout: 3 * time.Second, Shared: true},
		"beta":   {Command: []string{"provider", "beta"}, Timeout: 4 * time.Second, Exclusive: true},
		"unused": {Command: []string{"provider", "unused"}},
	}}
	newRunResourceManager = func(specs map[string]resource.Spec) runResourceManager {
		if len(specs) != len(loaded.Resources) {
			t.Fatalf("resource specs = %+v", specs)
		}
		for name, configured := range loaded.Resources {
			got := specs[name]
			if !slices.Equal(got.Command, configured.Command) || got.Timeout != configured.Timeout || got.Shared != configured.Shared || got.Exclusive != configured.Exclusive {
				t.Errorf("resource spec %s = %+v, want %+v", name, got, configured)
			}
		}
		return manager
	}
	targets := []goanalysis.Target{
		{ID: "beta-a", Capability: "beta"},
		{ID: "ordinary"},
		{ID: "alpha-a", Capability: "alpha"},
		{ID: "alpha-b", Capability: "alpha"},
	}
	gotManager, baseline, evidenceItems, environment, err := acquireResources(t.Context(), loaded, targets)
	wantEvidence := []report.Evidence{{Kind: "resource", ID: "alpha", Status: "ready"}, {Kind: "resource", ID: "beta", Status: "ready"}}
	if err != nil || gotManager != manager || !slices.Equal(manager.acquired, []string{"alpha", "beta"}) || !reflect.DeepEqual(evidenceItems, wantEvidence) ||
		!slices.Equal(environment, []string{"CACHE=ready", "COMMON=same", "DB=ready"}) || len(baseline) != len(targets) {
		t.Fatalf("acquireResources = (%T, %+v, %+v, %v, %v), acquired=%v", gotManager, baseline, evidenceItems, environment, err, manager.acquired)
	}
	wantTargetEnvironment := [][]string{
		{"CACHE=ready", "common=same"}, nil, {"DB=ready", "COMMON=same"}, {"DB=ready", "COMMON=same"},
	}
	for index := range baseline {
		if baseline[index].Target.ID != targets[index].ID || !slices.Equal(baseline[index].Environment, wantTargetEnvironment[index]) {
			t.Errorf("baseline target %d = %+v", index, baseline[index])
		}
	}
	manager.environments["alpha"][0] = "MUTATED=yes"
	if baseline[2].Environment[0] != "DB=ready" {
		t.Fatal("baseline target aliases lease environment")
	}
}

func TestAcquireResourcesHandlesNoCapabilitiesAcquireFailureAndEnvironmentConflict(t *testing.T) {
	previous := newRunResourceManager
	t.Cleanup(func() { newRunResourceManager = previous })
	t.Run("no capabilities", func(t *testing.T) {
		manager := &scriptedRunResourceManager{}
		newRunResourceManager = func(map[string]resource.Spec) runResourceManager { return manager }
		targets := []goanalysis.Target{{ID: "ordinary"}}
		gotManager, baseline, evidenceItems, environment, err := acquireResources(t.Context(), config.Config{}, targets)
		if err != nil || gotManager != manager || len(baseline) != 1 || baseline[0].Target.ID != "ordinary" || baseline[0].Environment != nil ||
			evidenceItems != nil || environment != nil || len(manager.acquired) != 0 || manager.closed != 0 {
			t.Fatalf("no capability = (%T, %+v, %+v, %v, %v), manager=%+v", gotManager, baseline, evidenceItems, environment, err, manager)
		}
	})
	t.Run("acquire failure", func(t *testing.T) {
		cause := errors.New("resource failed")
		manager := &scriptedRunResourceManager{errors: map[string]error{"alpha": cause}}
		newRunResourceManager = func(map[string]resource.Spec) runResourceManager { return manager }
		gotManager, baseline, evidenceItems, environment, err := acquireResources(t.Context(), config.Config{}, []goanalysis.Target{{Capability: "alpha"}})
		if !errors.Is(err, cause) || gotManager != nil || baseline != nil || evidenceItems != nil || environment != nil || manager.closed != 1 {
			t.Fatalf("acquire failure = (%T, %+v, %+v, %v, %v), closed=%d", gotManager, baseline, evidenceItems, environment, err, manager.closed)
		}
	})
	t.Run("environment conflict", func(t *testing.T) {
		manager := &scriptedRunResourceManager{environments: map[string][]string{"alpha": {"TOKEN=one"}, "beta": {"token=two"}}}
		newRunResourceManager = func(map[string]resource.Spec) runResourceManager { return manager }
		gotManager, baseline, evidenceItems, environment, err := acquireResources(t.Context(), config.Config{}, []goanalysis.Target{{Capability: "beta"}, {Capability: "alpha"}})
		if err == nil || !strings.Contains(err.Error(), "resource beta") || gotManager != nil || baseline != nil || evidenceItems != nil || environment != nil || manager.closed != 1 ||
			!slices.Equal(manager.acquired, []string{"alpha", "beta"}) {
			t.Fatalf("conflict = (%T, %+v, %+v, %v, %v), manager=%+v", gotManager, baseline, evidenceItems, environment, err, manager)
		}
	})
}

var _ runResourceManager = (*scriptedRunResourceManager)(nil)
