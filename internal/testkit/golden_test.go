// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package testkit_test

import (
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/P4suta/goatest/internal/report"
	"github.com/P4suta/goatest/internal/testkit"
)

const goldenSampleName = "golden_sample.txt"

var goldenSampleContents = []byte("sample golden fixture\n")

func TestGoldenPathResolvesUnderTestdata(t *testing.T) {
	t.Parallel()
	if got, want := testkit.GoldenPath(goldenSampleName), filepath.Join("testdata", goldenSampleName); got != want {
		t.Fatalf("GoldenPath = %q, want %q", got, want)
	}
}

func TestUpdateReportsTheRegisteredFlag(t *testing.T) {
	t.Parallel()
	// testkit registers -update exactly once for every test binary that links
	// it; a duplicate registration would panic before this test could run.
	registered := flag.Lookup("update")
	if registered == nil {
		t.Fatal("testkit does not register the -update flag")
	}
	if got, want := testkit.Update(), registered.Value.String() == "true"; got != want {
		t.Fatalf("Update = %t, want %t", got, want)
	}
}

func TestGoldenAcceptsMatchingBytesAndReportsMismatches(t *testing.T) {
	t.Parallel()
	if testkit.Update() {
		t.Skip("-update rewrites the sample fixture this test asserts on")
	}

	matching := recordFailures(t, func(recorder testing.TB) {
		testkit.Golden(recorder, goldenSampleName, goldenSampleContents)
	})
	if len(matching.errors) != 0 || len(matching.fatals) != 0 {
		t.Fatalf("matching bytes failed the test: %q %q", matching.errors, matching.fatals)
	}

	mismatching := recordFailures(t, func(recorder testing.TB) {
		testkit.Golden(recorder, goldenSampleName, []byte("different bytes\n"))
	})
	failures := append(append([]string{}, mismatching.errors...), mismatching.fatals...)
	if len(failures) != 1 {
		t.Fatalf("mismatching bytes reported %d failures, want one: %q", len(failures), failures)
	}
	if !strings.Contains(failures[0], testkit.GoldenPath(goldenSampleName)) {
		t.Errorf("failure %q does not name the golden file", failures[0])
	}

	missing := recordFailures(t, func(recorder testing.TB) {
		testkit.Golden(recorder, "absent_golden.txt", goldenSampleContents)
	})
	if len(missing.errors)+len(missing.fatals) == 0 {
		t.Error("a missing golden file was accepted")
	}
}

func TestCompareGoldenMatchesAndReportsMismatch(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "report.json")
	if err := os.WriteFile(path, goldenSampleContents, 0o644); err != nil {
		t.Fatal(err)
	}

	if err := testkit.CompareGolden(path, goldenSampleContents, false); err != nil {
		t.Fatalf("matching bytes: %v", err)
	}
	err := testkit.CompareGolden(path, []byte("different bytes\n"), false)
	if !errors.Is(err, testkit.ErrGoldenMismatch) {
		t.Fatalf("err = %v, want ErrGoldenMismatch", err)
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("error %q does not name the golden file", err)
	}
	stored, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(stored) != string(goldenSampleContents) {
		t.Errorf("a comparison rewrote the golden file: %q", stored)
	}
}

func TestCompareGoldenFailsClosedWhenTheGoldenFileIsMissing(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "absent.json")

	err := testkit.CompareGolden(path, goldenSampleContents, false)
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("err = %v, want a not-exist error", err)
	}
	if _, statErr := os.Stat(path); !errors.Is(statErr, fs.ErrNotExist) {
		t.Errorf("a comparison created the golden file: %v", statErr)
	}
}

func TestCompareGoldenRewritesTheFileWhenUpdating(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	created := filepath.Join(root, "nested", "created.json")
	if err := testkit.CompareGolden(created, goldenSampleContents, true); err != nil {
		t.Fatalf("creating a golden file: %v", err)
	}
	stored, err := os.ReadFile(created)
	if err != nil {
		t.Fatal(err)
	}
	if string(stored) != string(goldenSampleContents) {
		t.Fatalf("created golden = %q, want %q", stored, goldenSampleContents)
	}

	replacement := []byte("rewritten bytes\n")
	if err := testkit.CompareGolden(created, replacement, true); err != nil {
		t.Fatalf("rewriting a golden file: %v", err)
	}
	if stored, err = os.ReadFile(created); err != nil {
		t.Fatal(err)
	}
	if string(stored) != string(replacement) {
		t.Fatalf("rewritten golden = %q, want %q", stored, replacement)
	}
	if err := testkit.CompareGolden(created, replacement, true); err != nil {
		t.Fatalf("updating an already matching golden file: %v", err)
	}
}

func reportFixture() report.Report {
	return report.Report{
		Schema:   report.SchemaV1,
		RunID:    "9f2c1d4a5b6e7f80",
		RunKind:  report.RunFull,
		Verdict:  report.VerdictAssured,
		Contract: "standard-v1",
		Snapshot: "sha256:2f0d9c",
		Repository: report.Repository{
			Module:   "fixture.example/assured",
			Packages: []string{"fixture.example/assured"},
			Git: report.Git{
				Available: true, Commit: "6f1b2c3d4e5f", Dirty: true,
				MergeBase: "aa11bb22cc33", ChangedFiles: []string{"boundary.go"},
			},
		},
		Configuration: report.Configuration{Digest: "sha256:configuration"},
		Toolchain: report.Toolchain{
			Go: "go1.26.0", Goatest: "v0.1.0-dev", GoMutants: "v0.1.2", OS: "linux", Arch: "amd64",
		},
		Timing:   report.Timing{StartedAt: "2026-08-30T10:00:00Z", FinishedAt: "2026-08-30T10:01:00Z", DurationMS: 60_000},
		Findings: []report.Finding{{ID: "finding-one", Kind: "mutant-survived", Path: "boundary.go", Line: 4, Summary: "survived"}},
		Mutants:  []report.MutantDisposition{{ID: "mutant-one", Status: report.MutantSurvived, Path: "boundary.go", Line: 4}},
	}
}

