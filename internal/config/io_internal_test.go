// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package config

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/P4suta/goatest/internal/filemode"
)

const (
	initialConfigRename = 1
	backupConfigRename  = 2
	retryConfigRename   = 3
	restoreConfigRename = 4
)

type stubConfigFile struct {
	name        string
	writeErr    error
	syncErr     error
	chmodErr    error
	closeErr    error
	writes      int
	syncs       int
	chmods      int
	closes      int
	written     []byte
	writtenMode os.FileMode
}

func (file *stubConfigFile) Name() string { return file.name }

func (file *stubConfigFile) Write(data []byte) (int, error) {
	file.writes++
	file.written = append(file.written, data...)
	if file.writeErr != nil {
		return 0, file.writeErr
	}
	return len(data), nil
}

func (file *stubConfigFile) Sync() error {
	file.syncs++
	return file.syncErr
}

func (file *stubConfigFile) Chmod(mode os.FileMode) error {
	file.chmods++
	file.writtenMode = mode
	return file.chmodErr
}

func (file *stubConfigFile) Close() error {
	file.closes++
	return file.closeErr
}

func TestInitPropagatesEveryFileStageAndCleansPartialOutput(t *testing.T) {
	t.Parallel()
	openFailure := errors.New("open failure")
	for _, testCase := range []struct {
		name       string
		file       *stubConfigFile
		openErr    error
		want       error
		wantRemove bool
	}{
		{name: "open", openErr: openFailure, want: openFailure},
		{name: "write", file: &stubConfigFile{writeErr: errors.New("write failure")}, want: errors.New("write failure"), wantRemove: true},
		{name: "sync", file: &stubConfigFile{syncErr: errors.New("sync failure")}, want: errors.New("sync failure"), wantRemove: true},
		{name: "close", file: &stubConfigFile{closeErr: errors.New("close failure")}, want: errors.New("close failure"), wantRemove: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			if testCase.file != nil {
				testCase.file.name = filepath.Join(t.TempDir(), FileName)
				if testCase.file.writeErr != nil {
					testCase.want = testCase.file.writeErr
				}
				if testCase.file.syncErr != nil {
					testCase.want = testCase.file.syncErr
				}
				if testCase.file.closeErr != nil {
					testCase.want = testCase.file.closeErr
				}
			}
			removed := ""
			hooks := configIOHooks{
				open:   func(string, int, os.FileMode) (configWritableFile, error) { return testCase.file, testCase.openErr },
				remove: func(path string) error { removed = path; return nil },
			}
			err := initWithHooks(t.TempDir(), hooks.writeHooks())
			if !errors.Is(err, testCase.want) {
				t.Fatalf("Init error = %v, want %v", err, testCase.want)
			}
			if (removed != "") != testCase.wantRemove {
				t.Fatalf("removed = %q, want remove %t", removed, testCase.wantRemove)
			}
		})
	}
}

func TestSavePropagatesEveryAtomicWriteStage(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"marshal", "create", "write", "sync", "chmod", "close"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			failure := errors.New(stage + " failure")
			file := &stubConfigFile{name: filepath.Join(root, "temporary")}
			hooks := configIOHooks{
				create: func(string, string) (configWritableFile, error) { return file, nil },
			}
			switch stage {
			case "marshal":
				hooks.marshal = func(any) ([]byte, error) { return nil, failure }
			case "create":
				hooks.create = func(string, string) (configWritableFile, error) { return nil, failure }
			case "write":
				file.writeErr = failure
			case "sync":
				file.syncErr = failure
			case "chmod":
				file.chmodErr = failure
			case "close":
				file.closeErr = failure
			}
			if err := saveWithHooks(root, minimalConfig(), hooks.writeHooks()); !errors.Is(err, failure) {
				t.Fatalf("save error = %v, want %v", err, failure)
			}
		})
	}
}

