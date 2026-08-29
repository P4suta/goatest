// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package mutationbridge

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	gomutants "github.com/P4suta/go-mutants"
)

func TestCorpusPathCanonicalizesOnlyDirectStandardCorpusEntries(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		path string
		want string
		ok   bool
	}{
		{path: "testdata/fuzz/FuzzX/seed", want: "testdata/fuzz/FuzzX/seed", ok: true},
		{path: "./pkg/../pkg/testdata/fuzz/FuzzX/seed", want: "pkg/testdata/fuzz/FuzzX/seed", ok: true},
		{path: ""},
		{path: "."},
		{path: ".."},
		{path: "testdata/fuzz/FuzzX"},
		{path: "testdata/fuzz/FuzzX/seed/extra"},
		{path: "nottestdata/fuzz/FuzzX/seed"},
		{path: "testdata/notfuzz/FuzzX/seed"},
		{path: "testdata/fuzz//seed"},
		{path: "testdata/fuzz/FuzzX/.."},
		{path: "../testdata/fuzz/FuzzX/seed"},
		{path: "/testdata/fuzz/FuzzX/seed"},
		{path: "./A:/testdata/fuzz/FuzzX/seed"},
		{path: `testdata\fuzz\FuzzX\seed`},
		{path: "testdata/fuzz/FuzzX/\x00"},
	} {
		got, ok := corpusPath(test.path)
		if got != test.want || ok != test.ok {
			t.Errorf("corpusPath(%q) = (%q, %t), want (%q, %t)", test.path, got, ok, test.want, test.ok)
		}
	}
}

func TestPromoteCorpusValidatesBoundsHeaderAndDigestBeforeWriting(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	valid := []byte("go test fuzz v1\n[]byte(\"value\")\n")
	wrongDigest := strings.Repeat("0", sha256.Size*2)
	for _, artifact := range []gomutants.Artifact{
		{Path: "production.go", Data: valid},
		{Path: "testdata/fuzz/FuzzX/empty"},
		{Path: "testdata/fuzz/FuzzX/header", Data: []byte("not fuzz data")},
		{Path: "testdata/fuzz/FuzzX/large", Data: append([]byte("go test fuzz v1\n"), make([]byte, maximumCorpusBytes)...)},
		{Path: "testdata/fuzz/FuzzX/digest", Data: valid, SHA256: wrongDigest},
	} {
		if path, added, err := PromoteCorpus(root, artifact); err == nil || path != "" || added {
			t.Errorf("PromoteCorpus(%q) = (%q, %t, %v), want rejection", artifact.Path, path, added, err)
		}
	}

	bounded := make([]byte, maximumCorpusBytes)
	copy(bounded, "go test fuzz v1\n")
	sum := sha256.Sum256(bounded)
	artifact := gomutants.Artifact{
		Path:   "testdata/fuzz/FuzzX/bounded",
		Data:   bounded,
		SHA256: hex.EncodeToString(sum[:]),
	}
	path, added, err := PromoteCorpus(root, artifact)
	if err != nil || !added || path != artifact.Path {
		t.Fatalf("bounded PromoteCorpus = (%q, %t, %v)", path, added, err)
	}
}

