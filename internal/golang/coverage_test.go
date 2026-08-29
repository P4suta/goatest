// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package golang_test

import (
	"bufio"
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
