// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package config

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
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
			installConfigIO(t, configIOHooks{
				open:   func(string, int, os.FileMode) (configWritableFile, error) { return testCase.file, testCase.openErr },
				remove: func(path string) error { removed = path; return nil },
			})
			err := Init(t.TempDir())
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
	for _, stage := range []string{"marshal", "create", "write", "sync", "chmod", "close"} {
		t.Run(stage, func(t *testing.T) {
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
			installConfigIO(t, hooks)
			if err := save(root, minimalConfig()); !errors.Is(err, failure) {
				t.Fatalf("save error = %v, want %v", err, failure)
			}
		})
	}
}

func TestSaveRenameFallbackDistinguishesMissingAndRemovalFailures(t *testing.T) {
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
		{name: "replace", secondErr: nil, wantCalls: 2},
		{name: "missing-destination", removeErr: os.ErrNotExist, secondErr: secondRename, want: secondRename, wantCalls: 2},
		{name: "remove-failure", removeErr: removeFailure, want: firstRename, wantJoined: removeFailure, wantCalls: 1},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			root := t.TempDir()
			temporary := filepath.Join(root, "temporary")
			destination := filepath.Join(root, FileName)
			file := &stubConfigFile{name: temporary}
			renames := 0
			installConfigIO(t, configIOHooks{
				create: func(string, string) (configWritableFile, error) { return file, nil },
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
			})
			err := save(root, minimalConfig())
			if testCase.want == nil {
				if err != nil {
					t.Fatalf("save error = %v", err)
				}
			} else if !errors.Is(err, testCase.want) || testCase.wantJoined != nil && !errors.Is(err, testCase.wantJoined) {
				t.Fatalf("save error = %v, want %v joined with %v", err, testCase.want, testCase.wantJoined)
			}
			if renames != testCase.wantCalls {
				t.Fatalf("rename calls = %d, want %d", renames, testCase.wantCalls)
			}
		})
	}
}

func TestSaveSuccessWritesSyncsModesClosesAndRenames(t *testing.T) {
	root := t.TempDir()
	temporary := filepath.Join(root, "temporary")
	file := &stubConfigFile{name: temporary}
	renames := 0
	installConfigIO(t, configIOHooks{
		create: func(string, string) (configWritableFile, error) { return file, nil },
		rename: func(oldPath, newPath string) error {
			renames++
			if oldPath != temporary || newPath != filepath.Join(root, FileName) {
				t.Fatalf("rename(%q, %q)", oldPath, newPath)
			}
			return nil
		},
	})
	if err := save(root, minimalConfig()); err != nil {
		t.Fatal(err)
	}
	if file.writes != 1 || file.syncs != 1 || file.chmods != 1 || file.closes != 1 || file.writtenMode != 0o644 || renames != 1 {
		t.Fatalf("file = %+v, renames = %d", file, renames)
	}
	if len(file.written) == 0 {
		t.Fatal("save wrote no TOML")
	}
}

type configIOHooks struct {
	open    func(string, int, os.FileMode) (configWritableFile, error)
	create  func(string, string) (configWritableFile, error)
	marshal func(any) ([]byte, error)
	remove  func(string) error
	rename  func(string, string) error
}

func installConfigIO(t *testing.T, hooks configIOHooks) {
	t.Helper()
	oldOpen, oldCreate, oldMarshal := openConfigFile, createConfigTemp, marshalConfig
	oldRemove, oldRename := removeConfigFile, renameConfigFile
	t.Cleanup(func() {
		openConfigFile, createConfigTemp, marshalConfig = oldOpen, oldCreate, oldMarshal
		removeConfigFile, renameConfigFile = oldRemove, oldRename
	})
	if hooks.open != nil {
		openConfigFile = hooks.open
	}
	if hooks.create != nil {
		createConfigTemp = hooks.create
	}
	if hooks.marshal != nil {
		marshalConfig = hooks.marshal
	}
	if hooks.remove != nil {
		removeConfigFile = hooks.remove
	}
	if hooks.rename != nil {
		renameConfigFile = hooks.rename
	}
}

func minimalConfig() Config {
	return Config{Version: 1, Contract: "standard-v1", Resources: map[string]Resource{}}
}
