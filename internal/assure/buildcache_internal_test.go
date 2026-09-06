// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package assure

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	gomutants "github.com/P4suta/go-mutants"
	"github.com/P4suta/goatest/internal/buildcache"
	"github.com/P4suta/goatest/internal/filemode"
)

const (
	cacheRoutingCommandCount  = 2
	baselineCacheCommandCount = 4
)

func TestRacePersistentCompileIsOnlyNeededWithoutANativeProjection(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name  string
		cache runBuildCache
		want  bool
	}{
		{name: "no cache"},
		{name: "external cache only", cache: runBuildCache{plain: "program"}, want: true},
		{name: "native projection", cache: runBuildCache{plain: "program", native: "native"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := test.cache.needsPersistentCompile(); got != test.want {
				t.Fatalf("needsPersistentCompile = %t, want %t", got, test.want)
			}
		})
	}
}

func TestNativeAdmissionDoesNotWaitWhileOpen(t *testing.T) {
	t.Parallel()
	projection := &nativeCacheProjection{}
	projection.once.Do(func() { projection.attempted = true })
	cache := runBuildCache{plain: "program", native: "native", projection: projection}
	release, native := cache.beginNative()
	if !native {
		t.Fatal("open native admission fell back")
	}
	release()
}

func TestOnlyCommandsThatCompileOrListPersistToTheBaseLayer(t *testing.T) {
	t.Parallel()
	target := gomutants.Command{Argv: []string{filepath.Join("artifacts", "internal-assure.test"), "-test.run=^TestValue$"}}
	for _, test := range []struct {
		name string
		argv []string
		want bool
	}{
		{name: "baseline vet", argv: baselineGoCommand("vet", nil, []string{"./..."}), want: true},
		{name: "baseline build", argv: baselineBuildCommand(nil, []string{"./..."}), want: true},
		{name: "baseline build with tags", argv: baselineBuildCommand([]string{"integration"}, []string{"./..."}), want: true},
		{name: "baseline test binary compile", argv: baselineCompileCommand([]string{"fixture.example/module/pkg"}, "fixture.example/module/pkg", "binary", nil), want: true},
		{name: "workspace toolchain", argv: []string{"go", "version"}, want: true},
		{name: "workspace package list", argv: []string{"go", "list", "-json", "./..."}, want: true},
		{name: "workspace module list", argv: []string{"go", "list", "-m", "-json", "all"}, want: true},
		{name: "selected package list", argv: []string{"go", "list", "-json", "-tags=integration", "./internal/..."}, want: true},

		{name: "baseline target with test framing", argv: testFramedCommand(target).Argv, want: false},
		{name: "baseline target run directly", argv: target.Argv, want: false},
		{name: "race verification", argv: []string{"go", "test", "-race", "-count=1", "./..."}, want: false},
		{name: "original mutation control", argv: []string{"go", "test", "-count=1", "./..."}, want: false},
		{name: "candidate compile-only suite", argv: []string{"go", "test", "-run=^$", "./..."}, want: false},
		{name: "a test binary named like a subcommand", argv: []string{"go", "test", "-args", "-c"}, want: false},
		{name: "no command at all", argv: nil, want: false},
		{name: "a bare go", argv: []string{"go"}, want: false},

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

type recordingWorkspace struct{ commands []gomutants.Command }

func (workspace *recordingWorkspace) Exec(_ context.Context, command gomutants.Command) (gomutants.CommandResult, error) {
	workspace.commands = append(workspace.commands, command)
	return gomutants.CommandResult{}, nil
}

func TestBuildCacheWorkspaceAttachesThePersistingProgramToCompilesAlone(t *testing.T) {
	t.Parallel()
	cache := runBuildCache{scratch: "scratch", base: "base", fallback: "fallback", native: "native", plain: "goatest cacheprog", persisting: "goatest cacheprog --persist"}
	inner := &recordingWorkspace{}
	wrapped := withBuildCache(inner, cache)
	if _, err := wrapped.Exec(t.Context(), gomutants.Command{Argv: []string{"go", "build", "./..."}}); err != nil {
		t.Fatal(err)
	}
	if _, err := wrapped.Exec(t.Context(), gomutants.Command{
		Argv: []string{"binary", "-test.v=test2json", "-test.run=^TestValue$", "-test.coverprofile=value.cover"},
		Env:  []string{"RESOURCE=ready"},
	}); err != nil {
		t.Fatal(err)
	}
	if len(inner.commands) != cacheRoutingCommandCount {
		t.Fatalf("commands = %d, want two", len(inner.commands))
	}
	if !slices.Equal(inner.commands[0].Env, []string{"GOCACHE=fallback", "GOCACHEPROG=goatest cacheprog --persist"}) {
		t.Fatalf("compile environment = %q, want the persisting program", inner.commands[0].Env)
	}

	if !slices.Equal(inner.commands[1].Env, []string{"RESOURCE=ready", "GOCACHE=native", "GOCACHEPROG="}) {
		t.Fatalf("target environment = %q, want resources and the projected native cache", inner.commands[1].Env)
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
	wrapped := withBuildCache(inner, runBuildCache{fallback: "fallback", native: "native", plain: "program", persisting: "program --persist"})
	if _, err := wrapped.Exec(t.Context(), gomutants.Command{
		Argv: []string{"go", "list", "-json", "./..."}, Env: []string{"gocacheprog=stale", "gocache=stale", "RESOURCE=ready"},
	}); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(inner.commands[0].Env, []string{"RESOURCE=ready", "GOCACHE=fallback", "GOCACHEPROG=program --persist"}) {
		t.Fatalf("environment = %q, want stale cache settings replaced and resources kept", inner.commands[0].Env)
	}
}

func TestCollectBaselinePersistsItsCompilesAndNeverItsTargetRuns(t *testing.T) {
	workspace := &baselineFakeWorkspace{}

	workspace.exec = func(command gomutants.Command) (gomutants.CommandResult, error) {
		if profile := coverageProfileArgument(command); profile != "" {
			contents := "mode: set\nfixture.example/module/value.go:1.1,2.1 1 1\n"
			if err := os.WriteFile(profile, []byte(contents), filemode.PrivateFile); err != nil {
				t.Fatal(err)
			}
		}
		return gomutants.CommandResult{Duration: time.Second}, nil
	}
	cache := runBuildCache{scratch: "scratch", base: "base", fallback: "fallback", native: "native", plain: "program", persisting: "program --persist"}
	result, err := CollectBaseline(t.Context(), withBuildCache(workspace, cache), baselineModel(), []BaselineTarget{{
		Target: baselineTestTarget("TestValue"),
	}}, BaselineOptions{ArtifactDirectory: t.TempDir(), UseTestFraming: true})
	if err != nil || len(result.Targets) != 1 {
		t.Fatalf("CollectBaseline = (%+v, %v)", result, err)
	}
	if len(workspace.commands) != baselineCacheCommandCount {
		t.Fatalf("commands = %d, want vet, build, a compile, and one target run", len(workspace.commands))
	}
	for index, command := range workspace.commands[:3] {
		if !slices.Contains(command.Env, "GOCACHE=fallback") || !slices.Contains(command.Env, "GOCACHEPROG=program --persist") {
			t.Errorf("command %d %q = %q, want the persisting program", index, command.Argv, command.Env)
		}
	}
	targetRun := workspace.commands[3]
	if filepath.Base(targetRun.Argv[0]) != binaryName("fixture.example/module") || !slices.Contains(targetRun.Argv, "-test.v=test2json") {
		t.Fatalf("the fourth command was %q, want the directly executed framed target", targetRun.Argv)
	}
	if !slices.Contains(targetRun.Env, "GOCACHE=native") || !slices.Contains(targetRun.Env, "GOCACHEPROG=") {
		t.Fatalf("target environment = %q, want the projected native cache", targetRun.Env)
	}
}

func TestBuildCacheWorkspaceUsesNativeCacheForWholeSuitesAndRaceRuns(t *testing.T) {
	t.Parallel()
	cache := runBuildCache{fallback: "fallback", native: "native", plain: "program", persisting: "program --persist"}
	inner := &recordingWorkspace{}
	wrapped := withBuildCache(inner, cache)
	commands := []gomutants.Command{
		{Argv: []string{"binary", "-test.coverprofile=suite.cover", "-test.count=1"}},
		{Argv: []string{"go", "test", "-race", "-count=1", "./..."}},
		{Argv: []string{"binary", "-test.run=^TestValue$"}},
	}
	for _, command := range commands {
		if _, err := wrapped.Exec(t.Context(), command); err != nil {
			t.Fatal(err)
		}
	}
	for index, command := range inner.commands[:2] {
		if !slices.Contains(command.Env, "GOCACHE=native") || !slices.Contains(command.Env, "GOCACHEPROG=") {
			t.Errorf("command %d environment = %q, want the native run cache", index, command.Env)
		}
	}
	if command := inner.commands[2]; !slices.Contains(command.Env, "GOCACHE=fallback") || !slices.Contains(command.Env, "GOCACHEPROG=program") {
		t.Errorf("unclassified direct binary environment = %q, want the bounded external fallback", command.Env)
	}
}

func TestOnlyProjectExecutingCommandsUseTheProjectedNativeCache(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		argv []string
		want bool
	}{
		{argv: []string{"binary", "-test.run=^TestValue$", "-test.coverprofile=value.cover"}, want: true},
		{argv: []string{"binary", "-test.v=test2json", "-test.coverprofile=value.cover", "-test.run=^TestValue$"}, want: true},
		{argv: []string{"binary", "-test.coverprofile=suite.cover"}, want: true},
		{argv: []string{"binary", "-test.run=^TestValue$"}},
		{argv: []string{"go", "test", "-run=TestValue", "-coverprofile=value.cover", "./..."}, want: true},
		{argv: []string{"go", "test", "-c", "./pkg"}},
		{argv: []string{"go", "tool", "test2json", "binary"}},
		{argv: nil},
	} {
		if got := nativeExecutionCommand(test.argv); got != test.want {
			t.Errorf("nativeExecutionCommand(%q) = %t, want %t", test.argv, got, test.want)
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
			cache, err := openRunBuildCache(test.program, test.base, "", runScratch{dir: t.TempDir()}, 2<<30)
			if err != nil || cache.serves() || cache.environment() != nil || cache.persistingEnvironment() != nil {
				t.Fatalf("openRunBuildCache = (%+v, %v), want a cache that serves nothing", cache, err)
			}
			if summary := cache.summarize(); summary != "" {
				t.Fatalf("summary = %q, want nothing to report", summary)
			}
			if err := releaseBuildCache(Options{}, cache, runScratch{}, time.Now()); err != nil {
				t.Fatalf("releaseBuildCache = %v", err)
			}
		})
	}
}

func TestOpenRunBuildCacheRefusesAnUnownedScratchTopology(t *testing.T) {
	t.Parallel()
	cache, err := openRunBuildCache("/opt/goatest", t.TempDir(), "", runScratch{}, 0)
	if err == nil || cache.serves() || !strings.Contains(err.Error(), "run scratch is unavailable") {
		t.Fatalf("openRunBuildCache = (%+v, %v), want the ownership failure", cache, err)
	}
}

func TestOpenRunBuildCacheRendersBothProgramsAndRemovesOnlyItsScratch(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	temporary := t.TempDir()
	cache, err := openRunBuildCache("/opt/goatest", base, "", runScratch{dir: temporary}, 2<<30)
	if err != nil || !cache.serves() {
		t.Fatalf("openRunBuildCache = (%+v, %v)", cache, err)
	}
	if !strings.Contains(cache.persisting, "--persist") || strings.Contains(cache.plain, "--persist") {
		t.Fatalf("programs = plain %q persisting %q", cache.plain, cache.persisting)
	}
	if !strings.HasPrefix(cache.scratch, temporary) {
		t.Fatalf("scratch = %q, want it below %q", cache.scratch, temporary)
	}
	if filepath.Dir(cache.native) != filepath.Dir(base) || !ownedNativeProjection(cache) || cache.native == cache.scratch {
		t.Fatalf("native cache = %q, want a separate owned directory beside base %q", cache.native, base)
	}
	if filepath.Dir(cache.fallback) != cache.scratch || filepath.Base(cache.fallback) != goCacheScratchName {
		t.Fatalf("external backing cache = %q, want it inside run scratch %q", cache.fallback, cache.scratch)
	}
	if got := cache.environment(); !slices.Equal(got, []string{"GOCACHE=" + cache.fallback, "GOCACHEPROG=" + cache.plain}) {
		t.Fatalf("non-persisting environment = %q", got)
	}

	if _, err := os.Stat(filepath.Join(base, buildcache.MarkerName)); err != nil {
		t.Fatalf("base layer = %v, want it prepared", err)
	}

	if !strings.Contains(cache.plain, "--max-bytes") || !strings.Contains(cache.persisting, "--max-bytes") {
		t.Fatalf("programs = plain %q persisting %q, want the bound on both", cache.plain, cache.persisting)
	}
	if err := releaseBuildCache(Options{}, cache, runScratch{}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(cache.scratch); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("scratch after close = %v, want it gone", err)
	}
	if _, err := os.Stat(cache.native); cache.nativeShared == errors.Is(err, os.ErrNotExist) {
		t.Fatalf("native cache after close = %v, shared %t", err, cache.nativeShared)
	}
	if _, err := os.Stat(base); err != nil {
		t.Fatalf("base after close = %v, want the layer the machine keeps left alone", err)
	}
}

func TestEmptyBuildCachePreparationImportsIntoThePersistentLayer(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	source := t.TempDir()
	cache, err := openRunBuildCache("/opt/goatest", base, source, runScratch{dir: t.TempDir()}, 2<<30)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cache.close(false) })
	environment := cache.preparationEnvironment()
	if !slices.Contains(environment, "GOCACHE="+cache.fallback) {
		t.Fatalf("preparation environment = %q, want the bounded backing directory", environment)
	}
	var program string
	for _, entry := range environment {
		if strings.HasPrefix(entry, "GOCACHEPROG=") {
			program = strings.TrimPrefix(entry, "GOCACHEPROG=")
		}
	}
	if !strings.Contains(program, "--persist") || !strings.Contains(program, "--native-source "+source) {
		t.Fatalf("preparation program = %q, want persistent on-demand native imports", program)
	}
	if err := releaseBuildCache(Options{}, cache, runScratch{}, time.Now()); err != nil {
		t.Fatal(err)
	}
}

