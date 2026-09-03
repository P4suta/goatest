// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package buildcache

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

const (
	// actionsDirectory holds one line per cache key, naming the output it
	// produced. Its file times are the layer's LRU clock.
	actionsDirectory = "actions"
	// objectsDirectory holds the outputs themselves, content addressed, so an
	// output reached by many keys is stored once.
	objectsDirectory = "objects"
	// statsDirectory holds what each served go process asked for.
	statsDirectory = "stats"
	// MarkerName is the file that says a directory is goatest's build cache.
	//
	// It carries goatest's own name rather than being a README because it is
	// load bearing, not decorative: goatest collects and removes files below a
	// layer, so it adopts a directory that already holds files only when this
	// marker is in it. A README is a file a project may already keep in a
	// directory somebody pointed build_dir at, which is exactly the mistake
	// this name exists to catch.
	MarkerName = "goatest-build-cache-v1"
	// collectedName is the file whose time records when this layer was last
	// collected and whose lock keeps two processes from collecting it at once.
	collectedName = "goatest-build-cache-collected"
	// markerText is the note the marker carries. It has to say who made the
	// directory and that removing it is safe, because a user who found it while
	// hunting for disk space is exactly the reader it has.
	markerText = "This is goatest's build cache. goatest writes, reads, and collects every" +
		" file below this directory, and nothing else does; removing it whole is safe" +
		" and only costs the next run the work of compiling again.\n"
	// markerTemporaryPrefix and markerTemporarySuffix bracket the name the
	// marker is written under before it is renamed into place. They are spelled
	// out because claim has to recognise one: a process killed between the
	// create and the rename leaves exactly this name at the root of the layer,
	// and it is goatest's own.
	markerTemporaryPrefix = ".marker-"
	markerTemporarySuffix = ".tmp"

	// BaseTouchInterval is how stale an action's file time may become in the
	// layer the machine keeps before a read refreshes it. It is the go
	// command's own mtime granularity.
	BaseTouchInterval = time.Hour
	// ScratchTouchInterval is the same for the layer one run keeps. It is
	// shorter because the whole life of the layer is the life of one run, and a
	// go command inside it is bounded in minutes rather than hours.
	ScratchTouchInterval = time.Minute
	// MinIdleTouchIntervals is how many touch intervals a collection must leave
	// alone, and it may not be less than two.
	//
	// The inequality it states is what the LRU rests on. A collection may only
	// remove an entry whose last touch is older than MinIdle, and a read
	// refreshes an entry at most once per touch interval, so an entry a live
	// build is reading continuously carries a file time that is already up to
	// one whole touch interval stale. One interval covers that staleness. The
	// second covers the window the go command needs after the response: it
	// opens the file a response named *after* that response, so an entry served
	// moments ago must still be there.
	MinIdleTouchIntervals = 2
	// ScratchCollectInterval is how often one run's scratch layer is collected.
	//
	// A run starts one served process per go command and issues thousands of
	// them, and each one closes. Collecting on every close would walk the whole
	// layer once per go command, which is work proportional to the square of
	// the run; collecting once per interval is what makes the bound affordable.
	ScratchCollectInterval = time.Minute
)

// Entry is one stored output: what the action produced, how long it is, when
// it was stored, and where the bytes are.
type Entry struct {
	// OutputID identifies the content, as the go command computed it.
	OutputID []byte
	// Size is the length of the content in bytes.
	Size int64
	// Time is when the content was stored.
	Time time.Time
	// DiskPath is the absolute path of the file holding the content. The go
	// command opens it after the response naming it, so it must survive for a
	// while afterwards.
	DiskPath string
}

// Status is what one layer currently holds.
type Status struct {
	// Entries is the number of stored actions.
	Entries int
	// Bytes is what the stored objects occupy.
	Bytes int64
	// Oldest is the least recently read action's file time.
	Oldest time.Time
}

// Policy bounds one layer.
//
// The bound is soft by at most one MinIdle of writes: an entry read moments
// ago is never removed, however far over MaxBytes the layer is, because the go
// command that read it is about to open the file. A layer therefore settles at
// MaxBytes plus whatever the last MinIdle of activity added, and the next
// collection takes the rest.
type Policy struct {
	// MaxBytes bounds the objects of the layer. Zero or negative is unbounded.
	MaxBytes int64
	// TTL removes an action unread for longer than this. Zero never expires.
	TTL time.Duration
	// MinIdle protects an action read within this window, and an unreferenced
	// object written within it, from removal.
	MinIdle time.Duration
}

