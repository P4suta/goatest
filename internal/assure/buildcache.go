// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package assure

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	gomutants "github.com/P4suta/go-mutants"
	"github.com/P4suta/goatest/internal/buildcache"
	"github.com/P4suta/goatest/internal/config"
)

// cacheProgramVariable is the environment variable the go command reads the
// cache program from. It is named here and nowhere else.
const cacheProgramVariable = "GOCACHEPROG"

// runBuildCache is the build cache one run serves every go command it starts
// from: a scratch layer that dies with the run, and the base layer this machine
// keeps between runs.
//
// Its zero value serves nothing, which is a run that uses the toolchain's own
// cache exactly as it did before this layer existed. That is what a caller who
// named no program gets, and what a run gets when the cache could not be
// opened: a build cache is an optimization, and an optimization that cannot
// start is never a reason to fail a run.
type runBuildCache struct {
	// scratch is the layer this run removes when it ends.
	scratch string
	// base is the layer this machine keeps.
	base string
	// plain is the GOCACHEPROG value whose writes land in scratch.
	plain string
	// persisting is the GOCACHEPROG value whose writes land in base.
	persisting string
	// maxBytes is the bound both layers are collected to.
	maxBytes int64
}

// openRunBuildCache prepares both layers and renders the two cache programs one
// run needs. An empty program or base directory opens the zero cache, which
// serves nothing.
//
// Preparing the layers here rather than in the served process is deliberate: a
// layer that cannot be created or written is discovered by the run, which can
// carry on without a cache, instead of by a go command, which would fail the
// build it was in the middle of.
func openRunBuildCache(program, base, temporaryRoot string, maxBytes int64) (runBuildCache, error) {
	if program == "" || base == "" {
		return runBuildCache{}, nil
	}
	if err := (buildcache.Layer{Dir: base}).Prepare(); err != nil {
		return runBuildCache{}, err
	}
	scratch, err := os.MkdirTemp(temporaryRoot, "goatest-build-cache-")
	if err != nil {
		return runBuildCache{}, fmt.Errorf("goatest: create build cache scratch: %w", err)
	}
	// Everything from here on may fail, and the directory above already exists,
	// so every exit removes it. A run that ends without a cache must also end
	// without the empty layer it made on the way to not having one.
	discard := func(err error) (runBuildCache, error) {
		return runBuildCache{}, errors.Join(err, removeBuildCacheScratch(scratch))
	}
	if err := (buildcache.Layer{Dir: scratch, Touch: buildcache.ScratchTouchInterval}).Prepare(); err != nil {
		return discard(err)
	}
	cache := runBuildCache{scratch: scratch, base: base, maxBytes: maxBytes}
	if cache.plain, err = buildcache.Program(program, base, scratch, false, maxBytes); err != nil {
		return discard(err)
	}
	if cache.persisting, err = buildcache.Program(program, base, scratch, true, maxBytes); err != nil {
		return discard(err)
	}
	return cache, nil
}

// removeBuildCacheScratch removes a scratch layer, naming what it could not.
func removeBuildCacheScratch(scratch string) error {
	if err := os.RemoveAll(scratch); err != nil {
		return fmt.Errorf("goatest: remove build cache scratch: %w", err)
	}
	return nil
}

// collectBase bounds the layer this machine keeps, and reports whether it ran.
//
// A run collects it once, when the run ends. A bound that only a command a
// developer remembers to type applies is not a bound, and this is the moment
// the run knows it has stopped compiling. It yields to another process already
// collecting — the layer is shared by every repository on the machine — and
// what makes that safe for the builds of those other repositories is MinIdle:
// anything read within the last touch interval is spared by construction.
func (cache runBuildCache) collectBase(policy buildcache.Policy, now time.Time) (buildcache.Collected, bool, error) {
	if !cache.serves() {
		return buildcache.Collected{}, false, nil
	}
	return buildcache.Layer{Dir: cache.base}.CollectLocked(policy, 0, now)
}