func TestProjectExecutionProjectsTheBaseIntoTheNativeCache(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	cache, err := openRunBuildCache("/opt/goatest", base, "", runScratch{dir: t.TempDir()}, 2<<30)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cache.close(false) })
	action, output := buildCacheTestKey(1), buildCacheTestKey(0x21)
	body := "compiled archive"
	if _, err := (buildcache.Layers{Base: buildcache.Layer{Dir: base}, Persist: true}).Put(
		action, output, strings.NewReader(body), int64(len(body)), time.Now(),
	); err != nil {
		t.Fatal(err)
	}
	inner := &recordingWorkspace{}
	if _, err := withBuildCache(inner, cache).Exec(t.Context(), gomutants.Command{
		Argv: []string{"project.test", "-test.coverprofile=target.cover"},
	}); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(inner.commands[0].Env, "GOCACHE="+cache.native) || !slices.Contains(inner.commands[0].Env, "GOCACHEPROG=") {
		t.Fatalf("project environment = %q, want the native cache", inner.commands[0].Env)
	}
	baseInfo, err := os.Stat(buildCacheObjectPath(base, 0x21))
	if err != nil {
		t.Fatal(err)
	}
	name := hex.EncodeToString(output)
	nativeInfo, err := os.Stat(filepath.Join(cache.native, name[:2], name+"-d"))
	if err != nil || !os.SameFile(baseInfo, nativeInfo) {
		t.Fatalf("native output = (%v, %v), want a hard link to the base", nativeInfo, err)
	}
	if summary := cache.summarize(); !strings.Contains(summary, "native-seed=ready native-actions=1 native-objects=1 native-bytes=16") {
		t.Fatalf("summary = %q, want the successful projection measured", summary)
	}
	if err := releaseBuildCache(Options{}, cache, runScratch{}, time.Now()); err != nil {
		t.Fatal(err)
	}
}

