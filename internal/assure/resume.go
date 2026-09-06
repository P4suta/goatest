// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package assure

import (
	"fmt"
	"reflect"
	"slices"
	"sync"

	gomutants "github.com/P4suta/go-mutants"
	"github.com/P4suta/goatest/internal/checkpoint"
	goanalysis "github.com/P4suta/goatest/internal/golang"
	"github.com/P4suta/goatest/internal/report"
)

type runCheckpointJournal interface {
	AppendBaselineCheckpoint(string, checkpoint.BaselineTarget) error
	AppendBaselineSuiteCheckpoint(string, checkpoint.BaselineSuite) error
	AppendMutationCheckpoint(string, checkpoint.MutationResult) error
}

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
	currentPackages := make(map[string]bool, len(targets))
	for _, target := range targets {
		current[target.ID] = target
		currentPackages[target.Package] = true
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
	for _, suite := range controller.state.Baseline.Suites {
		if !currentPackages[suite.Package] {
			valid = false
			break
		}
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

	slices.SortFunc(state.Targets, func(left, right checkpoint.BaselineTarget) int {
		return compareText(left.ID, right.ID)
	})
	slices.SortFunc(state.Suites, func(left, right checkpoint.BaselineSuite) int {
		return compareText(left.Package, right.Package)
	})
	previous := controller.state.Baseline
	controller.state.Baseline = state
	journal, journaled := controller.store.(runCheckpointJournal)
	if journaled {
		if suffix, ok := baselineCheckpointJournalSuffix(previous, state); ok {
			for _, unit := range suffix {
				if err := journal.AppendBaselineCheckpoint(controller.digest, unit); err != nil {
					controller.disableCheckpointLocked(err)
					return
				}
			}
			return
		}
		if suffix, ok := baselineSuiteCheckpointJournalSuffix(previous, state); ok {
			for _, unit := range suffix {
				if err := journal.AppendBaselineSuiteCheckpoint(controller.digest, unit); err != nil {
					controller.disableCheckpointLocked(err)
					return
				}
			}
			return
		}
	}
	controller.persistLocked()
}

func baselineCheckpointJournalSuffix(previous, next checkpoint.Baseline) ([]checkpoint.BaselineTarget, bool) {
	if !previous.BuildVetComplete || !next.BuildVetComplete || previous.Complete || next.Complete ||
		!reflect.DeepEqual(previous.Evidence, next.Evidence) || !reflect.DeepEqual(previous.Findings, next.Findings) ||
		!reflect.DeepEqual(previous.Suites, next.Suites) ||
		len(next.Targets) <= len(previous.Targets) {
		return nil, false
	}
	before := make(map[string]checkpoint.BaselineTarget, len(previous.Targets))
	for _, unit := range previous.Targets {
		if _, duplicate := before[unit.ID]; duplicate {
			return nil, false
		}
		before[unit.ID] = unit
	}
	suffix := make([]checkpoint.BaselineTarget, 0, len(next.Targets)-len(previous.Targets))
	seen := make(map[string]bool, len(next.Targets))
	for _, unit := range next.Targets {
		if seen[unit.ID] {
			return nil, false
		}
		seen[unit.ID] = true
		if saved, exists := before[unit.ID]; exists {
			if !reflect.DeepEqual(saved, unit) {
				return nil, false
			}
			continue
		}
		suffix = append(suffix, unit)
	}
	if len(seen) != len(before)+len(suffix) {
		return nil, false
	}
	slices.SortFunc(suffix, func(left, right checkpoint.BaselineTarget) int {
		return compareText(left.ID, right.ID)
	})
	return suffix, true
}

func baselineSuiteCheckpointJournalSuffix(previous, next checkpoint.Baseline) ([]checkpoint.BaselineSuite, bool) {
	if !previous.BuildVetComplete || !next.BuildVetComplete || previous.Complete || next.Complete ||
		!reflect.DeepEqual(previous.Evidence, next.Evidence) || !reflect.DeepEqual(previous.Findings, next.Findings) ||
		!reflect.DeepEqual(previous.Targets, next.Targets) || len(next.Suites) <= len(previous.Suites) {
		return nil, false
	}
	before := make(map[string]checkpoint.BaselineSuite, len(previous.Suites))
	for _, unit := range previous.Suites {
		if _, duplicate := before[unit.Package]; duplicate {
			return nil, false
		}
		before[unit.Package] = unit
	}
	suffix := make([]checkpoint.BaselineSuite, 0, len(next.Suites)-len(previous.Suites))
	seen := make(map[string]bool, len(next.Suites))
	for _, unit := range next.Suites {
		if seen[unit.Package] {
			return nil, false
		}
		seen[unit.Package] = true
		if saved, exists := before[unit.Package]; exists {
			if !reflect.DeepEqual(saved, unit) {
				return nil, false
			}
			continue
		}
		suffix = append(suffix, unit)
	}
	if len(seen) != len(before)+len(suffix) {
		return nil, false
	}
	slices.SortFunc(suffix, func(left, right checkpoint.BaselineSuite) int {
		return compareText(left.Package, right.Package)
	})
	return suffix, true
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

func (controller *runCheckpointController) mutation(catalog gomutants.Catalog) map[string]MutationEvaluation {
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
		if _, exists := catalogIDs[saved.ID]; !exists {
			emit(controller.options, "checkpoint-warning", "saved mutation catalog entry is unavailable; discarding saved mutation work")
			controller.state.Mutation = &checkpoint.Mutation{CatalogFingerprint: fingerprint}
			controller.persistLocked()
			return nil
		}
		result[saved.ID] = MutationEvaluation{
			Evidence: slices.Clone(saved.Evidence), Findings: slices.Clone(saved.Findings),
			Provenance: saved.Provenance,
		}
	}
	controller.reusedMutants = len(result)
	return result
}

func (controller *runCheckpointController) probe(catalog gomutants.Catalog, targets []TargetEvidence, packages []string) (evaluation ProbeEvaluation, reused, valid bool) {
	if controller == nil || !controller.enabled {
		return ProbeEvaluation{}, false, true
	}
	controller.mutex.Lock()
	defer controller.mutex.Unlock()
	if controller.state.Mutation == nil || controller.state.Mutation.Probe == nil {
		return ProbeEvaluation{}, false, true
	}
	restored, ok := restoreMutationProbe(catalog, targets, packages, *controller.state.Mutation.Probe)
	if ok {
		return restored, true, true
	}
	emit(controller.options, "checkpoint-warning", "mutation probe inventory changed; discarding saved probe and mutation work")
	controller.state.Mutation = &checkpoint.Mutation{CatalogFingerprint: MutationCatalogFingerprint(catalog)}
	controller.reusedMutants = 0
	controller.persistLocked()
	return ProbeEvaluation{}, false, false
}

func (controller *runCheckpointController) saveProbe(catalog gomutants.Catalog, evaluation ProbeEvaluation) {
	if controller == nil {
		return
	}
	controller.mutex.Lock()
	defer controller.mutex.Unlock()
	if !controller.enabled || controller.state.Mutation == nil ||
		controller.state.Mutation.CatalogFingerprint != MutationCatalogFingerprint(catalog) {
		return
	}
	controller.state.Mutation.Probe = checkpointMutationProbe(catalog, evaluation)
	controller.persistLocked()
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
		ID: id, Evidence: slices.Clone(evaluation.Evidence), Findings: slices.Clone(evaluation.Findings),
		Provenance: evaluation.Provenance,
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
	if journal, ok := controller.store.(runCheckpointJournal); ok {
		if err := journal.AppendMutationCheckpoint(controller.digest, unit); err != nil {
			controller.disableCheckpointLocked(err)
		}
		return
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

	slices.SortFunc(controller.state.Mutation.Results, func(left, right checkpoint.MutationResult) int {
		return compareText(left.ID, right.ID)
	})
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
		controller.disableCheckpointLocked(err)
	}
}

func (controller *runCheckpointController) disableCheckpointLocked(err error) {
	emit(controller.options, "checkpoint-warning", err.Error()+"; disabling checkpoint writes for this run")
	controller.enabled = false
	_ = controller.store.DeleteCheckpoint(controller.digest)
}

func compareText(left, right string) int {
	switch {
	case left < right:
		return -1
	case left > right:
		return 1
	default:
		return 0
	}
}
