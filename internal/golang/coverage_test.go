// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package golang_test

import (
	"slices"
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
