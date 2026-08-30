// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package repair validates and atomically applies generated test and corpus
// candidates.
package repair

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/P4suta/goatest/internal/provider"
	"github.com/P4suta/goatest/internal/report"
)

type Status string

const (
	StatusApplied   Status = "applied"
	StatusArtifact  Status = "artifact"
	StatusCandidate Status = "candidate"
)

const CandidateVersion = "repair-candidate-v1"

// CandidateRecord is the provider-neutral, durable hand-off between read-only
// verification and an explicit fix --apply operation.
type CandidateRecord struct {
	Version    string             `json:"version"`
	ID         string             `json:"id"`
	Snapshot   string             `json:"snapshot"`
	Finding    report.Finding     `json:"finding"`
	Candidate  provider.Candidate `json:"candidate"`
	Validation string             `json:"validation"`
}

type Result struct {
	Status   Status
	Path     string
	Artifact string
}

type Application struct {
	Finding   report.Finding
	Candidate provider.Candidate
}

type applicationState struct {
	application Application
	target      string
	original    []byte
	existed     bool
	mode        os.FileMode
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
	applyRepairMutex      sync.Mutex
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
	validated, err := ValidateCandidate(ctx, root, finding, candidate, validator)
	if err != nil {
		return Result{}, err
	}
	return ApplyCandidate(root, finding, validated)
}

// ValidateCandidate performs every stability, kill, suite, path, and preimage
// check without changing source or corpus files.
func ValidateCandidate(ctx context.Context, root string, finding report.Finding, candidate provider.Candidate, validator Validator) (provider.Candidate, error) {
	normalized, ok := normalize(candidate.Path)
	if !ok || !AllowedPath(normalized) {
		return provider.Candidate{}, fmt.Errorf("goatest: repair path %q is outside _test.go and standard fuzz corpus", candidate.Path)
	}
	candidate.Path = normalized
	if candidate.Kind != "patch" && candidate.Kind != "corpus" {
		return provider.Candidate{}, fmt.Errorf("goatest: repair candidate kind %q is invalid", candidate.Kind)
	}
	if _, err := confinedPath(root, normalized); err != nil {
		return provider.Candidate{}, err
	}
	if validator == nil {
		return provider.Candidate{}, errors.New("goatest: repair validation requires a validator")
	}
	for range 3 {
		if err := validator.OriginalStable(ctx, candidate); err != nil {
			return provider.Candidate{}, fmt.Errorf("goatest: candidate is unstable on original code: %w", err)
		}
	}
	for range 2 {
		if err := validator.Kills(ctx, finding, candidate); err != nil {
			return provider.Candidate{}, fmt.Errorf("goatest: candidate does not detect target mutant: %w", err)
		}
	}
	if err := validator.Suite(ctx, candidate); err != nil {
		return provider.Candidate{}, fmt.Errorf("goatest: candidate fails related suite: %w", err)
	}
	return candidate, nil
}

// ApplyCandidate rechecks the preimage immediately before one atomic source or
// corpus update. A changed preimage is preserved and emitted as an artifact.
func ApplyCandidate(root string, finding report.Finding, candidate provider.Candidate) (Result, error) {
	results, err := ApplyCandidates(root, []Application{{Finding: finding, Candidate: candidate}})
	if err != nil {
		return Result{}, err
	}
	return results[0], nil
}

