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

// WriteReports atomically projects one verdict to every supported report
// format and to the latest-report index used by explain/replay/accept.
func WriteReports(root string, input report.Report) error {
	jsonReport, err := report.JSON(input)
	if err != nil {
		return err
	}
	htmlReport, err := report.HTML(input)
	if err != nil {
		return err
	}
	sarifReport, err := report.SARIF(input)
	if err != nil {
		return err
	}
	junitReport, err := report.JUnit(input)
	if err != nil {
		return err
	}
	schema, err := report.JSONSchema()
	if err != nil {
		return err
	}
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
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".goatest-report-*.tmp")
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
	if err := temporary.Chmod(0o644); err != nil {
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
		return os.Rename(temporaryPath, path)
	}
	return nil
}