func TestMutationPreparationUsesTheNativeProjection(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	cache, err := openRunBuildCache("/opt/goatest", base, "", runScratch{dir: t.TempDir()}, 2<<30)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cache.close(false) })
	defer func() {
		if err := releaseBuildCache(Options{}, cache, runScratch{}, time.Now()); err != nil {
			t.Error(err)
		}
	}()
	action, output := buildCacheTestKey(1), buildCacheTestKey(0x21)
	body := "compiled archive"
	if _, err := (buildcache.Layers{Base: buildcache.Layer{Dir: base}, Persist: true}).Put(
		action, output, strings.NewReader(body), int64(len(body)), time.Now(),
	); err != nil {
		t.Fatal(err)
	}
	environment := cache.preparationEnvironment()
	if !slices.Equal(environment, []string{"GOCACHE=" + cache.native, "GOCACHEPROG="}) {
		t.Fatalf("preparation environment = %q, want the native projection", environment)
	}
	name := hex.EncodeToString(output)
	if _, err := os.Stat(filepath.Join(cache.native, name[:2], name+"-d")); err != nil {
		t.Fatalf("native preparation object = %v", err)
	}
}

func TestMutationPreparationFallsBackFromAnUntrustworthyProjection(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	cache, err := openRunBuildCache("/opt/goatest", base, "", runScratch{dir: t.TempDir()}, 2<<30)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cache.close(false) })
	defer func() {
		if err := releaseBuildCache(Options{}, cache, runScratch{}, time.Now()); err != nil {
			t.Error(err)
		}
	}()
	action, output := buildCacheTestKey(1), buildCacheTestKey(0x21)
	body := "trusted object"
	if _, err := (buildcache.Layers{Base: buildcache.Layer{Dir: base}, Persist: true}).Put(
		action, output, strings.NewReader(body), int64(len(body)), time.Now(),
	); err != nil {
		t.Fatal(err)
	}
	name := hex.EncodeToString(output)
	directory := filepath.Join(cache.native, name[:2])
	if err := os.MkdirAll(directory, filemode.PrivateDirectory); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, name+"-d"), []byte("untrusted"), filemode.PrivateFile); err != nil {
		t.Fatal(err)
	}
	if environment := cache.preparationEnvironment(); !slices.Equal(environment, []string{
		"GOCACHE=" + cache.fallback, "GOCACHEPROG=" + cache.persisting,
	}) {
		t.Fatalf("preparation environment = %q, want the persistent fallback", environment)
	}
}

