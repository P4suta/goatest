// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package assure

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	gomutants "github.com/P4suta/go-mutants"
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
			cache, err := openRunBuildCache(test.program, test.base, t.TempDir())
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
	cache, err := openRunBuildCache("/opt/goatest", base, temporary)
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
	if _, err := os.Stat(filepath.Join(base, "README")); err != nil {
		t.Fatalf("base layer = %v, want it prepared", err)
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
	cache, err := openRunBuildCache("/opt/goatest", blocked, t.TempDir())
	if err == nil || cache.serves() {
		t.Fatalf("openRunBuildCache = (%+v, %v), want the unusable layer reported", cache, err)
	}
}

func TestReleaseBuildCacheKeepsAndNamesTheScratchItWasAskedToKeep(t *testing.T) {
	t.Parallel()
	cache, err := openRunBuildCache("/opt/goatest", t.TempDir(), t.TempDir())
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
