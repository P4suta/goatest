// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package assure

import (
	"fmt"
	"slices"
	"sync"

	gomutants "github.com/P4suta/go-mutants"
	"github.com/P4suta/goatest/internal/checkpoint"
	goanalysis "github.com/P4suta/goatest/internal/golang"
	"github.com/P4suta/goatest/internal/repair"
	"github.com/P4suta/goatest/internal/report"
)

type runCheckpointController struct {
	mutex   sync.Mutex
	store   runCache
	digest  string
	options Options
	state   checkpoint.State
	enabled bool
	claimed bool

	reusedTargets int
	reusedRace    int
	reusedMutants int
}

func openRunCheckpoint(store runCache, digest string, options Options, enabled bool) *runCheckpointController {
	if !enabled || store == nil {
		return nil
	}
	controller := &runCheckpointController{store: store, digest: digest, options: options, enabled: true}
	state, found, err := store.GetCheckpoint(digest)
	if err != nil {
		emit(options, "checkpoint-warning", err.Error()+"; starting cold")
		_ = store.DeleteCheckpoint(digest)
		found = false
	}
	if found {
		controller.state = state
		controller.state.Attempts++
	} else {
		controller.state = checkpoint.State{Schema: checkpoint.SchemaV1, InputDigest: digest, Attempts: 1}
	}
	// Claim the attempt before consuming saved work. A disk that cannot accept
	// this write gets a true cold run and never contributes prior evidence.
	if err := store.PutCheckpoint(digest, controller.state); err != nil {
		emit(options, "checkpoint-warning", err.Error()+"; starting cold")
		_ = store.DeleteCheckpoint(digest)
		controller.enabled = false
		return controller
	}
	controller.claimed = true
	return controller
}

func (controller *runCheckpointController) baseline(targets []goanalysis.Target) *checkpoint.Baseline {
	if controller == nil || !controller.enabled {
		return nil
	}
	controller.mutex.Lock()
	defer controller.mutex.Unlock()
	current := make(map[string]goanalysis.Target, len(targets))
	for _, target := range targets {
		current[target.ID] = target
	}
	valid := true
	for _, unit := range controller.state.Baseline.Targets {
		target, exists := current[unit.ID]
		if !exists || unit.Inventory.Name != target.Name || unit.Inventory.Kind != string(target.Kind) || unit.Inventory.Package != target.Package || unit.Inventory.Path != target.Path || unit.Inventory.Line != max(target.Line, 0) {
			valid = false
			break
		}
		if unit.Target != nil && (unit.Target.Target.ID != target.ID || unit.Target.Target.Path != target.Path || unit.Target.Target.Package != target.Package) {
			valid = false
			break
		}
	}
	if controller.state.Baseline.Complete && len(controller.state.Baseline.Targets) != len(targets) {
		valid = false
	}
	if !valid {
		emit(controller.options, "checkpoint-warning", "baseline target inventory changed; discarding saved baseline, race, and mutation work")
		controller.state.Baseline = checkpoint.Baseline{}
		controller.state.Race = nil
		controller.state.Mutation = nil
		controller.persistLocked()
		return nil
	}
	if !controller.state.Baseline.BuildVetComplete && len(controller.state.Baseline.Targets) == 0 {
		return nil
	}
	resume := controller.state.Baseline
	controller.reusedTargets = len(resume.Targets)
	return &resume
}

func (controller *runCheckpointController) saveBaseline(state checkpoint.Baseline) {
	if controller == nil {
		return
	}
	controller.mutex.Lock()
	defer controller.mutex.Unlock()
	if !controller.enabled {
		return
	}
	controller.state.Baseline = state
	controller.persistLocked()
}

func (controller *runCheckpointController) race(packages []string) (*checkpoint.Race, bool) {
	if controller == nil || !controller.enabled {
		return nil, false
	}
	controller.mutex.Lock()
	defer controller.mutex.Unlock()
	saved := controller.state.Race
	if saved == nil || !saved.Complete {
		return nil, false
	}
	want := slices.Clone(packages)
	slices.Sort(want)
	got := slices.Clone(saved.Packages)
	slices.Sort(got)
	if !slices.Equal(got, want) {
		emit(controller.options, "checkpoint-warning", "race package inventory changed; discarding saved race and mutation work")
		controller.state.Race = nil
		controller.state.Mutation = nil
		controller.persistLocked()
		return nil, false
	}
	copy := *saved
	controller.reusedRace = len(packages)
	return &copy, true
}

func (controller *runCheckpointController) saveRace(packages []string, result RaceResult) {
	if controller == nil {
		return
	}
	controller.mutex.Lock()
	defer controller.mutex.Unlock()
	if !controller.enabled {
		return
	}
	controller.state.Race = &checkpoint.Race{
		Complete: true, Packages: slices.Clone(packages), Evidence: slices.Clone(result.Evidence), Findings: slices.Clone(result.Findings),
	}
	controller.persistLocked()
}

