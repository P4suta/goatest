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

	"github.com/P4suta/goatest/internal/report"
)

type Store struct{ root string }

func New(root string) *Store { return &Store{root: root} }

func (store *Store) Get(digest string) (report.Report, bool, error) {
	path, err := store.path(digest)
	if err != nil {
		return report.Report{}, false, err
	}
	data, err := os.ReadFile(path)
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
	return result, true, nil
}

func (store *Store) Put(digest string, result report.Report) error {
	if result.Snapshot != digest {
		return errors.New("goatest: cache snapshot does not match its digest")
	}
	path, err := store.path(digest)
	if err != nil {
		return err
	}
	data := report.JSON(result)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".report-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
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
	if err := os.Rename(temporaryPath, path); err != nil {
		if removeErr := os.Remove(path); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			return errors.Join(err, removeErr)
		}
		if retryErr := os.Rename(temporaryPath, path); retryErr != nil {
			return retryErr
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
