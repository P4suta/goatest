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

	"github.com/P4suta/goatest/internal/report"
)

// ErrGoldenMismatch reports that recorded bytes differ from the golden file.
// A comparison never rewrites the file, so the recorded bytes must be accepted
// deliberately with -update.
var ErrGoldenMismatch = errors.New("goatest: golden file mismatch")

// The normalized values replace the identity a run legitimately varies, so
// that a golden report asserts on the assurance result rather than on the
// machine, the clock, or the checkout that produced it.
const (
	NormalizedRunID     = "normalized-run"
	NormalizedSnapshot  = "normalized-snapshot"
	NormalizedCommit    = "normalized-commit"
	NormalizedMergeBase = "normalized-merge-base"
	NormalizedGoVersion = "normalized-go"
	NormalizedTimestamp = "1970-01-01T00:00:00Z"
)

// updateGolden is registered once per test binary. Registering it here rather
// than in each test package keeps -update meaning the same thing everywhere.
var updateGolden = flag.Bool("update", false, "rewrite the golden files under testdata")

// Update reports whether the test binary was started with -update.
func Update() bool { return *updateGolden }

// GoldenPath is the location of a golden file relative to the package
// directory, the working directory of a Go test.
func GoldenPath(name string) string { return filepath.Join("testdata", name) }

// Golden compares got against the named golden file, reporting one failure
// that names the file when they differ, and rewriting the file instead under
// -update.
func Golden(t testing.TB, name string, got []byte) {
	t.Helper()
	if err := CompareGolden(GoldenPath(name), got, Update()); err != nil {
		t.Fatalf("%v (rerun with -update to accept the recorded bytes)", err)
	}
}

// CompareGolden is the whole comparison, separated from the test framework so
// that it is testable on its own. Without update it is read-only: a mismatch
// and a missing file are both errors, and the file is left exactly as it was.
// With update it writes got, creating the parent directories it needs, and
// leaves an already matching file untouched.
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
	if directoryErr := os.MkdirAll(filepath.Dir(path), 0o755); directoryErr != nil {
		return fmt.Errorf("goatest: creating the directory of golden file %s: %w", path, directoryErr)
	}
	if writeErr := os.WriteFile(path, got, 0o644); writeErr != nil {
		return fmt.Errorf("goatest: writing golden file %s: %w", path, writeErr)
	}
	return nil
}

// NormalizeReport replaces the identity fields that legitimately differ
// between two runs of the same assurance work with fixed values, and leaves
// every other field, including the evidence a golden file exists to protect,
// exactly as it was. An absent field stays absent, the input is not modified,
// and normalizing an already normalized report changes nothing.
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

// normalizeScope detaches a scope from the input report; scopes carry no
// run-specific identity of their own.
func normalizeScope(scope report.ScopeSpec) report.ScopeSpec {
	scope.Modules = slices.Clone(scope.Modules)
	scope.Packages = slices.Clone(scope.Packages)
	scope.Files = slices.Clone(scope.Files)
	return scope
}

// normalizeIdentity substitutes a fixed value for a field that varies between
// runs, keeping an absent field absent so that a golden file still proves the
// difference between reported and unreported identity.
func normalizeIdentity(value, normalized string) string {
	if value == "" {
		return ""
	}
	return normalized
}
