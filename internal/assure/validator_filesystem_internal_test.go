// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package assure

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/P4suta/goatest/internal/provider"
)

type candidateFileInfo struct {
	name string
	mode fs.FileMode
}

func (info candidateFileInfo) Name() string       { return info.name }
func (info candidateFileInfo) Size() int64        { return 0 }
func (info candidateFileInfo) Mode() fs.FileMode  { return info.mode }
func (info candidateFileInfo) ModTime() time.Time { return time.Time{} }
func (info candidateFileInfo) IsDir() bool        { return info.mode.IsDir() }
func (info candidateFileInfo) Sys() any           { return nil }

type candidateDirEntry struct {
	name    string
	mode    fs.FileMode
	infoErr error
}

func (entry candidateDirEntry) Name() string      { return entry.name }
func (entry candidateDirEntry) IsDir() bool       { return entry.mode.IsDir() }
func (entry candidateDirEntry) Type() fs.FileMode { return entry.mode.Type() }
func (entry candidateDirEntry) Info() (fs.FileInfo, error) {
	return candidateFileInfo{name: entry.name, mode: entry.mode}, entry.infoErr
}

type candidateReadFile struct {
	*strings.Reader
	closeErr error
	closed   int
}

func (file *candidateReadFile) Close() error { file.closed++; return file.closeErr }

type candidateWriteFile struct {
	bytes.Buffer
	closeErr error
	closed   int
}

func (file *candidateWriteFile) Close() error { file.closed++; return file.closeErr }

func TestRepositoryValidatorWithCandidateCleansEveryLifecycleOutcome(t *testing.T) {
	candidate := provider.Candidate{Kind: "patch", Path: "value_test.go", Content: []byte("candidate")}
	cause := errors.New("lifecycle failed")
	for _, test := range []struct {
		name       string
		tempErr    error
		copyErr    error
		writeErr   error
		actionErr  error
		wantRemove int
	}{
		{name: "temporary directory failure", tempErr: cause},
		{name: "copy failure", copyErr: cause, wantRemove: 1},
		{name: "candidate write failure", writeErr: cause, wantRemove: 1},
		{name: "action failure", actionErr: cause, wantRemove: 1},
		{name: "success", wantRemove: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			preserveCandidateLifecycleSeams(t)
			removed, copied, written, acted := 0, 0, 0, 0
			makeCandidateTemp = func(parent, pattern string) (string, error) {
				if parent != "temporary-parent" || pattern != "goatest-candidate-" {
					t.Fatalf("MkdirTemp(%q, %q)", parent, pattern)
				}
				return "isolated-root", test.tempErr
			}
			removeCandidateTemp = func(root string) error {
				removed++
				if root != "isolated-root" {
					t.Fatalf("RemoveAll(%q)", root)
				}
				return errors.New("ignored cleanup error")
			}
			copyCandidateRepository = func(source, destination string) error {
				copied++
				if source != "source-root" || destination != "isolated-root" {
					t.Fatalf("copyRepository(%q, %q)", source, destination)
				}
				return test.copyErr
			}
			writeCandidateRepositoryFile = func(root string, got provider.Candidate) error {
				written++
				if root != "isolated-root" || !reflect.DeepEqual(got, candidate) {
					t.Fatalf("writeCandidate(%q, %+v)", root, got)
				}
				return test.writeErr
			}
			validator := NewRepositoryValidator(RepositoryValidatorOptions{Root: "source-root", TempDirectory: "temporary-parent"})
			err := validator.withCandidate(t.Context(), candidate, func(_ context.Context, root string) error {
				acted++
				if root != "isolated-root" {
					t.Fatalf("action root = %q", root)
				}
				return test.actionErr
			})
			wantErr := test.tempErr != nil || test.copyErr != nil || test.writeErr != nil || test.actionErr != nil
			if (err != nil) != wantErr || removed != test.wantRemove {
				t.Fatalf("withCandidate = %v, removed=%d", err, removed)
			}
			if test.tempErr != nil && (copied != 0 || written != 0 || acted != 0) {
				t.Fatalf("work after temp failure = (%d,%d,%d)", copied, written, acted)
			}
			if test.copyErr != nil && (copied != 1 || written != 0 || acted != 0) {
				t.Fatalf("work after copy failure = (%d,%d,%d)", copied, written, acted)
			}
			if test.writeErr != nil && (copied != 1 || written != 1 || acted != 0) {
				t.Fatalf("work after write failure = (%d,%d,%d)", copied, written, acted)
			}
			if test.actionErr != nil && (copied != 1 || written != 1 || acted != 1 || !errors.Is(err, cause)) {
				t.Fatalf("action failure = (%d,%d,%d,%v)", copied, written, acted, err)
			}
		})
	}
}