func TestPersistentCommandRefreshesTheProjectionBeforeTheNextExecution(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	cache, err := openRunBuildCache("/opt/goatest", base, "", runScratch{dir: t.TempDir()}, 2<<30)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cache.close(false) })
	defer func() {
		if err := releaseBuildCache(Options{}, cache, runScratch{}, time.Now()); err != nil {
			t.Error(err)
		}
	}()
	layers := buildcache.Layers{Base: buildcache.Layer{Dir: base}, Persist: true}
	if _, err := layers.Put(buildCacheTestKey(1), buildCacheTestKey(0x21), strings.NewReader("first"), 5, time.Now()); err != nil {
		t.Fatal(err)
	}
	inner := &recordingWorkspace{}
	wrapper := withBuildCache(inner, cache)
	if _, err := wrapper.Exec(t.Context(), gomutants.Command{Argv: []string{"project.test", "-test.coverprofile=first.cover"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := layers.Put(buildCacheTestKey(2), buildCacheTestKey(0x22), strings.NewReader("second"), 6, time.Now()); err != nil {
		t.Fatal(err)
	}

	if _, err := wrapper.Exec(t.Context(), gomutants.Command{Argv: []string{"go", "test", "-race", "-c", "-o", os.DevNull, "./..."}}); err != nil {
		t.Fatal(err)
	}
	if _, err := wrapper.Exec(t.Context(), gomutants.Command{Argv: []string{"go", "test", "-race", "-count=1", "./..."}}); err != nil {
		t.Fatal(err)
	}
	secondName := hex.EncodeToString(buildCacheTestKey(0x22))
	if _, err := os.Stat(filepath.Join(cache.native, secondName[:2], secondName+"-d")); err != nil {
		t.Fatalf("persistent generation was not projected before execution: %v", err)
	}
	if summary := cache.summarize(); !strings.Contains(summary, "native-actions=2 native-objects=2") {
		t.Fatalf("refreshed projection summary = %q, want both persistent generations", summary)
	}
}

func TestPersistPreparationRequiresAndUpdatesAReadyProjection(t *testing.T) {
	sentinel := errors.New("not attempted")
	unavailable := &nativeCacheProjection{persistErr: sentinel}
	runBuildCache{projection: unavailable}.persistPreparation()
	if unavailable.persistErr != sentinel {
		t.Fatalf("unavailable persistence error = %v", unavailable.persistErr)
	}
	runBuildCache{plain: "program", native: "native"}.persistPreparation()

	base := t.TempDir()
	if err := (buildcache.Layer{Dir: base}).Prepare(); err != nil {
		t.Fatal(err)
	}
	projection := &nativeCacheProjection{persistErr: sentinel}
	cache := runBuildCache{base: base, native: t.TempDir(), plain: "program", projection: projection}
	cache.persistPreparation()
	projection.mutex.Lock()
	attempted := projection.attempted
	persistErr := projection.persistErr
	projection.mutex.Unlock()
	if !attempted || persistErr != nil {
		t.Fatalf("ready persistence = (attempted=%t, err=%v)", attempted, persistErr)
	}
}

func TestRecordNativePersistenceAccumulatesEveryMeasurement(t *testing.T) {
	const (
		initialMeasurement   = 2
		persistedMeasurement = 3
		totalMeasurement     = initialMeasurement + persistedMeasurement
	)
	projection := &nativeCacheProjection{
		seed:      buildcache.NativeSeed{Actions: initialMeasurement, Objects: initialMeasurement, Bytes: initialMeasurement, Skipped: initialMeasurement},
		persisted: buildcache.NativePersisted{Actions: initialMeasurement, Objects: initialMeasurement, Bytes: initialMeasurement, Skipped: initialMeasurement},
		collected: buildcache.NativeCollected{BeforeBytes: initialMeasurement, AfterBytes: initialMeasurement},
	}
	seed := projection.seed
	persisted := buildcache.NativePersisted{
		Actions: persistedMeasurement, Objects: persistedMeasurement, Bytes: persistedMeasurement, Skipped: persistedMeasurement, Deferred: true,
	}
	recordNativePersistence(projection, seed, persisted, nil)
	if projection.persisted.Actions != totalMeasurement || projection.persisted.Objects != totalMeasurement || projection.persisted.Bytes != totalMeasurement ||
		projection.persisted.Skipped != totalMeasurement || !projection.persisted.Deferred || projection.persistErr != nil {
		t.Fatalf("persisted summary = %+v, err = %v", projection.persisted, projection.persistErr)
	}
	if projection.seed.Actions != totalMeasurement || projection.seed.Objects != totalMeasurement || projection.seed.Bytes != totalMeasurement || projection.seed.Skipped != totalMeasurement {
		t.Fatalf("updated seed = %+v", projection.seed)
	}
	if projection.collected.BeforeBytes != totalMeasurement || projection.collected.AfterBytes != totalMeasurement {
		t.Fatalf("updated collection = %+v", projection.collected)
	}
}

func TestProjectExecutionFallsBackWhenTheNativeProjectionIsNotTrustworthy(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	cache, err := openRunBuildCache("/opt/goatest", base, "", runScratch{dir: t.TempDir()}, 2<<30)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cache.close(false) })
	action, output := buildCacheTestKey(1), buildCacheTestKey(0x21)
	body := "trusted object"
	if _, err := (buildcache.Layers{Base: buildcache.Layer{Dir: base}, Persist: true}).Put(
		action, output, strings.NewReader(body), int64(len(body)), time.Now(),
	); err != nil {
		t.Fatal(err)
	}
	name := hex.EncodeToString(output)
	directory := filepath.Join(cache.native, name[:2])
	if err := os.MkdirAll(directory, filemode.PrivateDirectory); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, name+"-d"), []byte("wrong contents"), filemode.PrivateFile); err != nil {
		t.Fatal(err)
	}
	inner := &recordingWorkspace{}
	if _, err := withBuildCache(inner, cache).Exec(t.Context(), gomutants.Command{
		Argv: []string{"project.test", "-test.coverprofile=target.cover"},
	}); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(inner.commands[0].Env, "GOCACHEPROG="+cache.plain) || slices.Contains(inner.commands[0].Env, "GOCACHEPROG=") {
		t.Fatalf("project environment = %q, want the bounded external fallback", inner.commands[0].Env)
	}
	if summary := cache.summarize(); !strings.Contains(summary, "native-seed=fallback") {
		t.Fatalf("summary = %q, want the projection fallback reported", summary)
	}
	if err := releaseBuildCache(Options{}, cache, runScratch{}, time.Now()); err != nil {
		t.Fatal(err)
	}
}