// Collected is what one collection removed.
type Collected struct {
	Before         Status
	After          Status
	RemovedActions int
	RemovedObjects int
	RemovedBytes   int64
}

// Layer is one directory of the build cache. Its zero value names no
// directory, which is a layer that stores nothing and answers every read with
// a miss.
type Layer struct {
	// Dir is where the layer lives.
	Dir string
	// Touch is how stale an action's file time may become before a read
	// refreshes it. Zero is BaseTouchInterval, so a caller that says nothing
	// gets the interval of the layer the machine keeps.
	Touch time.Duration
}

// touchInterval is the rate a read of this layer refreshes an entry at.
func (layer Layer) touchInterval() time.Duration {
	if layer.Touch > 0 {
		return layer.Touch
	}
	return BaseTouchInterval
}

// MinIdle is the shortest window a collection of this layer may leave alone.
// It is the layer's own touch interval times MinIdleTouchIntervals, which is
// the inequality documented there: a caller that asks the layer can never get
// the arithmetic wrong, and a caller that invents its own can.
func (layer Layer) MinIdle() time.Duration {
	return MinIdleTouchIntervals * layer.touchInterval()
}

// actionRecord is the one JSON line an action file holds.
type actionRecord struct {
	Output string    `json:"output"`
	Size   int64     `json:"size"`
	Time   time.Time `json:"time"`
}

// Prepare claims the directory as goatest's, creates the layer, and proves it
// is writable, so that a run learns a cache it cannot use before it hands the
// program to a go command that would fail on it.
//
// It runs once per run, in the run itself. The served processes a run starts do
// not prepare: there is one per go command and a run issues thousands, so a
// stat, a readdir, and an fsynced marker apiece would be pure cost.
func (layer Layer) Prepare() error { return layer.prepareWithHooks(layerHooks{}) }

// prepareWithHooks is Prepare against a filesystem the caller supplies.
func (layer Layer) prepareWithHooks(hooks layerHooks) error {
	hooks = hooks.resolved()
	if layer.Dir == "" {
		return errors.New("goatest: build cache layer has no directory")
	}
	if err := layer.claim(hooks); err != nil {
		return err
	}
	if err := layer.ensureWithHooks(hooks); err != nil {
		return err
	}
	// Writing the marker is also the writability probe: it is the one write
	// every layer takes, and a layer that cannot take it cannot serve.
	if err := writeFile(filepath.Join(layer.Dir, MarkerName), []byte(markerText),
		markerTemporaryPrefix+"*"+markerTemporarySuffix, hooks); err != nil {
		return fmt.Errorf("goatest: prepare build cache layer: %w", err)
	}
	return nil
}

// claim refuses a directory that already holds something goatest did not put
// there. It is what stops a mistyped build_dir — a home directory, a project
// directory — from becoming a cache goatest then collects and removes files
// from. A directory that does not exist, one that is empty, and one holding
// only goatest's own names are all claimable; anything else is somebody's, and
// the refusal writes nothing into it.
//
// It also sweeps the one piece of litter goatest's own crash can leave at the
// root, which is a marker temporary a process died before renaming.
func (layer Layer) claim(hooks layerHooks) error {
	entries, err := hooks.readDir(layer.Dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("goatest: read build cache layer: %w", err)
	}
	for _, entry := range entries {
		if !ownedLayerName(entry.Name()) {
			return fmt.Errorf(
				"goatest: %s is not a goatest build cache: it holds %q, which goatest did not write,"+
					" so goatest will not store in it, collect it, or remove anything from it",
				layer.Dir, entry.Name())
		}
		if markerTemporaryName(entry.Name()) {
			if err := layer.sweepMarkerTemporary(entry.Name(), hooks); err != nil {
				return err
			}
		}
	}
	return nil
}

