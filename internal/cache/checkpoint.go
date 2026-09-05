// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package cache

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

	"github.com/P4suta/goatest/internal/checkpoint"
)

const (
	CheckpointFileName        = "checkpoint-v1.json"
	CheckpointJournalFileName = "checkpoint-journal-v1.jsonl"
	checkpointJournalSchema   = "assurance-checkpoint-journal-v1"
)

type checkpointJournalRecord struct {
	Schema         string                     `json:"schema"`
	InputDigest    string                     `json:"input_digest"`
	BaseDigest     string                     `json:"base_digest"`
	BaselineTarget *checkpoint.BaselineTarget `json:"baseline_target,omitempty"`
	MutationResult *checkpoint.MutationResult `json:"mutation_result,omitempty"`
	Checksum       string                     `json:"checksum"`
}

// GetCheckpoint reads strict interrupted-run state for exactly digest.
func (store *Store) GetCheckpoint(digest string) (checkpoint.State, bool, error) {
	cacheOperationMutex.Lock()
	defer cacheOperationMutex.Unlock()
	path, err := store.checkpointPath(digest)
	if err != nil {
		return checkpoint.State{}, false, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return checkpoint.State{}, false, nil
	}
	if err != nil {
		return checkpoint.State{}, false, fmt.Errorf("goatest: read checkpoint: %w", err)
	}
	state, err := checkpoint.Decode(data)
	if err != nil {
		return checkpoint.State{}, false, err
	}
	if state.InputDigest != digest {
		return checkpoint.State{}, false, errors.New("goatest: checkpoint input identity mismatch")
	}
	baseDigest := checkpointFileDigest(data)
	state, err = store.applyCheckpointJournal(digest, baseDigest, state)
	if err != nil {
		return checkpoint.State{}, false, err
	}
	checkpointBaseDigests[path] = baseDigest
	return state, true, nil
}

// PendingCheckpoint reports whether this cache holds interrupted-run state for
// any input at all.
//
// GetCheckpoint answers for one digest, because a run resuming itself knows
// which input it is. Maintenance does not: what it needs to know before it
// collects something a resume would re-read is whether any run could still come
// back, and no digest names that question. A cache directory nothing has
// created yet holds no such run.
func (store *Store) PendingCheckpoint() (bool, error) {
	cacheOperationMutex.RLock()
	defer cacheOperationMutex.RUnlock()
	root := filepath.Join(store.root, "v1")
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("goatest: inspect checkpoints: %w", err)
	}
	for _, entry := range entries {
		// Only a real directory is an entry. A symbolic link is not followed
		// here for the same reason nothing else in this package follows one.
		if !entry.IsDir() {
			continue
		}
		_, err := os.Stat(filepath.Join(root, entry.Name(), CheckpointFileName))
		if err == nil {
			return true, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return false, fmt.Errorf("goatest: inspect checkpoint: %w", err)
		}
	}
	return false, nil
}

