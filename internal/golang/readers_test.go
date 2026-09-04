// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package golang_test

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	gotest "github.com/P4suta/goatest/internal/golang"
)

// TestRepositoryReadersNamesEveryPackageThatReadsAPathItComputes pins the
// detection from both sides. A package that walks, globs, or lists a directory
// can change its verdict when a file no key of its own describes changes, and
// a package that only opens files it names cannot.
func TestRepositoryReadersNamesEveryPackageThatReadsAPathItComputes(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeGo(t, root, "listing/listing_test.go", `package listing

import (
	"os"
	"testing"
)

func TestListing(t *testing.T) {
	if _, err := os.ReadDir("."); err != nil {
		t.Fatal(err)
	}
}
`)
	writeGo(t, root, "walking/walking.go", `package walking

import "path/filepath"

func Walk(root string) error {
	return filepath.WalkDir(root, nil)
}
`)
	writeGo(t, root, "aliased/aliased_test.go", `package aliased

import (
	stdfs "io/fs"
	"testing"
)

func TestAliased(t *testing.T) {
	if _, err := stdfs.Glob(nil, "*"); err != nil {
		t.Fatal(err)
	}
}
`)
	writeGo(t, root, "named/named.go", `package named

import "os"

type dir struct{}

// ReadDir is this package's own method, and a selector that only looks like the
// call the rule names.
func (dir) ReadDir(string) error { return nil }

func Open(name string) error {
	handle, err := os.Open(name)
	if err != nil {
		return err
	}
	return handle.Close()
}

func Local() error {
	var os dir
	return os.ReadDir(".")
}
`)
	packages := []gotest.Package{
		{ImportPath: "example.com/module/listing", RelativeDir: "listing"},
		{ImportPath: "example.com/module/walking", RelativeDir: "walking"},
		{ImportPath: "example.com/module/aliased", RelativeDir: "aliased"},
		{ImportPath: "example.com/module/named", RelativeDir: "named"},
	}

	readers := gotest.RepositoryReaders(root, packages)
	got := slices.Sorted(readersOf(readers))
	want := []string{
		"example.com/module/aliased", "example.com/module/listing", "example.com/module/walking",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("repository readers = %v, want %v", got, want)
	}
}

// TestRepositoryReadersAnswersConservativelyForAPackageItCannotRead pins the
// direction a failure has to fall in. A directory that cannot be listed and a
// file that cannot be parsed both leave the question unanswered, and an
// unanswered question about what a test reads is answered with the whole tree
// rather than with nothing.
func TestRepositoryReadersAnswersConservativelyForAPackageItCannotRead(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeGo(t, root, "broken/broken.go", "package broken\n\nfunc Broken( {}\n")
	packages := []gotest.Package{
		{ImportPath: "example.com/module/broken", RelativeDir: "broken"},
		{ImportPath: "example.com/module/absent", RelativeDir: "absent"},
	}

	readers := gotest.RepositoryReaders(root, packages)
	got := slices.Sorted(readersOf(readers))
	want := []string{"example.com/module/absent", "example.com/module/broken"}
	if !slices.Equal(got, want) {
		t.Fatalf("repository readers = %v, want %v", got, want)
	}
}

// TestRepositoryReadersReadsTheDirectoryAndNothingUnderIt pins the unit the
// answer is about: a package is its own directory, and a package below it is a
// package of its own with a question of its own.
func TestRepositoryReadersReadsTheDirectoryAndNothingUnderIt(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeGo(t, root, "quiet/quiet.go", "package quiet\n\nfunc Quiet() int { return 1 }\n")
	writeGo(t, root, "quiet/loud/loud.go", `package loud

import "os"

func Loud() ([]string, error) {
	entries, err := os.ReadDir(".")
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names, nil
}
`)
	if err := os.WriteFile(filepath.Join(root, "quiet", "notes.txt"), []byte("not go\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	packages := []gotest.Package{
		{ImportPath: "example.com/module/quiet", RelativeDir: "quiet"},
		{ImportPath: "example.com/module/quiet/loud", RelativeDir: "quiet/loud"},
	}

	readers := gotest.RepositoryReaders(root, packages)
	got := slices.Sorted(readersOf(readers))
	want := []string{"example.com/module/quiet/loud"}
	if !slices.Equal(got, want) {
		t.Fatalf("repository readers = %v, want %v", got, want)
	}
}

// readersOf yields the import paths the answer marked, so a test asserts on
// the packages rather than on the shape of the map they arrive in.
func readersOf(readers map[string]bool) func(func(string) bool) {
	return func(yield func(string) bool) {
		for path, reader := range readers {
			if !reader {
				continue
			}
			if !yield(path) {
				return
			}
		}
	}
}