func TestCopyRepositoryCopiesRegularTreeAndSkipsGeneratedRoots(t *testing.T) {
	preserveCandidateFileSeams(t)
	source, destination := t.TempDir(), t.TempDir()
	writeCandidateFixture(t, source, "value.go", "root")
	writeCandidateFixture(t, source, "pkg/nested.go", "nested")
	writeCandidateFixture(t, source, "pkg/reports/included.go", "nested report")
	for _, directory := range []string{".git", ".goatest", "reports", "dist"} {
		writeCandidateFixture(t, source, directory+"/ignored", "ignored")
	}
	if err := copyRepository(source, destination); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]string{
		"value.go": "root", "pkg/nested.go": "nested", "pkg/reports/included.go": "nested report",
	} {
		got, err := os.ReadFile(filepath.Join(destination, filepath.FromSlash(path)))
		if err != nil || string(got) != want {
			t.Errorf("copied %s = %q, %v", path, got, err)
		}
	}
	for _, directory := range []string{".git", ".goatest", "reports", "dist"} {
		if _, err := os.Stat(filepath.Join(destination, directory)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("excluded directory %s copied: %v", directory, err)
		}
	}
}

func TestCopyRepositoryReturnsRootValidationErrorBeforeWalking(t *testing.T) {
	preserveCandidateFileSeams(t)
	cause := errors.New("resolve failed")
	resolveCandidatePath = func(string) (string, error) { return "", cause }
	walkCandidateFiles = func(string, fs.WalkDirFunc) error {
		t.Fatal("repository walked after root validation failed")
		return nil
	}
	if err := copyRepository("source", "destination"); !errors.Is(err, cause) {
		t.Fatalf("copyRepository error = %v, want %v", err, cause)
	}
}

func TestExcludedCandidateDirectoryRecognizesOnlyGeneratedRoots(t *testing.T) {
	for _, name := range []string{".git", ".goatest", "reports", "dist"} {
		if !excludedCandidateDirectory(name) {
			t.Errorf("excludedCandidateDirectory(%q) = false", name)
		}
	}
	for _, name := range []string{"", ".github", "report", "pkg", "DIST"} {
		if excludedCandidateDirectory(name) {
			t.Errorf("excludedCandidateDirectory(%q) = true", name)
		}
	}
}