// sweepMarkerTemporary removes a marker temporary that no live Prepare can
// still be about to rename.
//
// It is only ever goatest's own litter: Prepare writes the marker through this
// temporary and renames it into place, so a process killed between the two
// leaves it at the root and nothing ever comes back for it. Left there it would
// make every later Prepare refuse the layer, which would take the machine's
// whole build cache away from the tool that made it until somebody deleted a
// file goatest wrote.
//
// Staleness is what keeps the sweep from being a race. A Prepare running beside
// this one is between the create and the rename for microseconds, and its
// temporary has to still be there for it to rename, so nothing younger than the
// window this layer already protects its entries for is touched. A temporary
// another process swept first is not an error either: gone is what was wanted.
func (layer Layer) sweepMarkerTemporary(name string, hooks layerHooks) error {
	path := filepath.Join(layer.Dir, name)
	info, err := hooks.stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("goatest: read build cache layer: %w", err)
	}
	if hooks.now().Sub(info.ModTime()) < layer.MinIdle() {
		return nil
	}
	if err := hooks.remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("goatest: remove build cache marker temporary: %w", err)
	}
	return nil
}

// ownedLayerName reports whether a name at the root of a layer is one goatest
// writes. A layer whose marker a user deleted still holds only these, so it is
// still claimable: the refusal is for directories that hold somebody else's
// files, not for ones goatest made and somebody tidied — or ones goatest made
// and its own crash left half finished.
func ownedLayerName(name string) bool {
	switch name {
	case MarkerName, collectedName, actionsDirectory, objectsDirectory, statsDirectory:
		return true
	}
	return markerTemporaryName(name)
}

// markerTemporaryName reports whether a name at the root of a layer is one the
// marker write can leave behind. It matches the whole pattern rather than any
// hidden temporary, because a directory somebody else owns may well hold
// hidden temporaries of its own and those are exactly what claim exists to
// refuse.
func markerTemporaryName(name string) bool {
	return strings.HasPrefix(name, markerTemporaryPrefix) && strings.HasSuffix(name, markerTemporarySuffix)
}

// ensureWithHooks creates the directories this layer's own writes need, and
// nothing else. It claims nothing and probes nothing, which is why it is what
// a served go command runs.
func (layer Layer) ensureWithHooks(hooks layerHooks) error {
	hooks = hooks.resolved()
	if layer.Dir == "" {
		return errors.New("goatest: build cache layer has no directory")
	}
	for _, directory := range []string{
		layer.Dir,
		filepath.Join(layer.Dir, actionsDirectory),
		filepath.Join(layer.Dir, objectsDirectory),
	} {
		if err := hooks.mkdirAll(directory, 0o755); err != nil {
			return fmt.Errorf("goatest: create build cache layer: %w", err)
		}
	}
	return nil
}

// readAction reads the line stored under one cache key, with the file time
// that is the layer's LRU clock.
func (layer Layer) readAction(actionID []byte, hooks layerHooks) (actionRecord, time.Time, bool, error) {
	if layer.Dir == "" || len(actionID) == 0 {
		return actionRecord{}, time.Time{}, false, nil
	}
	path := layer.actionPath(actionID)
	info, err := hooks.stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return actionRecord{}, time.Time{}, false, nil
	}
	if err != nil {
		return actionRecord{}, time.Time{}, false, fmt.Errorf("goatest: read build cache action: %w", err)
	}
	data, err := hooks.readFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return actionRecord{}, time.Time{}, false, nil
	}
	if err != nil {
		return actionRecord{}, time.Time{}, false, fmt.Errorf("goatest: read build cache action: %w", err)
	}
	var record actionRecord
	if err := json.Unmarshal(data, &record); err != nil || record.Output == "" || record.Size < 0 {
		return actionRecord{}, time.Time{}, false, nil
	}
	return record, info.ModTime(), true, nil
}

// object reports where the bytes of one output are and how long they are.
func (layer Layer) object(outputID []byte, hooks layerHooks) (string, int64, bool, error) {
	if layer.Dir == "" || len(outputID) == 0 {
		return "", 0, false, nil
	}
	path := layer.objectPath(outputID)
	info, err := hooks.stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", 0, false, nil
	}
	if err != nil {
		return "", 0, false, fmt.Errorf("goatest: read build cache object: %w", err)
	}
	return path, info.Size(), true, nil
}

// touch refreshes an action's file time when it has gone stale, which is what
// makes the file time a usable LRU clock without a write on every read.
func (layer Layer) touch(actionID []byte, modified, now time.Time, hooks layerHooks) {
	if now.IsZero() || now.Sub(modified) < layer.touchInterval() {
		return
	}
	// A refresh that fails costs the entry its place in the queue and nothing
	// else, so it is never worth failing a read over.
	_ = hooks.chtimes(layer.actionPath(actionID), now, now)
}

