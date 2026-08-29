// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package repair validates and atomically applies generated test and corpus
// candidates.
package repair

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/P4suta/goatest/internal/provider"
	"github.com/P4suta/goatest/internal/report"
)

type Status string

const (
	StatusApplied  Status = "applied"
	StatusArtifact Status = "artifact"
)

type Result struct {
	Status   Status
	Path     string
	Artifact string
}

type Validator interface {
	OriginalStable(context.Context, provider.Candidate) error
	Kills(context.Context, report.Finding, provider.Candidate) error
	Suite(context.Context, provider.Candidate) error
}

func AllowedPath(path string) bool {
	normalized, ok := normalize(path)
	if !ok {
		return false
	}
	if strings.HasSuffix(normalized, "_test.go") {
		return true
	}
	parts := strings.Split(normalized, "/")
	for i := 0; i+3 < len(parts); i++ {
		if parts[i] == "testdata" && parts[i+1] == "fuzz" && parts[i+2] != "" && parts[i+3] != "" {
			return true
		}
	}
	return false
}

func ValidateAndApply(ctx context.Context, root string, finding report.Finding, candidate provider.Candidate, validator Validator) (Result, error) {
	normalized, ok := normalize(candidate.Path)
	if !ok || !AllowedPath(normalized) {
		return Result{}, fmt.Errorf("goatest: repair path %q is outside _test.go and standard fuzz corpus", candidate.Path)
	}
	candidate.Path = normalized
	if candidate.Kind != "patch" && candidate.Kind != "corpus" {
		return Result{}, fmt.Errorf("goatest: repair candidate kind %q is invalid", candidate.Kind)
	}
	if _, err := confinedPath(root, normalized); err != nil {
		return Result{}, err
	}
	for range 3 {
		if err := validator.OriginalStable(ctx, candidate); err != nil {
			return Result{}, fmt.Errorf("goatest: candidate is unstable on original code: %w", err)
		}
	}
	for range 2 {
		if err := validator.Kills(ctx, finding, candidate); err != nil {
			return Result{}, fmt.Errorf("goatest: candidate does not detect target mutant: %w", err)
		}
	}
	if err := validator.Suite(ctx, candidate); err != nil {
		return Result{}, fmt.Errorf("goatest: candidate fails related suite: %w", err)
	}

	target, err := confinedPath(root, normalized)
	if err != nil {
		return Result{}, err
	}
	match, mode, err := matchesPreimage(target, candidate.PreimageSHA256)
	if err != nil {
		return Result{}, err
	}
	if !match {
		artifact, err := writeArtifact(root, finding, candidate)
		if err != nil {
			return Result{}, err
		}
		return Result{Status: StatusArtifact, Path: normalized, Artifact: artifact}, nil
	}
	if err := atomicWrite(root, normalized, candidate.Content, mode); err != nil {
		return Result{}, fmt.Errorf("goatest: apply repair %s: %w", normalized, err)
	}
	return Result{Status: StatusApplied, Path: normalized}, nil
}

func normalize(path string) (string, bool) {
	if path == "" || strings.ContainsAny(path, "\\:\x00") {
		return "", false
	}
	native := filepath.FromSlash(path)
	if filepath.IsAbs(native) || filepath.VolumeName(native) != "" {
		return "", false
	}
	clean := filepath.Clean(native)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) ||
		filepath.IsAbs(clean) || filepath.VolumeName(clean) != "" {
		return "", false
	}
	return filepath.ToSlash(clean), true
}

func confinedPath(root, normalized string) (string, error) {
	absoluteRoot, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("goatest: resolve repair root: %w", err)
	}
	resolvedRoot, err := filepath.EvalSymlinks(absoluteRoot)
	if err != nil {
		return "", fmt.Errorf("goatest: resolve repair root: %w", err)
	}
	rootInfo, err := os.Stat(resolvedRoot)
	if err != nil {
		return "", fmt.Errorf("goatest: inspect repair root: %w", err)
	}
	if !rootInfo.IsDir() {
		return "", fmt.Errorf("goatest: repair root %s is not a directory", root)
	}
	parts := strings.Split(normalized, "/")
	current := resolvedRoot
	for index, part := range parts {
		current = filepath.Join(current, filepath.FromSlash(part))
		info, statErr := os.Lstat(current)
		if errors.Is(statErr, os.ErrNotExist) {
			break
		}
		if statErr != nil {
			return "", fmt.Errorf("goatest: inspect repair path %s: %w", normalized, statErr)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("goatest: repair path %q crosses symbolic link %q", normalized, strings.Join(parts[:index+1], "/"))
		}
		if index+1 < len(parts) && !info.IsDir() {
			return "", fmt.Errorf("goatest: repair path %q crosses non-directory %q", normalized, strings.Join(parts[:index+1], "/"))
		}
	}
	return filepath.Join(resolvedRoot, filepath.FromSlash(normalized)), nil
}

func matchesPreimage(path, expected string) (bool, os.FileMode, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return expected == "", 0o644, nil
	}
	if err != nil {
		return false, 0, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return false, 0, err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]) == expected, info.Mode().Perm(), nil
}

func atomicWrite(root, relative string, data []byte, mode os.FileMode) error {
	path, err := confinedPath(root, relative)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".goatest-repair-*.tmp")
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
	if err := temporary.Chmod(mode); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}

func writeArtifact(root string, finding report.Finding, candidate provider.Candidate) (string, error) {
	identifier := report.FindingID("repair-artifact", finding.ID, candidate.Kind, candidate.Path, candidate.PreimageSHA256)
	relative := filepath.ToSlash(filepath.Join(".goatest", "patches", identifier+".json"))
	payload, err := json.MarshalIndent(struct {
		Finding   string             `json:"finding"`
		Reason    string             `json:"reason"`
		Candidate provider.Candidate `json:"candidate"`
	}{Finding: finding.ID, Reason: "preimage changed; user edit preserved", Candidate: candidate}, "", "  ")
	if err != nil {
		return "", err
	}
	payload = append(payload, '\n')
	if err := atomicWrite(root, relative, payload, 0o600); err != nil {
		return "", err
	}
	return relative, nil
}
