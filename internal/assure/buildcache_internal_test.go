// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package assure

import (
	"context"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	gomutants "github.com/P4suta/go-mutants"
	"github.com/P4suta/goatest/internal/buildcache"
)

// TestOnlyCommandsThatCompileOrListPersistToTheBaseLayer pins the one rule the
// build cache turns on: which of goatest's own commands may write into the
// layer this machine keeps.
//
// It is written from the argv builders the run uses rather than from literals,
// so a change to what goatest runs is a change this test sees. The case that
// matters most is the baseline target: it is the project's test binary wrapped
// in `go tool test2json`, so its argv begins with the go binary like every
// compile does, and a suite whose tests spawn go commands of their own — this
// repository's does — would fill the base layer with fixture packages and evict
// the standard library it exists to hold.
func TestOnlyCommandsThatCompileOrListPersistToTheBaseLayer(t *testing.T) {
	t.Parallel()
	target := gomutants.Command{Argv: []string{filepath.Join("artifacts", "internal-assure.test"), "-test.run=^TestValue$"}}
	for _, test := range []struct {
		name string
		argv []string
		want bool
	}{
		{name: "baseline vet", argv: baselineGoCommand("vet", nil, []string{"./..."}), want: true},
		{name: "baseline build", argv: baselineGoCommand("build", nil, []string{"./..."}), want: true},
		{name: "baseline build with tags", argv: baselineGoCommand("build", []string{"integration"}, []string{"./..."}), want: true},
		{name: "baseline test binary compile", argv: baselineCompileCommand("fixture.example/module", "fixture.example/module/pkg", "binary", nil), want: true},
		{name: "workspace toolchain", argv: []string{"go", "version"}, want: true},
		{name: "workspace package list", argv: []string{"go", "list", "-json", "./..."}, want: true},
		{name: "workspace module list", argv: []string{"go", "list", "-m", "-json", "all"}, want: true},
		{name: "selected package list", argv: []string{"go", "list", "-json", "-tags=integration", "./internal/..."}, want: true},

		{name: "baseline target under test2json", argv: test2JSONCommand("fixture.example/module/pkg", target).Argv, want: false},
		{name: "baseline target run directly", argv: target.Argv, want: false},
		{name: "race verification", argv: []string{"go", "test", "-race", "-count=1", "./..."}, want: false},
		{name: "original mutation control", argv: []string{"go", "test", "-count=1", "./..."}, want: false},
		{name: "candidate compile-only suite", argv: []string{"go", "test", "-run=^$", "./..."}, want: false},
		{name: "a test binary named like a subcommand", argv: []string{"go", "test", "-args", "-c"}, want: false},
		{name: "no command at all", argv: nil, want: false},
		{name: "a bare go", argv: []string{"go"}, want: false},

		// The go command accepts -C only as its first flag, and it changes the
		// directory before reading the subcommand. A rule that read argv[1]
		// blindly would classify every one of these as neither a compile nor a
		// test run and quietly stop persisting.
		{name: "a directory change before a build", argv: []string{"go", "-C", "sub", "build", "./..."}, want: true},
		{name: "a joined directory change before a build", argv: []string{"go", "-C=sub", "build", "./..."}, want: true},
		{name: "a directory change before a list", argv: []string{"go", "-C", "sub", "list", "-json", "./..."}, want: true},
		{name: "a directory change before a compile", argv: []string{"go", "-C=sub", "test", "-c", "-o", "binary", "./pkg"}, want: true},
		{name: "a directory change before a test run", argv: []string{"go", "-C", "sub", "test", "./..."}, want: false},
		{name: "a directory change before test2json", argv: []string{"go", "-C=sub", "tool", "test2json", "binary"}, want: false},
		{name: "a directory change and nothing after it", argv: []string{"go", "-C", "sub"}, want: false},
		{name: "a joined directory change and nothing after it", argv: []string{"go", "-C=sub"}, want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := persistingCommand(test.argv); got != test.want {
				t.Fatalf("persistingCommand(%q) = %t, want %t", test.argv, got, test.want)
			}
		})
	}
}

// TestPersistingCommandReadsTheSubcommandOfAnyGoBinary holds the rule to the go
// command wherever it is: a run may be given an absolute go binary, and Windows
// spells it with an extension.
func TestPersistingCommandReadsTheSubcommandOfAnyGoBinary(t *testing.T) {
	t.Parallel()
	for _, executable := range []string{"go", "go.exe", "/usr/local/go/bin/go", filepath.Join("C:", "Go", "bin", "go.exe")} {
		if !persistingCommand([]string{executable, "build", "./..."}) {
			t.Errorf("persistingCommand rejected the go binary %q", executable)
		}
		if persistingCommand([]string{executable, "test", "./..."}) {
			t.Errorf("persistingCommand persisted a test run of %q", executable)
		}
	}
	for _, executable := range []string{"gofmt", "cargo", "mygo", filepath.Join("bin", "gopher")} {
		if persistingCommand([]string{executable, "build", "./..."}) {
			t.Errorf("persistingCommand persisted %q, which is not the go command", executable)
		}
	}
}

