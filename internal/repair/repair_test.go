// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package repair_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
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

func TestAllowedPathMatrix(t *testing.T) {
	t.Parallel()
	for _, path := range []string{
		"root_test.go",
		"pkg/sub_test.go",
		"testdata/fuzz/FuzzRoundTrip/seed",
		"pkg/testdata/fuzz/FuzzRoundTrip/nested/seed",
		"./pkg/../pkg/value_test.go",
	} {
		if !repair.AllowedPath(path) {
			t.Errorf("AllowedPath(%q) = false", path)
		}
	}
	for _, path := range []string{
		"", ".", "..", "../escape_test.go", "/escape_test.go", `C:/escape_test.go`, `pkg\escape_test.go`,
		"production.go", "testdata/fuzz", "testdata/fuzz/FuzzRoundTrip", "testdata/other/FuzzRoundTrip/seed",
		"fuzz/FuzzRoundTrip/seed", "testdata/fuzz/FuzzRoundTrip/../seed", "pkg/testdata/fuzz/FuzzRoundTrip",
	} {
		if repair.AllowedPath(path) {
			t.Errorf("AllowedPath(%q) = true", path)
		}
	}
}

func TestValidateAndApplyRejectsInvalidKindAndEveryValidationFailure(t *testing.T) {
	root := t.TempDir()
	baseCandidate := provider.Candidate{Kind: "patch", Path: "candidate_test.go", Content: []byte("package fixture\n")}
	for _, kind := range []string{"", "PATCH", "test"} {
		candidate := baseCandidate
		candidate.Kind = kind
		checker := &validator{}
		_, err := repair.ValidateAndApply(t.Context(), root, report.Finding{ID: "finding-a"}, candidate, checker)
		want := fmt.Sprintf("goatest: repair candidate kind %q is invalid", kind)
		if err == nil || err.Error() != want || len(checker.calls) != 0 {
			t.Fatalf("kind %q: error=%v calls=%v", kind, err, checker.calls)
		}
	}

	for _, test := range []struct {
		name      string
		stage     string
		at        int
		wantCalls []string
		want      string
	}{
		{name: "first stable", stage: "stable", at: 1, wantCalls: []string{"stable"}, want: "goatest: candidate is unstable on original code: validation failed"},
		{name: "second stable", stage: "stable", at: 2, wantCalls: []string{"stable", "stable"}, want: "goatest: candidate is unstable on original code: validation failed"},
		{name: "third stable", stage: "stable", at: 3, wantCalls: []string{"stable", "stable", "stable"}, want: "goatest: candidate is unstable on original code: validation failed"},
		{name: "first kill", stage: "kill", at: 1, wantCalls: []string{"stable", "stable", "stable", "kill"}, want: "goatest: candidate does not detect target mutant: validation failed"},
		{name: "second kill", stage: "kill", at: 2, wantCalls: []string{"stable", "stable", "stable", "kill", "kill"}, want: "goatest: candidate does not detect target mutant: validation failed"},
		{name: "suite", stage: "suite", at: 1, wantCalls: []string{"stable", "stable", "stable", "kill", "kill", "suite"}, want: "goatest: candidate fails related suite: validation failed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			checker := &stagedValidator{stage: test.stage, at: test.at}
			_, err := repair.ValidateAndApply(t.Context(), root, report.Finding{ID: "finding-a"}, baseCandidate, checker)
			if err == nil || err.Error() != test.want || !slices.Equal(checker.calls, test.wantCalls) {
				t.Fatalf("error=%v calls=%v, want error=%q calls=%v", err, checker.calls, test.want, test.wantCalls)
			}
			if _, statErr := os.Stat(filepath.Join(root, "candidate_test.go")); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("failed candidate was applied: %v", statErr)
			}
		})
	}
}

