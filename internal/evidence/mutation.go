// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package evidence

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/P4suta/goatest/internal/filemode"
)

const (
	MutationSchemaV1 = "mutation-evidence-v1"

	MutationFileName = "mutation-evidence-v1.json"
)

const (
	MutationOutcomeKilled    = "killed"
	MutationOutcomeSurvived  = "survived"
	MutationOutcomeUnreached = "unreached"
)

type TargetKey struct {
	Package string `json:"package"`
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	Key     string `json:"key"`

	WholeTree bool `json:"whole_tree"`
}

type SuiteKey struct {
	Package string `json:"package"`
	Key     string `json:"key"`

	WholeTree bool `json:"whole_tree"`
}

type targetKeyJSON struct {
	Package   string `json:"package"`
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	Key       string `json:"key"`
	WholeTree *bool  `json:"whole_tree"`
}

func (target *TargetKey) UnmarshalJSON(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var decoded targetKeyJSON
	if err := decoder.Decode(&decoded); err != nil {
		return err
	}
	if decoded.WholeTree == nil {
		return errors.New("mutation target key requires whole_tree")
	}
	*target = TargetKey{
		Package: decoded.Package, Name: decoded.Name, Kind: decoded.Kind,
		Key: decoded.Key, WholeTree: *decoded.WholeTree,
	}
	return nil
}

type suiteKeyJSON struct {
	Package   string `json:"package"`
	Key       string `json:"key"`
	WholeTree *bool  `json:"whole_tree"`
}

func (suite *SuiteKey) UnmarshalJSON(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var decoded suiteKeyJSON
	if err := decoder.Decode(&decoded); err != nil {
		return err
	}
	if decoded.WholeTree == nil {
		return errors.New("mutation suite key requires whole_tree")
	}
	*suite = SuiteKey{Package: decoded.Package, Key: decoded.Key, WholeTree: *decoded.WholeTree}
	return nil
}

type FindingSeed struct {
	Kind    string `json:"kind"`
	Summary string `json:"summary"`
}

type MutationRecord struct {
	MutantID   string      `json:"mutant_id"`
	Path       string      `json:"path"`
	Package    string      `json:"package"`
	Outcome    string      `json:"outcome"`
	Provenance string      `json:"provenance"`
	KilledBy   []TargetKey `json:"killed_by,omitempty"`

	Exhausted []TargetKey  `json:"exhausted,omitempty"`
	Suite     *SuiteKey    `json:"suite,omitempty"`
	Finding   *FindingSeed `json:"finding,omitempty"`
}

type MutationStore struct {
	Schema     string           `json:"schema"`
	ModulePath string           `json:"module_path"`
	Records    []MutationRecord `json:"records"`
}

type mutationStoreJSON struct {
	Schema     string          `json:"schema"`
	ModulePath string          `json:"module_path"`
	Records    json.RawMessage `json:"records"`
}

func (store *MutationStore) UnmarshalJSON(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var decoded mutationStoreJSON
	if err := decoder.Decode(&decoded); err != nil {
		return err
	}
	if len(decoded.Records) == 0 {
		return errors.New("goatest: mutation evidence requires records")
	}
	*store = MutationStore{Schema: decoded.Schema, ModulePath: decoded.ModulePath}
	return decodeMutationField("records", decoded.Records, &store.Records)
}

type mutationRecordJSON struct {
	MutantID   string          `json:"mutant_id"`
	Path       string          `json:"path"`
	Package    string          `json:"package"`
	Outcome    string          `json:"outcome"`
	Provenance string          `json:"provenance"`
	KilledBy   json.RawMessage `json:"killed_by"`
	Exhausted  json.RawMessage `json:"exhausted"`
	Suite      json.RawMessage `json:"suite"`
	Finding    json.RawMessage `json:"finding"`
}

func (record *MutationRecord) UnmarshalJSON(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var decoded mutationRecordJSON
	if err := decoder.Decode(&decoded); err != nil {
		return err
	}
	*record = MutationRecord{
		MutantID: decoded.MutantID, Path: decoded.Path, Package: decoded.Package,
		Outcome: decoded.Outcome, Provenance: decoded.Provenance,
	}
	for _, field := range []struct {
		name string
		raw  json.RawMessage
		into any
	}{
		{"killed_by", decoded.KilledBy, &record.KilledBy},
		{"exhausted", decoded.Exhausted, &record.Exhausted},
		{"suite", decoded.Suite, &record.Suite},
		{"finding", decoded.Finding, &record.Finding},
	} {
		if err := decodeMutationField(field.name, field.raw, field.into); err != nil {
			return err
		}
	}
	return record.validateOutcomeFields(decoded)
}

func decodeMutationField(name string, raw json.RawMessage, into any) error {
	if len(raw) == 0 {
		return nil
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return fmt.Errorf("goatest: mutation evidence %s is null", name)
	}
	return json.Unmarshal(raw, into)
}

func (record MutationRecord) validateOutcomeFields(decoded mutationRecordJSON) error {
	required := mutationOutcomeFields(record.Outcome)
	if required == nil {
		return nil
	}
	for _, field := range []struct {
		name    string
		present bool
	}{
		{"killed_by", len(decoded.KilledBy) != 0},
		{"exhausted", len(decoded.Exhausted) != 0},
		{"suite", len(decoded.Suite) != 0},
		{"finding", len(decoded.Finding) != 0},
	} {
		if slices.Contains(required, field.name) == field.present {
			continue
		}
		return mutationOutcomeShapeError(record.Outcome, record.MutantID)
	}
	return nil
}

func mutationOutcomeFields(outcome string) []string {
	switch outcome {
	case MutationOutcomeKilled:
		return []string{"killed_by"}
	case MutationOutcomeSurvived:
		return []string{"exhausted", "finding"}
	case MutationOutcomeUnreached:
		return []string{"suite", "finding"}
	}
	return nil
}

