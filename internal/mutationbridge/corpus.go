// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package mutationbridge

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	gomutants "github.com/P4suta/go-mutants"
)

const maximumCorpusBytes = 2 << 20

type corpusWritableFile interface {
	Name() string
	Write([]byte) (int, error)
	Sync() error
	Chmod(os.FileMode) error
	Close() error
}

var (
	absoluteCorpusPath     = filepath.Abs
	evaluateCorpusSymlinks = filepath.EvalSymlinks
	statCorpusPath         = os.Stat
	lstatCorpusPath        = os.Lstat
	readCorpusFile         = os.ReadFile
	mkdirCorpusAll         = os.MkdirAll
	createCorpusTemp       = func(directory, pattern string) (corpusWritableFile, error) {
		return os.CreateTemp(directory, pattern)
	}
	removeCorpusFile = os.Remove
	linkCorpusFile   = os.Link
)

func PromoteCorpus(root string, artifact gomutants.Artifact) (string, bool, error) {
	normalized, ok := corpusPath(artifact.Path)
	if !ok {
		return "", false, fmt.Errorf("goatest: artifact path %q is not standard fuzz corpus", artifact.Path)
	}
	if len(artifact.Data) == 0 || len(artifact.Data) > maximumCorpusBytes || !bytes.HasPrefix(artifact.Data, []byte("go test fuzz v1\n")) {
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
	if existing, err := readCorpusFile(target); err == nil {
		if slices.Equal(existing, artifact.Data) {
			return normalized, false, nil
		}
		return "", false, fmt.Errorf("goatest: corpus path %s already exists with different bytes", normalized)
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", false, err
	}
	if err := mkdirCorpusAll(filepath.Dir(target), 0o755); err != nil {
		return "", false, err
	}
	target, err = safeTarget(root, normalized)
	if err != nil {
		return "", false, err
	}
	temporary, err := createCorpusTemp(filepath.Dir(target), ".goatest-corpus-*.tmp")
	if err != nil {
		return "", false, err
	}
	temporaryPath := temporary.Name()
	defer func() { _ = removeCorpusFile(temporaryPath) }()
	written, err := temporary.Write(artifact.Data)
	if err != nil {
		_ = temporary.Close()
		return "", false, err
	}
	if written != len(artifact.Data) {
		_ = temporary.Close()
		return "", false, io.ErrShortWrite
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
	if err := linkCorpusFile(temporaryPath, target); err != nil {
		if existing, readErr := readCorpusFile(target); readErr == nil && slices.Equal(existing, artifact.Data) {
			return normalized, false, nil
		}
		return "", false, fmt.Errorf("goatest: atomically promote corpus %s: %w", normalized, err)
	}
	return normalized, true, nil
}

func corpusPath(path string) (string, bool) {
	if path == "" || strings.HasPrefix(path, "/") || strings.ContainsAny(path, "\\:\x00") {
		return "", false
	}
	native := filepath.FromSlash(path)
	clean := filepath.Clean(native)
	if strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", false
	}
	normalized := filepath.ToSlash(clean)
	parts := strings.Split(normalized, "/")
	if len(parts) < 4 {
		return "", false
	}
	i := len(parts) - 4
	if parts[i] == "testdata" && parts[i+1] == "fuzz" {
		return normalized, true
	}
	return "", false
}

func safeTarget(root, relative string) (string, error) {
	normalized, ok := corpusPath(relative)
	if !ok || normalized != relative {
		return "", fmt.Errorf("goatest: corpus path %q is not canonical standard fuzz corpus", relative)
	}
	if root == "" {
		return "", errors.New("goatest: corpus root is empty")
	}
	absoluteRoot, err := absoluteCorpusPath(root)
	if err != nil {
		return "", err
	}
	resolvedRoot, err := evaluateCorpusSymlinks(absoluteRoot)
	if err != nil {
		return "", err
	}
	rootInfo, err := statCorpusPath(resolvedRoot)
	if err != nil {
		return "", err
	}
	if !rootInfo.IsDir() {
		return "", fmt.Errorf("goatest: corpus root %s is not a real directory", root)
	}
	current := resolvedRoot
	parts := strings.Split(relative, "/")
	for index, part := range parts {
		current = filepath.Join(current, part)
		info, err := lstatCorpusPath(current)
		if errors.Is(err, os.ErrNotExist) {
			break
		}
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("goatest: corpus path %q crosses symbolic link %q", relative, strings.Join(parts[:index+1], "/"))
		}
		if index+1 < len(parts) && !info.IsDir() {
			return "", fmt.Errorf("goatest: corpus parent %s is not a real directory", current)
		}
		if index+1 == len(parts) && !info.Mode().IsRegular() {
			return "", fmt.Errorf("goatest: corpus entry %s is not a regular file", current)
		}
	}
	return filepath.Join(resolvedRoot, filepath.FromSlash(relative)), nil
}