// putWithHooks stores one output and the cache key that produced it. Storing an
// output this layer already holds writes the key alone: the content is
// addressed by its own identity, so two keys that produced the same bytes share
// them.
//
// Every caller reaches it through Layers, which is the only thing that decides
// which layer a write lands in.
func (layer Layer) putWithHooks(actionID, outputID []byte, body io.Reader, size int64, now time.Time, hooks layerHooks) (Entry, error) {
	hooks = hooks.resolved()
	if layer.Dir == "" {
		return Entry{}, errors.New("goatest: build cache layer has no directory")
	}
	if len(actionID) == 0 || len(outputID) == 0 {
		return Entry{}, errors.New("goatest: build cache put requires an action and an output identifier")
	}
	if size < 0 {
		return Entry{}, fmt.Errorf("goatest: build cache put size %d is negative", size)
	}
	path, stored, found, err := layer.object(outputID, hooks)
	if err != nil {
		return Entry{}, err
	}
	if !found || stored != size {
		if err := layer.writeObject(outputID, body, size, hooks); err != nil {
			return Entry{}, err
		}
		path = layer.objectPath(outputID)
	}
	return layer.putAction(actionID, outputID, size, now, path, hooks)
}

// writeObject streams a body into the layer, refusing to publish content whose
// length is not the length the go command declared.
func (layer Layer) writeObject(outputID []byte, body io.Reader, size int64, hooks layerHooks) error {
	path := layer.objectPath(outputID)
	directory := filepath.Dir(path)
	if err := hooks.mkdirAll(directory, 0o755); err != nil {
		return fmt.Errorf("goatest: create build cache object directory: %w", err)
	}
	temporary, err := hooks.createTemporary(directory, ".object-*.tmp")
	if err != nil {
		return fmt.Errorf("goatest: create build cache object: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() { _ = hooks.remove(temporaryPath) }()
	written, err := hooks.copyBody(temporary, body)
	if err != nil {
		_ = temporary.Close()
		return fmt.Errorf("goatest: write build cache object: %w", err)
	}
	if written != size {
		_ = temporary.Close()
		return fmt.Errorf("goatest: build cache object is %d bytes, the go command declared %d", written, size)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("goatest: write build cache object: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("goatest: write build cache object: %w", err)
	}
	return publish(temporaryPath, path, hooks)
}

// putAction records one cache key against an output that is already stored,
// wherever it is stored: a scratch key may name an object the base layer holds.
func (layer Layer) putAction(actionID, outputID []byte, size int64, now time.Time, path string, hooks layerHooks) (Entry, error) {
	record := actionRecord{Output: hex.EncodeToString(outputID), Size: size, Time: now.UTC()}
	line, err := json.Marshal(record)
	if err != nil {
		return Entry{}, fmt.Errorf("goatest: encode build cache action: %w", err)
	}
	if err := writeFile(layer.actionPath(actionID), append(line, '\n'), ".action-*.tmp", hooks); err != nil {
		return Entry{}, fmt.Errorf("goatest: write build cache action: %w", err)
	}
	return Entry{OutputID: slices.Clone(outputID), Size: size, Time: record.Time, DiskPath: path}, nil
}

// Inspect reports what the layer holds, for a status line and for the run that
// decides whether to collect it.
func (layer Layer) Inspect() (Status, error) { return layer.inspectWithHooks(layerHooks{}) }

// inspectWithHooks is Inspect against a filesystem the caller supplies.
func (layer Layer) inspectWithHooks(hooks layerHooks) (Status, error) {
	hooks = hooks.resolved()
	actions, objects, err := layer.list(hooks)
	if err != nil {
		return Status{}, err
	}
	status := Status{Entries: len(actions)}
	for _, object := range objects {
		status.Bytes += object.size
	}
	for _, action := range actions {
		if status.Oldest.IsZero() || action.modified.Before(status.Oldest) {
			status.Oldest = action.modified
		}
	}
	return status, nil
}

// storedFile is one action or one object as a collection sees it.
type storedFile struct {
	// path is where the file is.
	path string
	// name is its hexadecimal identifier, which breaks ties in file time so
	// that two collections of one layer remove the same entries.
	name string
	// output is the hexadecimal output identifier an action names; it is empty
	// for an object.
	output string
	// size is what the file accounts for: the length of an object, and the
	// length an action claims its output has.
	size int64
	// modified is the file time: for an action the moment it was last read,
	// for an object the moment it was stored.
	modified time.Time
	// removed marks a file this collection decided to unlink.
	removed bool
}

// validate refuses a policy that cannot be meant. A negative bound is a
// configuration mistake, and a cache that silently did something else with one
// would be a cache nobody could reason about.
func (policy Policy) validate() error {
	if policy.MaxBytes < 0 || policy.TTL < 0 || policy.MinIdle < 0 {
		return errors.New("goatest: build cache policy must not be negative")
	}
	return nil
}

// collectWithHooks bounds the layer. It removes what has expired, then the
// least recently read entries until the layer is within its size, then every
// object no remaining action names. Nothing read within MinIdle is removed, and
// neither is an unreferenced object written within it, because the go command
// opens the file a response named after the response.
//
// Every removal is an unlink of a file nothing rewrites in place, so a
// collection needs no coordination with the go processes *reading* the layer
// beside it: the worst a concurrent process observes is a miss. Two collections
// of one layer would only duplicate each other's walk, which is what
// CollectLocked — the entry point every caller uses — serializes.
func (layer Layer) collectWithHooks(policy Policy, now time.Time, hooks layerHooks) (Collected, error) {
	hooks = hooks.resolved()
	if err := policy.validate(); err != nil {
		return Collected{}, err
	}
	actions, objects, err := layer.list(hooks)
	if err != nil {
		return Collected{}, err
	}
	sizes := make(map[string]int64, len(objects))
	byOutput := make(map[string]int, len(objects))
	for index, object := range objects {
		sizes[object.name] = object.size
		byOutput[object.name] = index
	}
	references := make(map[string]int, len(actions))
	for _, action := range actions {
		references[action.output]++
	}
	result := Collected{Before: Status{Entries: len(actions)}}
	for _, object := range objects {
		result.Before.Bytes += object.size
	}
	for _, action := range actions {
		if result.Before.Oldest.IsZero() || action.modified.Before(result.Before.Oldest) {
			result.Before.Oldest = action.modified
		}
	}
	// Ordering by file time and then by name makes two collections of one
	// layer remove the same entries, which is what lets a test state the order
	// rather than observe it.
	order := slices.Clone(actions)
	slices.SortFunc(order, func(first, second storedFile) int {
		if compared := first.modified.Compare(second.modified); compared != 0 {
			return compared
		}
		return strings.Compare(first.name, second.name)
	})
	remaining := result.Before.Bytes
	protected := func(file storedFile) bool {
		return policy.MinIdle > 0 && !now.IsZero() && now.Sub(file.modified) < policy.MinIdle
	}
	// An object nothing names is already garbage, so it is charged against the
	// bound as reclaimable before the least recently read entries are.
	release := func(output string) {
		index, ok := byOutput[output]
		if !ok || objects[index].removed || protected(objects[index]) {
			return
		}
		objects[index].removed = true
		remaining -= objects[index].size
	}
	for name := range sizes {
		if references[name] == 0 {
			release(name)
		}
	}
	drop := func(action *storedFile) error {
		if err := hooks.remove(action.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("goatest: collect build cache action: %w", err)
		}
		action.removed = true
		result.RemovedActions++
		references[action.output]--
		if references[action.output] <= 0 {
			release(action.output)
		}
		return nil
	}
	if policy.TTL > 0 && !now.IsZero() {
		for index := range order {
			if now.Sub(order[index].modified) < policy.TTL || protected(order[index]) {
				continue
			}
			if err := drop(&order[index]); err != nil {
				return Collected{}, err
			}
		}
	}
	if policy.MaxBytes > 0 {
		for index := range order {
			if remaining <= policy.MaxBytes {
				break
			}
			if order[index].removed || protected(order[index]) {
				continue
			}
			if err := drop(&order[index]); err != nil {
				return Collected{}, err
			}
		}
	}
	for index := range objects {
		if !objects[index].removed {
			continue
		}
		if err := hooks.remove(objects[index].path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return Collected{}, fmt.Errorf("goatest: collect build cache object: %w", err)
		}
		result.RemovedObjects++
		result.RemovedBytes += objects[index].size
	}
	for index := range order {
		if order[index].removed {
			continue
		}
		result.After.Entries++
		if result.After.Oldest.IsZero() || order[index].modified.Before(result.After.Oldest) {
			result.After.Oldest = order[index].modified
		}
	}
	result.After.Bytes = result.Before.Bytes - result.RemovedBytes
	return result, nil
}

// list reads both halves of the layer. A layer that does not exist yet is
// empty rather than an error: a run asks for the status of a base layer no
// machine has built yet.
func (layer Layer) list(hooks layerHooks) ([]storedFile, []storedFile, error) {
	actions, err := layer.walk(actionsDirectory, hooks)
	if err != nil {
		return nil, nil, err
	}
	objects, err := layer.walk(objectsDirectory, hooks)
	if err != nil {
		return nil, nil, err
	}
	for index := range actions {
		data, err := hooks.readFile(actions[index].path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, nil, fmt.Errorf("goatest: read build cache action: %w", err)
		}
		var parsed actionRecord
		if err := json.Unmarshal(data, &parsed); err != nil {
			continue
		}
		actions[index].output = parsed.Output
		actions[index].size = parsed.Size
	}
	return actions, objects, nil
}

// walk enumerates the two-level tree one half of a layer is stored in, sorted
// by path so that a collection is deterministic.
func (layer Layer) walk(half string, hooks layerHooks) ([]storedFile, error) {
	if layer.Dir == "" {
		return nil, nil
	}
	root := filepath.Join(layer.Dir, half)
	prefixes, err := hooks.readDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("goatest: read build cache layer: %w", err)
	}
	var files []storedFile
	for _, prefix := range prefixes {
		if !prefix.IsDir() {
			continue
		}
		directory := filepath.Join(root, prefix.Name())
		entries, err := hooks.readDir(directory)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("goatest: read build cache layer: %w", err)
		}
		for _, entry := range entries {
			if entry.IsDir() || !hexadecimal(entry.Name()) {
				continue
			}
			info, err := entry.Info()
			if err != nil {
				if errors.Is(err, os.ErrNotExist) {
					continue
				}
				return nil, fmt.Errorf("goatest: read build cache layer: %w", err)
			}
			files = append(files, storedFile{
				path: filepath.Join(directory, entry.Name()), name: entry.Name(),
				size: info.Size(), modified: info.ModTime(),
			})
		}
	}
	slices.SortFunc(files, func(first, second storedFile) int { return strings.Compare(first.path, second.path) })
	return files, nil
}

// actionPath is where one cache key's line is stored.
func (layer Layer) actionPath(actionID []byte) string {
	return layer.entryPath(actionsDirectory, actionID)
}

// objectPath is where one output's bytes are stored.
func (layer Layer) objectPath(outputID []byte) string {
	return layer.entryPath(objectsDirectory, outputID)
}

// entryPath spreads an identifier over its first byte, so that a layer holding
// hundreds of thousands of entries never fills one directory.
func (layer Layer) entryPath(half string, identifier []byte) string {
	name := hex.EncodeToString(identifier)
	return filepath.Join(layer.Dir, half, name[:2], name)
}

// hexadecimal reports whether a name is one this layer wrote, so that a file
// somebody else left in the tree is never mistaken for an entry.
func hexadecimal(name string) bool {
	if len(name) < 2 {
		return false
	}
	for _, character := range name {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

// writeFile publishes bytes at a path, through a temporary file in the same
// directory so that a reader sees either the old file or the whole new one.
func writeFile(path string, data []byte, pattern string, hooks layerHooks) error {
	directory := filepath.Dir(path)
	if err := hooks.mkdirAll(directory, 0o755); err != nil {
		return err
	}
	temporary, err := hooks.createTemporary(directory, pattern)
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer func() { _ = hooks.remove(temporaryPath) }()
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return publish(temporaryPath, path, hooks)
}

// publish renames a written temporary file into place, retrying once without
// the destination for the platforms whose rename refuses to replace a file.
func publish(temporaryPath, path string, hooks layerHooks) error {
	if err := hooks.rename(temporaryPath, path); err != nil {
		if removeErr := hooks.remove(path); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			return errors.Join(err, removeErr)
		}
		return hooks.rename(temporaryPath, path)
	}
	return nil
}