func TestSaveRenameFallbackPreservesThePreviousConfiguration(t *testing.T) {
	t.Parallel()
	firstRename := errors.New("first rename")
	retryFailure := errors.New("retry rename")
	backupFailure := errors.New("backup rename")
	restoreFailure := errors.New("restore rename")
	for _, testCase := range []struct {
		name       string
		backupErr  error
		retryErr   error
		restoreErr error
		want       []error
		wantCalls  int
	}{
		{name: "replace", wantCalls: 3},
		{name: "missing destination", backupErr: os.ErrNotExist, wantCalls: 3},
		{name: "missing destination retry failure", backupErr: os.ErrNotExist, retryErr: retryFailure, want: []error{firstRename, retryFailure}, wantCalls: 3},
		{name: "backup failure", backupErr: backupFailure, want: []error{firstRename, backupFailure}, wantCalls: 2},
		{name: "retry failure restores backup", retryErr: retryFailure, want: []error{firstRename, retryFailure}, wantCalls: 4},
		{name: "restore failure preserves backup", retryErr: retryFailure, restoreErr: restoreFailure, want: []error{firstRename, retryFailure, restoreFailure}, wantCalls: 4},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			temporary := filepath.Join(root, "temporary")
			destination := filepath.Join(root, FileName)
			backup := temporary + ".backup"
			file := &stubConfigFile{name: temporary}
			renames := 0
			hooks := configIOHooks{
				create: func(string, string) (configWritableFile, error) { return file, nil },
				rename: func(oldPath, newPath string) error {
					renames++
					switch renames {
					case initialConfigRename:
						if oldPath != temporary || newPath != destination {
							t.Fatalf("initial rename(%q, %q)", oldPath, newPath)
						}
						return firstRename
					case backupConfigRename:
						if oldPath != destination || newPath != backup {
							t.Fatalf("backup rename(%q, %q)", oldPath, newPath)
						}
						return testCase.backupErr
					case retryConfigRename:
						if oldPath != temporary || newPath != destination {
							t.Fatalf("retry rename(%q, %q)", oldPath, newPath)
						}
						return testCase.retryErr
					case restoreConfigRename:
						if oldPath != backup || newPath != destination {
							t.Fatalf("restore rename(%q, %q)", oldPath, newPath)
						}
						return testCase.restoreErr
					default:
						t.Fatalf("unexpected rename(%q, %q)", oldPath, newPath)
						return nil
					}
				},
			}
			err := saveWithHooks(root, minimalConfig(), hooks.writeHooks())
			if len(testCase.want) == 0 && err != nil {
				t.Fatalf("save error = %v", err)
			}
			for _, want := range testCase.want {
				if !errors.Is(err, want) {
					t.Fatalf("save error = %v, want joined error %v", err, want)
				}
			}
			if renames != testCase.wantCalls {
				t.Fatalf("rename calls = %d, want %d", renames, testCase.wantCalls)
			}
		})
	}
}

func TestSaveRestoresExistingFileAfterAReplacementFailure(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	path := filepath.Join(root, FileName)
	original := []byte("original configuration\n")
	if err := os.WriteFile(path, original, filemode.ReadableFile); err != nil {
		t.Fatal(err)
	}
	firstFailure := errors.New("platform refused replacement")
	retryFailure := errors.New("replacement retry failed")
	renames := 0
	hooks := configIOHooks{rename: func(oldPath, newPath string) error {
		renames++
		switch renames {
		case initialConfigRename:
			return firstFailure
		case retryConfigRename:
			return retryFailure
		default:
			return os.Rename(oldPath, newPath)
		}
	}}
	err := saveWithHooks(root, minimalConfig(), hooks.writeHooks())
	contents, readErr := os.ReadFile(path)
	if !errors.Is(err, firstFailure) || !errors.Is(err, retryFailure) || readErr != nil || string(contents) != string(original) || renames != restoreConfigRename {
		t.Fatalf("save = %v, config=%q read=%v renames=%d", err, contents, readErr, renames)
	}
}

func TestSaveSuccessWritesSyncsModesClosesAndRenames(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	temporary := filepath.Join(root, "temporary")
	file := &stubConfigFile{name: temporary}
	renames := 0
	hooks := configIOHooks{
		create: func(string, string) (configWritableFile, error) { return file, nil },
		rename: func(oldPath, newPath string) error {
			renames++
			if oldPath != temporary || newPath != filepath.Join(root, FileName) {
				t.Fatalf("rename(%q, %q)", oldPath, newPath)
			}
			return nil
		},
	}
	if err := saveWithHooks(root, minimalConfig(), hooks.writeHooks()); err != nil {
		t.Fatal(err)
	}
	if file.writes != 1 || file.syncs != 1 || file.chmods != 1 || file.closes != 1 || file.writtenMode != filemode.ReadableFile || renames != 1 {
		t.Fatalf("file = %+v, renames = %d", file, renames)
	}
	if len(file.written) == 0 {
		t.Fatal("save wrote no TOML")
	}
}

// configIOHooks names the operations a test drives, in the order this file's
// tests think about them.
//
// It is a separate shape from writeHooks so the tests read as they did before,
// and it is converted rather than installed: what a test supplies is reachable
// only from the call it passed it to, which is what lets this package run in
// parallel.
type configIOHooks struct {
	open    func(string, int, os.FileMode) (configWritableFile, error)
	create  func(string, string) (configWritableFile, error)
	marshal func(any) ([]byte, error)
	remove  func(string) error
	rename  func(string, string) error
}

// writeHooks converts a test's table into the value the code takes.
func (hooks configIOHooks) writeHooks() writeHooks {
	return writeHooks{
		open: hooks.open, createTemp: hooks.create, marshal: hooks.marshal,
		remove: hooks.remove, rename: hooks.rename,
	}
}

func minimalConfig() Config {
	return Config{Version: 1, Contract: "standard-v1", Resources: map[string]Resource{}}
}
