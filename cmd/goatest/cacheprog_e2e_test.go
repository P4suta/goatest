// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
// the layer the machine keeps. The fixture is one package with one standard
// library import and no third-party dependency, which is the smallest module
// that exercises both halves of the layer.
func TestTheGoCommandCompilesThroughTheCacheProgram(t *testing.T) {
	if testing.Short() {
		t.Skip("compiling through a real toolchain is not a short test")
	}
	goBinary, err := exec.LookPath("go")
	if err != nil {
		t.Skip("no go command on PATH")
	}
	fixture := writeCacheFixtureModule(t)
	base := preparedCacheLayer(t)
	// The run prepared the layer; the go commands it starts must not prepare it
	// again. One cacheprog child runs per go command, and a run issues
	// thousands, so a rewritten and fsynced marker per child is pure cost.
	markerBefore := markerTime(t, base)

	// The first build finds an empty cache and fills the layer the machine
	// keeps, because a compile is a command that may persist.
	compileCacheFixture(t, goBinary, fixture, base, t.TempDir(), true)
	stored := layerEntries(t, base)
	if stored == 0 {
		t.Fatal("base layer holds nothing after the first build")
	}
	if after := markerTime(t, base); !after.Equal(markerBefore) {
		t.Fatalf("marker was rewritten at %s (was %s); a served go command must not prepare the layer", after, markerBefore)
	}

	// The second build is a different run: a scratch layer of its own, and no
	// permission to write to the base one. Everything it needs is already
	// there, so it is answered out of the base layer without compiling again.
	second := t.TempDir()
	compileCacheFixture(t, goBinary, fixture, base, second, false)
	summary := cacheSummary(t, second)
	if summary.HitsBase == 0 {
		t.Fatalf("second build = %s, want the base layer to have answered it", summary.Detail())
	}
	if after := layerEntries(t, base); after != stored {
		t.Fatalf("base layer holds %d entries, want the %d the first build left: a run without --persist writes to its own scratch", after, stored)
	}
}

// TestTheSameTreeHitsAndACopiedOneMissesItsOwnPackages states what the cache
// does and does not carry between runs, counted in entries stored rather than
// in seconds saved.
//
// Building one tree twice hits for everything — the standard library closure
// and the project's own package alike — so a second build allowed to persist
// stores nothing. Moving that identical tree to another directory still hits
// for the standard library, which is most of the layer and the whole reason it
// is kept per machine, but misses the project's own packages.
//
// That miss is deliberate and is not a defect of this cache. The go command
// hashes the absolute directory of a package into its action ID unless
// -trimpath is set, and goatest does not set -trimpath: it would change the
// binaries under verification, and a verdict has to be about the build the
// project actually makes. What follows from that is where the effort goes
// instead — go-mutants is given a snapshot directory that is stable per
// repository root, so successive runs of one repository land on the same paths
// and hit each other's compiles.
func TestTheSameTreeHitsAndACopiedOneMissesItsOwnPackages(t *testing.T) {
	if testing.Short() {
		t.Skip("compiling through a real toolchain is not a short test")
	}
	goBinary, err := exec.LookPath("go")
	if err != nil {
		t.Skip("no go command on PATH")
	}
	base := preparedCacheLayer(t)
	here := writeCacheFixtureModule(t)

	compileCacheFixture(t, goBinary, here, base, t.TempDir(), true)
	cold := layerEntries(t, base)
	if cold < 10 {
		t.Fatalf("base layer holds %d entries after a cold build, want the standard library closure the fixture imports", cold)
	}

	// A second build of the same tree reaches one action the first never got
	// far enough to compute — the go command derives it only once the packages
	// below it are cached — so the layer settles on the build after the cold
	// one rather than on the cold one itself.
	compileCacheFixture(t, goBinary, here, base, t.TempDir(), true)
	warm := layerEntries(t, base)

	// (a) The same directory, once the layer has settled: every action the
	// build needs is in it under the identifier the build asks for, so a build
	// allowed to persist stores nothing at all and the standard library and the
	// project's own package alike come back out of the layer.
	same := t.TempDir()
	compileCacheFixture(t, goBinary, here, base, same, true)
	if after := layerEntries(t, base); after != warm {
		t.Fatalf("base layer holds %d entries after rebuilding the same tree, want the %d it had settled on", after, warm)
	}
	sameSummary := cacheSummary(t, same)
	if sameSummary.HitsBase < 10 || sameSummary.Misses != 0 {
		t.Fatalf("rebuilding the same tree = %s, want every action answered by the layer", sameSummary.Detail())
	}

	// (b) The identical source at another path. The standard library closure is
	// path independent and hits; the project's own package is not and misses,
	// so building it stores entries the layer did not have.
	elsewhere := copyCacheFixtureModule(t, here)
	copied := t.TempDir()
	compileCacheFixture(t, goBinary, elsewhere, base, copied, true)
	grown := layerEntries(t, base)
	if grown <= warm {
		t.Fatalf("base layer holds %d entries after building the copy, want more than the %d it had: the project's own package hashes its own directory", grown, warm)
	}
	copiedSummary := cacheSummary(t, copied)
	if copiedSummary.HitsBase < 10 {
		t.Fatalf("building the copy = %s, want the standard library closure to have hit", copiedSummary.Detail())
	}
	if copiedSummary.Misses == 0 {
		t.Fatalf("building the copy = %s, want the project's own package to have missed", copiedSummary.Detail())
	}
	// What it stored is the project's packages alone, so it is a small fraction
	// of a layer that is mostly compiled standard library.
	if grown-warm >= warm {
		t.Fatalf("base layer grew by %d of %d entries, want only the project's own packages stored again", grown-warm, warm)
	}

	// The copy now hits the way the original does, which is what proves the
	// misses above were the path rather than chance.
	settled := t.TempDir()
	compileCacheFixture(t, goBinary, elsewhere, base, settled, true)
	if after := layerEntries(t, base); after != grown {
		t.Fatalf("base layer holds %d entries after rebuilding the copy, want the %d it already had", after, grown)
	}
	if settledSummary := cacheSummary(t, settled); settledSummary.Misses != 0 {
		t.Fatalf("rebuilding the copy = %s, want every action answered by the layer", settledSummary.Detail())
	}
}