// recordingWorkspace is a workspace that answers nothing and remembers what it
// was asked to run.
type recordingWorkspace struct{ commands []gomutants.Command }

func (workspace *recordingWorkspace) Exec(_ context.Context, command gomutants.Command) (gomutants.CommandResult, error) {
	workspace.commands = append(workspace.commands, command)
	return gomutants.CommandResult{}, nil
}

func TestBuildCacheWorkspaceAttachesThePersistingProgramToCompilesAlone(t *testing.T) {
	t.Parallel()
	cache := runBuildCache{scratch: "scratch", base: "base", plain: "goatest cacheprog", persisting: "goatest cacheprog --persist"}
	inner := &recordingWorkspace{}
	wrapped := withBuildCache(inner, cache)
	if _, err := wrapped.Exec(t.Context(), gomutants.Command{Argv: []string{"go", "build", "./..."}}); err != nil {
		t.Fatal(err)
	}
	if _, err := wrapped.Exec(t.Context(), gomutants.Command{
		Argv: []string{"go", "tool", "test2json", "binary"}, Env: []string{"RESOURCE=ready"},
	}); err != nil {
		t.Fatal(err)
	}
	if len(inner.commands) != 2 {
		t.Fatalf("commands = %d, want two", len(inner.commands))
	}
	if !slices.Equal(inner.commands[0].Env, []string{"GOCACHEPROG=goatest cacheprog --persist"}) {
		t.Fatalf("compile environment = %q, want the persisting program", inner.commands[0].Env)
	}
	// A target run keeps the overlay it came with and gains nothing: it
	// inherits the scratch-writing program from the frozen environment of the
	// workspace, which is where every command of the run reads it from.
	if !slices.Equal(inner.commands[1].Env, []string{"RESOURCE=ready"}) {
		t.Fatalf("target environment = %q, want only the resources it was given", inner.commands[1].Env)
	}
}

func TestBuildCacheWorkspaceReplacesACacheProgramItWasHandedAndWrapsNothingWithoutACache(t *testing.T) {
	t.Parallel()
	inner := &recordingWorkspace{}
	if wrapped := withBuildCache(inner, runBuildCache{}); wrapped != CommandWorkspace(inner) {
		t.Fatalf("a run without a build cache wrapped its workspace as %T", wrapped)
	}
	if wrapped := withBuildCache(nil, runBuildCache{plain: "program", persisting: "program --persist"}); wrapped != nil {
		t.Fatalf("wrapping no workspace produced %T", wrapped)
	}
	wrapped := withBuildCache(inner, runBuildCache{plain: "program", persisting: "program --persist"})
	if _, err := wrapped.Exec(t.Context(), gomutants.Command{
		Argv: []string{"go", "list", "-json", "./..."}, Env: []string{"gocacheprog=stale", "RESOURCE=ready"},
	}); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(inner.commands[0].Env, []string{"RESOURCE=ready", "GOCACHEPROG=program --persist"}) {
		t.Fatalf("environment = %q, want the stale cache program replaced and the resources kept", inner.commands[0].Env)
	}
}

