// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package evidence

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

const MutationSchemaV1 = "mutation-evidence-v1"

// Outcomes a record may carry. Only dispositions a later run can reuse are
// stored: flaky and inconclusive results and compile rejections are never
// recorded, because a run that reused one would be reusing an answer the
// recording run did not have.
const (
	MutationOutcomeKilled    = "killed"
	MutationOutcomeSurvived  = "survived"
	MutationOutcomeUnreached = "unreached"
	MutationOutcomeTimedOut  = "timed-out"
)

// TargetKey names one test target and the behaviour it had when the record was
// written. The key is what a later run compares against: the same target with
// a different key is, for the purpose of reuse, a different target.
type TargetKey struct {
	Package string `json:"package"`
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	Key     string `json:"key"`
}

// SuiteKey names a package's whole test suite and the behaviour it had. An
// unreached mutant is a statement about the suite, not about one target.
type SuiteKey struct {
	Package string `json:"package"`
	Key     string `json:"key"`
}

// FindingSeed is what a reused verdict has to be able to report, so a run that
// reuses a record can raise the finding without executing anything.
type FindingSeed struct {
	Kind    string `json:"kind"`
	Summary string `json:"summary"`
}

type MutationRecord struct {
	MutantID   string     `json:"mutant_id"`
	Path       string     `json:"path"`
	Package    string     `json:"package"`
	Outcome    string     `json:"outcome"`
	Provenance string     `json:"provenance"`
	KilledBy   *TargetKey `json:"killed_by,omitempty"`
	// Exhausted is the set of targets the recording run actually executed
	// against the mutant and that passed. A target the run discharged by proof
	// without executing it is never listed here: a later run has to be able to
	// read this as "these targets ran and did not kill it", because that is the
	// claim it reuses.
	Exhausted []TargetKey  `json:"exhausted,omitempty"`
	Suite     *SuiteKey    `json:"suite,omitempty"`
	Finding   *FindingSeed `json:"finding,omitempty"`
}

type MutationStore struct {
	Schema     string           `json:"schema"`
	ModulePath string           `json:"module_path"`
	Records    []MutationRecord `json:"records"`
}

// LoadMutation reads the mutation evidence stored for modulePath. A store that
// is missing is not an error: there is simply nothing to reuse yet. A store
// that exists but cannot be trusted is, because the caller must then discard
// it and execute everything rather than reuse a verdict it cannot account for.
func LoadMutation(path, modulePath string) (MutationStore, bool, error) {
	return loadMutationWithHooks(path, modulePath, mutationHooks{})
}

