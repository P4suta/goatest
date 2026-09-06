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
	"sync"
	"time"

	gomutants "github.com/P4suta/go-mutants"
	"github.com/P4suta/goatest/internal/buildcache"
	"github.com/P4suta/goatest/internal/config"
	"github.com/P4suta/goatest/internal/filemode"
	"github.com/P4suta/goatest/internal/tempowner"
)

const (
	cacheProgramVariable = "GOCACHEPROG"
	goCacheVariable      = "GOCACHE"
	minimumGoCommandArgs = 2
	goSubcommandStart    = 1
	goDirectoryFlagArgs  = 2
)

type runBuildCache struct {
	scratch string

	base   string
	source string

	fallback string

	native string

	nativeOwner *tempowner.Owner
	nativeSweep tempowner.Result

	projection *nativeCacheProjection

	plain string

	persisting string
	maxBytes   int64
}

type nativeCacheProjection struct {
	once        sync.Once
	admission   sync.Mutex
	executions  sync.WaitGroup
	mutex       sync.Mutex
	attempted   bool
	seed        buildcache.NativeSeed
	err         error
	disabled    bool
	beforeDrain func()
	collected   buildcache.NativeCollected
	persisted   buildcache.NativePersisted
	collectErr  error
	refreshErr  error
	persistErr  error
	lastCollect time.Time

	generation       uint64
	seededGeneration uint64
}

func openRunBuildCache(program, base, source string, runScratch runScratch, maxBytes int64) (runBuildCache, error) {
	if program == "" || base == "" {
		return runBuildCache{}, nil
	}
	if err := (buildcache.Layer{Dir: base}).Prepare(); err != nil {
		return runBuildCache{}, err
	}
	scratch, err := runScratch.buildCacheLayer()
	if err != nil {
		return runBuildCache{}, fmt.Errorf("goatest: create build cache scratch: %w", err)
	}

	discard := func(err error) (runBuildCache, error) {
		return runBuildCache{}, errors.Join(err, removeBuildCacheScratch(scratch))
	}
	if err := (buildcache.Layer{Dir: scratch, Touch: buildcache.ScratchTouchInterval}).Prepare(); err != nil {
		return discard(err)
	}
	fallback := filepath.Join(scratch, goCacheScratchName)
	if err := os.Mkdir(fallback, filemode.PrivateDirectory); err != nil {
		return discard(fmt.Errorf("goatest: create external build cache backing directory: %w", err))
	}
	cache := runBuildCache{
		scratch: scratch, base: base, source: source, fallback: fallback,
		projection: &nativeCacheProjection{beforeDrain: releaseUntrackedNativeExecution}, maxBytes: maxBytes,
	}
	if cache.plain, err = buildcache.Program(buildcache.ProgramOptions{
		Executable: program, Base: base, Scratch: scratch, NativeSource: source, MaxBytes: maxBytes,
	}); err != nil {
		return discard(err)
	}
	if cache.persisting, err = buildcache.Program(buildcache.ProgramOptions{
		Executable: program, Base: base, Scratch: scratch, NativeSource: source, Persist: true, MaxBytes: maxBytes,
	}); err != nil {
		return discard(err)
	}
	cache.native, cache.nativeOwner, cache.nativeSweep, err = openNativeBuildCache(base, runScratch, time.Now())
	if err != nil {
		cache.projection.once.Do(func() {
			cache.projection.attempted = true
			cache.projection.err = err
		})
	}
	return cache, nil
}

