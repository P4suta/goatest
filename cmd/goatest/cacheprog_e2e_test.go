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
	"github.com/P4suta/goatest/internal/filemode"
	"github.com/P4suta/goatest/internal/testkit"
)

const minimumStandardLibraryCacheEntries = 10

const (
	cacheProgramHelper  = "GOATEST_CACHEPROG_HELPER"
	cacheProgramBase    = "GOATEST_CACHEPROG_BASE"
	cacheProgramScratch = "GOATEST_CACHEPROG_SCRATCH"
	cacheProgramPersist = "GOATEST_CACHEPROG_PERSIST"
)

func TestCacheProgramHelper(t *testing.T) {
	if !testkit.HelperEnabled(cacheProgramHelper) {
		return
	}
	arguments := []string{"cacheprog", "--base", os.Getenv(cacheProgramBase), "--scratch", os.Getenv(cacheProgramScratch)}
	if os.Getenv(cacheProgramPersist) != "" {
		arguments = append(arguments, "--persist")
	}
	os.Exit(realMainStreams(arguments, os.Stdin, os.Stdout, os.Stderr, nil))
}

func TestTheGoCommandCompilesThroughTheCacheProgram(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("compiling through a real toolchain is not a short test")
	}
	goBinary, err := exec.LookPath("go")
	if err != nil {
		t.Skip("no go command on PATH")
	}
	fixture := writeCacheFixtureModule(t)
	base := preparedCacheLayer(t)

	markerBefore := markerTime(t, base)

	compileCacheFixture(t, goBinary, fixture, base, t.TempDir(), true)
	stored := layerEntries(t, base)
	if stored == 0 {
		t.Fatal("base layer holds nothing after the first build")
	}
	if after := markerTime(t, base); !after.Equal(markerBefore) {
		t.Fatalf("marker was rewritten at %s (was %s); a served go command must not prepare the layer", after, markerBefore)
	}

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

func TestTheSameTreeHitsAndACopiedOneMissesItsOwnPackages(t *testing.T) {
	t.Parallel()
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
	if cold < minimumStandardLibraryCacheEntries {
		t.Fatalf("base layer holds %d entries after a cold build, want the standard library closure the fixture imports", cold)
	}

	compileCacheFixture(t, goBinary, here, base, t.TempDir(), true)
	warm := layerEntries(t, base)
	if warm != cold {
		t.Fatalf("base layer holds %d entries after a second build, want the %d the cold build stored: the fixture is aged so that every action is asked for on the first build", warm, cold)
	}

	same := t.TempDir()
	compileCacheFixture(t, goBinary, here, base, same, true)
	if after := layerEntries(t, base); after != warm {
		t.Fatalf("base layer holds %d entries after rebuilding the same tree, want the %d it had settled on", after, warm)
	}
	sameSummary := cacheSummary(t, same)
	if sameSummary.HitsBase < minimumStandardLibraryCacheEntries || sameSummary.Misses != 0 {
		t.Fatalf("rebuilding the same tree = %s, want every action answered by the layer", sameSummary.Detail())
	}

	elsewhere := copyCacheFixtureModule(t, here)
	copied := t.TempDir()
	compileCacheFixture(t, goBinary, elsewhere, base, copied, true)
	grown := layerEntries(t, base)
	if grown <= warm {
		t.Fatalf("base layer holds %d entries after building the copy, want more than the %d it had: the project's own package hashes its own directory", grown, warm)
	}
	copiedSummary := cacheSummary(t, copied)
	if copiedSummary.HitsBase < minimumStandardLibraryCacheEntries {
		t.Fatalf("building the copy = %s, want the standard library closure to have hit", copiedSummary.Detail())
	}
	if copiedSummary.Misses == 0 {
		t.Fatalf("building the copy = %s, want the project's own package to have missed", copiedSummary.Detail())
	}

	if grown-warm >= warm {
		t.Fatalf("base layer grew by %d of %d entries, want only the project's own packages stored again", grown-warm, warm)
	}

	settled := t.TempDir()
	compileCacheFixture(t, goBinary, elsewhere, base, settled, true)
	if after := layerEntries(t, base); after != grown {
		t.Fatalf("base layer holds %d entries after rebuilding the copy, want the %d it already had", after, grown)
	}
	if settledSummary := cacheSummary(t, settled); settledSummary.Misses != 0 {
		t.Fatalf("rebuilding the copy = %s, want every action answered by the layer", settledSummary.Detail())
	}
}

func preparedCacheLayer(t *testing.T) string {
	t.Helper()
	base := filepath.Join(t.TempDir(), "base")
	if err := (buildcache.Layer{Dir: base}).Prepare(); err != nil {
		t.Fatal(err)
	}
	return base
}

func markerTime(t *testing.T, dir string) time.Time {
	t.Helper()
	info, err := os.Stat(filepath.Join(dir, buildcache.MarkerName))
	if err != nil {
		t.Fatal(err)
	}
	return info.ModTime()
}

func layerEntries(t *testing.T, dir string) int {
	t.Helper()
	status, err := (buildcache.Layer{Dir: dir}).Inspect()
	if err != nil {
		t.Fatal(err)
	}
	return status.Entries
}

func cacheSummary(t *testing.T, scratch string) buildcache.Stats {
	t.Helper()
	summary, err := buildcache.Summarize(scratch)
	if err != nil {
		t.Fatal(err)
	}
	return summary
}

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
		if err := os.WriteFile(filepath.Join(to, entry.Name()), data, filemode.ReadableFile); err != nil {
			t.Fatal(err)
		}
	}
	ageCacheFixture(t, to)
	return to
}

func ageCacheFixture(t *testing.T, directory string) {
	t.Helper()
	aged := time.Now().Add(-time.Hour)
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if err := os.Chtimes(filepath.Join(directory, entry.Name()), aged, aged); err != nil {
			t.Fatal(err)
		}
	}
}

func compileCacheFixture(t *testing.T, goBinary, fixture, base, scratch string, persist bool) {
	t.Helper()
	program := quoteCacheProgram(os.Args[0]) + " -test.run=^TestCacheProgramHelper$"
	environment := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + t.TempDir(),
		"GOPATH=" + t.TempDir(),
		"GOCACHE=" + t.TempDir(),

		"GOFLAGS=-buildvcs=false",
		"GOPROXY=off", "GOSUMDB=off", "GOTOOLCHAIN=local", "GOTELEMETRY=off",

		"GOCOVERDIR=" + t.TempDir(),
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

func writeCacheFixtureModule(t *testing.T) string {
	t.Helper()
	directory := t.TempDir()
	for name, contents := range map[string]string{
		"go.mod": "module fixture.example/cachefixture\n\ngo 1.26\n",
		"value.go": "package cachefixture\n\nimport \"strings\"\n\n" +
			"func Value() string { return strings.ToUpper(\"x\") }\n",
	} {
		if err := os.WriteFile(filepath.Join(directory, name), []byte(contents), filemode.ReadableFile); err != nil {
			t.Fatal(err)
		}
	}
	ageCacheFixture(t, directory)
	return directory
}

func quoteCacheProgram(path string) string {
	if !strings.ContainsAny(path, " \t\n\r") {
		return path
	}
	return "'" + path + "'"
}