func TestCopyRepositoryPropagatesWalkEntryAndIOFailures(t *testing.T) {
	cause := errors.New("injected failure")
	for _, test := range []struct {
		name string
		run  func(*testing.T, string, string)
	}{
		{name: "walk failure", run: func(t *testing.T, _, _ string) {
			walkCandidateFiles = func(string, fs.WalkDirFunc) error { return cause }
		}},
		{name: "walk callback failure", run: func(t *testing.T, source, _ string) {
			walkCandidateFiles = singleCandidateWalk(source, candidateDirEntry{name: "value.go"}, cause)
		}},
		{name: "relative failure", run: func(t *testing.T, source, _ string) {
			walkCandidateFiles = singleCandidateWalk(source, candidateDirEntry{name: "value.go"}, nil)
			relativeCandidatePath = func(string, string) (string, error) { return "", cause }
		}},
		{name: "entry info failure", run: func(t *testing.T, source, _ string) {
			walkCandidateFiles = singleCandidateWalk(source, candidateDirEntry{name: "value.go", infoErr: cause}, nil)
		}},
		{name: "directory create failure", run: func(t *testing.T, source, _ string) {
			walkCandidateFiles = singleCandidateWalk(source, candidateDirEntry{name: "pkg", mode: fs.ModeDir | 0o750}, nil)
			makeCandidateDirectory = func(string, os.FileMode) error { return cause }
		}},
		{name: "irregular file", run: func(t *testing.T, source, _ string) {
			walkCandidateFiles = singleCandidateWalk(source, candidateDirEntry{name: "pipe", mode: fs.ModeNamedPipe}, nil)
		}},
		{name: "parent create failure", run: func(t *testing.T, source, _ string) {
			walkCandidateFiles = singleCandidateWalk(source, candidateDirEntry{name: "value.go"}, nil)
			makeCandidateDirectory = func(string, os.FileMode) error { return cause }
		}},
		{name: "input open failure", run: func(t *testing.T, source, _ string) {
			walkCandidateFiles = singleCandidateWalk(source, candidateDirEntry{name: "value.go"}, nil)
			openCandidateInput = func(string) (validationReadCloser, error) { return nil, cause }
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			preserveCandidateFileSeams(t)
			source, destination := t.TempDir(), t.TempDir()
			test.run(t, source, destination)
			if err := copyRepository(source, destination); !errors.Is(err, cause) && !strings.Contains(test.name, "irregular") {
				t.Fatalf("copyRepository error = %v, want %v", err, cause)
			} else if strings.Contains(test.name, "irregular") && (err == nil || !strings.Contains(err.Error(), "irregular file")) {
				t.Fatalf("irregular error = %v", err)
			}
		})
	}
}

func TestCopyRepositoryRejectsSymlinkBeforeReadingInfo(t *testing.T) {
	preserveCandidateFileSeams(t)
	source, destination := t.TempDir(), t.TempDir()
	walkCandidateFiles = singleCandidateWalk(source, candidateDirEntry{name: "alias", mode: fs.ModeSymlink, infoErr: errors.New("Info must not be called")}, nil)
	if err := copyRepository(source, destination); err == nil || !strings.Contains(err.Error(), "symbolic link alias") {
		t.Fatalf("symlink error = %v", err)
	}
}

func TestCopyRepositorySkipsRootAndEachExcludedDirectory(t *testing.T) {
	for _, name := range []string{".", ".git", ".goatest", "reports", "dist"} {
		t.Run(name, func(t *testing.T) {
			preserveCandidateFileSeams(t)
			source, destination := t.TempDir(), t.TempDir()
			makeCalls := 0
			makeCandidateDirectory = func(string, os.FileMode) error { makeCalls++; return nil }
			walkCandidateFiles = func(root string, callback fs.WalkDirFunc) error {
				path := root
				entryName := filepath.Base(root)
				if name != "." {
					path = filepath.Join(root, name)
					entryName = name
				}
				err := callback(path, candidateDirEntry{name: entryName, mode: fs.ModeDir}, nil)
				if name == "." && err != nil {
					t.Fatalf("root callback = %v", err)
				}
				if name != "." && !errors.Is(err, filepath.SkipDir) {
					t.Fatalf("excluded callback = %v", err)
				}
				return nil
			}
			if err := copyRepository(source, destination); err != nil || makeCalls != 0 {
				t.Fatalf("copyRepository = %v, mkdir calls=%d", err, makeCalls)
			}
		})
	}
}

