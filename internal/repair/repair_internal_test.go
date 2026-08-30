// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package repair

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
	"github.com/P4suta/goatest/internal/report"
)

func TestNormalizeCanonicalizesLocalPathsAndRejectsEveryEscapeForm(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		path string
		want string
		ok   bool
	}{
		{path: "a_test.go", want: "a_test.go", ok: true},
		{path: "./pkg/../pkg/a_test.go", want: "pkg/a_test.go", ok: true},
		{path: "pkg//a_test.go", want: "pkg/a_test.go", ok: true},
		{path: ""},
		{path: "."},
		{path: "a/.."},
		{path: ".."},
		{path: "../a_test.go"},
		{path: "a/../.."},
		{path: "/a_test.go"},
		{path: `C:/a_test.go`},
		{path: `pkg\a_test.go`},
		{path: "pkg/\x00_test.go"},
	} {
		got, ok := normalize(test.path)
		if got != test.want || ok != test.ok {
			t.Errorf("normalize(%q) = (%q, %t), want (%q, %t)", test.path, got, ok, test.want, test.ok)
		}
	}
}

func TestConfinedPathHandlesMissingFinalPathsAndRejectsNonDirectories(t *testing.T) {
	t.Parallel()
	root := resolvedTempDir(t)
	want := filepath.Join(root, "new", "value_test.go")
	got, err := confinedPath(root, "new/value_test.go")
	if err != nil || got != want {
		t.Fatalf("confinedPath = (%q, %v), want %q", got, err, want)
	}
	final := filepath.Join(root, "existing_test.go")
	if err := os.WriteFile(final, []byte("existing"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, err := confinedPath(root, "existing_test.go"); err != nil || got != final {
		t.Fatalf("existing confinedPath = (%q, %v)", got, err)
	}
	if err := os.WriteFile(filepath.Join(root, "blocked"), []byte("file"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = confinedPath(root, "blocked/value_test.go")
	const wantError = `goatest: repair path "blocked/value_test.go" crosses non-directory "blocked"`
	if err == nil || err.Error() != wantError {
		t.Fatalf("confinedPath error = %v, want %q", err, wantError)
	}
	fileRoot := filepath.Join(t.TempDir(), "root-file")
	if err := os.WriteFile(fileRoot, []byte("file"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := confinedPath(fileRoot, "value_test.go"); err == nil || !strings.Contains(err.Error(), "is not a directory") {
		t.Fatalf("file root error = %v", err)
	}
	if _, err := confinedPath(filepath.Join(t.TempDir(), "missing"), "value_test.go"); err == nil || !strings.HasPrefix(err.Error(), "goatest: resolve repair root: ") {
		t.Fatalf("missing root error = %v", err)
	}
}

func TestConfinedPathPropagatesEveryFilesystemFailure(t *testing.T) {
	for _, stage := range []string{"absolute", "evaluate", "stat", "lstat"} {
		t.Run(stage, func(t *testing.T) {
			preserveRepairHooks(t)
			root := t.TempDir()
			sentinel := errors.New(stage + " failed")
			switch stage {
			case "absolute":
				absoluteRepairPath = func(string) (string, error) { return "", sentinel }
			case "evaluate":
				evaluateRepairSymlinks = func(string) (string, error) { return "", sentinel }
			case "stat":
				statRepairPath = func(string) (os.FileInfo, error) { return nil, sentinel }
			case "lstat":
				lstatRepairPath = func(string) (os.FileInfo, error) { return nil, sentinel }
			}
			_, err := confinedPath(root, "value_test.go")
			if !errors.Is(err, sentinel) {
				t.Fatalf("confinedPath error = %v", err)
			}
		})
	}
}

func TestMatchesPreimageCoversMissingMatchingDirtyAndFaults(t *testing.T) {
	root := t.TempDir()
	missing := filepath.Join(root, "missing_test.go")
	for _, test := range []struct {
		expected string
		match    bool
	}{
		{expected: "", match: true},
		{expected: strings.Repeat("0", 64), match: false},
	} {
		match, mode, err := matchesPreimage(missing, test.expected)
		if err != nil || match != test.match || mode != 0o644 {
			t.Fatalf("matchesPreimage(missing, %q) = (%t, %o, %v)", test.expected, match, mode, err)
		}
	}
	path := filepath.Join(root, "existing_test.go")
	contents := []byte("package fixture\n")
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	match, mode, err := matchesPreimage(path, sha256Hex(contents))
	if err != nil || !match || mode != info.Mode().Perm() {
		t.Fatalf("matching preimage = (%t, %o, %v), want mode %o", match, mode, err, info.Mode().Perm())
	}
	match, _, err = matchesPreimage(path, strings.Repeat("f", 64))
	if err != nil || match {
		t.Fatalf("dirty preimage = (%t, %v)", match, err)
	}

	for _, stage := range []string{"read", "stat"} {
		t.Run(stage, func(t *testing.T) {
			preserveRepairHooks(t)
			sentinel := errors.New(stage + " failed")
			if stage == "read" {
				readRepairFile = func(string) ([]byte, error) { return nil, sentinel }
			} else {
				statRepairPath = func(string) (os.FileInfo, error) { return nil, sentinel }
			}
			_, _, err := matchesPreimage(path, sha256Hex(contents))
			if !errors.Is(err, sentinel) {
				t.Fatalf("matchesPreimage error = %v", err)
			}
		})
	}
}

func TestAtomicWritePropagatesEveryStageAndCleansTemporaryFile(t *testing.T) {
	for _, stage := range []string{"confine", "mkdir", "create", "write", "sync", "chmod", "close", "rename"} {
		t.Run(stage, func(t *testing.T) {
			preserveRepairHooks(t)
			root := t.TempDir()
			if stage == "confine" {
				root = filepath.Join(root, "missing")
			}
			sentinel := errors.New(stage + " failed")
			file := &fakeRepairFile{name: filepath.Join(root, "temporary")}
			removed := ""
			removeRepairFile = func(path string) error { removed = path; return nil }
			createRepairTemp = func(string, string) (repairWritableFile, error) {
				if stage == "create" {
					return nil, sentinel
				}
				return file, nil
			}
			if stage == "mkdir" {
				mkdirRepairAll = func(string, os.FileMode) error { return sentinel }
			}
			file.failure = stage
			file.err = sentinel
			renameRepairFile = func(string, string) error {
				if stage == "rename" {
					return sentinel
				}
				return nil
			}
			err := atomicWrite(root, "nested/value_test.go", []byte("contents"), 0o640)
			if !errors.Is(err, sentinel) && stage != "confine" {
				t.Fatalf("atomicWrite error = %v", err)
			}
			if stage == "confine" && err == nil {
				t.Fatal("atomicWrite accepted missing root")
			}
			if stage != "confine" && stage != "mkdir" && stage != "create" && removed != file.name {
				t.Fatalf("temporary removed = %q, want %q", removed, file.name)
			}
			if stage == "write" || stage == "sync" || stage == "chmod" {
				if file.closes != 1 {
					t.Fatalf("close calls = %d", file.closes)
				}
			}
			if stage == "rename" && (file.mode != 0o640 || !slices.Equal(file.data, []byte("contents"))) {
				t.Fatalf("file mode=%o data=%q", file.mode, file.data)
			}
		})
	}
}

func TestArtifactMarshalAndApplyFailuresArePropagated(t *testing.T) {
	t.Run("marshal", func(t *testing.T) {
		preserveRepairHooks(t)
		sentinel := errors.New("marshal failed")
		marshalRepairArtifact = func(any, string, string) ([]byte, error) { return nil, sentinel }
		_, err := writeArtifact(t.TempDir(), report.Finding{ID: "finding-a"}, provider.Candidate{Kind: "patch", Path: "value_test.go"})
		if !errors.Is(err, sentinel) {
			t.Fatalf("writeArtifact error = %v", err)
		}
	})
	t.Run("artifact write", func(t *testing.T) {
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, ".goatest"), []byte("blocked"), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := writeArtifact(root, report.Finding{ID: "finding-a"}, provider.Candidate{Kind: "patch", Path: "value_test.go"})
		if err == nil || !strings.Contains(err.Error(), "crosses non-directory") {
			t.Fatalf("writeArtifact error = %v", err)
		}
	})
	t.Run("apply", func(t *testing.T) {
		preserveRepairHooks(t)
		sentinel := errors.New("rename failed")
		renameRepairFile = func(string, string) error { return sentinel }
		result, err := ValidateAndApply(context.Background(), t.TempDir(), report.Finding{ID: "finding-a"}, provider.Candidate{
			Kind: "patch", Path: "value_test.go", Content: []byte("package fixture\n"),
		}, successfulValidator{})
		if result != (Result{}) || !errors.Is(err, sentinel) || !strings.HasPrefix(err.Error(), "goatest: apply repair value_test.go: ") {
			t.Fatalf("ValidateAndApply = (%+v, %v)", result, err)
		}
	})
}

func TestApplyCandidatesCommitsAllFilesAndRejectsDuplicatePaths(t *testing.T) {
	root := t.TempDir()
	applications := []Application{
		{Finding: report.Finding{ID: "finding-a"}, Candidate: provider.Candidate{Kind: "patch", Path: "a_test.go", Content: []byte("package fixture\n")}},
		{Finding: report.Finding{ID: "finding-b"}, Candidate: provider.Candidate{Kind: "corpus", Path: "testdata/fuzz/FuzzValue/seed", Content: []byte("go test fuzz v1\nstring(\"seed\")\n")}},
	}
	results, err := ApplyCandidates(root, applications)
	if err != nil || len(results) != 2 || results[0].Status != StatusApplied || results[1].Status != StatusApplied {
		t.Fatalf("ApplyCandidates = (%+v, %v)", results, err)
	}
	for index, application := range applications {
		data, readErr := os.ReadFile(filepath.Join(root, filepath.FromSlash(application.Candidate.Path)))
		if readErr != nil || !slices.Equal(data, application.Candidate.Content) {
			t.Fatalf("applied file %d = %q, %v", index, data, readErr)
		}
	}
	_, err = ApplyCandidates(root, []Application{
		{Finding: report.Finding{ID: "finding-a"}, Candidate: provider.Candidate{Kind: "patch", Path: "same_test.go"}},
		{Finding: report.Finding{ID: "finding-b"}, Candidate: provider.Candidate{Kind: "patch", Path: "SAME_test.go"}},
	})
	if err == nil || !strings.Contains(err.Error(), "duplicate path") {
		t.Fatalf("duplicate batch error = %v", err)
	}
}

func TestApplyCandidatesPreimageMismatchAppliesNothing(t *testing.T) {
	root := t.TempDir()
	originalA, originalB := []byte("package fixture\n// a\n"), []byte("package fixture\n// user edit\n")
	if err := os.WriteFile(filepath.Join(root, "a_test.go"), originalA, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "b_test.go"), originalB, 0o644); err != nil {
		t.Fatal(err)
	}
	results, err := ApplyCandidates(root, []Application{
		{Finding: report.Finding{ID: "finding-a"}, Candidate: provider.Candidate{Kind: "patch", Path: "a_test.go", PreimageSHA256: sha256Hex(originalA), Content: []byte("new a")}},
		{Finding: report.Finding{ID: "finding-b"}, Candidate: provider.Candidate{Kind: "patch", Path: "b_test.go", PreimageSHA256: sha256Hex([]byte("stale b")), Content: []byte("new b")}},
	})
	if err != nil || results[0].Status != StatusCandidate || results[1].Status != StatusArtifact || results[1].Artifact == "" {
		t.Fatalf("ApplyCandidates = (%+v, %v)", results, err)
	}
	for path, want := range map[string][]byte{"a_test.go": originalA, "b_test.go": originalB} {
		got, readErr := os.ReadFile(filepath.Join(root, path))
		if readErr != nil || !slices.Equal(got, want) {
			t.Fatalf("%s changed = %q, %v", path, got, readErr)
		}
	}
}

func TestApplyCandidatesRollsBackEarlierWritesAfterLaterFailure(t *testing.T) {
	preserveRepairHooks(t)
	root := t.TempDir()
	originalA, originalB := []byte("package fixture\n// a\n"), []byte("package fixture\n// b\n")
	for path, content := range map[string][]byte{"a_test.go": originalA, "b_test.go": originalB} {
		if err := os.WriteFile(filepath.Join(root, path), content, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	sentinel := errors.New("second commit failed")
	originalRename := renameRepairFile
	renames := 0
	renameRepairFile = func(source, target string) error {
		renames++
		if renames == 2 {
			return sentinel
		}
		return originalRename(source, target)
	}
	results, err := ApplyCandidates(root, []Application{
		{Finding: report.Finding{ID: "finding-a"}, Candidate: provider.Candidate{Kind: "patch", Path: "a_test.go", PreimageSHA256: sha256Hex(originalA), Content: []byte("new a")}},
		{Finding: report.Finding{ID: "finding-b"}, Candidate: provider.Candidate{Kind: "patch", Path: "b_test.go", PreimageSHA256: sha256Hex(originalB), Content: []byte("new b")}},
	})
	if !errors.Is(err, sentinel) || results[0].Status != StatusCandidate || renames != 3 {
		t.Fatalf("ApplyCandidates = (%+v, %v), renames=%d", results, err, renames)
	}
	for path, want := range map[string][]byte{"a_test.go": originalA, "b_test.go": originalB} {
		got, readErr := os.ReadFile(filepath.Join(root, path))
		if readErr != nil || !slices.Equal(got, want) {
			t.Fatalf("%s after rollback = %q, %v", path, got, readErr)
		}
	}
}

func TestValidateAndApplyRechecksConfinementAfterValidation(t *testing.T) {
	root := t.TempDir()
	moved := root + "-moved"
	validator := callbackValidator{suite: func() error {
		if err := os.Rename(root, moved); err != nil {
			t.Fatalf("move repair root: %v", err)
		}
		return nil
	}}
	t.Cleanup(func() { _ = os.Rename(moved, root) })
	_, err := ValidateAndApply(context.Background(), root, report.Finding{ID: "finding-a"}, provider.Candidate{
		Kind: "patch", Path: "value_test.go", Content: []byte("package fixture\n"),
	}, validator)
	if err == nil || !strings.HasPrefix(err.Error(), "goatest: resolve repair root: ") {
		t.Fatalf("ValidateAndApply error = %v", err)
	}
}

func TestValidateAndApplyPropagatesPostValidationReadAndArtifactFailures(t *testing.T) {
	t.Run("preimage read", func(t *testing.T) {
		preserveRepairHooks(t)
		sentinel := errors.New("read failed")
		readRepairFile = func(string) ([]byte, error) { return nil, sentinel }
		result, err := ValidateAndApply(context.Background(), t.TempDir(), report.Finding{ID: "finding-a"}, provider.Candidate{
			Kind: "patch", Path: "value_test.go", Content: []byte("package fixture\n"),
		}, successfulValidator{})
		if result != (Result{}) || !errors.Is(err, sentinel) {
			t.Fatalf("ValidateAndApply = (%+v, %v)", result, err)
		}
	})
	t.Run("artifact write", func(t *testing.T) {
		preserveRepairHooks(t)
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, "value_test.go"), []byte("user edit"), 0o644); err != nil {
			t.Fatal(err)
		}
		sentinel := errors.New("artifact rename failed")
		renameRepairFile = func(string, string) error { return sentinel }
		result, err := ValidateAndApply(context.Background(), root, report.Finding{ID: "finding-a"}, provider.Candidate{
			Kind: "patch", Path: "value_test.go", PreimageSHA256: sha256Hex([]byte("old")), Content: []byte("candidate"),
		}, successfulValidator{})
		if result != (Result{}) || !errors.Is(err, sentinel) {
			t.Fatalf("ValidateAndApply = (%+v, %v)", result, err)
		}
	})
}

type successfulValidator struct{}

func (successfulValidator) OriginalStable(context.Context, provider.Candidate) error { return nil }
func (successfulValidator) Kills(context.Context, report.Finding, provider.Candidate) error {
	return nil
}
func (successfulValidator) Suite(context.Context, provider.Candidate) error { return nil }

type callbackValidator struct{ suite func() error }

func (callbackValidator) OriginalStable(context.Context, provider.Candidate) error { return nil }
func (callbackValidator) Kills(context.Context, report.Finding, provider.Candidate) error {
	return nil
}
func (validator callbackValidator) Suite(context.Context, provider.Candidate) error {
	return validator.suite()
}

type fakeRepairFile struct {
	name    string
	failure string
	err     error
	data    []byte
	mode    os.FileMode
	closes  int
}

func (file *fakeRepairFile) Name() string { return file.name }
func (file *fakeRepairFile) Write(data []byte) (int, error) {
	if file.failure == "write" {
		return 0, file.err
	}
	file.data = slices.Clone(data)
	return len(data), nil
}
func (file *fakeRepairFile) Sync() error {
	if file.failure == "sync" {
		return file.err
	}
	return nil
}
func (file *fakeRepairFile) Chmod(mode os.FileMode) error {
	file.mode = mode
	if file.failure == "chmod" {
		return file.err
	}
	return nil
}
func (file *fakeRepairFile) Close() error {
	file.closes++
	if file.failure == "close" {
		return file.err
	}
	return nil
}

func preserveRepairHooks(t *testing.T) {
	t.Helper()
	abs, eval := absoluteRepairPath, evaluateRepairSymlinks
	stat, lstat, read := statRepairPath, lstatRepairPath, readRepairFile
	mkdir, create, remove := mkdirRepairAll, createRepairTemp, removeRepairFile
	rename, marshal := renameRepairFile, marshalRepairArtifact
	t.Cleanup(func() {
		absoluteRepairPath, evaluateRepairSymlinks = abs, eval
		statRepairPath, lstatRepairPath, readRepairFile = stat, lstat, read
		mkdirRepairAll, createRepairTemp, removeRepairFile = mkdir, create, remove
		renameRepairFile, marshalRepairArtifact = rename, marshal
	})
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// resolvedTempDir returns t.TempDir() with symbolic links and short names
// resolved, matching the root that confinedPath canonicalizes before joining.
func resolvedTempDir(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return root
}