func LoadMutation(path, modulePath string) (MutationStore, bool, error) {
	return loadMutationWithHooks(path, modulePath, mutationHooks{})
}

func loadMutationWithHooks(path, modulePath string, hooks mutationHooks) (MutationStore, bool, error) {
	data, err := hooks.resolved().readStore(path)
	if errors.Is(err, os.ErrNotExist) {
		return MutationStore{}, false, nil
	}
	if err != nil {
		return MutationStore{}, false, err
	}
	store, err := decodeMutation(data)
	if err != nil {
		return MutationStore{}, false, err
	}

	if store.Schema != MutationSchemaV1 || store.ModulePath == "" || store.ModulePath != modulePath {
		return MutationStore{}, false, fmt.Errorf("goatest: mutation evidence identity mismatch")
	}
	if err := store.validate(); err != nil {
		return MutationStore{}, false, err
	}
	return store, true, nil
}

func decodeMutation(data []byte) (MutationStore, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var store MutationStore
	if err := decoder.Decode(&store); err != nil {
		return MutationStore{}, fmt.Errorf("goatest: decode mutation evidence: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return MutationStore{}, fmt.Errorf("goatest: mutation evidence has trailing data")
	}
	return store, nil
}

func SaveMutation(path string, store MutationStore) error {
	return saveMutationWithHooks(path, store, mutationHooks{})
}

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

	var stored MutationStore
	if err := hooks.unmarshalStore(data, &stored); err != nil {
		return err
	}
	if err := stored.validate(); err != nil {
		return err
	}
	data = append(data, '\n')
	if err := hooks.mkdirAll(filepath.Dir(path), filemode.ReadableDirectory); err != nil {
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

func (store MutationStore) canonical() MutationStore {
	result := store
	result.Records = make([]MutationRecord, len(store.Records))
	copy(result.Records, store.Records)
	for index := range result.Records {
		killedBy := slices.Clone(result.Records[index].KilledBy)
		slices.SortFunc(killedBy, compareTargetKeys)
		result.Records[index].KilledBy = killedBy
		exhausted := slices.Clone(result.Records[index].Exhausted)
		slices.SortFunc(exhausted, compareTargetKeys)
		result.Records[index].Exhausted = exhausted
	}
	slices.SortFunc(result.Records, func(first, second MutationRecord) int {
		return strings.Compare(first.MutantID, second.MutantID)
	})
	return result
}

func compareTargetKeys(first, second TargetKey) int {
	if order := strings.Compare(first.Package, second.Package); order != 0 {
		return order
	}
	if order := strings.Compare(first.Name, second.Name); order != 0 {
		return order
	}
	return strings.Compare(first.Kind, second.Kind)
}

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
	case MutationOutcomeKilled, MutationOutcomeSurvived, MutationOutcomeUnreached:
	default:
		return fmt.Errorf("goatest: mutation evidence record %s outcome %q is not a reusable outcome", record.MutantID, record.Outcome)
	}
	if err := record.validateKeys(); err != nil {
		return err
	}
	return record.validateShape()
}

func (record MutationRecord) validateKeys() error {
	seenKilledBy := make(map[TargetKey]struct{}, len(record.KilledBy))
	for _, target := range record.KilledBy {
		if err := target.validate(record.MutantID, "killed_by"); err != nil {
			return err
		}
		identity := TargetKey{Package: target.Package, Name: target.Name, Kind: target.Kind}
		if _, duplicate := seenKilledBy[identity]; duplicate {
			return fmt.Errorf("goatest: mutation evidence record %s is killed by target %s %s twice", record.MutantID, identity.Package, identity.Name)
		}
		seenKilledBy[identity] = struct{}{}
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

func (record MutationRecord) validateShape() error {
	switch record.Outcome {
	case MutationOutcomeKilled:
		if len(record.KilledBy) == 0 || len(record.Exhausted) > 0 || record.Suite != nil || record.Finding != nil {
			return mutationOutcomeShapeError(record.Outcome, record.MutantID)
		}
	case MutationOutcomeSurvived:
		if len(record.KilledBy) > 0 || len(record.Exhausted) == 0 || record.Suite != nil || record.Finding == nil {
			return mutationOutcomeShapeError(record.Outcome, record.MutantID)
		}
	case MutationOutcomeUnreached:
		if len(record.KilledBy) > 0 || len(record.Exhausted) > 0 || record.Suite == nil || record.Finding == nil {
			return mutationOutcomeShapeError(record.Outcome, record.MutantID)
		}
	}
	return nil
}

func mutationOutcomeShapeError(outcome, mutantID string) error {
	switch outcome {
	case MutationOutcomeKilled:
		return fmt.Errorf("goatest: mutation evidence killed record %s requires a killer set that is non-empty and nothing else", mutantID)
	case MutationOutcomeSurvived:
		return fmt.Errorf("goatest: mutation evidence %s record %s requires exhausted targets and a finding", outcome, mutantID)
	case MutationOutcomeUnreached:
		return fmt.Errorf("goatest: mutation evidence unreached record %s requires a suite and a finding", mutantID)
	}
	return nil
}

func (target TargetKey) validate(mutantID, field string) error {
	if target.Package == "" || target.Name == "" || target.Kind == "" {
		return fmt.Errorf("goatest: mutation evidence record %s %s requires a package, a name, and a kind", mutantID, field)
	}
	if !isDigest(target.Key) {
		return fmt.Errorf("goatest: mutation evidence record %s %s key %q is not a sha256 digest", mutantID, field, target.Key)
	}
	return nil
}

func isDigest(value string) bool {
	if len(value) != hex.EncodedLen(sha256.Size) {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}