func openNativeBuildCache(base string, scratch runScratch, now time.Time) (string, *tempowner.Owner, tempowner.Result, error) {
	parent := filepath.Dir(base)
	swept, sweepErr := tempowner.Sweep(parent, []string{buildcache.NativeDirectoryPrefix}, now)
	directory, err := os.MkdirTemp(parent, buildcache.NativeDirectoryPrefix)
	if err != nil {
		return "", nil, swept, errors.Join(sweepErr, fmt.Errorf("goatest: create native build cache scratch: %w", err))
	}
	owner, err := tempowner.Claim(directory, tempowner.Marker{RunID: scratch.id, Root: scratch.root}, now)
	if err != nil {
		return "", nil, swept, errors.Join(sweepErr, fmt.Errorf("goatest: claim native build cache scratch: %w", err), removeBuildCacheScratch(directory))
	}
	if sweepErr != nil {
		swept.Errors = append(swept.Errors, sweepErr)
	}
	return directory, owner, swept, nil
}

func removeBuildCacheScratch(scratch string) error {
	if scratch == "" {
		return nil
	}
	if err := os.RemoveAll(scratch); err != nil {
		return fmt.Errorf("goatest: remove build cache scratch: %w", err)
	}
	return nil
}

func (cache runBuildCache) collectBase(policy buildcache.Policy, now time.Time) (buildcache.Collected, bool, error) {
	if !cache.serves() {
		return buildcache.Collected{}, false, nil
	}
	return buildcache.Layer{Dir: cache.base}.CollectLocked(policy, 0, now)
}

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

func planMoment(options Options) time.Time {
	if options.Now != nil {
		return options.Now()
	}
	return time.Now()
}

func (cache runBuildCache) serves() bool { return cache.plain != "" }

func (cache runBuildCache) needsPersistentCompile() bool {
	return cache.serves() && !cache.seedNative()
}

func (cache runBuildCache) environment() []string {
	if !cache.serves() {
		return nil
	}
	var result []string
	if cache.fallback != "" {
		result = append(result, goCacheVariable+"="+cache.fallback)
	}
	return append(result, cacheProgramVariable+"="+cache.plain)
}

func (cache runBuildCache) nativeEnvironment() []string {
	if !cache.serves() {
		return nil
	}
	if cache.native == "" {
		return []string{cacheProgramVariable + "=" + cache.plain}
	}
	return []string{goCacheVariable + "=" + cache.native, cacheProgramVariable + "="}
}

func (cache runBuildCache) preparationEnvironment() []string {
	if cache.seedNative() {
		if cache.projection == nil {
			return cache.nativeEnvironment()
		}
		cache.projection.mutex.Lock()
		hasActions := cache.projection.seed.Actions > 0
		cache.projection.mutex.Unlock()
		if hasActions {
			return cache.nativeEnvironment()
		}
		cache.markNativeDirty()
	}
	return cache.persistingEnvironment()
}

func (cache runBuildCache) seedNative() bool {
	if !cache.serves() || cache.native == "" {
		return false
	}

	if cache.projection == nil {
		return true
	}
	cache.projection.once.Do(func() {
		cache.projection.mutex.Lock()
		generation := cache.projection.generation
		cache.projection.mutex.Unlock()
		seed, err := buildcache.SeedNative(cache.base, cache.native, time.Now())
		now := time.Now()
		cache.projection.mutex.Lock()
		defer cache.projection.mutex.Unlock()
		cache.projection.attempted = true
		cache.projection.seed = seed
		cache.projection.err = err
		if err == nil {
			cache.projection.seededGeneration = generation
			cache.projection.lastCollect = now
			cache.projection.collected.BeforeBytes = seed.Bytes
			cache.projection.collected.AfterBytes = seed.Bytes
		}
	})
	cache.projection.mutex.Lock()
	err := cache.projection.err
	disabled := cache.projection.disabled
	cache.projection.mutex.Unlock()
	return err == nil && !disabled
}

type nativeExecutionRelease func()

func releaseUntrackedNativeExecution() {}