func TestValidateAndApplyCreatesNewFileAndPreservesExistingMode(t *testing.T) {
	root := t.TempDir()
	newResult, err := repair.ValidateAndApply(t.Context(), root, report.Finding{ID: "finding-new"}, provider.Candidate{
		Kind: "patch", Path: "new_test.go", Content: []byte("package fixture\n"),
	}, &validator{})
	if err != nil || newResult != (repair.Result{Status: repair.StatusApplied, Path: "new_test.go"}) {
		t.Fatalf("new result = %+v, %v", newResult, err)
	}
	newInfo, err := os.Stat(filepath.Join(root, "new_test.go"))
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && newInfo.Mode().Perm() != 0o644 || runtime.GOOS == "windows" && newInfo.Mode().Perm()&0o200 == 0 {
		t.Fatalf("new mode = %o", newInfo.Mode().Perm())
	}

	existingPath := filepath.Join(root, "existing_test.go")
	before := []byte("package fixture\n")
	if err := os.WriteFile(existingPath, before, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(existingPath, 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := repair.ValidateAndApply(t.Context(), root, report.Finding{ID: "finding-existing"}, provider.Candidate{
		Kind: "patch", Path: "existing_test.go", PreimageSHA256: digest(before), Content: []byte("package fixture // changed\n"),
	}, &validator{})
	if err != nil || result != (repair.Result{Status: repair.StatusApplied, Path: "existing_test.go"}) {
		t.Fatalf("existing result = %+v, %v", result, err)
	}
	info, err := os.Stat(existingPath)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 || runtime.GOOS == "windows" && info.Mode().Perm()&0o200 == 0 {
		t.Fatalf("existing mode = %o", info.Mode().Perm())
	}
}

func TestDirtyArtifactContainsDeterministicReasonAndCandidate(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "candidate_test.go")
	if err := os.WriteFile(path, []byte("user edit"), 0o644); err != nil {
		t.Fatal(err)
	}
	candidate := provider.Candidate{
		Kind: "patch", Path: "candidate_test.go", PreimageSHA256: digest([]byte("old")), Content: []byte("candidate"), Replay: "goatest replay finding-a",
	}
	result, err := repair.ValidateAndApply(t.Context(), root, report.Finding{ID: "finding-a"}, candidate, &validator{})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(result.Artifact)))
	if err != nil {
		t.Fatal(err)
	}
	if len(payload) == 0 || payload[len(payload)-1] != '\n' {
		t.Fatalf("artifact has no final newline: %q", payload)
	}
	var document struct {
		Finding   string             `json:"finding"`
		Reason    string             `json:"reason"`
		Candidate provider.Candidate `json:"candidate"`
	}
	if err := json.Unmarshal(payload, &document); err != nil {
		t.Fatal(err)
	}
	if document.Finding != "finding-a" || document.Reason != "preimage changed; user edit preserved" || document.Candidate.Path != candidate.Path || !slices.Equal(document.Candidate.Content, candidate.Content) {
		t.Fatalf("artifact = %+v", document)
	}
	info, err := os.Stat(filepath.Join(root, filepath.FromSlash(result.Artifact)))
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 || runtime.GOOS == "windows" && info.Mode().Perm()&0o200 == 0 {
		t.Fatalf("artifact mode = %o", info.Mode().Perm())
	}
}

type stagedValidator struct {
	stage string
	at    int
	seen  map[string]int
	calls []string
}

func (validator *stagedValidator) record(stage string) error {
	if validator.seen == nil {
		validator.seen = make(map[string]int)
	}
	validator.seen[stage]++
	validator.calls = append(validator.calls, stage)
	if validator.stage == stage && validator.seen[stage] == validator.at {
		return errors.New("validation failed")
	}
	return nil
}

func (validator *stagedValidator) OriginalStable(context.Context, provider.Candidate) error {
	return validator.record("stable")
}

func (validator *stagedValidator) Kills(context.Context, report.Finding, provider.Candidate) error {
	return validator.record("kill")
}

func (validator *stagedValidator) Suite(context.Context, provider.Candidate) error {
	return validator.record("suite")
}

func digest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
