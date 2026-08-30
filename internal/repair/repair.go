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

type repairWritableFile interface {
	Name() string
	Write([]byte) (int, error)
	Sync() error
	Chmod(os.FileMode) error
	Close() error
}

var (
	absoluteRepairPath     = filepath.Abs
	evaluateRepairSymlinks = filepath.EvalSymlinks
	statRepairPath         = os.Stat
	lstatRepairPath        = os.Lstat
	readRepairFile         = os.ReadFile
	mkdirRepairAll         = os.MkdirAll
	createRepairTemp       = func(directory, pattern string) (repairWritableFile, error) {
		return os.CreateTemp(directory, pattern)
	}
	removeRepairFile      = os.Remove
	renameRepairFile      = os.Rename
	marshalRepairArtifact = json.MarshalIndent
)

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
		if parts[i] == "testdata" && parts[i+1] == "fuzz" {
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
	if path == "" || strings.HasPrefix(path, "/") || strings.ContainsAny(path, "\\:\x00") {
		return "", false
	}
	native := filepath.FromSlash(path)
	clean := filepath.Clean(native)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", false
	}
	return filepath.ToSlash(clean), true
}

func confinedPath(root, normalized string) (string, error) {
	absoluteRoot, err := absoluteRepairPath(root)
	if err != nil {
		return "", fmt.Errorf("goatest: resolve repair root: %w", err)
	}
	resolvedRoot, err := evaluateRepairSymlinks(absoluteRoot)
	if err != nil {
		return "", fmt.Errorf("goatest: resolve repair root: %w", err)
	}
	rootInfo, err := statRepairPath(resolvedRoot)
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
		info, statErr := lstatRepairPath(current)
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

func matchesPreimage(path, expected string) (match bool, mode os.FileMode, resultErr error) {
	data, readErr := readRepairFile(path)
	if errors.Is(readErr, os.ErrNotExist) {
		return expected == "", 0o644, nil
	}
	if readErr != nil {
		resultErr = readErr
		return
	}
	info, statErr := statRepairPath(path)
	if statErr != nil {
		resultErr = statErr
		return
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]) == expected, info.Mode().Perm(), nil
}

func atomicWrite(root, relative string, data []byte, mode os.FileMode) error {
	path, err := confinedPath(root, relative)
	if err != nil {
		return err
	}
	if err := mkdirRepairAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	temporary, err := createRepairTemp(filepath.Dir(path), ".goatest-repair-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer func() { _ = removeRepairFile(temporaryPath) }()
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
	return renameRepairFile(temporaryPath, path)
}

func writeArtifact(root string, finding report.Finding, candidate provider.Candidate) (string, error) {
	identifier := report.FindingID("repair-artifact", finding.ID, candidate.Kind, candidate.Path, candidate.PreimageSHA256)
	relative := filepath.ToSlash(filepath.Join(".goatest", "patches", identifier+".json"))
	payload, err := marshalRepairArtifact(struct {
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