func (cache runBuildCache) beginNative() (nativeExecutionRelease, bool) {
	if !cache.seedNative() {
		return nil, false
	}
	if cache.projection == nil {
		return releaseUntrackedNativeExecution, true
	}
	projection := cache.projection
	projection.admission.Lock()
	defer projection.admission.Unlock()
	projection.mutex.Lock()
	if projection.disabled {
		projection.mutex.Unlock()
		return nil, false
	}
	now := time.Now()
	refresh := projection.seededGeneration != projection.generation
	collect := cache.maxBytes > 0 && (projection.lastCollect.IsZero() ||
		now.Sub(projection.lastCollect) >= buildcache.NativeCollectInterval)
	if refresh || collect {
		projection.mutex.Unlock()
		projection.beforeDrain()
		projection.executions.Wait()
		projection.mutex.Lock()
		if projection.seededGeneration != projection.generation {
			seed, err := buildcache.RefreshNative(cache.base, cache.native, time.Now())
			now = time.Now()
			projection.seed = seed
			projection.refreshErr = err
			if err != nil {
				projection.disabled = true
			} else {
				projection.seededGeneration = projection.generation
				projection.lastCollect = now
				projection.collected.BeforeBytes = max(projection.collected.BeforeBytes, seed.Bytes)
				projection.collected.AfterBytes = max(projection.collected.AfterBytes, seed.Bytes)
			}
		}
		cache.collectNativeLocked(false, time.Now())
	}
	if projection.disabled {
		projection.mutex.Unlock()
		return nil, false
	}
	projection.executions.Add(1)
	projection.mutex.Unlock()
	return projection.executions.Done, true
}

func (cache runBuildCache) markNativeDirty() {
	if cache.projection == nil || cache.native == "" {
		return
	}
	cache.projection.mutex.Lock()
	cache.projection.generation++
	cache.projection.mutex.Unlock()
}

func (cache runBuildCache) persistPreparation() {
	if cache.projection == nil {
		return
	}
	if !cache.seedNative() {
		return
	}
	cache.projection.mutex.Lock()
	seed := cache.projection.seed
	cache.projection.mutex.Unlock()
	persisted, err := buildcache.PersistNative(cache.base, cache.native, seed, time.Now())
	recordNativePersistence(cache.projection, seed, persisted, err)
}

func recordNativePersistence(projection *nativeCacheProjection, seed buildcache.NativeSeed, persisted buildcache.NativePersisted, err error) {
	projection.mutex.Lock()
	defer projection.mutex.Unlock()
	projection.persisted.Actions += persisted.Actions
	projection.persisted.Objects += persisted.Objects
	projection.persisted.Bytes += persisted.Bytes
	projection.persisted.Skipped += persisted.Skipped
	projection.persisted.Deferred = projection.persisted.Deferred || persisted.Deferred
	projection.persistErr = err
	if err == nil {
		bytes := seed.Bytes + persisted.Bytes
		projection.seed.Actions += persisted.Actions
		projection.seed.Objects += persisted.Objects
		projection.seed.Bytes = bytes
		projection.seed.Skipped += persisted.Skipped
		projection.collected.BeforeBytes = max(projection.collected.BeforeBytes, bytes)
		projection.collected.AfterBytes = max(projection.collected.AfterBytes, bytes)
	}
}

func (cache runBuildCache) collectNativeLocked(force bool, now time.Time) {
	if cache.maxBytes <= 0 || cache.projection.err != nil || cache.projection.disabled {
		return
	}
	if !force && !cache.projection.lastCollect.IsZero() && now.Sub(cache.projection.lastCollect) < buildcache.NativeCollectInterval {
		return
	}
	cache.projection.lastCollect = now
	collected, err := buildcache.CollectNative(cache.native, cache.maxBytes)
	if err != nil {
		cache.projection.collectErr = err

		cache.projection.disabled = true
		return
	}
	cache.projection.collectErr = nil
	cache.projection.collected.BeforeBytes = max(cache.projection.collected.BeforeBytes, collected.BeforeBytes)
	cache.projection.collected.AfterBytes = collected.AfterBytes
	cache.projection.collected.RemovedObjects += collected.RemovedObjects
	cache.projection.collected.RemovedActions += collected.RemovedActions
	cache.projection.collected.RemovedBytes += collected.RemovedBytes
}

