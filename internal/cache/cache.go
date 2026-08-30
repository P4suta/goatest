// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package cache stores exact-input assurance evidence.
package cache

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/P4suta/goatest/internal/report"
)

type Store struct {
	root     string
	maxBytes int64
	ttl      time.Duration
	now      func() time.Time
}

var cacheOperationMutex sync.RWMutex

type cacheWritableFile interface {
	Name() string
	Write([]byte) (int, error)
	Sync() error
	Close() error
}

var (
	readCacheFile   = os.ReadFile
	mkdirCacheAll   = os.MkdirAll
	createCacheTemp = func(directory, pattern string) (cacheWritableFile, error) {
		return os.CreateTemp(directory, pattern)
	}
	removeCacheFile = os.Remove
	renameCacheFile = os.Rename
)

func New(root string) *Store { return &Store{root: root} }

// NewWithPolicy enables bounded automatic collection after successful writes.
func NewWithPolicy(root string, maxBytes int64, ttl time.Duration) *Store {
	return &Store{root: root, maxBytes: maxBytes, ttl: ttl, now: time.Now}
}

func (store *Store) Get(digest string) (report.Report, bool, error) {
	cacheOperationMutex.RLock()
	defer cacheOperationMutex.RUnlock()
	path, err := store.path(digest)
	if err != nil {
		return report.Report{}, false, err
	}
	data, err := readCacheFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return report.Report{}, false, nil
	}
	if err != nil {
		return report.Report{}, false, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var result report.Report
	if err := decoder.Decode(&result); err != nil {
		return report.Report{}, false, fmt.Errorf("goatest: cache decode: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return report.Report{}, false, errors.New("goatest: cache report has trailing data")
	}
	if result.Schema != report.SchemaV1 || result.Snapshot != digest {
		return report.Report{}, false, errors.New("goatest: cache report identity mismatch")
	}
	if err := report.Validate(result); err != nil {
		return report.Report{}, false, fmt.Errorf("goatest: invalid cache report: %w", err)
	}
	return result, true, nil
}

func (store *Store) Put(digest string, result report.Report) error {
	cacheOperationMutex.Lock()
	defer cacheOperationMutex.Unlock()
	if result.Snapshot != digest {
		return errors.New("goatest: cache snapshot does not match its digest")
	}
	if err := report.Validate(result); err != nil {
		return fmt.Errorf("goatest: invalid cache report: %w", err)
	}
	path, err := store.path(digest)
	if err != nil {
		return err
	}
	data := report.JSON(result)
	if err := mkdirCacheAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	temporary, err := createCacheTemp(filepath.Dir(path), ".report-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer func() { _ = removeCacheFile(temporaryPath) }()
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
	if err := renameCacheFile(temporaryPath, path); err != nil {
		if removeErr := removeCacheFile(path); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			return errors.Join(err, removeErr)
		}
		if retryErr := renameCacheFile(temporaryPath, path); retryErr != nil {
			return retryErr
		}
	}
	if store.maxBytes > 0 || store.ttl > 0 {
		now := time.Now
		if store.now != nil {
			now = store.now
		}
		if _, err := collectUnlocked(store.root, store.maxBytes, store.ttl, now()); err != nil {
			return fmt.Errorf("goatest: cache collection: %w", err)
		}
	}
	return nil
}

func (store *Store) path(digest string) (string, error) {
	if digest == "" || digest == "." || digest == ".." || strings.ContainsAny(digest, `/\`) {
		return "", fmt.Errorf("goatest: invalid cache digest %q", digest)
	}
	return filepath.Join(store.root, "v1", digest, "report.json"), nil
}
