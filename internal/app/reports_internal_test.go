// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package app

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/P4suta/goatest/internal/report"
)

type stubReportFile struct {
	path     string
	writeErr error
	syncErr  error
	chmodErr error
	closeErr error
}

func (file *stubReportFile) Name() string { return file.path }
func (file *stubReportFile) Write(data []byte) (int, error) {
	if file.writeErr != nil {
		return 0, file.writeErr
	}
	return len(data), nil
}
func (file *stubReportFile) Sync() error             { return file.syncErr }
func (file *stubReportFile) Chmod(os.FileMode) error { return file.chmodErr }
func (file *stubReportFile) Close() error            { return file.closeErr }

func TestAtomicWritePropagatesEveryFilesystemStageAndRenameFallback(t *testing.T) {
	stageErr := errors.New("stage failed")
	fallbackErr := errors.New("fallback failed")
	for _, testCase := range []struct {
		name       string
		configure  func(*atomicWriteOperations, *stubReportFile)
		want       error
		wantRename int
	}{
		{name: "success", wantRename: 1},
		{name: "mkdir", configure: func(ops *atomicWriteOperations, _ *stubReportFile) {
			ops.mkdirAll = func(string, os.FileMode) error { return stageErr }
		}, want: stageErr},
		{name: "create", configure: func(ops *atomicWriteOperations, _ *stubReportFile) {
			ops.createTemp = func(string, string) (atomicReportFile, error) { return nil, stageErr }
		}, want: stageErr},
		{name: "write", configure: func(_ *atomicWriteOperations, file *stubReportFile) { file.writeErr = stageErr }, want: stageErr},
		{name: "sync", configure: func(_ *atomicWriteOperations, file *stubReportFile) { file.syncErr = stageErr }, want: stageErr},
		{name: "chmod", configure: func(_ *atomicWriteOperations, file *stubReportFile) { file.chmodErr = stageErr }, want: stageErr},
		{name: "close", configure: func(_ *atomicWriteOperations, file *stubReportFile) { file.closeErr = stageErr }, want: stageErr},
		{name: "remove-fails", configure: func(ops *atomicWriteOperations, _ *stubReportFile) {
			ops.rename = sequenceErrors(stageErr)
			ops.remove = func(string) error { return fallbackErr }
		}, want: stageErr, wantRename: 1},
		{name: "missing-destination", configure: func(ops *atomicWriteOperations, _ *stubReportFile) {
			ops.rename = sequenceErrors(stageErr, nil)
			ops.remove = func(string) error { return os.ErrNotExist }
		}, wantRename: 2},
		{name: "second-rename-fails", configure: func(ops *atomicWriteOperations, _ *stubReportFile) {
			ops.rename = sequenceErrors(stageErr, fallbackErr)
		}, want: fallbackErr, wantRename: 2},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			file := &stubReportFile{path: filepath.Join(t.TempDir(), "temporary")}
			renameCalls := 0
			ops := atomicWriteOperations{
				mkdirAll:   func(string, os.FileMode) error { return nil },
				createTemp: func(string, string) (atomicReportFile, error) { return file, nil },
				remove:     func(string) error { return nil },
				rename:     func(string, string) error { renameCalls++; return nil },
			}
			if testCase.configure != nil {
				testCase.configure(&ops, file)
				wrappedRename := ops.rename
				renameCalls = 0
				ops.rename = func(oldPath, newPath string) error {
					renameCalls++
					return wrappedRename(oldPath, newPath)
				}
			}
			err := atomicWriteWith(filepath.Join(t.TempDir(), "report.json"), []byte("report"), ops)
			if testCase.want == nil && err != nil {
				t.Fatalf("atomic write error = %v", err)
			}
			if testCase.want != nil && !errors.Is(err, testCase.want) {
				t.Fatalf("atomic write error = %v, want %v", err, testCase.want)
			}
			if renameCalls != testCase.wantRename {
				t.Fatalf("rename calls = %d, want %d", renameCalls, testCase.wantRename)
			}
		})
	}
}

func TestWriteReportsNamesTheArtifactWhoseAtomicWriteFailed(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".goatest"), []byte("blocks directory"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := WriteReports(root, validReportFixture())
	if err == nil || !strings.Contains(err.Error(), "write report") || !strings.Contains(err.Error(), ".goatest") {
		t.Fatalf("WriteReports error = %v", err)
	}
}

func sequenceErrors(errors ...error) func(string, string) error {
	index := 0
	return func(string, string) error {
		if index >= len(errors) {
			return nil
		}
		err := errors[index]
		index++
		return err
	}
}

func validReportFixture() report.Report {
	return report.Report{Schema: report.SchemaV1, Verdict: report.VerdictAssured}
}
