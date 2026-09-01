// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package evidence

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

type stubEvidenceEntry struct {
	name    string
	dir     bool
	typ     fs.FileMode
	info    fs.FileInfo
	infoErr error
}

func (entry stubEvidenceEntry) Name() string               { return entry.name }
func (entry stubEvidenceEntry) IsDir() bool                { return entry.dir }
func (entry stubEvidenceEntry) Type() fs.FileMode          { return entry.typ }
func (entry stubEvidenceEntry) Info() (fs.FileInfo, error) { return entry.info, entry.infoErr }

type stubEvidenceInfo struct {
	name string
	mode fs.FileMode
}

func (info stubEvidenceInfo) Name() string       { return info.name }
func (info stubEvidenceInfo) Size() int64        { return 0 }
func (info stubEvidenceInfo) Mode() fs.FileMode  { return info.mode }
func (info stubEvidenceInfo) ModTime() time.Time { return time.Time{} }
func (info stubEvidenceInfo) IsDir() bool        { return info.mode.IsDir() }
func (info stubEvidenceInfo) Sys() any           { return nil }

type failingEvidenceReader struct {
	err    error
	closed bool
}

func (reader *failingEvidenceReader) Read([]byte) (int, error) { return 0, reader.err }
func (reader *failingEvidenceReader) Close() error {
	reader.closed = true
	return nil
}

type stubEvidenceFile struct {
	name     string
	writeErr error
	syncErr  error
	closeErr error
	writes   int
	syncs    int
	closes   int
	written  []byte
}

func (file *stubEvidenceFile) Name() string { return file.name }

func (file *stubEvidenceFile) Write(data []byte) (int, error) {
	file.writes++
	file.written = append(file.written, data...)
	if file.writeErr != nil {
		return 0, file.writeErr
	}
	return len(data), nil
}

func (file *stubEvidenceFile) Sync() error {
	file.syncs++
	return file.syncErr
}

func (file *stubEvidenceFile) Close() error {
	file.closes++
	return file.closeErr
}

func TestScanPropagatesEveryFilesystemStage(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"walk", "callback", "relative", "symlink", "info", "irregular", "digest"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			failure := errors.New(stage + " failure")
			hooks := scanHooks{}
			entry := stubEvidenceEntry{
				name: "sample", info: stubEvidenceInfo{name: "sample", mode: 0o644},
			}
			hooks.walk = func(root string, visit fs.WalkDirFunc) error {
				if stage == "walk" {
					return failure
				}
				if stage == "callback" {
					return visit(filepath.Join(root, "sample"), entry, failure)
				}
				return visit(filepath.Join(root, "sample"), entry, nil)
			}
			switch stage {
			case "relative":
				hooks.relative = func(string, string) (string, error) { return "", failure }
			case "symlink":
				entry.typ = fs.ModeSymlink
			case "info":
				entry.infoErr = failure
			case "irregular":
				entry.info = stubEvidenceInfo{name: "sample", mode: fs.ModeNamedPipe}
			case "digest":
				hooks.digestFile = func(string, fs.FileMode) (string, error) { return "", failure }
			}
			files, corpus, err := scanWithHooks(t.TempDir(), hooks)
			if stage == "symlink" || stage == "irregular" {
				if err == nil {
					t.Fatalf("Scan error = nil, want refusal at %s", stage)
				}
			} else if !errors.Is(err, failure) {
				t.Fatalf("Scan error = %v, want %v", err, failure)
			}
			if files != nil || corpus != nil {
				t.Fatalf("failed Scan returned %v / %v", files, corpus)
			}
		})
	}
}

func TestFileDigestPropagatesOpenAndCopyFailures(t *testing.T) {
	t.Parallel()
	openFailure := errors.New("open failure")
	openHooks := scanHooks{open: func(string) (io.ReadCloser, error) { return nil, openFailure }}
	if _, err := fileDigestWithHooks("missing", 0o644, openHooks); !errors.Is(err, openFailure) {
		t.Fatalf("open error = %v", err)
	}

	copyFailure := errors.New("copy failure")
	reader := &failingEvidenceReader{err: copyFailure}
	copyHooks := scanHooks{open: func(string) (io.ReadCloser, error) { return reader, nil }}
	if _, err := fileDigestWithHooks("unreadable", 0o644, copyHooks); !errors.Is(err, copyFailure) {
		t.Fatalf("copy error = %v", err)
	}
	if !reader.closed {
		t.Fatal("fileDigestWithHooks did not close its reader")
	}
}

// TestScanDigestsThroughItsOpenHook keeps the wiring the default digest hook
// depends on: a scan that is given only an open hook still digests through it.
func TestScanDigestsThroughItsOpenHook(t *testing.T) {
	t.Parallel()
	failure := errors.New("open failure")
	entry := stubEvidenceEntry{name: "sample", info: stubEvidenceInfo{name: "sample", mode: 0o644}}
	hooks := scanHooks{
		walk: func(root string, visit fs.WalkDirFunc) error {
			return visit(filepath.Join(root, "sample"), entry, nil)
		},
		open: func(string) (io.ReadCloser, error) { return nil, failure },
	}
	if _, _, err := scanWithHooks(t.TempDir(), hooks); !errors.Is(err, failure) {
		t.Fatalf("Scan error = %v, want %v", err, failure)
	}
}