func (controller *runCheckpointController) mutation(catalog gomutants.Catalog, root string) map[string]MutationEvaluation {
	if controller == nil || !controller.enabled {
		return nil
	}
	controller.mutex.Lock()
	defer controller.mutex.Unlock()
	fingerprint := MutationCatalogFingerprint(catalog)
	if controller.state.Mutation == nil || controller.state.Mutation.CatalogFingerprint != fingerprint {
		if controller.state.Mutation != nil {
			emit(controller.options, "checkpoint-warning", "mutation catalog changed; discarding saved mutation work")
		}
		controller.state.Mutation = &checkpoint.Mutation{CatalogFingerprint: fingerprint}
		controller.persistLocked()
		return nil
	}
	catalogIDs := make(map[string]struct{}, len(catalog.Mutants))
	for _, mutant := range catalog.Mutants {
		catalogIDs[mutant.ID] = struct{}{}
	}
	result := make(map[string]MutationEvaluation, len(controller.state.Mutation.Results))
	for _, saved := range controller.state.Mutation.Results {
		if _, exists := catalogIDs[saved.ID]; !exists || !checkpointArtifactsPresent(root, saved) {
			emit(controller.options, "checkpoint-warning", "saved mutation artifact or catalog entry is unavailable; discarding saved mutation work")
			controller.state.Mutation = &checkpoint.Mutation{CatalogFingerprint: fingerprint}
			controller.persistLocked()
			return nil
		}
		result[saved.ID] = MutationEvaluation{
			Evidence: slices.Clone(saved.Evidence), Findings: slices.Clone(saved.Findings), Repairs: slices.Clone(saved.Repairs), Applied: saved.Applied,
		}
	}
	controller.reusedMutants = len(result)
	return result
}

func checkpointArtifactsPresent(root string, saved checkpoint.MutationResult) bool {
	for _, item := range saved.Repairs {
		if item.Status != string(repair.StatusCandidate) {
			continue
		}
		if _, err := repair.LoadCandidate(root, item.ID); err != nil {
			return false
		}
	}
	return true
}

func (controller *runCheckpointController) saveMutant(id string, evaluation MutationEvaluation) {
	if controller == nil {
		return
	}
	controller.mutex.Lock()
	defer controller.mutex.Unlock()
	if !controller.enabled || controller.state.Mutation == nil {
		return
	}
	unit := checkpoint.MutationResult{
		ID: id, Evidence: slices.Clone(evaluation.Evidence), Findings: slices.Clone(evaluation.Findings), Repairs: slices.Clone(evaluation.Repairs), Applied: evaluation.Applied,
	}
	replaced := false
	for index := range controller.state.Mutation.Results {
		if controller.state.Mutation.Results[index].ID == id {
			controller.state.Mutation.Results[index] = unit
			replaced = true
			break
		}
	}
	if !replaced {
		controller.state.Mutation.Results = append(controller.state.Mutation.Results, unit)
	}
	controller.persistLocked()
}

func (controller *runCheckpointController) completeMutation() {
	if controller == nil {
		return
	}
	controller.mutex.Lock()
	defer controller.mutex.Unlock()
	if !controller.enabled || controller.state.Mutation == nil {
		return
	}
	controller.state.Mutation.Complete = true
	controller.persistLocked()
}

func (controller *runCheckpointController) resumeMetadata() *report.Resume {
	if controller == nil || !controller.claimed {
		return nil
	}
	controller.mutex.Lock()
	defer controller.mutex.Unlock()
	return &report.Resume{
		Attempts: controller.state.Attempts, ReusedTargets: controller.reusedTargets,
		ReusedRacePackages: controller.reusedRace, ReusedMutants: controller.reusedMutants,
	}
}

func (controller *runCheckpointController) discard() {
	if controller == nil {
		return
	}
	controller.mutex.Lock()
	defer controller.mutex.Unlock()
	controller.enabled = false
	if err := controller.store.DeleteCheckpoint(controller.digest); err != nil {
		emit(controller.options, "checkpoint-warning", fmt.Sprintf("discard checkpoint: %v", err))
	}
}

func (controller *runCheckpointController) persistLocked() {
	if !controller.enabled {
		return
	}
	if err := controller.store.PutCheckpoint(controller.digest, controller.state); err != nil {
		emit(controller.options, "checkpoint-warning", err.Error()+"; disabling checkpoint writes for this run")
		controller.enabled = false
		_ = controller.store.DeleteCheckpoint(controller.digest)
	}
}