// preparedCacheLayer is a base layer prepared the way a run prepares it: once,
// in the parent, before any go command has been handed the cache program.
func preparedCacheLayer(t *testing.T) string {
	t.Helper()
	base := filepath.Join(t.TempDir(), "base")
	if err := (buildcache.Layer{Dir: base}).Prepare(); err != nil {
		t.Fatal(err)
	}
	return base
}

// markerTime is when a layer's marker was last written, which is how this test
// observes whether anything prepared the layer again.
func markerTime(t *testing.T, dir string) time.Time {
	t.Helper()
	info, err := os.Stat(filepath.Join(dir, buildcache.MarkerName))
	if err != nil {
		t.Fatal(err)
	}
	return info.ModTime()
}

// layerEntries is how many cache keys a layer holds.
func layerEntries(t *testing.T, dir string) int {
	t.Helper()
	status, err := (buildcache.Layer{Dir: dir}).Inspect()
	if err != nil {
		t.Fatal(err)
	}
	return status.Entries
}

// cacheSummary is what the go processes served from one scratch layer asked for.
func cacheSummary(t *testing.T, scratch string) buildcache.Stats {
	t.Helper()
	summary, err := buildcache.Summarize(scratch)
	if err != nil {
		t.Fatal(err)
	}
	return summary
}

// copyCacheFixtureModule copies the fixture byte for byte to another directory,
// so that the path is the only thing that differs between two builds of it.
func copyCacheFixtureModule(t *testing.T, from string) string {
	t.Helper()
	to := t.TempDir()
	entries, err := os.ReadDir(from)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		data, readErr := os.ReadFile(filepath.Join(from, entry.Name()))
		if readErr != nil {
			t.Fatal(readErr)
		}
		if err := os.WriteFile(filepath.Join(to, entry.Name()), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return to
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

// writeCacheFixtureModule writes the smallest module that exercises both halves
// of the cache: one package, no third-party dependency and nothing to download,
// importing one standard library package so that the closure a per-machine
// layer exists to hold is actually compiled. The whole fixture costs a few tens
// of megabytes of layer to build once.
func writeCacheFixtureModule(t *testing.T) string {
	t.Helper()
	directory := t.TempDir()
	for name, contents := range map[string]string{
		"go.mod": "module fixture.example/cachefixture\n\ngo 1.26\n",
		"value.go": "package cachefixture\n\nimport \"strings\"\n\n" +
			"// Value is the one thing this fixture compiles.\nfunc Value() string { return strings.ToUpper(\"x\") }\n",
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