func TestCopyRepositoryClosesFilesAndJoinsOutputFailures(t *testing.T) {
	for _, test := range []struct {
		name      string
		outputErr error
		copyErr   error
		inputErr  error
		closeOut  error
		closeIn   error
	}{
		{name: "output open", outputErr: errors.New("output open")},
		{name: "copy", copyErr: errors.New("copy")},
		{name: "output close", closeOut: errors.New("output close")},
		{name: "input close", closeIn: errors.New("input close")},
	} {
		t.Run(test.name, func(t *testing.T) {
			preserveCandidateFileSeams(t)
			source, destination := t.TempDir(), t.TempDir()
			walkCandidateFiles = singleCandidateWalk(source, candidateDirEntry{name: "value.go"}, nil)
			input := &candidateReadFile{Reader: strings.NewReader("contents"), closeErr: test.closeIn}
			output := &candidateWriteFile{closeErr: test.closeOut}
			openCandidateInput = func(string) (validationReadCloser, error) { return input, test.inputErr }
			openCandidateOutput = func(string, int, os.FileMode) (validationWriteCloser, error) { return output, test.outputErr }
			copyCandidateContent = func(destination io.Writer, source io.Reader) (int64, error) {
				if test.copyErr != nil {
					return 0, test.copyErr
				}
				return io.Copy(destination, source)
			}
			err := copyRepository(source, destination)
			want := test.outputErr
			if want == nil {
				want = errors.Join(test.copyErr, test.closeOut, test.closeIn)
			}
			if !errors.Is(err, want) && !(test.copyErr != nil && errors.Is(err, test.copyErr)) && !(test.closeOut != nil && errors.Is(err, test.closeOut)) && !(test.closeIn != nil && errors.Is(err, test.closeIn)) {
				t.Fatalf("copyRepository error = %v, want %v", err, want)
			}
			if test.outputErr != nil {
				if input.closed != 1 || output.closed != 0 {
					t.Fatalf("open failure closes = input %d output %d", input.closed, output.closed)
				}
			} else if input.closed != 1 || output.closed != 1 {
				t.Fatalf("copy closes = input %d output %d", input.closed, output.closed)
			}
		})
	}
}