// PutCheckpoint atomically replaces interrupted-run state without writing a
// completed report or changing a latest-report index.
func (store *Store) PutCheckpoint(digest string, state checkpoint.State) error {
	cacheOperationMutex.Lock()
	defer cacheOperationMutex.Unlock()
	if state.InputDigest != digest {
		return errors.New("goatest: checkpoint input digest does not match its cache entry")
	}
	if err := checkpoint.Validate(state); err != nil {
		return err
	}
	path, err := store.checkpointPath(digest)
	if err != nil {
		return err
	}
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return fmt.Errorf("goatest: create checkpoint directory: %w", err)
	}
	temporary, err := os.CreateTemp(directory, ".checkpoint-*.tmp")
	if err != nil {
		return fmt.Errorf("goatest: create checkpoint temporary file: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	data := checkpoint.JSON(state)
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("goatest: write checkpoint: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("goatest: sync checkpoint: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("goatest: close checkpoint: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("goatest: atomically publish checkpoint: %w", err)
	}
	checkpointBaseDigests[path] = checkpointFileDigest(data)
	journalPath := filepath.Join(directory, CheckpointJournalFileName)
	if err := os.Remove(journalPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("goatest: remove compacted checkpoint journal: %w", err)
	}
	return nil
}

// AppendBaselineCheckpoint durably adds one completely classified baseline
// target without rewriting the growing checkpoint document. The journal is
// folded into the strict state by GetCheckpoint and compacted by the next full
// PutCheckpoint.
func (store *Store) AppendBaselineCheckpoint(digest string, unit checkpoint.BaselineTarget) error {
	return store.appendCheckpointRecord(digest, checkpointJournalRecord{BaselineTarget: &unit})
}

// AppendMutationCheckpoint is the mutation-unit form of
// AppendBaselineCheckpoint.
func (store *Store) AppendMutationCheckpoint(digest string, unit checkpoint.MutationResult) error {
	return store.appendCheckpointRecord(digest, checkpointJournalRecord{MutationResult: &unit})
}

func (store *Store) appendCheckpointRecord(digest string, record checkpointJournalRecord) error {
	cacheOperationMutex.Lock()
	defer cacheOperationMutex.Unlock()
	path, err := store.checkpointPath(digest)
	if err != nil {
		return err
	}
	baseDigest := checkpointBaseDigests[path]
	if baseDigest == "" {
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return fmt.Errorf("goatest: read checkpoint before journaling: %w", readErr)
		}
		baseDigest = checkpointFileDigest(data)
		checkpointBaseDigests[path] = baseDigest
	}
	record.Schema = checkpointJournalSchema
	record.InputDigest = digest
	record.BaseDigest = baseDigest
	record.Checksum = checkpointJournalChecksum(record)
	data, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("goatest: encode checkpoint journal: %w", err)
	}
	data = append(data, '\n')
	journalPath := filepath.Join(filepath.Dir(path), CheckpointJournalFileName)
	journal, err := os.OpenFile(journalPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("goatest: open checkpoint journal: %w", err)
	}
	if _, err := journal.Write(data); err != nil {
		_ = journal.Close()
		return fmt.Errorf("goatest: append checkpoint journal: %w", err)
	}
	if err := journal.Sync(); err != nil {
		_ = journal.Close()
		return fmt.Errorf("goatest: sync checkpoint journal: %w", err)
	}
	if err := journal.Close(); err != nil {
		return fmt.Errorf("goatest: close checkpoint journal: %w", err)
	}
	return nil
}

