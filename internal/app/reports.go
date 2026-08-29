// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package app

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

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

// WriteReports atomically projects one verdict to every supported report
// format and to the latest-report index used by explain/replay/accept.
func WriteReports(root string, input report.Report) error {
	jsonReport := report.JSON(input)
	htmlReport := report.HTML(input)
	sarifReport := report.SARIF(input)
	junitReport := report.JUnit(input)
	schema := report.JSONSchema()
	artifacts := []struct {
		path string
		data []byte
	}{
		{filepath.Join(root, ".goatest", "report.json"), jsonReport},
		{filepath.Join(root, "reports", "assurance-report-v1.json"), jsonReport},
		{filepath.Join(root, "reports", "assurance-report-v1.html"), htmlReport},
		{filepath.Join(root, "reports", "assurance-report-v1.sarif"), sarifReport},
		{filepath.Join(root, "reports", "assurance-report-v1.junit.xml"), junitReport},
		{filepath.Join(root, "reports", "assurance-report-v1.schema.json"), schema},
	}
	for _, artifact := range artifacts {
		if err := atomicWrite(artifact.path, artifact.data); err != nil {
			return fmt.Errorf("goatest: write report %s: %w", artifact.path, err)
		}
	}
	return nil
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