func TestValidateCopyRootsCoversResolutionContainmentAndSiblingBoundaries(t *testing.T) {
	preserveCandidateFileSeams(t)
	root := t.TempDir()
	missing := filepath.Join(root, "missing")
	if err := validateCopyRoots(missing, root); err == nil || !strings.Contains(err.Error(), "resolve candidate source") {
		t.Fatalf("source resolution error = %v", err)
	}
	if err := validateCopyRoots(root, missing); err == nil || !strings.Contains(err.Error(), "resolve candidate destination") {
		t.Fatalf("destination resolution error = %v", err)
	}

	parent := t.TempDir()
	source := filepath.Join(parent, "source")
	destination := filepath.Join(source, "nested")
	if err := os.MkdirAll(destination, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := validateCopyRoots(source, destination); err == nil {
		t.Fatal("destination inside source accepted")
	}
	if err := validateCopyRoots(destination, source); err == nil {
		t.Fatal("source inside destination accepted")
	}
	if err := validateCopyRoots(source, source); err == nil {
		t.Fatal("identical roots accepted")
	}
	sibling := filepath.Join(parent, "source-other")
	if err := os.MkdirAll(sibling, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := validateCopyRoots(source, sibling); err != nil {
		t.Fatalf("prefix sibling rejected: %v", err)
	}
}

func TestCandidatePathInsideFailsClosedOnRelativeErrors(t *testing.T) {
	preserveCandidateFileSeams(t)
	cause := errors.New("relative failed")
	relativeCandidatePath = func(string, string) (string, error) { return "apparently-local", cause }
	if candidatePathInside("root", "candidate") {
		t.Fatal("relative error treated candidate as inside")
	}
	relativeCandidatePath = filepath.Rel
	root := t.TempDir()
	if !candidatePathInside(root, root) || !candidatePathInside(root, filepath.Join(root, "nested")) || candidatePathInside(filepath.Join(root, "nested"), root) {
		t.Fatal("candidatePathInside containment matrix is incorrect")
	}
}

func TestWriteCandidateValidatesSafetyExistencePreimageAndWritesExactly(t *testing.T) {
	root := t.TempDir()
	if err := writeCandidate(root, provider.Candidate{Path: "value.go"}); err == nil || !strings.Contains(err.Error(), "unsafe") {
		t.Fatalf("unsafe path error = %v", err)
	}
	if err := writeCandidate(root, provider.Candidate{Path: "missing_test.go", PreimageSHA256: strings.Repeat("a", 64)}); err == nil || !strings.Contains(err.Error(), "expects a missing file") {
		t.Fatalf("missing preimage error = %v", err)
	}

	newCandidate := provider.Candidate{Path: "nested/new_test.go", Content: []byte("new")}
	if err := writeCandidate(root, newCandidate); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(filepath.Join(root, "nested", "new_test.go")); err != nil || !slices.Equal(got, newCandidate.Content) {
		t.Fatalf("new candidate = %q, %v", got, err)
	}

	existingPath := filepath.Join(root, "existing_test.go")
	existing := []byte("old")
	if err := os.WriteFile(existingPath, existing, 0o600); err != nil {
		t.Fatal(err)
	}
	matching := provider.Candidate{Path: "existing_test.go", PreimageSHA256: candidateDigest(existing), Content: []byte("replacement")}
	if err := writeCandidate(root, matching); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(existingPath); err != nil || !slices.Equal(got, matching.Content) {
		t.Fatalf("matching candidate = %q, %v", got, err)
	}
	if err := writeCandidate(root, provider.Candidate{Path: "existing_test.go", PreimageSHA256: strings.Repeat("f", 64)}); err == nil || !strings.Contains(err.Error(), "preimage does not match") {
		t.Fatalf("dirty preimage error = %v", err)
	}
}

func TestWriteCandidatePropagatesReadDirectoryAndWriteFailures(t *testing.T) {
	for _, test := range []struct {
		name      string
		readData  []byte
		readErr   error
		preimage  string
		mkdirErr  error
		writeErr  error
		wantCause error
	}{
		{name: "read", readErr: errors.New("read failed")},
		{name: "mkdir", readErr: os.ErrNotExist, mkdirErr: errors.New("mkdir failed")},
		{name: "write", readErr: os.ErrNotExist, writeErr: errors.New("write failed")},
	} {
		t.Run(test.name, func(t *testing.T) {
			preserveCandidateFileSeams(t)
			test.wantCause = test.readErr
			if test.mkdirErr != nil {
				test.wantCause = test.mkdirErr
			}
			if test.writeErr != nil {
				test.wantCause = test.writeErr
			}
			readCandidateFile = func(string) ([]byte, error) { return test.readData, test.readErr }
			makeCandidateDirectory = func(string, os.FileMode) error { return test.mkdirErr }
			writeCandidateFile = func(string, []byte, os.FileMode) error { return test.writeErr }
			err := writeCandidate(t.TempDir(), provider.Candidate{Path: "value_test.go", PreimageSHA256: test.preimage, Content: []byte("candidate")})
			if !errors.Is(err, test.wantCause) {
				t.Fatalf("writeCandidate error = %v, want %v", err, test.wantCause)
			}
		})
	}
}

func singleCandidateWalk(root string, entry fs.DirEntry, walkErr error) func(string, fs.WalkDirFunc) error {
	return func(_ string, callback fs.WalkDirFunc) error {
		return callback(filepath.Join(root, entry.Name()), entry, walkErr)
	}
}

func writeCandidateFixture(t *testing.T, root, relative, contents string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

func candidateDigest(contents []byte) string {
	sum := sha256.Sum256(contents)
	return hex.EncodeToString(sum[:])
}

func preserveCandidateLifecycleSeams(t *testing.T) {
	t.Helper()
	previousMake, previousRemove := makeCandidateTemp, removeCandidateTemp
	previousCopy, previousWrite := copyCandidateRepository, writeCandidateRepositoryFile
	t.Cleanup(func() {
		makeCandidateTemp, removeCandidateTemp = previousMake, previousRemove
		copyCandidateRepository, writeCandidateRepositoryFile = previousCopy, previousWrite
	})
}

func preserveCandidateFileSeams(t *testing.T) {
	t.Helper()
	previousResolve, previousWalk, previousRelative := resolveCandidatePath, walkCandidateFiles, relativeCandidatePath
	previousMkdir := makeCandidateDirectory
	previousOpenInput, previousOpenOutput := openCandidateInput, openCandidateOutput
	previousCopy := copyCandidateContent
	previousRead, previousWrite := readCandidateFile, writeCandidateFile
	t.Cleanup(func() {
		resolveCandidatePath, walkCandidateFiles, relativeCandidatePath = previousResolve, previousWalk, previousRelative
		makeCandidateDirectory = previousMkdir
		openCandidateInput, openCandidateOutput = previousOpenInput, previousOpenOutput
		copyCandidateContent = previousCopy
		readCandidateFile, writeCandidateFile = previousRead, previousWrite
	})
}