func TestPromoteCorpusRejectsExistingDifferentBytesAndNonDirectoryParents(t *testing.T) {
	t.Parallel()
	data := []byte("go test fuzz v1\n[]byte(\"new\")\n")

	t.Run("different bytes", func(t *testing.T) {
		root := t.TempDir()
		target := filepath.Join(root, "testdata", "fuzz", "FuzzX", "seed")
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, []byte("user data"), 0o600); err != nil {
			t.Fatal(err)
		}
		path, added, err := PromoteCorpus(root, gomutants.Artifact{Path: "testdata/fuzz/FuzzX/seed", Data: data})
		if err == nil || path != "" || added {
			t.Fatalf("different existing corpus = (%q, %t, %v)", path, added, err)
		}
		got, err := os.ReadFile(target)
		if err != nil || string(got) != "user data" {
			t.Fatalf("existing corpus = %q, %v", got, err)
		}
	})

	t.Run("identical bytes", func(t *testing.T) {
		root := t.TempDir()
		relative := "testdata/fuzz/FuzzX/seed"
		target := filepath.Join(root, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, data, 0o600); err != nil {
			t.Fatal(err)
		}
		path, added, err := PromoteCorpus(root, gomutants.Artifact{Path: relative, Data: data})
		if err != nil || path != relative || added {
			t.Fatalf("identical existing corpus = (%q, %t, %v)", path, added, err)
		}
	})

	t.Run("non-directory parent", func(t *testing.T) {
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, "testdata"), []byte("blocked"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, _, err := PromoteCorpus(root, gomutants.Artifact{Path: "testdata/fuzz/FuzzX/seed", Data: data})
		if err == nil || !strings.Contains(err.Error(), "is not a real directory") {
			t.Fatalf("non-directory parent error = %v", err)
		}
	})

	t.Run("file root", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "root-file")
		if err := os.WriteFile(root, []byte("blocked"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, _, err := PromoteCorpus(root, gomutants.Artifact{Path: "testdata/fuzz/FuzzX/seed", Data: data}); err == nil {
			t.Fatal("file root was accepted")
		}
	})
}

