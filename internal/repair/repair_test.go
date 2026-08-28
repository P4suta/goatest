// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package repair_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"slices"
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

func TestCorpusPathsAreAllowed(t *testing.T) {
	if !repair.AllowedPath("testdata/fuzz/FuzzRoundTrip/abc123") || !repair.AllowedPath("pkg/testdata/fuzz/FuzzX/seed") {
		t.Fatal("standard fuzz corpus path was refused")
	}
}

func digest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
