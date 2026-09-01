// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package cache

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/P4suta/goatest/internal/report"
)

type stubCacheFile struct {
	name     string
	writeErr error
	syncErr  error
	closeErr error
	writes   int
	syncs    int
	closes   int
	written  []byte
}

func (file *stubCacheFile) Name() string { return file.name }

func (file *stubCacheFile) Write(data []byte) (int, error) {
	file.writes++
	file.written = append(file.written, data...)
	if file.writeErr != nil {
		return 0, file.writeErr
	}
	return len(data), nil
}

func (file *stubCacheFile) Sync() error {
	file.syncs++
	return file.syncErr
}

func (file *stubCacheFile) Close() error {
	file.closes++
	return file.closeErr
}

func TestGetReturnsTheReadFailureWithoutDecodingFallbackBytes(t *testing.T) {
	t.Parallel()
	failure := errors.New("read failure")
	hooks := storeHooks{
		read: func(string) ([]byte, error) { return []byte(`{"schema":"assurance-report-v1"}`), failure },
	}
	got, ok, err := New(t.TempDir()).getWithHooks("digest-a", hooks)
	if !errors.Is(err, failure) || ok || !reflect.DeepEqual(got, report.Report{}) {
		t.Fatalf("Get = %+v, ok %v, err %v", got, ok, err)
	}
}

func TestPutPropagatesEveryAtomicWriteStage(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"mkdir", "create", "write", "sync", "close"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			failure := errors.New(stage + " failure")
			file := &stubCacheFile{name: filepath.Join(root, "temporary")}
			hooks := storeHooks{
				createTemporary: func(string, string) (cacheWritableFile, error) { return file, nil },
			}
			switch stage {
			case "mkdir":
				hooks.mkdirAll = func(string, os.FileMode) error { return failure }
			case "create":
				hooks.createTemporary = func(string, string) (cacheWritableFile, error) { return nil, failure }
			case "write":
				file.writeErr = failure
			case "sync":
				file.syncErr = failure
			case "close":
				file.closeErr = failure
			}
			if err := New(root).putWithHooks("digest-a", cachedReport(), hooks); !errors.Is(err, failure) {
				t.Fatalf("Put error = %v, want %v", err, failure)
			}
		})
	}
}

func TestPutRenameFallbackDistinguishesMissingAndRemovalFailures(t *testing.T) {
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
			destination := filepath.Join(root, "v1", "digest-a", "report.json")
			file := &stubCacheFile{name: temporary}
			renames := 0
			hooks := storeHooks{
				createTemporary: func(string, string) (cacheWritableFile, error) { return file, nil },
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
			err := New(root).putWithHooks("digest-a", cachedReport(), hooks)
			if testCase.want == nil {
				if err != nil {
					t.Fatalf("Put error = %v", err)
				}
			} else if !errors.Is(err, testCase.want) || testCase.wantJoined != nil && !errors.Is(err, testCase.wantJoined) {
				t.Fatalf("Put error = %v, want %v joined with %v", err, testCase.want, testCase.wantJoined)
			}
			if renames != testCase.wantCalls {
				t.Fatalf("rename calls = %d, want %d", renames, testCase.wantCalls)
			}
		})
	}
}

func TestPutSuccessWritesSyncsClosesAndRenames(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	temporary := filepath.Join(root, "temporary")
	file := &stubCacheFile{name: temporary}
	renames := 0
	hooks := storeHooks{
		createTemporary: func(string, string) (cacheWritableFile, error) { return file, nil },
		rename: func(oldPath, newPath string) error {
			renames++
			if oldPath != temporary || newPath != filepath.Join(root, "v1", "digest-a", "report.json") {
				t.Fatalf("rename(%q, %q)", oldPath, newPath)
			}
			return nil
		},
	}
	if err := New(root).putWithHooks("digest-a", cachedReport(), hooks); err != nil {
		t.Fatal(err)
	}
	if file.writes != 1 || file.syncs != 1 || file.closes != 1 || renames != 1 {
		t.Fatalf("file = %+v, renames = %d", file, renames)
	}
	if got := string(file.written); got != string(report.JSON(cachedReport())) {
		t.Fatalf("written = %q", got)
	}
}

func TestPutTreatsPostCommitCollectionAsBestEffort(t *testing.T) {
	t.Parallel()
	hooks := storeHooks{
		collect: func(string, int64, time.Duration, time.Time) (GCResult, error) {
			return GCResult{}, errors.New("collection failed after commit")
		},
	}
	root := t.TempDir()
	store := NewWithPolicy(root, 1, time.Hour)
	if err := store.putWithHooks("digest-a", cachedReport(), hooks); err != nil {
		t.Fatalf("Put reported post-commit collection failure: %v", err)
	}
	got, found, err := store.Get("digest-a")
	if err != nil || !found || got.Snapshot != "digest-a" {
		t.Fatalf("committed cache entry = (%+v, %t, %v)", got, found, err)
	}
}

// TestPutTrimsTheBoundedCacheAfterCommittingAnEntry pins the collection a
// policy store runs for itself. Every other collection test replaces the
// collector, so without this one nothing exercises the default: a store wired
// to the locking Collect, or to no collector at all, would still pass them.
func TestPutTrimsTheBoundedCacheAfterCommittingAnEntry(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	store := NewWithPolicy(root, 1, time.Hour)
	if err := store.Put("digest-a", cachedReport()); err != nil {
		t.Fatal(err)
	}
	got, found, err := store.Get("digest-a")
	if err != nil || found {
		t.Fatalf("entry over the byte budget survived its own write: (%+v, %t, %v)", got, found, err)
	}
	entries, err := os.ReadDir(filepath.Join(root, "v1"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("collected cache still holds %v", entries)
	}
}

func cachedReport() report.Report {
	return report.Report{Schema: report.SchemaV1, Verdict: report.VerdictAssured, Snapshot: "digest-a"}
}