func TestNormalizeReportFixesIdentityFieldsAndPreservesEverythingElse(t *testing.T) {
	t.Parallel()
	input := reportFixture()
	normalized := testkit.NormalizeReport(input)

	if normalized.RunID != testkit.NormalizedRunID {
		t.Errorf("RunID = %q, want %q", normalized.RunID, testkit.NormalizedRunID)
	}
	if normalized.Snapshot != testkit.NormalizedSnapshot {
		t.Errorf("Snapshot = %q, want %q", normalized.Snapshot, testkit.NormalizedSnapshot)
	}
	wantTiming := report.Timing{StartedAt: testkit.NormalizedTimestamp, FinishedAt: testkit.NormalizedTimestamp}
	if normalized.Timing != wantTiming {
		t.Errorf("Timing = %+v, want %+v", normalized.Timing, wantTiming)
	}
	if normalized.Toolchain.Go != testkit.NormalizedGoVersion {
		t.Errorf("Toolchain.Go = %q, want %q", normalized.Toolchain.Go, testkit.NormalizedGoVersion)
	}
	wantGit := report.Git{
		Available: true, Commit: testkit.NormalizedCommit, Dirty: true,
		MergeBase: testkit.NormalizedMergeBase, ChangedFiles: []string{"boundary.go"},
	}
	if !reflect.DeepEqual(normalized.Repository.Git, wantGit) {
		t.Errorf("Git = %+v, want %+v", normalized.Repository.Git, wantGit)
	}

	preserved := reportFixture()
	preserved.RunID, preserved.Snapshot = testkit.NormalizedRunID, testkit.NormalizedSnapshot
	preserved.Timing = wantTiming
	preserved.Toolchain.Go = testkit.NormalizedGoVersion
	preserved.Repository.Git = wantGit
	if !reflect.DeepEqual(normalized, preserved) {
		t.Errorf("NormalizeReport changed unrelated fields:\n got %+v\nwant %+v", normalized, preserved)
	}
	if again := testkit.NormalizeReport(normalized); !reflect.DeepEqual(again, normalized) {
		t.Error("NormalizeReport is not idempotent")
	}
}

func TestNormalizeReportKeepsEmptyIdentityFieldsEmptyAndDoesNotMutateItsInput(t *testing.T) {
	t.Parallel()
	empty := testkit.NormalizeReport(report.Report{})
	if empty.RunID != "" || empty.Snapshot != "" || empty.Toolchain.Go != "" {
		t.Errorf("empty identity fields were filled in: %+v", empty)
	}
	if empty.Repository.Git.Commit != "" || empty.Repository.Git.MergeBase != "" {
		t.Errorf("empty git identity was filled in: %+v", empty.Repository.Git)
	}
	if empty.Timing != (report.Timing{}) {
		t.Errorf("empty timing was filled in: %+v", empty.Timing)
	}

	input := reportFixture()
	normalized := testkit.NormalizeReport(input)
	normalized.Findings[0].ID = "mutated"
	normalized.Mutants[0].ID = "mutated"
	normalized.Repository.Git.ChangedFiles[0] = "mutated"
	if !reflect.DeepEqual(input, reportFixture()) {
		t.Errorf("NormalizeReport mutated its input: %+v", input)
	}
}

// recordingTB captures the failures a testkit helper reports without failing
// the enclosing test. Embedding testing.TB satisfies the interface while
// leaving every method the helpers must not call unimplemented.
type recordingTB struct {
	testing.TB
	errors []string
	fatals []string
}

var errRecordedTestStopped = errors.New("testkit test: recorded fatal failure")

func (recorder *recordingTB) Helper() {}

func (recorder *recordingTB) Name() string { return "recorded" }

func (recorder *recordingTB) Log(arguments ...any) {}

func (recorder *recordingTB) Logf(format string, arguments ...any) {}

func (recorder *recordingTB) Error(arguments ...any) {
	recorder.errors = append(recorder.errors, fmt.Sprint(arguments...))
}

func (recorder *recordingTB) Errorf(format string, arguments ...any) {
	recorder.errors = append(recorder.errors, fmt.Sprintf(format, arguments...))
}

func (recorder *recordingTB) Fatal(arguments ...any) {
	recorder.fatals = append(recorder.fatals, fmt.Sprint(arguments...))
	panic(errRecordedTestStopped)
}

func (recorder *recordingTB) Fatalf(format string, arguments ...any) {
	recorder.fatals = append(recorder.fatals, fmt.Sprintf(format, arguments...))
	panic(errRecordedTestStopped)
}

// recordFailures runs call against a recording testing.TB, translating a fatal
// report into the early return that testing.T would perform.
func recordFailures(t *testing.T, call func(testing.TB)) (recorder *recordingTB) {
	t.Helper()
	recorder = &recordingTB{}
	defer func() {
		if recovered := recover(); recovered != nil && recovered != errRecordedTestStopped {
			panic(recovered)
		}
	}()
	call(recorder)
	return recorder
}
