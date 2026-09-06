// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package testkit

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/P4suta/goatest/internal/filemode"
	"github.com/P4suta/goatest/internal/report"
)

var ErrGoldenMismatch = errors.New("goatest: golden file mismatch")

const (
	NormalizedRunID     = "normalized-run"
	NormalizedSnapshot  = "normalized-snapshot"
	NormalizedCommit    = "normalized-commit"
	NormalizedMergeBase = "normalized-merge-base"
	NormalizedGoVersion = "normalized-go"
	NormalizedTimestamp = "1970-01-01T00:00:00Z"
)

var updateGolden = flag.Bool("update", false, "rewrite the golden files under testdata")

func Update() bool { return *updateGolden }

func GoldenPath(name string) string { return filepath.Join("testdata", name) }

func Golden(t testing.TB, name string, got []byte) {
	t.Helper()
	if err := CompareGolden(GoldenPath(name), got, Update()); err != nil {
		t.Fatalf("%v (rerun with -update to accept the recorded bytes)", err)
	}
}

func CompareGolden(path string, got []byte, update bool) error {
	want, err := os.ReadFile(path)
	if err == nil && bytes.Equal(want, got) {
		return nil
	}
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("goatest: reading golden file %s: %w", path, err)
	}
	if !update {
		if err != nil {
			return fmt.Errorf("goatest: golden file %s is missing: %w", path, err)
		}
		return fmt.Errorf("goatest: golden file %s: %w", path, ErrGoldenMismatch)
	}
	if directoryErr := os.MkdirAll(filepath.Dir(path), filemode.ReadableDirectory); directoryErr != nil {
		return fmt.Errorf("goatest: creating the directory of golden file %s: %w", path, directoryErr)
	}
	if writeErr := os.WriteFile(path, got, filemode.ReadableFile); writeErr != nil {
		return fmt.Errorf("goatest: writing golden file %s: %w", path, writeErr)
	}
	return nil
}

func NormalizeReport(input report.Report) report.Report {
	normalized := input
	normalized.Scope.Requested = normalizeScope(input.Scope.Requested)
	normalized.Scope.Resolved = normalizeScope(input.Scope.Resolved)
	normalized.Repository.Packages = slices.Clone(input.Repository.Packages)
	normalized.Repository.Git.ChangedFiles = slices.Clone(input.Repository.Git.ChangedFiles)
	normalized.Mutants = slices.Clone(input.Mutants)
	normalized.Acceptances = slices.Clone(input.Acceptances)
	normalized.Evidence = slices.Clone(input.Evidence)
	normalized.Findings = slices.Clone(input.Findings)
	normalized.Repairs = slices.Clone(input.Repairs)
	normalized.Limitations = slices.Clone(input.Limitations)

	normalized.RunID = normalizeIdentity(input.RunID, NormalizedRunID)
	normalized.Snapshot = normalizeIdentity(input.Snapshot, NormalizedSnapshot)
	normalized.Repository.Git.Commit = normalizeIdentity(input.Repository.Git.Commit, NormalizedCommit)
	normalized.Repository.Git.MergeBase = normalizeIdentity(input.Repository.Git.MergeBase, NormalizedMergeBase)
	normalized.Toolchain.Go = normalizeIdentity(input.Toolchain.Go, NormalizedGoVersion)
	normalized.Timing.StartedAt = normalizeIdentity(input.Timing.StartedAt, NormalizedTimestamp)
	normalized.Timing.FinishedAt = normalizeIdentity(input.Timing.FinishedAt, NormalizedTimestamp)
	normalized.Timing.DurationMS = 0
	return normalized
}

func normalizeScope(scope report.ScopeSpec) report.ScopeSpec {
	scope.Modules = slices.Clone(scope.Modules)
	scope.Packages = slices.Clone(scope.Packages)
	scope.Files = slices.Clone(scope.Files)
	return scope
}

func normalizeIdentity(value, normalized string) string {
	if value == "" {
		return ""
	}
	return normalized
}