// collectRunBuildCache bounds the layer this machine keeps and reports what it
// did, at the end of one run. It is the only enforcement of build_max_bytes a
// developer never has to remember, which is what makes the setting a bound
// rather than a suggestion.
//
// Nothing here can fail a run: the run has finished, and a layer that could not
// be collected costs the disk and never the verdict.
func collectRunBuildCache(options Options, loaded config.Config, cache runBuildCache, now time.Time) {
	base := buildcache.Layer{Dir: cache.base}
	collected, ran, err := cache.collectBase(buildcache.Policy{
		MaxBytes: loaded.Cache.BuildMaxBytes, TTL: loaded.Cache.TTL, MinIdle: base.MinIdle(),
	}, now)
	switch {
	case err != nil:
		emit(options, "build-cache-unavailable", err.Error())
	case ran && collected.RemovedActions+collected.RemovedObjects > 0:
		emit(options, "build-cache-collected", fmt.Sprintf(
			"removed-actions=%d removed-objects=%d removed-bytes=%d remaining-bytes=%d",
			collected.RemovedActions, collected.RemovedObjects, collected.RemovedBytes, collected.After.Bytes))
	}
}

// planMoment is the clock a plan collects against. A plan has no round to
// timestamp, so it reads the one the caller supplied or the wall clock.
func planMoment(options Options) time.Time {
	if options.Now != nil {
		return options.Now()
	}
	return time.Now()
}

// serves reports whether this cache answers anything at all.
func (cache runBuildCache) serves() bool { return cache.plain != "" }

// environment is the overlay every go command a run starts carries. Its writes
// land in the scratch layer, so whatever a run compiles that the machine has no
// use for afterwards — mutants, candidate trees, and every fixture module a
// test suite compiles in a directory of its own — dies with the run.
func (cache runBuildCache) environment() []string {
	if !cache.serves() {
		return nil
	}
	return []string{cacheProgramVariable + "=" + cache.plain}
}

// persistingEnvironment is the overlay a command that compiles or lists carries
// instead. Its writes land in the base layer, which is what makes the standard
// library and the project's dependencies survive into the next run.
func (cache runBuildCache) persistingEnvironment() []string {
	if !cache.serves() {
		return nil
	}
	return []string{cacheProgramVariable + "=" + cache.persisting}
}

// summarize reports what the go commands of this run asked the cache for. An
// unreadable record summarizes to nothing: the record is a progress note, and
// no note is worth failing a finished run over.
func (cache runBuildCache) summarize() string {
	if !cache.serves() {
		return ""
	}
	stats, err := buildcache.Summarize(cache.scratch)
	if err != nil {
		return ""
	}
	return stats.Detail()
}

// close removes the scratch layer. A run asked to keep its temporary
// directories keeps this one too, and names it, because a directory left
// behind and never named is litter rather than something a developer can find.
func (cache runBuildCache) close(keep bool) error {
	if !cache.serves() || keep {
		return nil
	}
	return removeBuildCacheScratch(cache.scratch)
}

// buildCacheWorkspace attaches the build cache to every command a run issues.
//
// The rule it applies is the whole of the policy, and it lives in one place so
// that no call site can get it wrong:
//
//   - A command that compiles or lists writes into the base layer this machine
//     keeps, so that the standard library, the dependencies, and the project's
//     own packages are compiled once rather than once per run.
//   - Every other command writes into the run's scratch layer, which is
//     removed when the run ends.
//
// The second half is not a detail. A baseline target is the project's own test
// binary, and a test suite spawns go commands of its own: goatest's does, and
// so does every suite with a fixture module, a golden build, or a `go list`
// under test. Those children inherit the cache program of the process that
// started them. Were a target run to carry the persisting program, every
// throwaway package those fixtures compile would be written into the base
// layer and would evict the standard library the layer exists to hold — the
// cache would grow without bound and get slower the more it was used.
//
// Only the workspace of a run is wrapped. The validator opens a workspace of
// its own for each candidate tree, and it is never wrapped: a candidate is a
// tree that does not exist yet and may never be applied, so nothing it
// compiles has earned a place in what the machine keeps.
type buildCacheWorkspace struct {
	workspace  CommandWorkspace
	persisting []string
}

