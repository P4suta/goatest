// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package repair_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/P4suta/goatest/internal/provider"
	"github.com/P4suta/goatest/internal/repair"
	"github.com/P4suta/goatest/internal/report"
)

type validator struct{ calls []string }

func (validator *validator) OriginalStable(context.Context, provider.Candidate) error {
	validator.calls = append(validator.calls, "stable")
	return nil
}
func (validator *validator) Kills(context.Context, report.Finding, provider.Candidate) error {
	validator.calls = append(validator.calls, "kill")
	return nil
}
func (validator *validator) Suite(context.Context, provider.Candidate) error {
	validator.calls = append(validator.calls, "suite")
	return nil
}

func TestValidateAndApplyUsesRequiredRunsAndAtomicPreimage(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "roundtrip_test.go")
	before := []byte("package fixture\n")
	after := []byte("package fixture\n\nfunc TestRoundTrip() {}\n")
	if err := os.WriteFile(path, before, 0o644); err != nil {
		t.Fatal(err)
	}
	checker := &validator{}
	result, err := repair.ValidateAndApply(t.Context(), root, report.Finding{ID: "finding-a"}, provider.Candidate{
		Kind: "patch", Path: "roundtrip_test.go", PreimageSHA256: digest(before), Content: after,
	}, checker)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != repair.StatusApplied {
		t.Errorf("result = %+v", result)
	}
	wantCalls := []string{"stable", "stable", "stable", "kill", "kill", "suite"}
	if !slices.Equal(checker.calls, wantCalls) {
		t.Errorf("validation calls = %v, want %v", checker.calls, wantCalls)
	}
	got, err := os.ReadFile(path)
	if err != nil || !slices.Equal(got, after) {
		t.Fatalf("applied bytes = %q, %v", got, err)
	}
}

func TestApplyRefusesProductionPathsAndPreservesDirtyEdits(t *testing.T) {
	root := t.TempDir()
	checker := &validator{}
	if _, err := repair.ValidateAndApply(t.Context(), root, report.Finding{ID: "finding-a"}, provider.Candidate{
		Kind: "patch", Path: "production.go", Content: []byte("changed"),
	}, checker); err == nil {
		t.Fatal("production path was accepted")
	}
	if len(checker.calls) != 0 {
		t.Errorf("invalid path reached validation: %v", checker.calls)
	}

	path := filepath.Join(root, "roundtrip_test.go")
	userEdit := []byte("package fixture // user edit\n")
	if err := os.WriteFile(path, userEdit, 0o644); err != nil {
		t.Fatal(err)
	}
	checker = &validator{}
	result, err := repair.ValidateAndApply(t.Context(), root, report.Finding{ID: "finding-dirty"}, provider.Candidate{
		Kind: "patch", Path: "roundtrip_test.go", PreimageSHA256: digest([]byte("old")), Content: []byte("candidate"),
	}, checker)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != repair.StatusArtifact || result.Artifact == "" {
		t.Fatalf("result = %+v", result)
	}
	got, _ := os.ReadFile(path)
	if !slices.Equal(got, userEdit) {
		t.Fatalf("dirty edit was overwritten: %q", got)
	}
	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(result.Artifact))); err != nil {
		t.Fatalf("artifact missing: %v", err)
	}
}

func TestApplyRejectsSymlinkDirectoryEscapingRepository(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "linked")); err != nil {
		t.Skipf("directory symlinks are unavailable: %v", err)
	}
	checker := &validator{}
	_, err := repair.ValidateAndApply(t.Context(), root, report.Finding{ID: "finding-escape"}, provider.Candidate{
		Kind: "patch", Path: "linked/escape_test.go", Content: []byte("package escaped\n"),
	}, checker)
	if err == nil {
		t.Fatal("repair followed a symlink outside the repository")
	}
	if len(checker.calls) != 0 {
		t.Fatalf("symlink escape reached validation: %v", checker.calls)
	}
	if _, statErr := os.Stat(filepath.Join(outside, "escape_test.go")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("outside target exists after rejected repair: %v", statErr)
	}
}

func TestApplyAcceptsRepositoryRootAlias(t *testing.T) {
	realRoot := t.TempDir()
	aliasRoot := filepath.Join(t.TempDir(), "repository-alias")
	if err := os.Symlink(realRoot, aliasRoot); err != nil {
		t.Skipf("directory symlinks are unavailable: %v", err)
	}
	result, err := repair.ValidateAndApply(t.Context(), aliasRoot, report.Finding{ID: "finding-alias"}, provider.Candidate{
		Kind: "patch", Path: "alias_test.go", Content: []byte("package fixture\n"),
	}, &validator{})
	if err != nil || result.Status != repair.StatusApplied {
		t.Fatalf("aliased-root repair = %+v, %v", result, err)
	}
	if _, err := os.Stat(filepath.Join(realRoot, "alias_test.go")); err != nil {
		t.Fatalf("repair was not written through root alias: %v", err)
	}
}

func TestDirtyArtifactHashesUntrustedFindingIdentity(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "repository")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "candidate_test.go")
	if err := os.WriteFile(path, []byte("user edit"), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := repair.ValidateAndApply(t.Context(), root, report.Finding{ID: "../../../outside"}, provider.Candidate{
		Kind: "patch", Path: "candidate_test.go", PreimageSHA256: digest([]byte("old")), Content: []byte("candidate"),
	}, &validator{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != repair.StatusArtifact || strings.Contains(result.Artifact, "..") {
		t.Fatalf("artifact = %+v", result)
	}
	absolute := filepath.Join(root, filepath.FromSlash(result.Artifact))
	relative, err := filepath.Rel(root, absolute)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		t.Fatalf("artifact escaped repository: path=%q relative=%q err=%v", absolute, relative, err)
	}
	if _, statErr := os.Stat(filepath.Join(parent, "outside.json")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("outside artifact exists: %v", statErr)
	}
}

func TestCorpusPathsAreAllowed(t *testing.T) {
	if !repair.AllowedPath("testdata/fuzz/FuzzRoundTrip/abc123") || !repair.AllowedPath("pkg/testdata/fuzz/FuzzX/seed") {
		t.Fatal("standard fuzz corpus path was refused")
	}
	for _, path := range []string{"./A:/escape_test.go", "pkg/\x00_test.go"} {
		if repair.AllowedPath(path) {
			t.Errorf("cross-platform-invalid path %q was accepted", path)
		}
	}
}

func digest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
