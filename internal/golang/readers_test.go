// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package golang_test

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/P4suta/goatest/internal/filemode"
	gotest "github.com/P4suta/goatest/internal/golang"
)

func TestRepositoryReadCandidatesNameEveryPackageThatReadsAPathItComputes(t *testing.T) {
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

import "strings"

type dir struct{}

func (dir) ReadDir(string) error { return nil }

func Local(name string) error {
	var os dir
	return os.ReadDir(strings.TrimSpace(name))
}
`)
	packages := []gotest.Package{
		{ImportPath: "example.com/module/listing", RelativeDir: "listing"},
		{ImportPath: "example.com/module/walking", RelativeDir: "walking"},
		{ImportPath: "example.com/module/aliased", RelativeDir: "aliased"},
		{ImportPath: "example.com/module/named", RelativeDir: "named"},
	}

	candidates := gotest.RepositoryReadCandidates(root, packages)
	got := slices.Sorted(candidatesOf(candidates))
	want := []string{
		"example.com/module/aliased", "example.com/module/listing", "example.com/module/walking",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("repository readers = %v, want %v", got, want)
	}
}

func TestRepositoryReadCandidatesAnswerConservativelyForAPackageItCannotRead(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeGo(t, root, "broken/broken.go", "package broken\n\nfunc Broken( {}\n")
	packages := []gotest.Package{
		{ImportPath: "example.com/module/broken", RelativeDir: "broken"},
		{ImportPath: "example.com/module/absent", RelativeDir: "absent"},
	}

	candidates := gotest.RepositoryReadCandidates(root, packages)
	got := slices.Sorted(candidatesOf(candidates))
	want := []string{"example.com/module/absent", "example.com/module/broken"}
	if !slices.Equal(got, want) {
		t.Fatalf("repository readers = %v, want %v", got, want)
	}
}

func TestRepositoryReadCandidatesReadTheDirectoryAndNothingUnderIt(t *testing.T) {
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
	if err := os.WriteFile(filepath.Join(root, "quiet", "notes.txt"), []byte("not go\n"), filemode.ReadableFile); err != nil {
		t.Fatal(err)
	}
	packages := []gotest.Package{
		{ImportPath: "example.com/module/quiet", RelativeDir: "quiet"},
		{ImportPath: "example.com/module/quiet/loud", RelativeDir: "quiet/loud"},
	}

	candidates := gotest.RepositoryReadCandidates(root, packages)
	got := slices.Sorted(candidatesOf(candidates))
	want := []string{"example.com/module/quiet/loud"}
	if !slices.Equal(got, want) {
		t.Fatalf("repository readers = %v, want %v", got, want)
	}
}

func TestRepositoryReadCandidatesKeepPreRunAndGenericFSReadsConservative(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeGo(t, root, "observed/observed_test.go", `package observed

import "os"

func read(path string) {
	readDirectory := os.ReadDir
	_, _ = readDirectory(path)
}
`)
	writeGo(t, root, "initializer/initializer.go", `package initializer

import "os"

var readDirectory = os.ReadDir
`)
	writeGo(t, root, "initial/initial.go", `package initial

import "os"

func init() { _, _ = os.ReadDir(".") }
`)
	writeGo(t, root, "packagevalue/packagevalue.go", `package packagevalue

import "os"

var entries, readError = os.ReadDir(".")
`)
	writeGo(t, root, "main/main_test.go", `package main

import (
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	_, _ = os.ReadDir(".")
	os.Exit(m.Run())
}
`)
	writeGo(t, root, "generic/generic.go", `package generic

import "io/fs"

func read(fileSystem fs.FS) { _, _ = fs.ReadDir(fileSystem, ".") }
`)
	packages := []gotest.Package{
		{ImportPath: "example.com/module/observed", RelativeDir: "observed"},
		{ImportPath: "example.com/module/initializer", RelativeDir: "initializer"},
		{ImportPath: "example.com/module/initial", RelativeDir: "initial"},
		{ImportPath: "example.com/module/packagevalue", RelativeDir: "packagevalue"},
		{ImportPath: "example.com/module/main", RelativeDir: "main"},
		{ImportPath: "example.com/module/generic", RelativeDir: "generic"},
	}
	candidates := gotest.RepositoryReadCandidates(root, packages)
	for _, path := range []string{"example.com/module/observed", "example.com/module/initializer"} {
		candidate, found := candidates[path]
		if !found || candidate.Unobservable {
			t.Errorf("observable candidate %s = %+v, %t", path, candidate, found)
		}
	}
	for _, path := range []string{"example.com/module/initial", "example.com/module/packagevalue", "example.com/module/main", "example.com/module/generic"} {
		candidate, found := candidates[path]
		if !found || !candidate.Unobservable {
			t.Errorf("candidate %s = %+v, %t; want conservative", path, candidate, found)
		}
	}
}

func TestRepositoryReadCandidatesFollowOnlyProductionDependencies(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeGo(t, root, "production/production.go", `package production

import "os"

func Entries() { _, _ = os.ReadDir(".") }
`)
	writeGo(t, root, "testonly/testonly_test.go", `package testonly

import (
	"os"
	"testing"
)

func TestEntries(t *testing.T) { _, _ = os.ReadDir(".") }
`)
	writeGo(t, root, "initial/initial.go", `package initial

import "os"

func init() { _, _ = os.ReadDir(".") }
`)
	writeGo(t, root, "consumer/consumer_test.go", "package consumer\n")
	writeGo(t, root, "quiet/quiet_test.go", "package quiet\n")
	writeGo(t, root, "preinit/preinit_test.go", "package preinit\n")
	packages := []gotest.Package{
		{ImportPath: "example.com/module/production", RelativeDir: "production"},
		{ImportPath: "example.com/module/testonly", RelativeDir: "testonly"},
		{ImportPath: "example.com/module/initial", RelativeDir: "initial"},
		{
			ImportPath: "example.com/module/consumer", RelativeDir: "consumer",
			Dependencies: []string{"example.com/module/production"},
		},
		{
			ImportPath: "example.com/module/quiet", RelativeDir: "quiet",
			Dependencies: []string{"example.com/module/testonly"},
		},
		{
			ImportPath: "example.com/module/preinit", RelativeDir: "preinit",
			Dependencies: []string{"example.com/module/initial"},
		},
	}
	candidates := gotest.RepositoryReadCandidates(root, packages)
	if candidate, found := candidates["example.com/module/consumer"]; !found || candidate.Unobservable {
		t.Fatalf("production reader dependency = (%+v, %t)", candidate, found)
	}
	if _, found := candidates["example.com/module/quiet"]; found {
		t.Fatal("dependency test source leaked into a consumer test binary")
	}
	if candidate, found := candidates["example.com/module/preinit"]; !found || !candidate.Unobservable {
		t.Fatalf("production initializer dependency = (%+v, %t)", candidate, found)
	}
}

func TestRepositoryReadCandidatesCoverLoggedAndUnloggedPathOperations(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeGo(t, root, "logged/logged_test.go", `package logged

import (
	"os"
	"path/filepath"
)

func TestPaths() {
	_, _ = os.Open("input")
	_, _ = os.OpenFile("input", os.O_RDONLY, 0)
	_, _ = os.ReadFile("input")
	_, _ = os.ReadDir(".")
	_, _ = os.Stat("input")
	_, _ = os.Lstat("input")
	_ = os.Chdir(".")
	_, _ = os.Create("output")
	_ = os.WriteFile("output", nil, 0)
	_ = os.DirFS(".")
	_, _ = os.OpenRoot(".")
	_, _ = os.OpenInRoot(".", "input")
	_, _ = filepath.Glob("*")
	_, _ = filepath.EvalSymlinks("input")
}
`)
	writeGo(t, root, "unlogged/unlogged_test.go", `package unlogged

import (
	"io/fs"
	"os"
)

func TestPaths(fileSystem fs.FS) {
	_, _ = os.Readlink("input")
	_, _ = fs.ReadFile(fileSystem, "input")
}
`)
	packages := []gotest.Package{
		{ImportPath: "example.com/module/logged", RelativeDir: "logged"},
		{ImportPath: "example.com/module/unlogged", RelativeDir: "unlogged"},
	}
	candidates := gotest.RepositoryReadCandidates(root, packages)
	if candidate, found := candidates["example.com/module/logged"]; !found || candidate.Unobservable {
		t.Fatalf("logged path operations = (%+v, %t)", candidate, found)
	}
	if candidate, found := candidates["example.com/module/unlogged"]; !found || !candidate.Unobservable {
		t.Fatalf("unlogged path operations = (%+v, %t)", candidate, found)
	}
}

func TestRepositoryReadCandidatesFollowPreRunHelpersAcrossFiles(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeGo(t, root, "main/main_test.go", `package main

import "testing"

func TestMain(m *testing.M) {
	beforeTests()
	_ = m.Run()
	afterTests()
}
`)
	writeGo(t, root, "main/helpers_test.go", `package main

import "os"

func beforeTests() { readRepository() }
func afterTests() { readRepository() }
func readRepository() { _, _ = os.ReadFile("input") }
`)
	writeGo(t, root, "initial/initial.go", `package initial

func init() { prepare() }
`)
	writeGo(t, root, "initial/helper.go", `package initial

import "os"

func prepare() { _, _ = os.Stat("input") }
`)
	writeGo(t, root, "ordinary/ordinary_test.go", `package ordinary

import "testing"

func TestRead(t *testing.T) { readRepository() }
`)
	writeGo(t, root, "ordinary/helper_test.go", `package ordinary

import "os"

func readRepository() { _, _ = os.ReadFile("input") }
`)
	packages := []gotest.Package{
		{ImportPath: "example.com/module/main", RelativeDir: "main"},
		{ImportPath: "example.com/module/initial", RelativeDir: "initial"},
		{ImportPath: "example.com/module/ordinary", RelativeDir: "ordinary"},
	}
	candidates := gotest.RepositoryReadCandidates(root, packages)
	for _, path := range []string{"example.com/module/main", "example.com/module/initial"} {
		candidate, found := candidates[path]
		if !found || !candidate.Unobservable {
			t.Errorf("pre-run helper %s = (%+v, %t)", path, candidate, found)
		}
	}
	if candidate, found := candidates["example.com/module/ordinary"]; !found || candidate.Unobservable {
		t.Fatalf("ordinary helper = (%+v, %t)", candidate, found)
	}
}

func TestRepositoryReadCandidatesFollowFunctionAliasesFromTestMain(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeGo(t, root, "alias/alias_test.go", `package alias

import (
	"os"
	"testing"
)

var read = readRepository

func readRepository() { _, _ = os.ReadFile("input") }

func TestMain(m *testing.M) {
	local := read
	local()
	_ = m.Run()
}
`)
	candidates := gotest.RepositoryReadCandidates(root, []gotest.Package{{ImportPath: "example.com/module/alias", RelativeDir: "alias"}})
	if candidate, found := candidates["example.com/module/alias"]; !found || !candidate.Unobservable {
		t.Fatalf("aliased pre-run helper = (%+v, %t)", candidate, found)
	}
}

func TestRepositoryReadCandidatesKeepPreRunDependencyReadersConservative(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeGo(t, root, "reader/reader.go", `package reader

import "os"

func Read() ([]byte, error) { return os.ReadFile("input") }
`)
	writeGo(t, root, "bridge/bridge.go", `package bridge

import "example.com/module/reader"

func Load() ([]byte, error) { return reader.Read() }
`)
	writeGo(t, root, "main/main_test.go", `package main

import (
	"example.com/module/bridge"
	"testing"
)

func TestMain(m *testing.M) {
	_, _ = bridge.Load()
	_ = m.Run()
}
`)
	writeGo(t, root, "initfn/initfn.go", `package initfn

import "example.com/module/bridge"

func init() { _, _ = bridge.Load() }
`)
	writeGo(t, root, "initvar/initvar.go", `package initvar

import "example.com/module/bridge"

var loaded, loadErr = bridge.Load()
`)
	writeGo(t, root, "ordinary/ordinary_test.go", `package ordinary

import (
	"example.com/module/bridge"
	"testing"
)

func TestLoad(t *testing.T) { _, _ = bridge.Load() }
`)
	reader := "example.com/module/reader"
	bridge := "example.com/module/bridge"
	packages := []gotest.Package{
		{ImportPath: reader, RelativeDir: "reader"},
		{ImportPath: bridge, RelativeDir: "bridge", Dependencies: []string{reader}},
		{ImportPath: "example.com/module/main", RelativeDir: "main", Dependencies: []string{bridge, reader}},
		{ImportPath: "example.com/module/initfn", RelativeDir: "initfn", Dependencies: []string{bridge, reader}},
		{ImportPath: "example.com/module/initvar", RelativeDir: "initvar", Dependencies: []string{bridge, reader}},
		{ImportPath: "example.com/module/ordinary", RelativeDir: "ordinary", Dependencies: []string{bridge, reader}},
	}
	candidates := gotest.RepositoryReadCandidates(root, packages)
	for _, path := range []string{"example.com/module/main", "example.com/module/initfn", "example.com/module/initvar"} {
		if candidate, found := candidates[path]; !found || !candidate.Unobservable {
			t.Errorf("pre-run dependency reader %s = (%+v, %t)", path, candidate, found)
		}
	}
	if candidate, found := candidates["example.com/module/ordinary"]; !found || candidate.Unobservable {
		t.Fatalf("ordinary dependency reader = (%+v, %t)", candidate, found)
	}
}

func candidatesOf(candidates map[string]gotest.RepositoryReadCandidate) func(func(string) bool) {
	return func(yield func(string) bool) {
		for path := range candidates {
			if !yield(path) {
				return
			}
		}
	}
}

func TestRepositoryReadCandidatesWidenEveryPathTheActionLogCannotSee(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeGo(t, root, "rawsyscall/rawsyscall.go", `package rawsyscall

import "syscall"

func Read(name string) error {
	var info syscall.Stat_t
	return syscall.Stat(name, &info)
}
`)
	writeGo(t, root, "extended/extended.go", `package extended

import "golang.org/x/sys/unix"

func Read(name string) error {
	var info unix.Stat_t
	return unix.Stat(name, &info)
}
`)
	writeGo(t, root, "child/child.go", `package child

import "os/exec"

func Run(name string) error {
	return exec.Command("cat", name).Run()
}
`)
	writeGo(t, root, "loaded/loaded.go", `package loaded

import "plugin"

func Load(name string) error {
	_, err := plugin.Open(name)
	return err
}
`)
	writeGo(t, root, "native/native.go", `package native

import "C"

func Native() {}
`)
	writeGo(t, root, "booted/booted.go", `package booted

import "os/exec"

func init() { _ = exec.Command("true").Run() }
`)
	writeGo(t, root, "consumer/consumer.go", `package consumer

func Consume() int { return 1 }
`)
	writeGo(t, root, "ordinary/ordinary.go", `package ordinary

import "os"

func Read(name string) ([]byte, error) { return os.ReadFile(name) }
`)
	packages := []gotest.Package{
		{ImportPath: "example.com/module/rawsyscall", RelativeDir: "rawsyscall"},
		{ImportPath: "example.com/module/extended", RelativeDir: "extended"},
		{ImportPath: "example.com/module/child", RelativeDir: "child"},
		{ImportPath: "example.com/module/loaded", RelativeDir: "loaded"},
		{ImportPath: "example.com/module/native", RelativeDir: "native"},
		{ImportPath: "example.com/module/booted", RelativeDir: "booted"},
		{
			ImportPath: "example.com/module/consumer", RelativeDir: "consumer",
			Dependencies: []string{"example.com/module/child"},
		},
		{ImportPath: "example.com/module/ordinary", RelativeDir: "ordinary"},
	}

	candidates := gotest.RepositoryReadCandidates(root, packages)
	for _, path := range []string{
		"example.com/module/rawsyscall", "example.com/module/extended",
		"example.com/module/child", "example.com/module/loaded",
		"example.com/module/native", "example.com/module/booted",
		"example.com/module/consumer",
	} {
		candidate, found := candidates[path]
		if !found || !candidate.Unobservable {
			t.Errorf("unobservable reader %s = (%+v, %t)", path, candidate, found)
		}
	}
	if candidate, found := candidates["example.com/module/ordinary"]; !found || candidate.Unobservable {
		t.Fatalf("observable reader = (%+v, %t)", candidate, found)
	}
}
