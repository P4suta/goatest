// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package app

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/P4suta/goatest/internal/report"
)

type atomicReportFile interface {
	Name() string
	Write([]byte) (int, error)
	Sync() error
	Chmod(os.FileMode) error
	Close() error
}

type atomicWriteOperations struct {
	mkdirAll   func(string, os.FileMode) error
	createTemp func(string, string) (atomicReportFile, error)
	remove     func(string) error
	rename     func(string, string) error
}

var operatingSystemAtomicWrites = atomicWriteOperations{
	mkdirAll: os.MkdirAll,
	createTemp: func(directory, pattern string) (atomicReportFile, error) {
		return os.CreateTemp(directory, pattern)
	},
	remove: os.Remove,
	rename: os.Rename,
}

// WriteReports atomically projects one completed run to its immutable history
// directory and then advances the scope-aware indexes. A changeset, package,
// or replay run can never replace latest-full.
func WriteReports(root string, input report.Report) error {
	if err := report.ValidateForPersistence(input); err != nil {
		return err
	}
	if !safeRunID(input.RunID) {
		return fmt.Errorf("goatest: unsafe report run ID %q", input.RunID)
	}
	jsonReport := report.JSON(input)
	htmlReport := report.HTML(input)
	sarifReport := report.SARIF(input)
	junitReport := report.JUnit(input)
	schema := report.JSONSchema()
	runsDirectory := filepath.Join(root, "reports", "runs")
	runDirectory := filepath.Join(runsDirectory, input.RunID)
	if _, err := os.Stat(runDirectory); err == nil {
		return fmt.Errorf("goatest: report run %s already exists", input.RunID)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("goatest: inspect report run %s: %w", input.RunID, err)
	}
	if err := os.MkdirAll(runsDirectory, 0o755); err != nil {
		return fmt.Errorf("goatest: create report history: %w", err)
	}
	stagingDirectory, err := os.MkdirTemp(runsDirectory, ".goatest-run-*")
	if err != nil {
		return fmt.Errorf("goatest: stage report run %s: %w", input.RunID, err)
	}
	defer func() { _ = os.RemoveAll(stagingDirectory) }()
	artifacts := []struct {
		name string
		data []byte
	}{
		{"assurance-report-v1.json", jsonReport},
		{"assurance-report-v1.html", htmlReport},
		{"assurance-report-v1.sarif", sarifReport},
		{"assurance-report-v1.junit.xml", junitReport},
		{"assurance-report-v1.schema.json", schema},
	}
	for _, artifact := range artifacts {
		path := filepath.Join(stagingDirectory, artifact.name)
		if err := atomicWrite(path, artifact.data); err != nil {
			return fmt.Errorf("goatest: write report %s: %w", path, err)
		}
	}
	if err := os.Rename(stagingDirectory, runDirectory); err != nil {
		return fmt.Errorf("goatest: publish report run %s: %w", input.RunID, err)
	}
	indexes := []string{
		filepath.Join(root, ".goatest", "latest-any.json"),
		filepath.Join(root, "reports", "latest-any.json"),
	}
	if input.RunKind == report.RunFull {
		indexes = append(indexes,
			filepath.Join(root, ".goatest", "latest-full.json"),
			filepath.Join(root, "reports", "latest-full.json"),
		)
	}
	for _, path := range indexes {
		if err := atomicWrite(path, jsonReport); err != nil {
			return fmt.Errorf("goatest: write report index %s: %w", path, err)
		}
	}
	return nil
}

func safeRunID(id string) bool {
	if id == "" || id == "." || id == ".." || strings.ContainsAny(id, `/\\`) {
		return false
	}
	for _, character := range id {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' || strings.ContainsRune("._-", character) {
			continue
		}
		return false
	}
	return true
}

func atomicWrite(path string, data []byte) error {
	return atomicWriteWith(path, data, operatingSystemAtomicWrites)
}

func atomicWriteWith(path string, data []byte, operations atomicWriteOperations) error {
	if err := operations.mkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	temporary, err := operations.createTemp(filepath.Dir(path), ".goatest-report-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer func() { _ = operations.remove(temporaryPath) }()
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Chmod(0o644); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := operations.rename(temporaryPath, path); err != nil {
		if removeErr := operations.remove(path); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			return errors.Join(err, removeErr)
		}
		return operations.rename(temporaryPath, path)
	}
	return nil
}