func (store *Store) applyCheckpointJournal(digest, baseDigest string, state checkpoint.State) (checkpoint.State, error) {
	path, err := store.checkpointPath(digest)
	if err != nil {
		return checkpoint.State{}, err
	}
	journalPath := filepath.Join(filepath.Dir(path), CheckpointJournalFileName)
	data, err := os.ReadFile(journalPath)
	if errors.Is(err, os.ErrNotExist) {
		return state, nil
	}
	if err != nil {
		return checkpoint.State{}, fmt.Errorf("goatest: read checkpoint journal: %w", err)
	}
	if len(data) == 0 {
		return state, nil
	}
	// A killed writer may leave only the tail of its last record. Newline is
	// the commit marker: every complete line is checksummed and replayed, while
	// an unterminated tail was never a durable scheduling boundary.
	lastNewline := bytes.LastIndexByte(data, '\n')
	if lastNewline < 0 {
		return state, nil
	}
	baselineIndexes := make(map[string]int, len(state.Baseline.Targets))
	for index, unit := range state.Baseline.Targets {
		baselineIndexes[unit.ID] = index
	}
	mutationIndexes := make(map[string]int)
	if state.Mutation != nil {
		mutationIndexes = make(map[string]int, len(state.Mutation.Results))
		for index, unit := range state.Mutation.Results {
			mutationIndexes[unit.ID] = index
		}
	}
	lines := bytes.Split(data[:lastNewline], []byte{'\n'})
	currentBaseSeen := false
	for index, line := range lines {
		decoder := json.NewDecoder(bytes.NewReader(line))
		decoder.DisallowUnknownFields()
		var record checkpointJournalRecord
		if err := decoder.Decode(&record); err != nil {
			return checkpoint.State{}, fmt.Errorf("goatest: checkpoint journal line %d: %w", index+1, err)
		}
		if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
			return checkpoint.State{}, fmt.Errorf("goatest: checkpoint journal line %d has trailing data", index+1)
		}
		if record.Schema != checkpointJournalSchema || record.InputDigest != digest ||
			record.Checksum == "" || record.Checksum != checkpointJournalChecksum(record) {
			return checkpoint.State{}, fmt.Errorf("goatest: checkpoint journal line %d has an invalid identity or checksum", index+1)
		}
		if record.BaseDigest != baseDigest {
			// A crash after publishing a compacted base but before deleting its
			// old journal is expected. Skip that stale prefix: a later process
			// can already have appended records for the current base. Once a
			// current record appears, another base would splice histories and is
			// corruption rather than a compaction remainder.
			if !currentBaseSeen {
				continue
			}
			return checkpoint.State{}, fmt.Errorf("goatest: checkpoint journal line %d changed base identity", index+1)
		}
		currentBaseSeen = true
		if (record.BaselineTarget == nil) == (record.MutationResult == nil) {
			return checkpoint.State{}, fmt.Errorf("goatest: checkpoint journal line %d does not contain exactly one unit", index+1)
		}
		if record.BaselineTarget != nil {
			if existing, replaced := baselineIndexes[record.BaselineTarget.ID]; replaced {
				state.Baseline.Targets[existing] = *record.BaselineTarget
			} else {
				baselineIndexes[record.BaselineTarget.ID] = len(state.Baseline.Targets)
				state.Baseline.Targets = append(state.Baseline.Targets, *record.BaselineTarget)
			}
			continue
		}
		if state.Mutation == nil {
			return checkpoint.State{}, fmt.Errorf("goatest: checkpoint journal line %d records a mutant before its catalog", index+1)
		}
		if existing, replaced := mutationIndexes[record.MutationResult.ID]; replaced {
			state.Mutation.Results[existing] = *record.MutationResult
		} else {
			mutationIndexes[record.MutationResult.ID] = len(state.Mutation.Results)
			state.Mutation.Results = append(state.Mutation.Results, *record.MutationResult)
		}
	}
	slices.SortFunc(state.Baseline.Targets, func(left, right checkpoint.BaselineTarget) int {
		return strings.Compare(left.ID, right.ID)
	})
	if state.Mutation != nil {
		slices.SortFunc(state.Mutation.Results, func(left, right checkpoint.MutationResult) int {
			return strings.Compare(left.ID, right.ID)
		})
	}
	if err := checkpoint.Validate(state); err != nil {
		return checkpoint.State{}, fmt.Errorf("goatest: apply checkpoint journal: %w", err)
	}
	return state, nil
}

func checkpointFileDigest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func checkpointJournalChecksum(record checkpointJournalRecord) string {
	record.Checksum = ""
	data, _ := json.Marshal(record)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// DeleteCheckpoint removes only interrupted-run state. A completed cached
// report in the same exact-input entry remains intact.
func (store *Store) DeleteCheckpoint(digest string) error {
	cacheOperationMutex.Lock()
	defer cacheOperationMutex.Unlock()
	path, err := store.checkpointPath(digest)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("goatest: remove checkpoint: %w", err)
	}
	directory := filepath.Dir(path)
	if err := os.Remove(filepath.Join(directory, CheckpointJournalFileName)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("goatest: remove checkpoint journal: %w", err)
	}
	delete(checkpointBaseDigests, path)
	entries, err := os.ReadDir(directory)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("goatest: inspect checkpoint directory: %w", err)
	}
	if len(entries) == 0 {
		if err := os.Remove(directory); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("goatest: remove empty checkpoint directory: %w", err)
		}
	}
	return nil
}

func (store *Store) checkpointPath(digest string) (string, error) {
	reportPath, err := store.path(digest)
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(reportPath), CheckpointFileName), nil
}
