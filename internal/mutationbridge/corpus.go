// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package mutationbridge

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	gomutants "github.com/P4suta/go-mutants"
)

const maximumCorpusBytes = 2 << 20

func PromoteCorpus(root string, artifact gomutants.Artifact) (string, bool, error) {
	normalized, ok := corpusPath(artifact.Path)
	if !ok {
		return "", false, fmt.Errorf("goatest: artifact path %q is not standard fuzz corpus", artifact.Path)
	}
	if len(artifact.Data) == 0 || len(artifact.Data) > maximumCorpusBytes || !strings.HasPrefix(string(artifact.Data), "go test fuzz v1\n") {
		return "", false, errors.New("goatest: artifact is not bounded standard Go fuzz v1 data")
	}
	sum := sha256.Sum256(artifact.Data)
	digest := hex.EncodeToString(sum[:])
	if artifact.SHA256 != "" && artifact.SHA256 != digest {
		return "", false, errors.New("goatest: artifact digest does not match its bytes")
	}
	target, err := safeTarget(root, normalized)
	if err != nil {
		return "", false, err
	}
	if existing, err := os.ReadFile(target); err == nil {
		if slices.Equal(existing, artifact.Data) {
			return normalized, false, nil
		}
		return "", false, fmt.Errorf("goatest: corpus path %s already exists with different bytes", normalized)
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", false, err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return "", false, err
	}
	temporary, err := os.CreateTemp(filepath.Dir(target), ".goatest-corpus-*.tmp")
	if err != nil {
		return "", false, err
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if _, err := temporary.Write(artifact.Data); err != nil {
		_ = temporary.Close()
		return "", false, err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return "", false, err
	}
	if err := temporary.Chmod(0o644); err != nil {
		_ = temporary.Close()
		return "", false, err
	}
	if err := temporary.Close(); err != nil {
		return "", false, err
	}
	if err := os.Link(temporaryPath, target); err != nil {
		if existing, readErr := os.ReadFile(target); readErr == nil && slices.Equal(existing, artifact.Data) {
			return normalized, false, nil
		}
		return "", false, fmt.Errorf("goatest: atomically promote corpus %s: %w", normalized, err)
	}
	return normalized, true, nil
}

func corpusPath(path string) (string, bool) {
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
	normalized := filepath.ToSlash(clean)
	parts := strings.Split(normalized, "/")
	for i := 0; i+3 < len(parts); i++ {
		if parts[i] == "testdata" && parts[i+1] == "fuzz" && parts[i+2] != "" && parts[i+3] != "" {
			return normalized, true
		}
	}
	return "", false
}

func safeTarget(root, relative string) (string, error) {
	absoluteRoot, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	current := absoluteRoot
	parts := strings.Split(relative, "/")
	for _, part := range parts[:len(parts)-1] {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return "", fmt.Errorf("goatest: corpus parent %s is not a real directory", current)
		}
	}
	return filepath.Join(absoluteRoot, filepath.FromSlash(relative)), nil
}
