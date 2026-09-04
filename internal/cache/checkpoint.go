// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package cache

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/P4suta/goatest/internal/checkpoint"
)

const CheckpointFileName = "checkpoint-v1.json"

// GetCheckpoint reads strict interrupted-run state for exactly digest.
func (store *Store) GetCheckpoint(digest string) (checkpoint.State, bool, error) {
	cacheOperationMutex.RLock()
	defer cacheOperationMutex.RUnlock()
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
	if _, err := temporary.Write(checkpoint.JSON(state)); err != nil {
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
	return nil
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