// TestCollectBaselinePersistsItsCompilesAndNeverItsTargetRuns holds the rule
// where it is actually applied: a whole baseline round through the wrapper.
func TestCollectBaselinePersistsItsCompilesAndNeverItsTargetRuns(t *testing.T) {
	workspace := &baselineFakeWorkspace{}
	// The coverage profile is written whichever way the target was run, so the
	// round completes with the target under test2json — the form whose argv
	// begins with the go binary, and the one the rule has to get right.
	workspace.exec = func(command gomutants.Command) (gomutants.CommandResult, error) {
		if profile := coverageProfileArgument(command); profile != "" {
			contents := "mode: set\nfixture.example/module/value.go:1.1,2.1 1 1\n"
			if err := os.WriteFile(profile, []byte(contents), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		return gomutants.CommandResult{Duration: time.Second}, nil
	}
	cache := runBuildCache{scratch: "scratch", base: "base", plain: "program", persisting: "program --persist"}
	result, err := CollectBaseline(t.Context(), withBuildCache(workspace, cache), baselineModel(), []BaselineTarget{{
		Target: baselineTestTarget("TestValue"),
	}}, BaselineOptions{ArtifactDirectory: t.TempDir(), UseTest2JSON: true})
	if err != nil || len(result.Targets) != 1 {
		t.Fatalf("CollectBaseline = (%+v, %v)", result, err)
	}
	if len(workspace.commands) != 4 {
		t.Fatalf("commands = %d, want vet, build, a compile, and one target run", len(workspace.commands))
	}
	for index, command := range workspace.commands[:3] {
		if !slices.Contains(command.Env, "GOCACHEPROG=program --persist") {
			t.Errorf("command %d %q = %q, want the persisting program", index, command.Argv, command.Env)
		}
	}
	targetRun := workspace.commands[3]
	if targetRun.Argv[0] != "go" || targetRun.Argv[1] != "tool" {
		t.Fatalf("the fourth command was %q, want the target under test2json", targetRun.Argv)
	}
	for _, entry := range targetRun.Env {
		if strings.HasPrefix(entry, cacheProgramVariable+"=") {
			t.Fatalf("the target run carried %q; a test binary's children would fill the base layer", entry)
		}
	}
}

func TestOpenRunBuildCacheServesNothingWithoutAProgramOrABase(t *testing.T) {
	t.Parallel()
	for _, test := range []struct{ name, program, base string }{
		{name: "no program", base: t.TempDir()},
		{name: "no base", program: "goatest"},
		{name: "neither"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			cache, err := openRunBuildCache(test.program, test.base, runScratch{fallback: t.TempDir()}, 2<<30)
			if err != nil || cache.serves() || cache.environment() != nil || cache.persistingEnvironment() != nil {
				t.Fatalf("openRunBuildCache = (%+v, %v), want a cache that serves nothing", cache, err)
			}
			if summary := cache.summarize(); summary != "" {
				t.Fatalf("summary = %q, want nothing to report", summary)
			}
			if err := releaseBuildCache(Options{}, cache); err != nil {
				t.Fatalf("releaseBuildCache = %v", err)
			}
		})
	}
}

func TestOpenRunBuildCacheRendersBothProgramsAndRemovesOnlyItsScratch(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	temporary := t.TempDir()
	cache, err := openRunBuildCache("/opt/goatest", base, runScratch{fallback: temporary}, 2<<30)
	if err != nil || !cache.serves() {
		t.Fatalf("openRunBuildCache = (%+v, %v)", cache, err)
	}
	if !strings.Contains(cache.persisting, "--persist") || strings.Contains(cache.plain, "--persist") {
		t.Fatalf("programs = plain %q persisting %q", cache.plain, cache.persisting)
	}
	if !strings.HasPrefix(cache.scratch, temporary) {
		t.Fatalf("scratch = %q, want it below %q", cache.scratch, temporary)
	}
	// Preparing the base layer is the writability probe, so the layer exists
	// before a single go command has been handed the program.
	if _, err := os.Stat(filepath.Join(base, buildcache.MarkerName)); err != nil {
		t.Fatalf("base layer = %v, want it prepared", err)
	}
	// The bound the run's scratch layer is pruned to travels to the served
	// child on its command line, because the child reads no configuration.
	if !strings.Contains(cache.plain, "--max-bytes") || !strings.Contains(cache.persisting, "--max-bytes") {
		t.Fatalf("programs = plain %q persisting %q, want the bound on both", cache.plain, cache.persisting)
	}
	if err := releaseBuildCache(Options{}, cache); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(cache.scratch); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("scratch after close = %v, want it gone", err)
	}
	if _, err := os.Stat(base); err != nil {
		t.Fatalf("base after close = %v, want the layer the machine keeps left alone", err)
	}
}