// loadMutationWithHooks is LoadMutation against a filesystem the caller
// supplies.
func loadMutationWithHooks(path, modulePath string, hooks mutationHooks) (MutationStore, bool, error) {
	data, err := hooks.resolved().readStore(path)
	if errors.Is(err, os.ErrNotExist) {
		return MutationStore{}, false, nil
	}
	if err != nil {
		return MutationStore{}, false, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var store MutationStore
	if err := decoder.Decode(&store); err != nil {
		return MutationStore{}, false, fmt.Errorf("goatest: decode mutation evidence: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return MutationStore{}, false, fmt.Errorf("goatest: mutation evidence has trailing data")
	}
	// A store written under another schema or for another module is never
	// trusted, however plausible its records look.
	if store.Schema != MutationSchemaV1 || store.ModulePath == "" || store.ModulePath != modulePath {
		return MutationStore{}, false, fmt.Errorf("goatest: mutation evidence identity mismatch")
	}
	if err := store.validate(); err != nil {
		return MutationStore{}, false, err
	}
	return store, true, nil
}

// SaveMutation writes store to path in canonical form, replacing whatever was
// there. An inconsistent store is refused before anything is created, so the
// only document that can exist is one this package would load back.
func SaveMutation(path string, store MutationStore) error {
	return saveMutationWithHooks(path, store, mutationHooks{})
}

// saveMutationWithHooks is SaveMutation against a filesystem the caller
// supplies.
func saveMutationWithHooks(path string, store MutationStore, hooks mutationHooks) error {
	hooks = hooks.resolved()
	store.Schema = MutationSchemaV1
	if err := store.validate(); err != nil {
		return err
	}
	data, err := hooks.marshalStore(store.canonical(), "", "  ")
	if err != nil {
		return err
	}
	// Read the encoding back and check it again. What reaches disk is then a
	// document this package has loaded once already, so a later run cannot be
	// handed a store that only looked consistent as a value.
	var stored MutationStore
	if err := hooks.unmarshalStore(data, &stored); err != nil {
		return err
	}
	if err := stored.validate(); err != nil {
		return err
	}
	data = append(data, '\n')
	if err := hooks.mkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	temporary, err := hooks.createTemporary(filepath.Dir(path), ".mutation-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer func() { _ = hooks.remove(temporaryPath) }()
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := hooks.rename(temporaryPath, path); err != nil {
		if removeErr := hooks.remove(path); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			return errors.Join(err, removeErr)
		}
		return hooks.rename(temporaryPath, path)
	}
	return nil
}

// canonical returns the store in the one form it is ever written in: records
// ordered by mutant id and each record's exhausted targets ordered by package,
// name, and kind. Two runs that recorded the same evidence then produce the
// same bytes, so a diff of the store reads as the evidence that changed.
//
// The record list is always allocated, so an empty store is written as an
// empty array rather than null and every stored document satisfies the array
// contract the schema states.
func (store MutationStore) canonical() MutationStore {
	result := store
	result.Records = make([]MutationRecord, len(store.Records))
	copy(result.Records, store.Records)
	for index := range result.Records {
		exhausted := slices.Clone(result.Records[index].Exhausted)
		slices.SortFunc(exhausted, compareTargetKeys)
		result.Records[index].Exhausted = exhausted
	}
	slices.SortFunc(result.Records, func(first, second MutationRecord) int {
		return strings.Compare(first.MutantID, second.MutantID)
	})
	return result
}

// compareTargetKeys orders target keys by the identity of the target, not by
// its behaviour key: two records of the same target are the duplicate the
// store refuses, whatever key each carries.
func compareTargetKeys(first, second TargetKey) int {
	if order := strings.Compare(first.Package, second.Package); order != 0 {
		return order
	}
	if order := strings.Compare(first.Name, second.Name); order != 0 {
		return order
	}
	return strings.Compare(first.Kind, second.Kind)
}

// validate reports the first way in which a store contradicts itself. Both
// LoadMutation and SaveMutation call it, so an inconsistent store is neither
// written nor returned: the caller sees an error and executes everything,
// which is the direction that can only cost time, never assurance.
func (store MutationStore) validate() error {
	if store.ModulePath == "" {
		return fmt.Errorf("goatest: mutation evidence requires a module path")
	}
	seen := make(map[string]struct{}, len(store.Records))
	for _, record := range store.Records {
		if err := record.validate(); err != nil {
			return err
		}
		if _, duplicate := seen[record.MutantID]; duplicate {
			return fmt.Errorf("goatest: mutation evidence records mutant %s twice", record.MutantID)
		}
		seen[record.MutantID] = struct{}{}
	}
	return nil
}

// validate reports the first way in which a record contradicts itself.
func (record MutationRecord) validate() error {
	if !isDigest(record.MutantID) {
		return fmt.Errorf("goatest: mutation evidence mutant id %q is not a sha256 digest", record.MutantID)
	}
	if record.Path == "" || record.Package == "" {
		return fmt.Errorf("goatest: mutation evidence record %s requires a path and a package", record.MutantID)
	}
	if snapshot, ok := strings.CutPrefix(record.Provenance, "snapshot="); !ok || !isDigest(snapshot) {
		return fmt.Errorf("goatest: mutation evidence record %s provenance %q is not a run snapshot", record.MutantID, record.Provenance)
	}
	switch record.Outcome {
	case MutationOutcomeKilled, MutationOutcomeSurvived, MutationOutcomeUnreached, MutationOutcomeTimedOut:
	default:
		return fmt.Errorf("goatest: mutation evidence record %s outcome %q is not a reusable outcome", record.MutantID, record.Outcome)
	}
	if err := record.validateKeys(); err != nil {
		return err
	}
	return record.validateShape()
}

// validateKeys checks every target, suite, and finding the record carries,
// whatever its outcome allows it to carry.
func (record MutationRecord) validateKeys() error {
	if record.KilledBy != nil {
		if err := record.KilledBy.validate(record.MutantID, "killed_by"); err != nil {
			return err
		}
	}
	seen := make(map[TargetKey]struct{}, len(record.Exhausted))
	for _, target := range record.Exhausted {
		if err := target.validate(record.MutantID, "exhausted"); err != nil {
			return err
		}
		identity := TargetKey{Package: target.Package, Name: target.Name, Kind: target.Kind}
		if _, duplicate := seen[identity]; duplicate {
			return fmt.Errorf("goatest: mutation evidence record %s exhausts target %s %s twice", record.MutantID, identity.Package, identity.Name)
		}
		seen[identity] = struct{}{}
	}
	if record.Suite != nil {
		if record.Suite.Package == "" {
			return fmt.Errorf("goatest: mutation evidence record %s suite requires a package", record.MutantID)
		}
		if !isDigest(record.Suite.Key) {
			return fmt.Errorf("goatest: mutation evidence record %s suite key %q is not a sha256 digest", record.MutantID, record.Suite.Key)
		}
	}
	if record.Finding != nil && (record.Finding.Kind == "" || record.Finding.Summary == "") {
		return fmt.Errorf("goatest: mutation evidence record %s finding requires a kind and a summary", record.MutantID)
	}
	return nil
}

// validateShape checks that the record carries exactly what its outcome means.
// The shape is the claim: a killed mutant names its killer and nothing else, a
// survived or timed-out mutant names the targets that ran without killing it,
// and an unreached mutant names the suite that never reached it. A record
// whose shape does not match its outcome states something the recording run
// cannot have observed.
func (record MutationRecord) validateShape() error {
	switch record.Outcome {
	case MutationOutcomeKilled:
		if record.KilledBy == nil || len(record.Exhausted) > 0 || record.Suite != nil || record.Finding != nil {
			return fmt.Errorf("goatest: mutation evidence killed record %s requires a killer and nothing else", record.MutantID)
		}
	case MutationOutcomeSurvived, MutationOutcomeTimedOut:
		if record.KilledBy != nil || len(record.Exhausted) == 0 || record.Suite != nil || record.Finding == nil {
			return fmt.Errorf("goatest: mutation evidence %s record %s requires exhausted targets and a finding", record.Outcome, record.MutantID)
		}
	case MutationOutcomeUnreached:
		if record.KilledBy != nil || len(record.Exhausted) > 0 || record.Suite == nil || record.Finding == nil {
			return fmt.Errorf("goatest: mutation evidence unreached record %s requires a suite and a finding", record.MutantID)
		}
	}
	return nil
}

// validate checks one target key, naming the field it was read from so the
// error points at the record that has to be fixed.
func (target TargetKey) validate(mutantID, field string) error {
	if target.Package == "" || target.Name == "" || target.Kind == "" {
		return fmt.Errorf("goatest: mutation evidence record %s %s requires a package, a name, and a kind", mutantID, field)
	}
	if !isDigest(target.Key) {
		return fmt.Errorf("goatest: mutation evidence record %s %s key %q is not a sha256 digest", mutantID, field, target.Key)
	}
	return nil
}

// isDigest reports whether a value is a sha256 digest in the lowercase hex
// form go-mutants ids and behaviour keys are written in. An uppercase spelling
// of the same bytes is a different string to every consumer that compares
// them, so it is refused rather than folded.
func isDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}