func TestGraphJSONPropagatesMarshalFailure(t *testing.T) {
	t.Parallel()
	failure := errors.New("graph marshal failure")
	hooks := graphHooks{marshalGraph: func(any, string, string) ([]byte, error) { return nil, failure }}
	if data, err := (Graph{}).jsonWithHooks(hooks); !errors.Is(err, failure) || data != nil {
		t.Fatalf("Graph.JSON = %q, %v", data, err)
	}
}

func TestLoadGraphReturnsReadFailureWithoutDecodeFallback(t *testing.T) {
	t.Parallel()
	failure := errors.New("read failure")
	hooks := graphHooks{
		readGraph: func(string) ([]byte, error) { return []byte(`{"schema":"evidence-graph-v1"}`), failure },
	}
	got, ok, err := loadGraphWithHooks("graph.json", hooks)
	if !errors.Is(err, failure) || ok || !reflect.DeepEqual(got, GraphRecord{}) {
		t.Fatalf("LoadGraph = %+v, ok %v, err %v", got, ok, err)
	}
}

func TestSaveGraphPropagatesEverySerializationAndWriteStage(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"graph-marshal", "graph-unmarshal", "record-marshal", "mkdir", "create", "write", "sync", "close"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			failure := errors.New(stage + " failure")
			file := &stubEvidenceFile{name: filepath.Join(root, "temporary")}
			hooks := graphHooks{
				createTemporary: func(string, string) (evidenceWritableFile, error) { return file, nil },
			}
			switch stage {
			case "graph-marshal":
				hooks.marshalGraph = func(any, string, string) ([]byte, error) { return nil, failure }
			case "graph-unmarshal":
				hooks.unmarshalGraph = func([]byte, any) error { return failure }
			case "record-marshal":
				hooks.marshalRecord = func(any, string, string) ([]byte, error) { return nil, failure }
			case "mkdir":
				hooks.mkdirAll = func(string, os.FileMode) error { return failure }
			case "create":
				hooks.createTemporary = func(string, string) (evidenceWritableFile, error) { return nil, failure }
			case "write":
				file.writeErr = failure
			case "sync":
				file.syncErr = failure
			case "close":
				file.closeErr = failure
			}
			if err := saveGraphWithHooks(filepath.Join(root, "graph.json"), graphRecord(), hooks); !errors.Is(err, failure) {
				t.Fatalf("SaveGraph error = %v, want %v", err, failure)
			}
		})
	}
}

func TestSaveGraphRenameFallbackDistinguishesMissingAndRemovalFailures(t *testing.T) {
	t.Parallel()
	firstRename := errors.New("first rename")
	secondRename := errors.New("second rename")
	removeFailure := errors.New("remove destination")
	for _, testCase := range []struct {
		name       string
		removeErr  error
		secondErr  error
		want       error
		wantJoined error
		wantCalls  int
	}{
		{name: "replace", wantCalls: 2},
		{name: "missing-destination", removeErr: os.ErrNotExist, secondErr: secondRename, want: secondRename, wantCalls: 2},
		{name: "remove-failure", removeErr: removeFailure, want: firstRename, wantJoined: removeFailure, wantCalls: 1},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			temporary := filepath.Join(root, "temporary")
			destination := filepath.Join(root, "graph.json")
			file := &stubEvidenceFile{name: temporary}
			renames := 0
			hooks := graphHooks{
				createTemporary: func(string, string) (evidenceWritableFile, error) { return file, nil },
				rename: func(oldPath, newPath string) error {
					if oldPath != temporary || newPath != destination {
						t.Fatalf("rename(%q, %q)", oldPath, newPath)
					}
					renames++
					if renames == 1 {
						return firstRename
					}
					return testCase.secondErr
				},
				remove: func(path string) error {
					if path == destination {
						return testCase.removeErr
					}
					return nil
				},
			}
			err := saveGraphWithHooks(destination, graphRecord(), hooks)
			if testCase.want == nil {
				if err != nil {
					t.Fatalf("SaveGraph error = %v", err)
				}
			} else if !errors.Is(err, testCase.want) || testCase.wantJoined != nil && !errors.Is(err, testCase.wantJoined) {
				t.Fatalf("SaveGraph error = %v, want %v joined with %v", err, testCase.want, testCase.wantJoined)
			}
			if renames != testCase.wantCalls {
				t.Fatalf("rename calls = %d, want %d", renames, testCase.wantCalls)
			}
		})
	}
}

func graphRecord() GraphRecord {
	return GraphRecord{ModulePath: "example/module", Graph: Graph{FilePackages: map[string]string{"a.go": "example/module"}}}
}