func (cache runBuildCache) persistingEnvironment() []string {
	if !cache.serves() {
		return nil
	}
	var result []string
	if cache.fallback != "" {
		result = append(result, goCacheVariable+"="+cache.fallback)
	}

	return append(result, cacheProgramVariable+"="+cache.persisting)
}

func (cache runBuildCache) summarize() string {
	if !cache.serves() {
		return ""
	}
	stats, err := buildcache.Summarize(cache.scratch)
	if err != nil {
		return ""
	}
	detail := stats.Detail()
	if len(cache.nativeSweep.Removed) != 0 || len(cache.nativeSweep.Errors) != 0 {
		detail += " native-sweep-" + cache.nativeSweep.Detail("removed")
	}
	if cache.projection == nil {
		return detail
	}
	cache.projection.mutex.Lock()
	attempted := cache.projection.attempted
	projectionErr := cache.projection.err
	seed := cache.projection.seed
	collected := cache.projection.collected
	persisted := cache.projection.persisted
	collectErr := cache.projection.collectErr
	refreshErr := cache.projection.refreshErr
	persistErr := cache.projection.persistErr
	cache.projection.mutex.Unlock()
	if !attempted {
		return detail
	}
	if projectionErr != nil {
		return detail + " native-seed=fallback"
	}
	status := "ready"
	if refreshErr != nil {
		status = "refresh-failed"
	} else if collectErr != nil {
		status = "collection-failed"
	} else if persistErr != nil {
		status = "persistence-failed"
	}
	return fmt.Sprintf("%s native-seed=%s native-actions=%d native-objects=%d native-bytes=%d native-skipped=%d native-persisted-actions=%d native-persisted-objects=%d native-persisted-bytes=%d native-persisted-skipped=%d native-persist-deferred=%t native-pruned-bytes=%d native-after-bytes=%d",
		detail, status, seed.Actions, seed.Objects, seed.Bytes, seed.Skipped, persisted.Actions, persisted.Objects, persisted.Bytes,
		persisted.Skipped, persisted.Deferred, collected.RemovedBytes, collected.AfterBytes)
}

type nativeMutationSession struct {
	MutationSession
	cache runBuildCache
}

func withNativeBuildCache(session MutationSession, cache runBuildCache) MutationSession {
	if session == nil || !cache.serves() {
		return session
	}
	return nativeMutationSession{MutationSession: session, cache: cache}
}

func (session nativeMutationSession) Exec(ctx context.Context, request gomutants.ExecRequest) (gomutants.MutantResult, error) {
	release, native := session.cache.beginNative()
	if !native {
		request.Env = overlayEnvironment(request.Env, session.cache.environment())
		return session.MutationSession.Exec(ctx, request)
	}
	defer release()
	request.Env = overlayEnvironment(request.Env, session.cache.nativeEnvironment())
	return session.MutationSession.Exec(ctx, request)
}

func (session nativeMutationSession) Probe(ctx context.Context, request gomutants.ProbeRequest) (gomutants.ProbeResult, error) {
	release, native := session.cache.beginNative()
	if !native {
		request.Env = overlayEnvironment(request.Env, session.cache.environment())
		return session.MutationSession.Probe(ctx, request)
	}
	defer release()
	request.Env = overlayEnvironment(request.Env, session.cache.nativeEnvironment())
	return session.MutationSession.Probe(ctx, request)
}

func (cache runBuildCache) close(keep bool) error {
	if !cache.serves() {
		return nil
	}
	if keep {
		if cache.projection != nil {
			cache.projection.mutex.Lock()
			cache.collectNativeLocked(true, time.Now())
			cache.projection.mutex.Unlock()
		}
		if cache.nativeOwner != nil {
			return cache.nativeOwner.Keep()
		}
		return nil
	}
	var releaseErr error
	if cache.nativeOwner != nil {
		releaseErr = cache.nativeOwner.Release()
	}
	return errors.Join(releaseErr, removeBuildCacheScratch(cache.scratch), removeBuildCacheScratch(cache.native))
}

