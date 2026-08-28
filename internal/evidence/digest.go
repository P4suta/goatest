// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package evidence computes complete assurance identities and impact graphs.
package evidence

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

type Inputs struct {
	Files            map[string]string
	Dependencies     map[string]string
	Toolchain        string
	Platform         string
	Environment      []string
	Resources        map[string]string
	Corpus           map[string]string
	Contract         string
	GoatestVersion   string
	GoMutantsVersion string
}

func (inputs Inputs) Clone() Inputs {
	return Inputs{
		Files:            cloneMap(inputs.Files),
		Dependencies:     cloneMap(inputs.Dependencies),
		Toolchain:        inputs.Toolchain,
		Platform:         inputs.Platform,
		Environment:      slices.Clone(inputs.Environment),
		Resources:        cloneMap(inputs.Resources),
		Corpus:           cloneMap(inputs.Corpus),
		Contract:         inputs.Contract,
		GoatestVersion:   inputs.GoatestVersion,
		GoMutantsVersion: inputs.GoMutantsVersion,
	}
}

func cloneMap(input map[string]string) map[string]string {
	result := make(map[string]string, len(input))
	for key, value := range input {
		result[key] = value
	}
	return result
}

// Digest identifies every input whose change can invalidate assurance.
func Digest(inputs Inputs) string {
	h := sha256.New()
	write(h, "goatest-evidence-v1")
	writeMap(h, "files", inputs.Files)
	writeMap(h, "dependencies", inputs.Dependencies)
	write(h, "toolchain", inputs.Toolchain)
	write(h, "platform", inputs.Platform)
	environment := slices.Clone(inputs.Environment)
	slices.Sort(environment)
	write(h, "environment")
	for _, entry := range environment {
		write(h, entry)
	}
	writeMap(h, "resources", inputs.Resources)
	writeMap(h, "corpus", inputs.Corpus)
	write(h, "contract", inputs.Contract)
	write(h, "goatest", inputs.GoatestVersion)
	write(h, "go-mutants", inputs.GoMutantsVersion)
	return hex.EncodeToString(h.Sum(nil))
}

func writeMap(h hash.Hash, domain string, values map[string]string) {
	write(h, domain)
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	for _, key := range keys {
		write(h, key, values[key])
	}
}

func write(h hash.Hash, fields ...string) {
	for _, field := range fields {
		var length [4]byte
		binary.BigEndian.PutUint32(length[:], uint32(len(field)))
		_, _ = h.Write(length[:])
		_, _ = h.Write([]byte(field))
	}
}

// Scan hashes the repository's observable files, separating standard fuzz
// corpus entries so callers can invalidate corpus evidence independently.
func Scan(root string) (map[string]string, map[string]string, error) {
	files := make(map[string]string)
	corpus := make(map[string]string)
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if entry.IsDir() {
			if relative != "." && excludedDirectory(relative) {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Type()&fs.ModeSymlink != 0 {
			return fmt.Errorf("goatest: evidence refuses symbolic link %s", relative)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("goatest: evidence refuses irregular file %s", relative)
		}
		digest, err := fileDigest(path, info.Mode())
		if err != nil {
			return err
		}
		if isCorpus(relative) {
			corpus[relative] = digest
		} else {
			files[relative] = digest
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	return files, corpus, nil
}

func excludedDirectory(relative string) bool {
	first, _, _ := strings.Cut(relative, "/")
	return first == ".git" || first == ".goatest" || first == "reports" || first == "dist"
}

func isCorpus(relative string) bool {
	return strings.HasPrefix(relative, "testdata/fuzz/") || strings.Contains(relative, "/testdata/fuzz/")
}

func fileDigest(path string, mode fs.FileMode) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = file.Close() }()
	h := sha256.New()
	write(h, mode.String())
	if _, err := io.Copy(h, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