func TestBuildCacheCloseToleratesAnUnavailableNativeOwner(t *testing.T) {
	t.Parallel()
	for _, keep := range []bool{false, true} {
		t.Run(strconv.FormatBool(keep), func(t *testing.T) {
			t.Parallel()
			scratch := t.TempDir()
			cache := runBuildCache{
				scratch: scratch, plain: "fallback-program",
				projection: &nativeCacheProjection{},
			}
			if err := cache.close(keep); err != nil {
				t.Fatal(err)
			}
			_, err := os.Stat(scratch)
			if keep && err != nil {
				t.Fatalf("kept external scratch = %v, want it preserved", err)
			}
			if !keep && !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("closed external scratch = %v, want it removed", err)
			}
		})
	}
}

func TestPreparedMutationExecutionsUseTheProjectedNativeCache(t *testing.T) {
	t.Parallel()
	underlying := &mutationUnitSession{catalog: gomutants.Catalog{Digest: "catalog-digest"}}
	wrapped := withNativeBuildCache(underlying, runBuildCache{
		fallback: "fallback", native: "native", plain: "program", persisting: "program --persist",
	})
	if wrapped == MutationSession(underlying) {
		t.Fatal("withNativeBuildCache returned the unwrapped session")
	}
	if _, err := wrapped.Exec(t.Context(), gomutants.ExecRequest{
		Mutant: "mutant", Env: []string{"gocache=stale", "GOCACHEPROG=stale", "RESOURCE=ready"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := wrapped.Probe(t.Context(), gomutants.ProbeRequest{
		Env: []string{"RESOURCE=ready"},
	}); err != nil {
		t.Fatal(err)
	}
	requests := underlying.requests
	if len(requests) != 1 || !slices.Equal(requests[0].Env, []string{"RESOURCE=ready", "GOCACHE=native", "GOCACHEPROG="}) {
		t.Fatalf("mutant environment = %q, want resources and the native cache", requests[0].Env)
	}
	probes := underlying.probeRequests()
	if len(probes) != 1 || !slices.Equal(probes[0].Env, []string{"RESOURCE=ready", "GOCACHE=native", "GOCACHEPROG="}) {
		t.Fatalf("probe environment = %q, want resources and the native cache", probes[0].Env)
	}
	if got := wrapped.Catalog(); got.Digest != underlying.catalog.Digest {
		t.Fatalf("wrapped catalog = %+v, want the underlying catalog", got)
	}

	fallbackProjection := &nativeCacheProjection{}
	fallbackProjection.once.Do(func() {
		fallbackProjection.attempted = true
		fallbackProjection.err = errors.New("projection unavailable")
	})
	fallbackSession := &mutationUnitSession{}
	fallbackWrapped := withNativeBuildCache(fallbackSession, runBuildCache{
		fallback: "fallback", native: "native", plain: "program",
		projection: fallbackProjection,
	})
	if _, err := fallbackWrapped.Exec(t.Context(), gomutants.ExecRequest{
		Mutant: "mutant", Env: []string{"GOCACHE=stale", "GOCACHEPROG=stale", "RESOURCE=ready"},
	}); err != nil {
		t.Fatal(err)
	}
	if got := fallbackSession.requests[0].Env; !slices.Equal(got, []string{"RESOURCE=ready", "GOCACHE=fallback", "GOCACHEPROG=program"}) {
		t.Fatalf("mutant fallback environment = %q, want independent external backing", got)
	}

	noNativeSession := &mutationUnitSession{}
	noNativeWrapped := withNativeBuildCache(noNativeSession, runBuildCache{
		fallback: "fallback", plain: "program", persisting: "program --persist",
	})
	if noNativeWrapped == MutationSession(noNativeSession) {
		t.Fatal("a cache without a native directory left executions unwrapped")
	}
	if _, err := noNativeWrapped.Exec(t.Context(), gomutants.ExecRequest{
		Mutant: "mutant", Env: []string{"GOCACHE=fallback", "GOCACHEPROG=program --persist", "RESOURCE=ready"},
	}); err != nil {
		t.Fatal(err)
	}
	if got := noNativeSession.requests[0].Env; !slices.Equal(got, []string{"RESOURCE=ready", "GOCACHE=fallback", "GOCACHEPROG=program"}) {
		t.Fatalf("no-native mutant environment = %q, want the bounded external fallback", got)
	}
}

func TestNativeCollectionDrainsActiveExecutionsBeforeRemovingAnything(t *testing.T) {
	base := t.TempDir()
	cache, err := openRunBuildCache("/opt/goatest", base, "", runScratch{dir: t.TempDir()}, 2<<30)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cache.close(false) })
	defer func() {
		if err := releaseBuildCache(Options{}, cache, runScratch{}, time.Now()); err != nil {
			t.Error(err)
		}
	}()
	firstRelease, native := cache.beginNative()
	if !native {
		t.Fatal("first native execution fell back")
	}
	cache.projection.mutex.Lock()
	cache.projection.lastCollect = time.Now().Add(-buildcache.NativeCollectInterval - time.Second)
	cache.projection.mutex.Unlock()
	drained := false
	cache.projection.beforeDrain = func() {
		if drained {
			panic("repeated drain")
		}
		drained = true
		firstRelease()
	}
	secondRelease, native := cache.beginNative()
	if !native || !drained {
		t.Fatalf("second native execution = admitted %t, drained %t", native, drained)
	}
	secondRelease()
}

func TestNativeCollectionFailureClosesAdmissionAndFallsBack(t *testing.T) {
	base := t.TempDir()
	cache, err := openRunBuildCache("/opt/goatest", base, "", runScratch{dir: t.TempDir()}, 2<<30)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cache.close(false) })
	release, native := cache.beginNative()
	if !native {
		t.Fatal("initial native execution fell back")
	}
	release()

	if err := cache.nativeOwner.Release(); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(cache.native); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cache.native, []byte("not a cache directory"), filemode.PrivateFile); err != nil {
		t.Fatal(err)
	}
	cache.projection.mutex.Lock()
	cache.projection.lastCollect = time.Now().Add(-buildcache.NativeCollectInterval - time.Second)
	cache.projection.mutex.Unlock()
	if release, admitted := cache.beginNative(); admitted {
		release()
		t.Fatal("native execution entered after collection failed")
	}
	if release, admitted := cache.beginNative(); admitted {
		release()
		t.Fatal("native execution re-entered after the fallback became sticky")
	}
	inner := &recordingWorkspace{}
	if _, err := withBuildCache(inner, cache).Exec(t.Context(), gomutants.Command{
		Argv: []string{"project.test", "-test.coverprofile=target.cover"},
	}); err != nil {
		t.Fatal(err)
	}
	if got := inner.commands[0].Env; !slices.Equal(got, []string{"GOCACHE=" + cache.fallback, "GOCACHEPROG=" + cache.plain}) {
		t.Fatalf("collection fallback environment = %q, want independent external backing", got)
	}
	if summary := cache.summarize(); !strings.Contains(summary, "native-seed=collection-failed") {
		t.Fatalf("summary = %q, want the collection fallback reported", summary)
	}
}

