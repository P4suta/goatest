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

// UpdateFlagName is the flag that accepts the bytes a run recorded, rather
// than comparing against the bytes a golden file holds.
const UpdateFlagName = "update"

// init registers UpdateFlagName unless something already has.
//
// Registering a flag twice panics, and the panic lands before a single test
// runs, so a binary that links two packages each convinced it owns this name
// cannot even report which two. go-mutants registers the same name in its own
// harness, and the two trees are being merged into one module pair, which is
// exactly the third package that turns a latent collision into a certain one.
//
// Tolerating an existing registration is the conservative half of the fix. The
// other half is Update, which reads the flag through the set rather than
// through a pointer this package holds, so a value registered elsewhere is
// still the value this package obeys.
func init() { registerUpdateFlag() }

// registerUpdateFlag registers UpdateFlagName unless it is already registered,
// and reports whether this call was the one that registered it.
//
// It is separate from init so that a test can call it a second time and show
// that the second call is a no-op rather than a panic.
func registerUpdateFlag() bool {
	if flag.Lookup(UpdateFlagName) != nil {
		return false
	}
	flag.Bool(UpdateFlagName, false, "rewrite the golden files under testdata")
	return true
}

// Update reports whether this run was asked to accept what it recorded.
//
// It reads the flag set rather than a pointer captured at registration, so it
// answers for whichever package registered the flag. An unregistered flag - a
// binary that linked this package but not the testing flags, which is not a
// test binary - reports false, because "accept whatever you produced" is not a
// safe default to reach by accident.
func Update() bool {
	registered := flag.Lookup(UpdateFlagName)
	if registered == nil {
		return false
	}
	return registered.Value.String() == "true"
}

// GoldenPath is where the golden file for one name lives, relative to the
// package under test.
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
