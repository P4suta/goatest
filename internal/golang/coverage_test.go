// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package golang_test

import (
	"bufio"
	"reflect"
	"slices"
	"strings"
	"testing"

	gotest "github.com/P4suta/goatest/internal/golang"
)

func TestCoverageFilesKeepsReachedModuleRelativeFiles(t *testing.T) {
	profile := []byte(`mode: set
example.com/sample/a.go:3.10,5.2 2 1
example.com/sample/a.go:7.2,8.2 1 0
example.com/sample/sub/b.go:1.1,2.2 1 4
`)
	got, err := gotest.CoverageFiles(profile, "example.com/sample")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, []string{"a.go", "sub/b.go"}) {
		t.Errorf("files = %v", got)
	}
}

func TestCoverageFilesRejectsMalformedAndForeignProfiles(t *testing.T) {
	for _, profile := range [][]byte{
		[]byte("not a profile\n"),
		[]byte("mode: set\nother.example/a.go:1.1,2.2 1 1\n"),
	} {
		if _, err := gotest.CoverageFiles(profile, "example.com/sample"); err == nil {
			t.Errorf("profile %q was accepted", profile)
		}
	}
}

func TestCoverageFilesReportsExactMalformedLineDiagnostics(t *testing.T) {
	t.Parallel()
	const valid = "example.com/sample/a.go:1.1,2.2 1 1\n"
	for _, test := range []struct {
		name string
		line string
		want string
	}{
		{name: "fields", line: "broken\n", want: "goatest: malformed coverage line 3"},
		{name: "count", line: "example.com/sample/b.go:1.1,2.2 1 nope\n", want: "goatest: malformed coverage count on line 3"},
		{name: "location", line: "broken 1 1\n", want: "goatest: malformed coverage location on line 3"},
		{name: "span", line: "example.com/sample/b.go:1.1,2 1 1\n", want: "goatest: malformed coverage span on line 3"},
	} {
		t.Run(test.name, func(t *testing.T) {
			profile := []byte("mode: set\n" + valid + test.line)
			_, err := gotest.CoverageFiles(profile, "example.com/sample")
			if err == nil || err.Error() != test.want {
				t.Fatalf("CoverageFiles error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestCoverageFilesIgnoresZeroCountsAndCanonicalizesPaths(t *testing.T) {
	t.Parallel()
	profile := []byte("mode: atomic\n" +
		`example.com\sample\zero.go:1.1,2.2 1 0` + "\n" +
		`example.com\sample\z.go:1.1,2.2 1 2` + "\n" +
		`example.com\sample\a.go:1.1,2.2 1 1` + "\n" +
		`example.com\sample\z.go:3.1,4.2 1 3` + "\n")
	got, err := gotest.CoverageFiles(profile, "example.com/sample/")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, []string{"a.go", "z.go"}) {
		t.Fatalf("CoverageFiles = %v", got)
	}
}

func TestCoverageFilesRejectsColonAtFirstBoundaryAsForeign(t *testing.T) {
	t.Parallel()
	_, err := gotest.CoverageFiles([]byte("mode: set\nm:1.1,2.2 1 1\n"), "m")
	const want = `goatest: coverage path "m" is outside module "m"`
	if err == nil || err.Error() != want {
		t.Fatalf("CoverageFiles error = %v, want %q", err, want)
	}
}

func TestCoverageFilesReturnsScannerFailure(t *testing.T) {
	t.Parallel()
	profile := []byte("mode: set\n" + strings.Repeat("x", bufio.MaxScanTokenSize+1))
	_, err := gotest.CoverageFiles(profile, "example.com/sample")
	if err == nil || !strings.Contains(err.Error(), "token too long") {
		t.Fatalf("CoverageFiles error = %v, want scanner token failure", err)
	}
}

func TestParseCoverageSeparatesCoveredFromInstrumentedBlocks(t *testing.T) {
	t.Parallel()
	profile := []byte("mode: set\n" +
		"example.com/sample/a.go:3.10,5.2 2 1\n" +
		"example.com/sample/a.go:7.2,8.2 1 0\n" +
		"example.com/sample/sub/b.go:1.1,2.2 1 4\n")
	coverage, err := gotest.ParseCoverage(profile, "example.com/sample")
	if err != nil {
		t.Fatal(err)
	}
	wantCovered := []gotest.FileCoverage{
		{Path: "a.go", Blocks: []gotest.CoverageBlock{{StartLine: 3, StartColumn: 10, EndLine: 5, EndColumn: 2}}},
		{Path: "sub/b.go", Blocks: []gotest.CoverageBlock{{StartLine: 1, StartColumn: 1, EndLine: 2, EndColumn: 2}}},
	}
	if !reflect.DeepEqual(coverage.Covered, wantCovered) {
		t.Errorf("covered = %+v, want %+v", coverage.Covered, wantCovered)
	}
	wantInstrumented := []gotest.FileCoverage{
		{Path: "a.go", Blocks: []gotest.CoverageBlock{
			{StartLine: 3, StartColumn: 10, EndLine: 5, EndColumn: 2},
			{StartLine: 7, StartColumn: 2, EndLine: 8, EndColumn: 2},
		}},
		{Path: "sub/b.go", Blocks: []gotest.CoverageBlock{{StartLine: 1, StartColumn: 1, EndLine: 2, EndColumn: 2}}},
	}
	if !reflect.DeepEqual(coverage.Instrumented, wantInstrumented) {
		t.Errorf("instrumented = %+v, want %+v", coverage.Instrumented, wantInstrumented)
	}
}

func TestParseCoverageSortsAndDeduplicatesBlocks(t *testing.T) {
	t.Parallel()
	profile := []byte("mode: atomic\n" +
		`example.com\sample\z.go:9.2,9.10 1 1` + "\n" +
		"example.com/sample/a.go:5.29,6.16 1 1\n" +
		"example.com/sample/a.go:5.29,6.16 1 2\n" +
		"example.com/sample/a.go:5.29,5.40 1 1\n" +
		"example.com/sample/a.go:5.29,6.12 1 1\n" +
		"example.com/sample/a.go:5.3,6.16 1 1\n" +
		"example.com/sample/a.go:4.1,5.29 1 1\n")
	coverage, err := gotest.ParseCoverage(profile, "example.com/sample")
	if err != nil {
		t.Fatal(err)
	}
	want := []gotest.FileCoverage{
		{Path: "a.go", Blocks: []gotest.CoverageBlock{
			{StartLine: 4, StartColumn: 1, EndLine: 5, EndColumn: 29},
			{StartLine: 5, StartColumn: 3, EndLine: 6, EndColumn: 16},
			{StartLine: 5, StartColumn: 29, EndLine: 5, EndColumn: 40},
			{StartLine: 5, StartColumn: 29, EndLine: 6, EndColumn: 12},
			{StartLine: 5, StartColumn: 29, EndLine: 6, EndColumn: 16},
		}},
		{Path: "z.go", Blocks: []gotest.CoverageBlock{{StartLine: 9, StartColumn: 2, EndLine: 9, EndColumn: 10}}},
	}
	if !reflect.DeepEqual(coverage.Covered, want) {
		t.Fatalf("covered = %+v, want %+v", coverage.Covered, want)
	}
	if !reflect.DeepEqual(coverage.Instrumented, want) {
		t.Fatalf("instrumented = %+v, want %+v", coverage.Instrumented, want)
	}
}

func TestParseCoverageReturnsEmptyNonNilSlicesForAProfileWithOnlyAHeader(t *testing.T) {
	t.Parallel()
	coverage, err := gotest.ParseCoverage([]byte("mode: set\n"), "example.com/sample")
	if err != nil {
		t.Fatal(err)
	}
	if coverage.Covered == nil || len(coverage.Covered) != 0 {
		t.Errorf("covered = %#v, want an empty non-nil slice", coverage.Covered)
	}
	if coverage.Instrumented == nil || len(coverage.Instrumented) != 0 {
		t.Errorf("instrumented = %#v, want an empty non-nil slice", coverage.Instrumented)
	}
}

func TestParseCoverageReportsAMalformedSpan(t *testing.T) {
	t.Parallel()
	for _, span := range []string{"1.1,2", "a.b,2.2", "1.1;2.2", "1.1,2.2,3.3", "1.1,2.x"} {
		profile := []byte("mode: set\nexample.com/sample/a.go:" + span + " 1 1\n")
		coverage, err := gotest.ParseCoverage(profile, "example.com/sample")
		const want = "goatest: malformed coverage span on line 2"
		if err == nil || err.Error() != want {
			t.Errorf("ParseCoverage(%q) error = %v, want %q", span, err, want)
		}
		if coverage.Covered != nil || coverage.Instrumented != nil {
			t.Errorf("ParseCoverage(%q) = %+v, want no coverage", span, coverage)
		}
	}
}

func TestCoverageBlockContainsIsHalfOpen(t *testing.T) {
	t.Parallel()
	block := gotest.CoverageBlock{StartLine: 7, StartColumn: 5, EndLine: 9, EndColumn: 3}
	for _, test := range []struct {
		line, column int
		want         bool
	}{
		{line: 7, column: 5, want: true},
		{line: 7, column: 4},
		{line: 8, column: 1, want: true},
		{line: 9, column: 2, want: true},
		{line: 9, column: 3},
		{line: 6, column: 99},
		{line: 10, column: 1},
	} {
		if got := block.Contains(test.line, test.column); got != test.want {
			t.Errorf("%+v.Contains(%d, %d) = %t, want %t", block, test.line, test.column, got, test.want)
		}
	}
	single := gotest.CoverageBlock{StartLine: 4, StartColumn: 3, EndLine: 4, EndColumn: 8}
	if !single.Contains(4, 3) || single.Contains(4, 8) {
		t.Errorf("single line block %+v is not half open", single)
	}
}

func TestFileCoverageContainsFindsTheBlockAroundAPositionAndNotAGap(t *testing.T) {
	t.Parallel()
	file := gotest.FileCoverage{Path: "boundary.go", Blocks: []gotest.CoverageBlock{
		{StartLine: 5, StartColumn: 29, EndLine: 6, EndColumn: 16},
		{StartLine: 6, StartColumn: 16, EndLine: 8, EndColumn: 3},
		{StartLine: 9, StartColumn: 2, EndLine: 9, EndColumn: 10},
	}}
	for _, test := range []struct {
		line, column int
		want         bool
	}{
		{line: 5, column: 29, want: true},
		{line: 6, column: 16, want: true},
		{line: 8, column: 2, want: true},
		{line: 8, column: 3},
		{line: 9, column: 2, want: true},
		{line: 9, column: 10},
	} {
		if got := file.Contains(test.line, test.column); got != test.want {
			t.Errorf("Contains(%d, %d) = %t, want %t", test.line, test.column, got, test.want)
		}
	}
	var missing gotest.FileCoverage
	if missing.Contains(5, 29) {
		t.Error("a file with no blocks contains a position")
	}
}

func TestMergeFileCoverageUnionsFilesAndBlocksDeterministically(t *testing.T) {
	t.Parallel()
	first := []gotest.FileCoverage{
		{Path: "a.go", Blocks: []gotest.CoverageBlock{{StartLine: 1, StartColumn: 1, EndLine: 2, EndColumn: 2}}},
		{Path: "b.go", Blocks: []gotest.CoverageBlock{{StartLine: 3, StartColumn: 1, EndLine: 4, EndColumn: 2}}},
	}
	second := []gotest.FileCoverage{
		{Path: "a.go", Blocks: []gotest.CoverageBlock{
			{StartLine: 5, StartColumn: 1, EndLine: 6, EndColumn: 2},
			{StartLine: 1, StartColumn: 1, EndLine: 2, EndColumn: 2},
		}},
		{Path: "c.go", Blocks: []gotest.CoverageBlock{{StartLine: 7, StartColumn: 1, EndLine: 8, EndColumn: 2}}},
	}
	want := []gotest.FileCoverage{
		{Path: "a.go", Blocks: []gotest.CoverageBlock{
			{StartLine: 1, StartColumn: 1, EndLine: 2, EndColumn: 2},
			{StartLine: 5, StartColumn: 1, EndLine: 6, EndColumn: 2},
		}},
		{Path: "b.go", Blocks: []gotest.CoverageBlock{{StartLine: 3, StartColumn: 1, EndLine: 4, EndColumn: 2}}},
		{Path: "c.go", Blocks: []gotest.CoverageBlock{{StartLine: 7, StartColumn: 1, EndLine: 8, EndColumn: 2}}},
	}
	forward := gotest.MergeFileCoverage(first, second)
	if !reflect.DeepEqual(forward, want) {
		t.Fatalf("merge = %+v, want %+v", forward, want)
	}
	if backward := gotest.MergeFileCoverage(second, first); !reflect.DeepEqual(backward, forward) {
		t.Fatalf("reversed merge = %+v, want %+v", backward, forward)
	}
	if len(first[0].Blocks) != 1 || len(second[0].Blocks) != 2 {
		t.Fatalf("merge rewrote its inputs: %+v %+v", first, second)
	}
	if merged := gotest.MergeFileCoverage(nil, nil); merged == nil || len(merged) != 0 {
		t.Fatalf("merge of nothing = %#v, want an empty non-nil slice", merged)
	}
}

func TestFindFileCoverageNamesOnlyTheExactPath(t *testing.T) {
	t.Parallel()
	files := []gotest.FileCoverage{
		{Path: "a.go", Blocks: []gotest.CoverageBlock{{StartLine: 1, StartColumn: 1, EndLine: 2, EndColumn: 2}}},
		{Path: "sub/b.go", Blocks: []gotest.CoverageBlock{{StartLine: 3, StartColumn: 1, EndLine: 4, EndColumn: 2}}},
	}
	for _, path := range []string{"a.go", "sub/b.go"} {
		file, ok := gotest.FindFileCoverage(files, path)
		if !ok || file.Path != path {
			t.Errorf("FindFileCoverage(%q) = (%+v, %t)", path, file, ok)
		}
	}
	for _, path := range []string{"b.go", "sub/c.go", ""} {
		if file, ok := gotest.FindFileCoverage(files, path); ok || file.Path != "" || file.Blocks != nil {
			t.Errorf("FindFileCoverage(%q) = (%+v, %t), want no coverage", path, file, ok)
		}
	}
	if file, ok := gotest.FindFileCoverage(nil, "a.go"); ok || file.Path != "" {
		t.Errorf("FindFileCoverage of nothing = (%+v, %t)", file, ok)
	}
}

func TestCoverageFilesIsTheCoveredPathsOfParseCoverage(t *testing.T) {
	t.Parallel()
	profile := []byte("mode: set\n" +
		"example.com/sample/a.go:3.10,5.2 2 1\n" +
		"example.com/sample/only-instrumented.go:1.1,2.2 1 0\n" +
		"example.com/sample/sub/b.go:1.1,2.2 1 4\n")
	files, err := gotest.CoverageFiles(profile, "example.com/sample")
	if err != nil {
		t.Fatal(err)
	}
	coverage, err := gotest.ParseCoverage(profile, "example.com/sample")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(files, gotest.CoveredPaths(coverage.Covered)) {
		t.Fatalf("CoverageFiles = %v, covered paths = %v", files, gotest.CoveredPaths(coverage.Covered))
	}
	if !slices.Equal(files, []string{"a.go", "sub/b.go"}) {
		t.Fatalf("CoverageFiles = %v", files)
	}
	if paths := gotest.CoveredPaths(nil); paths == nil || len(paths) != 0 {
		t.Fatalf("CoveredPaths of nothing = %#v, want an empty non-nil slice", paths)
	}
}