// ApplyCandidates applies a validated candidate set as one best-effort atomic
// batch. It checks every preimage before writing, checks each one again at its
// commit boundary, and rolls already-written files back in reverse order if a
// later write fails. Concurrent user edits are never overwritten by rollback.
func ApplyCandidates(root string, applications []Application) ([]Result, error) {
	applyRepairMutex.Lock()
	defer applyRepairMutex.Unlock()
	if len(applications) == 0 {
		return []Result{}, nil
	}
	states := make([]applicationState, len(applications))
	results := make([]Result, len(applications))
	paths := make(map[string]struct{}, len(applications))
	preimageMismatch := false
	for index, application := range applications {
		normalized, ok := normalize(application.Candidate.Path)
		if !ok || !AllowedPath(normalized) {
			return nil, fmt.Errorf("goatest: repair path %q is invalid", application.Candidate.Path)
		}
		key := strings.ToUpper(normalized)
		if _, duplicate := paths[key]; duplicate {
			return nil, fmt.Errorf("goatest: repair batch contains duplicate path %q", normalized)
		}
		paths[key] = struct{}{}
		application.Candidate.Path = normalized
		state, match, err := inspectApplication(root, application)
		if err != nil {
			return nil, err
		}
		states[index] = state
		results[index] = Result{Status: StatusCandidate, Path: normalized}
		if !match {
			preimageMismatch = true
			artifact, artifactErr := writeArtifact(root, application.Finding, application.Candidate)
			if artifactErr != nil {
				return nil, artifactErr
			}
			results[index] = Result{Status: StatusArtifact, Path: normalized, Artifact: artifact}
		}
	}
	if preimageMismatch {
		return results, nil
	}
	applied := 0
	for index, state := range states {
		match, _, err := matchesPreimage(state.target, state.application.Candidate.PreimageSHA256)
		if err != nil || !match {
			rollbackErr := rollbackApplications(root, states[:applied])
			for prior := range applied {
				results[prior].Status = StatusCandidate
			}
			if err != nil {
				return results, errors.Join(err, rollbackErr)
			}
			artifact, artifactErr := writeArtifact(root, state.application.Finding, state.application.Candidate)
			results[index] = Result{Status: StatusArtifact, Path: state.application.Candidate.Path, Artifact: artifact}
			return results, errors.Join(rollbackErr, artifactErr)
		}
		if err := atomicWrite(root, state.application.Candidate.Path, state.application.Candidate.Content, state.mode); err != nil {
			rollbackErr := rollbackApplications(root, states[:applied])
			for prior := range applied {
				results[prior].Status = StatusCandidate
			}
			return results, fmt.Errorf("goatest: apply repair %s: %w", state.application.Candidate.Path, errors.Join(err, rollbackErr))
		}
		results[index].Status = StatusApplied
		applied++
	}
	return results, nil
}

func inspectApplication(root string, application Application) (applicationState, bool, error) {
	target, err := confinedPath(root, application.Candidate.Path)
	if err != nil {
		return applicationState{}, false, err
	}
	data, readErr := readRepairFile(target)
	if errors.Is(readErr, os.ErrNotExist) {
		return applicationState{application: application, target: target, mode: 0o644}, application.Candidate.PreimageSHA256 == "", nil
	}
	if readErr != nil {
		return applicationState{}, false, readErr
	}
	info, err := statRepairPath(target)
	if err != nil {
		return applicationState{}, false, err
	}
	sum := sha256.Sum256(data)
	return applicationState{
		application: application, target: target, original: slices.Clone(data), existed: true, mode: info.Mode().Perm(),
	}, hex.EncodeToString(sum[:]) == application.Candidate.PreimageSHA256, nil
}

func rollbackApplications(root string, states []applicationState) error {
	var rollbackErr error
	for index := len(states) - 1; index >= 0; index-- {
		state := states[index]
		current, readErr := readRepairFile(state.target)
		if errors.Is(readErr, os.ErrNotExist) && !state.existed {
			continue
		}
		if readErr != nil {
			rollbackErr = errors.Join(rollbackErr, fmt.Errorf("rollback %s: %w", state.application.Candidate.Path, readErr))
			continue
		}
		if !bytes.Equal(current, state.application.Candidate.Content) {
			rollbackErr = errors.Join(rollbackErr, fmt.Errorf("rollback %s refused a concurrent edit", state.application.Candidate.Path))
			continue
		}
		if state.existed {
			if err := atomicWrite(root, state.application.Candidate.Path, state.original, state.mode); err != nil {
				rollbackErr = errors.Join(rollbackErr, fmt.Errorf("rollback %s: %w", state.application.Candidate.Path, err))
			}
		} else if err := removeRepairFile(state.target); err != nil && !errors.Is(err, os.ErrNotExist) {
			rollbackErr = errors.Join(rollbackErr, fmt.Errorf("rollback %s: %w", state.application.Candidate.Path, err))
		}
	}
	return rollbackErr
}

