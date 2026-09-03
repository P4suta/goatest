// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/P4suta/goatest/internal/buildcache"
)

// The variables the helper process is told where to serve from. The cache
// program takes its layers on the command line in production, but the go
// command starts it with an argument list this test cannot extend: the
// program here is the test binary, and everything after it on that line has to
// remain the test flag that selects the helper.
const (
	cacheProgramHelper  = "GOATEST_CACHEPROG_HELPER"
	cacheProgramBase    = "GOATEST_CACHEPROG_BASE"
	cacheProgramScratch = "GOATEST_CACHEPROG_SCRATCH"
	cacheProgramPersist = "GOATEST_CACHEPROG_PERSIST"
)

// TestCacheProgramHelper is the cache program the go command starts in the test
// below. It is the real dispatch: the same argument list a production
// invocation carries, through the same entry point, on the process's own
// standard streams.
//
// It exits rather than returning, because everything the test framework would
// print afterwards would land in the middle of the protocol the go command is
// reading.
func TestCacheProgramHelper(t *testing.T) {
	if os.Getenv(cacheProgramHelper) == "" {
		t.Skip("not the cache program helper process")
	}
	arguments := []string{"cacheprog", "--base", os.Getenv(cacheProgramBase), "--scratch", os.Getenv(cacheProgramScratch)}
	if os.Getenv(cacheProgramPersist) != "" {
		arguments = append(arguments, "--persist")
	}
	os.Exit(realMainStreams(arguments, os.Stdin, os.Stdout, os.Stderr, nil))
}

// TestTheGoCommandCompilesThroughTheCacheProgram holds the cache program to the
// only authority on the protocol it speaks: a real go command.
//
// Everything below the wire is already covered by unit tests. What cannot be
// covered by them is whether the go command agrees — that the opening response
// advertises what it expects, that a stored object comes back as a file it can
// open, and that a second build with an empty scratch layer is answered out of
// the layer the machine keeps. The fixture is one package with no dependencies
// so that agreement costs a few hundred kilobytes to establish.
func TestTheGoCommandCompilesThroughTheCacheProgram(t *testing.T) {
	if testing.Short() {
		t.Skip("compiling through a real toolchain is not a short test")
	}
	goBinary, err := exec.LookPath("go")
	if err != nil {
		t.Skip("no go command on PATH")
	}
	fixture := writeCacheFixtureModule(t)
	base := t.TempDir()

	// The first build finds an empty cache and fills the layer the machine
	// keeps, because a compile is a command that may persist.
	first := t.TempDir()
	compileCacheFixture(t, goBinary, fixture, base, first, true)
	stored, err := (buildcache.Layer{Dir: base}).Inspect()
	if err != nil {
		t.Fatal(err)
	}
	if stored.Entries == 0 || stored.Bytes == 0 {
		t.Fatalf("base layer = %+v, want what the first build compiled", stored)
	}

	// The second build is a different run: a scratch layer of its own, and no
	// permission to write to the base one. Everything it needs is already
	// there, so it is answered out of the base layer without compiling again.
	second := t.TempDir()
	compileCacheFixture(t, goBinary, fixture, base, second, false)
	summary, err := buildcache.Summarize(second)
	if err != nil {
		t.Fatal(err)
	}
	if summary.HitsBase == 0 {
		t.Fatalf("second build = %s, want the base layer to have answered it", summary.Detail())
	}
	after, err := (buildcache.Layer{Dir: base}).Inspect()
	if err != nil {
		t.Fatal(err)
	}
	if after.Entries != stored.Entries {
		t.Fatalf("base layer = %+v, want the %d entries the first build left: a run without --persist writes to its own scratch", after, stored.Entries)
	}
}

// compileCacheFixture builds the fixture module with the cache program serving
// the two layers it is given.
func compileCacheFixture(t *testing.T, goBinary, fixture, base, scratch string, persist bool) {
	t.Helper()
	program := quoteCacheProgram(os.Args[0]) + " -test.run=^TestCacheProgramHelper$"
	environment := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + t.TempDir(),
		"GOPATH=" + t.TempDir(),
		"GOCACHE=" + t.TempDir(),
		// A stray repository above the temporary directory turns VCS stamping
		// into a hard failure for every go command below it.
		"GOFLAGS=-buildvcs=false",
		"GOPROXY=off", "GOSUMDB=off", "GOTOOLCHAIN=local", "GOTELEMETRY=off",
		"GOCACHEPROG=" + program,
		cacheProgramHelper + "=1",
		cacheProgramBase + "=" + base,
		cacheProgramScratch + "=" + scratch,
	}
	if persist {
		environment = append(environment, cacheProgramPersist+"=1")
	}
	command := exec.Command(goBinary, "build", "./...")
	command.Dir = fixture
	command.Env = environment
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("go build through the cache program: %v\n%s", err, output)
	}
	if len(output) != 0 {
		t.Fatalf("go build through the cache program said %q, want a silent build", output)
	}
}

// writeCacheFixtureModule writes the smallest module a go command can compile:
// one package, no dependencies, and nothing to download.
func writeCacheFixtureModule(t *testing.T) string {
	t.Helper()
	directory := t.TempDir()
	for name, contents := range map[string]string{
		"go.mod":   "module fixture.example/cachefixture\n\ngo 1.26\n",
		"value.go": "package cachefixture\n\n// Value is the one thing this fixture compiles.\nfunc Value() int { return 42 }\n",
	} {
		if err := os.WriteFile(filepath.Join(directory, name), []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return directory
}

// quoteCacheProgram renders one path the way the go command reads a GOCACHEPROG
// value, which is the same rule the production rendering follows.
func quoteCacheProgram(path string) string {
	if !strings.ContainsAny(path, " \t\n\r") {
		return path
	}
	return "'" + path + "'"
}