func TestOpenRunBuildCacheReportsABaseLayerItCannotPrepare(t *testing.T) {
	t.Parallel()
	blocked := filepath.Join(t.TempDir(), "occupied")
	if err := os.WriteFile(blocked, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	cache, err := openRunBuildCache("/opt/goatest", blocked, runScratch{fallback: t.TempDir()}, 2<<30)
	if err == nil || cache.serves() {
		t.Fatalf("openRunBuildCache = (%+v, %v), want the unusable layer reported", cache, err)
	}
}

// TestOpenRunBuildCacheRemovesTheScratchItCannotRenderAProgramFor holds the
// error path to the same cleanliness rule as the happy one. The scratch
// directory is made before the two programs are rendered, so a program the go
// command cannot be handed leaves a run with no cache — and must leave it with
// no directory either.
func TestOpenRunBuildCacheRemovesTheScratchItCannotRenderAProgramFor(t *testing.T) {
	t.Parallel()
	temporary := t.TempDir()
	// A path holding both kinds of quote cannot be rendered as a GOCACHEPROG
	// value at all, which is the one failure that falls between making the
	// directory and returning the cache.
	cache, err := openRunBuildCache(`/opt/o'say"what/goatest`, t.TempDir(), runScratch{fallback: temporary}, 2<<30)
	if err == nil || cache.serves() {
		t.Fatalf("openRunBuildCache = (%+v, %v), want the unrenderable program reported", cache, err)
	}
	left, readErr := os.ReadDir(temporary)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(left) != 0 {
		names := make([]string, 0, len(left))
		for _, entry := range left {
			names = append(names, entry.Name())
		}
		t.Fatalf("temporary directory holds %v, want the scratch layer removed with the failure", names)
	}
}

// TestCollectBaseBoundsTheLayerTheMachineKeeps is the cap actually being a cap.
// A bound enforced only by a command a developer remembers to type is not a
// bound, so a run collects the base layer when it ends.
func TestCollectBaseBoundsTheLayerTheMachineKeeps(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	cache, err := openRunBuildCache("/opt/goatest", base, runScratch{fallback: t.TempDir()}, 20)
	if err != nil {
		t.Fatal(err)
	}
	layer := buildcache.Layer{Dir: base}
	layers := buildcache.Layers{Base: layer, Persist: true}
	moment := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	for _, key := range []byte{1, 2, 3} {
		if _, err := layers.Put(buildCacheTestKey(key), buildCacheTestKey(key+0x10),
			strings.NewReader("0123456789"), 10, moment); err != nil {
			t.Fatal(err)
		}
	}
	// Two entries were last read long ago; the third is one a build running
	// beside this collection is reading, so MinIdle must spare it.
	for _, key := range []byte{1, 2} {
		aged := moment.Add(-90 * 24 * time.Hour)
		if err := os.Chtimes(buildCacheActionPath(base, key), aged, aged); err != nil {
			t.Fatal(err)
		}
	}
	collected, ran, err := cache.collectBase(buildcache.Policy{
		MaxBytes: 20, TTL: 30 * 24 * time.Hour, MinIdle: layer.MinIdle(),
	}, moment)
	if err != nil || !ran {
		t.Fatalf("collectBase = (%+v, %t, %v), want a collection", collected, ran, err)
	}
	if collected.RemovedActions != 2 || collected.After.Entries != 1 {
		t.Fatalf("collectBase = %+v, want the two stale entries gone and the live one spared", collected)
	}
	status, err := layer.Inspect()
	if err != nil {
		t.Fatal(err)
	}
	if status.Entries != 1 || status.Bytes != 10 {
		t.Fatalf("base layer after collection = %+v, want it inside its bound", status)
	}
}

func TestCollectBaseSkipsWhatAnotherProcessIsAlreadyCollecting(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	cache, err := openRunBuildCache("/opt/goatest", base, runScratch{fallback: t.TempDir()}, 20)
	if err != nil {
		t.Fatal(err)
	}
	// The concurrent collector is a run of another repository, which shares
	// this layer. Yielding to it costs this run nothing: the layer is bounded
	// either way, and the next run collects whatever this one left.
	release, held, err := (buildcache.Layer{Dir: base}).HoldCollection()
	if err != nil || !held {
		t.Fatalf("holding the collection lock = (%t, %v)", held, err)
	}
	collected, ran, err := cache.collectBase(buildcache.Policy{MaxBytes: 1}, time.Now())
	if err != nil || ran || collected != (buildcache.Collected{}) {
		t.Fatalf("collectBase against a held lock = (%+v, %t, %v), want it skipped without an error", collected, ran, err)
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
	if _, ran, err := cache.collectBase(buildcache.Policy{MaxBytes: 1}, time.Now()); err != nil || !ran {
		t.Fatalf("collectBase after the lock was released = (%t, %v), want a collection", ran, err)
	}
}

func TestCollectBaseDoesNothingForARunWithoutACache(t *testing.T) {
	t.Parallel()
	collected, ran, err := runBuildCache{}.collectBase(buildcache.Policy{MaxBytes: 1}, time.Now())
	if err != nil || ran || collected != (buildcache.Collected{}) {
		t.Fatalf("collectBase without a cache = (%+v, %t, %v), want nothing", collected, ran, err)
	}
}

// buildCacheTestKey renders an identifier of the length the go command uses.
func buildCacheTestKey(value byte) []byte {
	identifier := make([]byte, 32)
	for index := range identifier {
		identifier[index] = value
	}
	return identifier
}

// buildCacheActionPath is where a layer stores one cache key, as this test
// knows the layout rather than as the package computes it.
func buildCacheActionPath(dir string, key byte) string {
	name := hex.EncodeToString(buildCacheTestKey(key))
	return filepath.Join(dir, "actions", name[:2], name)
}

func TestReleaseBuildCacheKeepsAndNamesTheScratchItWasAskedToKeep(t *testing.T) {
	t.Parallel()
	cache, err := openRunBuildCache("/opt/goatest", t.TempDir(), runScratch{fallback: t.TempDir()}, 2<<30)
	if err != nil {
		t.Fatal(err)
	}
	if err := releaseBuildCache(Options{KeepTemp: true}, cache); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(cache.scratch); err != nil {
		t.Fatalf("scratch after a kept close = %v, want it left where it was made", err)
	}
}
