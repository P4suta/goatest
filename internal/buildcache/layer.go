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

	"github.com/P4suta/goatest/internal/filemode"
)

const (
	entryPrefixHexDigits = 2

	actionsDirectory = "actions"

	objectsDirectory = "objects"

	statsDirectory = "stats"

	MarkerName = "goatest-build-cache-v1"

	collectedName = "goatest-build-cache-collected"

	markerText = "This is goatest's build cache. goatest writes, reads, and collects every" +
		" file below this directory, and nothing else does; removing it whole is safe" +
		" and only costs the next run the work of compiling again.\n"

	markerTemporaryPrefix = ".marker-"
	markerTemporarySuffix = ".tmp"

	BaseTouchInterval = time.Hour

	ScratchTouchInterval = time.Minute

	MinIdleTouchIntervals = 2

	ScratchCollectInterval = time.Minute
)

type Entry struct {
	OutputID []byte

	Size int64

	Time time.Time

	DiskPath string
}

type Status struct {
	Entries int

	Bytes int64

	Oldest time.Time
}

type Policy struct {
	MaxBytes int64

	TTL time.Duration

	MinIdle time.Duration
}

type Collected struct {
	Before         Status
	After          Status
	RemovedActions int
	RemovedObjects int
	RemovedBytes   int64
}

type Layer struct {
	Dir string

	Touch time.Duration
}

func (layer Layer) touchInterval() time.Duration {
	if layer.Touch > 0 {
		return layer.Touch
	}
	return BaseTouchInterval
}

func (layer Layer) MinIdle() time.Duration {
	return MinIdleTouchIntervals * layer.touchInterval()
}

type actionRecord struct {
	Output string    `json:"output"`
	Size   int64     `json:"size"`
	Time   time.Time `json:"time"`
}

func (layer Layer) Prepare() error { return layer.prepareWithHooks(layerHooks{}) }

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

	if err := writeFile(filepath.Join(layer.Dir, MarkerName), []byte(markerText),
		markerTemporaryPrefix+"*"+markerTemporarySuffix, hooks); err != nil {
		return fmt.Errorf("goatest: prepare build cache layer: %w", err)
	}
	return nil
}

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

func ownedLayerName(name string) bool {
	switch name {
	case MarkerName, collectedName, actionsDirectory, objectsDirectory, statsDirectory:
		return true
	}
	return markerTemporaryName(name)
}

func markerTemporaryName(name string) bool {
	return strings.HasPrefix(name, markerTemporaryPrefix) && strings.HasSuffix(name, markerTemporarySuffix)
}

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
		if err := hooks.mkdirAll(directory, filemode.ReadableDirectory); err != nil {
			return fmt.Errorf("goatest: create build cache layer: %w", err)
		}
	}
	return nil
}

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

func (layer Layer) touch(actionID []byte, modified, now time.Time, hooks layerHooks) {
	if now.IsZero() || now.Sub(modified) < layer.touchInterval() {
		return
	}

	_ = hooks.chtimes(layer.actionPath(actionID), now, now)
}

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

func (layer Layer) writeObject(outputID []byte, body io.Reader, size int64, hooks layerHooks) error {
	path := layer.objectPath(outputID)
	directory := filepath.Dir(path)
	if err := hooks.mkdirAll(directory, filemode.ReadableDirectory); err != nil {
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

func (layer Layer) Inspect() (Status, error) { return layer.inspectWithHooks(layerHooks{}) }

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

type storedFile struct {
	path string

	name string

	output string

	size int64

	modified time.Time

	removed bool
}

func (policy Policy) validate() error {
	if policy.MaxBytes < 0 || policy.TTL < 0 || policy.MinIdle < 0 {
		return errors.New("goatest: build cache policy must not be negative")
	}
	return nil
}

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

func (layer Layer) actionPath(actionID []byte) string {
	return layer.entryPath(actionsDirectory, actionID)
}

func (layer Layer) objectPath(outputID []byte) string {
	return layer.entryPath(objectsDirectory, outputID)
}

func (layer Layer) entryPath(half string, identifier []byte) string {
	name := hex.EncodeToString(identifier)
	return filepath.Join(layer.Dir, half, name[:entryPrefixHexDigits], name)
}

func hexadecimal(name string) bool {
	if len(name) < entryPrefixHexDigits {
		return false
	}
	for _, character := range name {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func writeFile(path string, data []byte, pattern string, hooks layerHooks) error {
	directory := filepath.Dir(path)
	if err := hooks.mkdirAll(directory, filemode.ReadableDirectory); err != nil {
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

func publish(temporaryPath, path string, hooks layerHooks) error {
	if err := hooks.rename(temporaryPath, path); err != nil {
		if removeErr := hooks.remove(path); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			return errors.Join(err, removeErr)
		}
		return hooks.rename(temporaryPath, path)
	}
	return nil
}