// withBuildCache wraps a workspace so that its commands that compile or list
// reach the base layer. A cache that serves nothing wraps nothing, so a run
// without a cache runs exactly the commands it ran before.
func withBuildCache(workspace CommandWorkspace, cache runBuildCache) CommandWorkspace {
	if workspace == nil || !cache.serves() {
		return workspace
	}
	return buildCacheWorkspace{workspace: workspace, persisting: cache.persistingEnvironment()}
}

// Exec runs one command, having decided which layer its cache writes land in.
func (wrapper buildCacheWorkspace) Exec(ctx context.Context, command gomutants.Command) (gomutants.CommandResult, error) {
	if persistingCommand(command.Argv) {
		command.Env = overlayEnvironment(command.Env, wrapper.persisting)
	}
	return wrapper.workspace.Exec(ctx, command)
}

// persistingCommand reports whether a command may write into the base layer.
//
// Only a go command that compiles or lists may: vet, build, list, version, and
// a test that is compiled and not run. Nothing that runs the project's tests
// ever may, and the check reads the subcommand rather than the executable
// because the command that runs a baseline target begins with the go binary
// too: it is `go tool test2json` wrapped around the compiled test binary. That
// command is precisely the one whose children fill a cache with garbage, so
// treating every argv that starts with "go" as a compile would defeat the rule
// exactly where it matters most.
func persistingCommand(argv []string) bool {
	if len(argv) < 2 || !goExecutable(argv[0]) {
		return false
	}
	// -C changes the directory before the subcommand is read, and the go
	// command accepts it only as its first flag. A rule that read argv[1]
	// blindly would see "-C" and classify every such command as neither a
	// compile nor a test run, which fails silently: nothing would persist.
	first := 1
	switch {
	case argv[first] == "-C":
		first += 2
	case strings.HasPrefix(argv[first], "-C="):
		first++
	}
	if first >= len(argv) {
		return false
	}
	argv = argv[first:]
	switch argv[0] {
	case "vet", "build", "list", "version":
		return true
	case "test":
		// -c compiles the test binary and does not run it. Every other form of
		// go test runs the project's tests, including `go test -run=^$`, which
		// still starts the binary and every TestMain in it.
		//
		// Everything after -args belongs to the test binary, so a -c there is
		// an argument of the suite and not an instruction to the go command.
		return slices.Contains(argv[1:argumentSeparator(argv)], "-c")
	default:
		return false
	}
}

// argumentSeparator is the index of -args, the point after which a go test
// command line stops being the go command's and becomes the test binary's. A
// command line without one ends without ever reaching it.
func argumentSeparator(argv []string) int {
	if index := slices.Index(argv, "-args"); index >= 0 {
		return index
	}
	return len(argv)
}

// goExecutable reports whether a path names the go command. A run may have been
// given an absolute go binary, and Windows spells it with an extension.
func goExecutable(path string) bool {
	name := filepath.Base(path)
	return name == "go" || name == "go.exe"
}

// overlayEnvironment replaces the cache program of one command's environment
// overlay. An overlay the caller already filled in is kept: it carries the
// resources a target needs, and only the cache program is this layer's to say.
func overlayEnvironment(existing, overlay []string) []string {
	result := make([]string, 0, len(existing)+len(overlay))
	for _, entry := range existing {
		if key, _, ok := strings.Cut(entry, "="); ok && strings.ToUpper(key) == cacheProgramVariable {
			continue
		}
		result = append(result, entry)
	}
	return append(result, overlay...)
}