func TestOpenRunBuildCacheReportsABaseLayerItCannotPrepare(t *testing.T) {
	t.Parallel()
	blocked := filepath.Join(t.TempDir(), "occupied")
	if err := os.WriteFile(blocked, []byte("not a directory"), filemode.PrivateFile); err != nil {
		t.Fatal(err)
	}
	cache, err := openRunBuildCache("/opt/goatest", blocked, "", runScratch{dir: t.TempDir()}, 2<<30)
	if err == nil || cache.serves() {
		t.Fatalf("openRunBuildCache = (%+v, %v), want the unusable layer reported", cache, err)
	}
}

func TestOpenRunBuildCacheRemovesTheScratchItCannotRenderAProgramFor(t *testing.T) {
	t.Parallel()
	temporary := t.TempDir()

	cache, err := openRunBuildCache(`/opt/o'say"what/goatest`, t.TempDir(), "", runScratch{dir: temporary}, 2<<30)
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

func TestCollectBaseBoundsTheLayerTheMachineKeeps(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	cache, err := openRunBuildCache("/opt/goatest", base, "", runScratch{dir: t.TempDir()}, 20)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cache.close(false) })
	layer := buildcache.Layer{Dir: base}
	layers := buildcache.Layers{Base: layer, Persist: true}
	moment := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	for _, key := range []byte{1, 2, 3} {
		if _, err := layers.Put(buildCacheTestKey(key), buildCacheTestKey(key+0x10),
			strings.NewReader("0123456789"), 10, moment); err != nil {
			t.Fatal(err)
		}
	}

	for _, key := range []byte{1, 2} {
		aged := moment.Add(-90 * 24 * time.Hour)
		if err := os.Chtimes(buildCacheActionPath(base, key), aged, aged); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(buildCacheObjectPath(base, key+0x10), aged, aged); err != nil {
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
	cache, err := openRunBuildCache("/opt/goatest", base, "", runScratch{dir: t.TempDir()}, 20)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cache.close(false) })

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