// StoreCandidate saves a bounded candidate as an internal artifact. It never
// writes candidate bytes to their proposed source/corpus path.
func StoreCandidate(root string, record CandidateRecord) (string, error) {
	if record.Version == "" {
		record.Version = CandidateVersion
	}
	if record.Version != CandidateVersion || !safeCandidateID(record.ID) || record.Finding.ID == "" || record.Candidate.Path == "" {
		return "", errors.New("goatest: invalid repair candidate record")
	}
	payload, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return "", err
	}
	payload = append(payload, '\n')
	if len(payload) > 8<<20 {
		return "", errors.New("goatest: repair candidate record exceeds 8 MiB")
	}
	relative := filepath.ToSlash(filepath.Join(".goatest", "candidates", record.ID+".json"))
	target, err := confinedPath(root, relative)
	if err != nil {
		return "", err
	}
	existing, readErr := readRepairFile(target)
	if readErr == nil {
		if bytes.Equal(existing, payload) {
			return relative, nil
		}
		return "", fmt.Errorf("goatest: repair candidate ID %s already stores different evidence", record.ID)
	}
	if !errors.Is(readErr, os.ErrNotExist) {
		return "", readErr
	}
	if err := atomicWrite(root, relative, payload, 0o600); err != nil {
		return "", err
	}
	return relative, nil
}

func LoadCandidate(root, id string) (CandidateRecord, error) {
	if !safeCandidateID(id) {
		return CandidateRecord{}, fmt.Errorf("goatest: invalid repair candidate ID %q", id)
	}
	path := filepath.Join(root, ".goatest", "candidates", id+".json")
	data, err := readRepairFile(path)
	if err != nil {
		return CandidateRecord{}, fmt.Errorf("goatest: read repair candidate %s: %w", id, err)
	}
	if len(data) > 8<<20 {
		return CandidateRecord{}, errors.New("goatest: repair candidate record exceeds 8 MiB")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var record CandidateRecord
	if err := decoder.Decode(&record); err != nil {
		return CandidateRecord{}, fmt.Errorf("goatest: decode repair candidate %s: %w", id, err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return CandidateRecord{}, errors.New("goatest: repair candidate has trailing data")
	}
	if record.Version != CandidateVersion || record.ID != id || record.Finding.ID == "" ||
		record.Candidate.Path == "" || (record.Candidate.Kind != "patch" && record.Candidate.Kind != "corpus") {
		return CandidateRecord{}, errors.New("goatest: repair candidate identity mismatch")
	}
	return record, nil
}

// ListCandidates returns every valid stored candidate in stable ID order. A
// malformed record fails the whole operation so fix never silently omits it.
func ListCandidates(root string) ([]CandidateRecord, error) {
	directory := filepath.Join(root, ".goatest", "candidates")
	entries, err := os.ReadDir(directory)
	if errors.Is(err, os.ErrNotExist) {
		return []CandidateRecord{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("goatest: read repair candidates: %w", err)
	}
	records := make([]CandidateRecord, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), ".json")
		if !safeCandidateID(id) {
			return nil, fmt.Errorf("goatest: unsafe repair candidate file %q", entry.Name())
		}
		record, loadErr := LoadCandidate(root, id)
		if loadErr != nil {
			return nil, loadErr
		}
		records = append(records, record)
	}
	slices.SortFunc(records, func(a, b CandidateRecord) int { return strings.Compare(a.ID, b.ID) })
	return records, nil
}

// CurrentContent reads the proposed target through the same confinement and
// symlink checks used for application. exists is false for a missing file.
func CurrentContent(root, path string) (content []byte, exists bool, resultErr error) {
	normalized, ok := normalize(path)
	if !ok || !AllowedPath(normalized) {
		return nil, false, fmt.Errorf("goatest: repair path %q is invalid", path)
	}
	target, err := confinedPath(root, normalized)
	if err != nil {
		return nil, false, err
	}
	data, err := readRepairFile(target)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return data, true, nil
}

func safeCandidateID(id string) bool {
	if len(id) != 16 {
		return false
	}
	for _, character := range id {
		if character < '0' || character > '9' {
			if character < 'a' || character > 'f' {
				return false
			}
		}
	}
	return true
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