type buildCacheWorkspace struct {
	workspace     CommandWorkspace
	nonPersisting []string
	native        []string
	persisting    []string
	cache         runBuildCache
}

func withBuildCache(workspace CommandWorkspace, cache runBuildCache) CommandWorkspace {
	if workspace == nil || !cache.serves() {
		return workspace
	}
	return buildCacheWorkspace{
		workspace: workspace, nonPersisting: cache.environment(), native: cache.nativeEnvironment(),
		persisting: cache.persistingEnvironment(), cache: cache,
	}
}

func (wrapper buildCacheWorkspace) Exec(ctx context.Context, command gomutants.Command) (gomutants.CommandResult, error) {
	persisting := persistingCommand(command.Argv)
	if persisting {
		command.Env = overlayEnvironment(command.Env, wrapper.persisting)
	} else if nativeExecutionCommand(command.Argv) {
		release, native := wrapper.cache.beginNative()
		if native {
			defer release()
			command.Env = overlayEnvironment(command.Env, wrapper.native)
		} else {
			command.Env = overlayEnvironment(command.Env, wrapper.nonPersisting)
		}
	} else {
		command.Env = overlayEnvironment(command.Env, wrapper.nonPersisting)
	}
	result, err := wrapper.workspace.Exec(ctx, command)
	if persisting {
		wrapper.cache.markNativeDirty()
	}
	return result, err
}

func nativeExecutionCommand(argv []string) bool {
	if len(argv) < minimumGoCommandArgs {
		return false
	}
	if !goExecutable(argv[0]) {
		return slices.ContainsFunc(argv[1:], func(argument string) bool {
			return strings.HasPrefix(argument, "-test.coverprofile=")
		})
	}
	first := goSubcommandIndex(argv)
	if first >= len(argv) {
		return false
	}
	command := argv[first:]
	return command[0] == "test" && !slices.Contains(command[1:argumentSeparator(command)], "-c")
}

func persistingCommand(argv []string) bool {
	if len(argv) < minimumGoCommandArgs || !goExecutable(argv[0]) {
		return false
	}

	first := goSubcommandIndex(argv)
	if first >= len(argv) {
		return false
	}
	argv = argv[first:]
	switch argv[0] {
	case "vet", "build", "list", "version":
		return true
	case "test":

		return slices.Contains(argv[1:argumentSeparator(argv)], "-c")
	default:
		return false
	}
}

func goSubcommandIndex(argv []string) int {
	if len(argv) < minimumGoCommandArgs {
		return len(argv)
	}
	first := goSubcommandStart
	switch {
	case argv[first] == "-C":
		first += goDirectoryFlagArgs
	case strings.HasPrefix(argv[first], "-C="):
		first++
	}
	return first
}

func argumentSeparator(argv []string) int {
	if index := slices.Index(argv, "-args"); index >= 0 {
		return index
	}
	return len(argv)
}

func goExecutable(path string) bool {
	name := filepath.Base(path)
	return name == "go" || name == "go.exe"
}

func overlayEnvironment(existing, overlay []string) []string {
	replaced := make(map[string]bool, len(overlay))
	for _, entry := range overlay {
		if key, _, ok := strings.Cut(entry, "="); ok {
			replaced[strings.ToUpper(key)] = true
		}
	}
	result := make([]string, 0, len(existing)+len(overlay))
	for _, entry := range existing {
		if key, _, ok := strings.Cut(entry, "="); ok && replaced[strings.ToUpper(key)] {
			continue
		}
		result = append(result, entry)
	}
	return append(result, overlay...)
}