func buildCacheTestKey(value byte) []byte {
	identifier := make([]byte, sha256.Size)
	for index := range identifier {
		identifier[index] = value
	}
	return identifier
}

func buildCacheActionPath(dir string, key byte) string {
	name := hex.EncodeToString(buildCacheTestKey(key))
	return filepath.Join(dir, "actions", name[:2], name)
}

func buildCacheObjectPath(dir string, key byte) string {
	name := hex.EncodeToString(buildCacheTestKey(key))
	return filepath.Join(dir, "objects", name[:2], name)
}

func TestReleaseBuildCacheKeepsAndNamesTheScratchItWasAskedToKeep(t *testing.T) {
	t.Parallel()
	cache, err := openRunBuildCache("/opt/goatest", t.TempDir(), "", runScratch{dir: t.TempDir()}, 2<<30)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cache.close(false) })
	if err := releaseBuildCache(Options{KeepTemp: true}, cache, runScratch{}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(cache.scratch); err != nil {
		t.Fatalf("scratch after a kept close = %v, want it left where it was made", err)
	}
	if _, err := os.Stat(cache.native); err != nil {
		t.Fatalf("native cache after a kept close = %v, want it left where it was made", err)
	}
}

func TestReleaseBuildCacheBoundsANativeCacheBeforeKeepingIt(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	cache, err := openRunBuildCache("/opt/goatest", base, "", runScratch{dir: t.TempDir()}, 10)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cache.close(false) })
	layers := buildcache.Layers{Base: buildcache.Layer{Dir: base}, Persist: true}
	for index := byte(1); index <= 2; index++ {
		if _, err := layers.Put(buildCacheTestKey(index), buildCacheTestKey(index+0x20),
			strings.NewReader("0123456789"), 10, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	inner := &recordingWorkspace{}
	if _, err := withBuildCache(inner, cache).Exec(t.Context(), gomutants.Command{
		Argv: []string{"project.test", "-test.coverprofile=target.cover"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := releaseBuildCache(Options{KeepTemp: true}, cache, runScratch{}, time.Now()); err != nil {
		t.Fatal(err)
	}
	status, err := buildcache.CollectNative(cache.native, 0)
	if err != nil || status.BeforeBytes > 10 {
		t.Fatalf("kept native cache = (%+v, %v), want it inside the configured bound", status, err)
	}
}

func ownedNativeProjection(cache runBuildCache) bool {
	if cache.nativeOwner == nil {
		return false
	}
	name := filepath.Base(cache.native)
	return strings.HasPrefix(name, buildcache.NativeDirectoryPrefix) ||
		strings.HasPrefix(name, buildcache.NativeSharedDirectoryPrefix)
}

func TestASecondRunReusesTheSharedNativeProjectionUnlessItIsHeld(t *testing.T) {
	t.Parallel()
	parent := t.TempDir()
	base := filepath.Join(parent, "build")
	first, err := openRunBuildCache("/opt/goatest", base, "", runScratch{dir: t.TempDir()}, 2<<30)
	if err != nil || !first.serves() {
		t.Fatalf("openRunBuildCache = (%+v, %v)", first, err)
	}
	if !first.nativeShared || filepath.Base(first.native) != buildcache.NativeSharedDirectoryName(base) {
		t.Fatalf("first native cache = %q, shared %t; want the shared projection", first.native, first.nativeShared)
	}

	concurrent, err := openRunBuildCache("/opt/goatest", base, "", runScratch{dir: t.TempDir()}, 2<<30)
	if err != nil || !concurrent.serves() {
		t.Fatalf("concurrent openRunBuildCache = (%+v, %v)", concurrent, err)
	}
	if concurrent.nativeShared || concurrent.native == first.native {
		t.Fatalf("concurrent native cache = %q, shared %t; want a private projection", concurrent.native, concurrent.nativeShared)
	}
	if err := concurrent.close(false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(concurrent.native); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("private projection after close = %v, want it gone", err)
	}

	if err := first.close(false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(first.native); err != nil {
		t.Fatalf("shared projection after close = %v, want it kept for the next run", err)
	}

	again, err := openRunBuildCache("/opt/goatest", base, "", runScratch{dir: t.TempDir()}, 2<<30)
	if err != nil || again.native != first.native || !again.nativeShared {
		t.Fatalf("second run native cache = %q (%v), want the same shared projection", again.native, err)
	}
	if err := again.close(false); err != nil {
		t.Fatal(err)
	}
}