func TestSafeTargetRequiresCanonicalPathAndRealRoot(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	for _, relative := range []string{"", "./testdata/fuzz/FuzzX/seed", "testdata/fuzz/FuzzX/seed/extra"} {
		if _, err := safeTarget(root, relative); err == nil {
			t.Errorf("safeTarget accepted %q", relative)
		}
	}
	want := filepath.Join(root, "testdata", "fuzz", "FuzzX", "seed")
	got, err := safeTarget(root, "testdata/fuzz/FuzzX/seed")
	if err != nil || got != want {
		t.Fatalf("safeTarget = (%q, %v), want %q", got, err, want)
	}
	if _, err := safeTarget(filepath.Join(root, "missing"), "testdata/fuzz/FuzzX/seed"); err == nil {
		t.Fatal("safeTarget accepted a missing root")
	}
	if target, err := safeTarget("", "testdata/fuzz/FuzzX/seed"); err == nil || target != "" {
		t.Fatalf("safeTarget accepted empty root: (%q, %v)", target, err)
	}
	fileRoot := filepath.Join(root, "root-file")
	if err := os.WriteFile(fileRoot, []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	if target, err := safeTarget(fileRoot, "testdata/fuzz/FuzzX/seed"); err == nil || target != "" {
		t.Fatalf("safeTarget accepted file root: (%q, %v)", target, err)
	}
}

func TestPromoteCorpusPreservesConfinementFailuresBeforeAndAfterMkdir(t *testing.T) {
	data := []byte("go test fuzz v1\n[]byte(\"value\")\n")
	for _, phase := range []string{"initial", "after mkdir"} {
		t.Run(phase, func(t *testing.T) {
			preserveCorpusHooks(t)
			root := t.TempDir()
			sentinel := errors.New(phase + " confinement failed")
			original := absoluteCorpusPath
			calls := 0
			absoluteCorpusPath = func(path string) (string, error) {
				calls++
				if phase == "initial" || calls == 2 {
					return "", sentinel
				}
				return original(path)
			}
			path, added, err := PromoteCorpus(root, gomutants.Artifact{Path: "testdata/fuzz/FuzzX/seed", Data: data})
			if path != "" || added || !errors.Is(err, sentinel) {
				t.Fatalf("PromoteCorpus = (%q, %t, %v), want confinement failure", path, added, err)
			}
			wantCalls := 1
			if phase == "after mkdir" {
				wantCalls = 2
			}
			if calls != wantCalls {
				t.Fatalf("absolute path calls = %d, want %d", calls, wantCalls)
			}
		})
	}
}

func TestSafeTargetPropagatesEveryFilesystemFailure(t *testing.T) {
	for _, stage := range []string{"absolute", "evaluate", "stat", "lstat"} {
		t.Run(stage, func(t *testing.T) {
			preserveCorpusHooks(t)
			root := t.TempDir()
			sentinel := errors.New(stage + " failed")
			switch stage {
			case "absolute":
				absoluteCorpusPath = func(string) (string, error) { return "", sentinel }
			case "evaluate":
				evaluateCorpusSymlinks = func(string) (string, error) { return "", sentinel }
			case "stat":
				statCorpusPath = func(string) (os.FileInfo, error) { return nil, sentinel }
			case "lstat":
				lstatCorpusPath = func(string) (os.FileInfo, error) { return nil, sentinel }
			}
			if _, err := safeTarget(root, "testdata/fuzz/FuzzX/seed"); !errors.Is(err, sentinel) {
				t.Fatalf("safeTarget error = %v", err)
			}
		})
	}
}

func TestSafeTargetRejectsSymbolicLinksAndNonRegularFinalEntries(t *testing.T) {
	for _, test := range []struct {
		name string
		mode os.FileMode
		dir  bool
	}{
		{name: "symbolic link", mode: os.ModeSymlink},
		{name: "directory", mode: os.ModeDir, dir: true},
		{name: "named pipe", mode: os.ModeNamedPipe},
	} {
		t.Run(test.name, func(t *testing.T) {
			preserveCorpusHooks(t)
			root := t.TempDir()
			parent := filepath.Join(root, "testdata", "fuzz", "FuzzX")
			if err := os.MkdirAll(parent, 0o755); err != nil {
				t.Fatal(err)
			}
			final := filepath.Join(parent, "seed")
			original := lstatCorpusPath
			lstatCorpusPath = func(path string) (os.FileInfo, error) {
				if path == final {
					return fakeCorpusInfo{name: "seed", mode: test.mode, dir: test.dir}, nil
				}
				return original(path)
			}
			_, err := safeTarget(root, "testdata/fuzz/FuzzX/seed")
			if err == nil {
				t.Fatalf("safeTarget accepted final %s", test.name)
			}
			if test.name == "symbolic link" {
				const want = `goatest: corpus path "testdata/fuzz/FuzzX/seed" crosses symbolic link "testdata/fuzz/FuzzX/seed"`
				if err.Error() != want {
					t.Fatalf("symbolic link error = %q, want %q", err, want)
				}
			}
		})
	}
}

func TestPromoteCorpusPropagatesEveryAtomicStageAndCleansTemporaryFile(t *testing.T) {
	data := []byte("go test fuzz v1\n[]byte(\"value\")\n")
	for _, stage := range []string{"read", "mkdir", "create", "write", "short write", "sync", "chmod", "close", "link"} {
		t.Run(stage, func(t *testing.T) {
			preserveCorpusHooks(t)
			root := t.TempDir()
			sentinel := errors.New(stage + " failed")
			file := &fakeCorpusFile{name: filepath.Join(root, "temporary"), failure: stage, err: sentinel}
			removed := ""
			removeCorpusFile = func(path string) error { removed = path; return nil }
			if stage == "read" {
				readCorpusFile = func(string) ([]byte, error) { return nil, sentinel }
			}
			if stage == "mkdir" {
				mkdirCorpusAll = func(string, os.FileMode) error { return sentinel }
			}
			createCorpusTemp = func(string, string) (corpusWritableFile, error) {
				if stage == "create" {
					return nil, sentinel
				}
				return file, nil
			}
			var linkedOld, linkedNew string
			linkCorpusFile = func(oldname, newname string) error {
				linkedOld, linkedNew = oldname, newname
				if stage == "link" {
					return sentinel
				}
				return nil
			}

			path, added, err := PromoteCorpus(root, gomutants.Artifact{Path: "testdata/fuzz/FuzzX/seed", Data: data})
			wantError := sentinel
			if stage == "short write" {
				wantError = io.ErrShortWrite
			}
			if path != "" || added || !errors.Is(err, wantError) {
				t.Fatalf("PromoteCorpus = (%q, %t, %v), want %v", path, added, err, wantError)
			}
			created := stage != "read" && stage != "mkdir" && stage != "create"
			if created && removed != file.name {
				t.Fatalf("temporary removed = %q, want %q", removed, file.name)
			}
			if !created && removed != "" {
				t.Fatalf("unexpected temporary removal %q", removed)
			}
			if stage == "write" || stage == "short write" || stage == "sync" || stage == "chmod" {
				if file.closes != 1 {
					t.Fatalf("close calls = %d, want 1", file.closes)
				}
			}
			if stage == "link" {
				wantTarget := filepath.Join(root, "testdata", "fuzz", "FuzzX", "seed")
				if linkedOld != file.name || linkedNew != wantTarget || file.mode != 0o644 || !slices.Equal(file.data, data) || file.closes != 1 {
					t.Fatalf("link=(%q,%q) file={mode:%o data:%q closes:%d}", linkedOld, linkedNew, file.mode, file.data, file.closes)
				}
			}
		})
	}
}

func TestPromoteCorpusHandlesConcurrentIdenticalAndDifferentCreation(t *testing.T) {
	data := []byte("go test fuzz v1\n[]byte(\"value\")\n")
	for _, identical := range []bool{true, false} {
		t.Run(map[bool]string{true: "identical", false: "different"}[identical], func(t *testing.T) {
			preserveCorpusHooks(t)
			root := t.TempDir()
			linkCorpusFile = func(_ string, target string) error {
				contents := []byte("other")
				if identical {
					contents = data
				}
				if err := os.WriteFile(target, contents, 0o600); err != nil {
					return err
				}
				return os.ErrExist
			}
			path, added, err := PromoteCorpus(root, gomutants.Artifact{Path: "testdata/fuzz/FuzzX/seed", Data: data})
			if identical {
				if err != nil || added || path != "testdata/fuzz/FuzzX/seed" {
					t.Fatalf("identical race = (%q, %t, %v)", path, added, err)
				}
				return
			}
			if err == nil || added || path != "" || !strings.Contains(err.Error(), "atomically promote corpus") {
				t.Fatalf("different race = (%q, %t, %v)", path, added, err)
			}
		})
	}
}

type fakeCorpusFile struct {
	name    string
	failure string
	err     error
	data    []byte
	mode    os.FileMode
	closes  int
}

func (file *fakeCorpusFile) Name() string { return file.name }

func (file *fakeCorpusFile) Write(data []byte) (int, error) {
	if file.failure == "write" {
		return 0, file.err
	}
	file.data = slices.Clone(data)
	if file.failure == "short write" {
		return len(data) - 1, nil
	}
	return len(data), nil
}

func (file *fakeCorpusFile) Sync() error {
	if file.failure == "sync" {
		return file.err
	}
	return nil
}

func (file *fakeCorpusFile) Chmod(mode os.FileMode) error {
	file.mode = mode
	if file.failure == "chmod" {
		return file.err
	}
	return nil
}

func (file *fakeCorpusFile) Close() error {
	file.closes++
	if file.failure == "close" {
		return file.err
	}
	return nil
}

type fakeCorpusInfo struct {
	name string
	mode os.FileMode
	dir  bool
}

func (info fakeCorpusInfo) Name() string      { return info.name }
func (fakeCorpusInfo) Size() int64            { return 0 }
func (info fakeCorpusInfo) Mode() os.FileMode { return info.mode }
func (fakeCorpusInfo) ModTime() time.Time     { return time.Time{} }
func (info fakeCorpusInfo) IsDir() bool       { return info.dir }
func (fakeCorpusInfo) Sys() any               { return nil }

func preserveCorpusHooks(t *testing.T) {
	t.Helper()
	abs, eval := absoluteCorpusPath, evaluateCorpusSymlinks
	stat, lstat, read := statCorpusPath, lstatCorpusPath, readCorpusFile
	mkdir, create, remove, link := mkdirCorpusAll, createCorpusTemp, removeCorpusFile, linkCorpusFile
	t.Cleanup(func() {
		absoluteCorpusPath, evaluateCorpusSymlinks = abs, eval
		statCorpusPath, lstatCorpusPath, readCorpusFile = stat, lstat, read
		mkdirCorpusAll, createCorpusTemp, removeCorpusFile, linkCorpusFile = mkdir, create, remove, link
	})
}
