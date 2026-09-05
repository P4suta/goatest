// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package cache

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

type Status struct {
	Entries int
	Bytes   int64
	Oldest  time.Time
	Newest  time.Time
}

type GCResult struct {
	Before         Status
	After          Status
	RemovedEntries int
	RemovedBytes   int64
}

type cacheEntry struct {
	name     string
	size     int64
	modified time.Time
	expired  bool
	version  os.FileInfo
}

func Inspect(root string) (Status, error) {
	cacheOperationMutex.RLock()
	defer cacheOperationMutex.RUnlock()
	status, _, err := inspectUnlocked(root, 0, time.Time{})
	return status, err
}

// Collect removes expired entries first and then the oldest entries until the
// configured capacity is met. It only removes direct, non-symlink children of
// the cache's v1 directory.
func Collect(root string, maxBytes int64, ttl time.Duration, now time.Time) (GCResult, error) {
	if maxBytes < 0 || ttl < 0 {
		return GCResult{}, errors.New("goatest: cache policy must not be negative")
	}
	cacheOperationMutex.Lock()
	defer cacheOperationMutex.Unlock()
	return collectUnlocked(root, maxBytes, ttl, now)
}

// Flush removes every exact-input cache entry. It deliberately has the same
// confined-directory rules as Collect: a malformed entry stops the operation
// before anything is removed, and neither operation follows symbolic links.
func Flush(root string) (GCResult, error) {
	return flushWithHook(root, nil)
}

// flushWithHook gives a test one deterministic point after descriptor-rooted
// validation and before removal. Production never supplies the hook.
func flushWithHook(root string, beforeRemove func()) (GCResult, error) {
	cacheOperationMutex.Lock()
	defer cacheOperationMutex.Unlock()
	before, entries, err := inspectUnlocked(root, 0, time.Time{})
	if err != nil {
		return GCResult{}, err
	}
	result := GCResult{Before: before}
	if err := removeEntries(root, entries, beforeRemove); err != nil {
		return GCResult{}, err
	}
	for _, entry := range entries {
		result.RemovedEntries++
		result.RemovedBytes += entry.size
	}
	result.After, _, err = inspectUnlocked(root, 0, time.Time{})
	return result, err
}

func collectUnlocked(root string, maxBytes int64, ttl time.Duration, now time.Time) (GCResult, error) {
	before, entries, err := inspectUnlocked(root, ttl, now)
	if err != nil {
		return GCResult{}, err
	}
	result := GCResult{Before: before}
	slices.SortFunc(entries, func(a, b cacheEntry) int {
		if a.expired != b.expired {
			if a.expired {
				return -1
			}
			return 1
		}
		if compared := a.modified.Compare(b.modified); compared != 0 {
			return compared
		}
		return strings.Compare(a.name, b.name)
	})
	remaining := before.Bytes
	removals := make([]cacheEntry, 0, len(entries))
	for _, entry := range entries {
		if !entry.expired && (maxBytes <= 0 || remaining <= maxBytes) {
			continue
		}
		removals = append(removals, entry)
		remaining -= entry.size
	}
	if err := removeEntries(root, removals, nil); err != nil {
		return GCResult{}, err
	}
	for _, entry := range removals {
		result.RemovedEntries++
		result.RemovedBytes += entry.size
	}
	result.After, _, err = inspectUnlocked(root, 0, time.Time{})
	return result, err
}

func inspectUnlocked(root string, ttl time.Duration, now time.Time) (Status, []cacheEntry, error) {
	versionRoot := filepath.Join(root, "v1")
	versionInfo, err := os.Lstat(versionRoot)
	if errors.Is(err, os.ErrNotExist) {
		return Status{}, []cacheEntry{}, nil
	}
	if err != nil {
		return Status{}, nil, fmt.Errorf("goatest: inspect cache: %w", err)
	}
	if versionInfo.Mode()&os.ModeSymlink != 0 || !versionInfo.IsDir() {
		return Status{}, nil, errors.New("goatest: cache v1 root is not a confined directory")
	}
	directories, err := os.ReadDir(versionRoot)
	if err != nil {
		return Status{}, nil, fmt.Errorf("goatest: inspect cache: %w", err)
	}
	var status Status
	entries := make([]cacheEntry, 0, len(directories))
	for _, directory := range directories {
		if !safeEntryName(directory.Name()) {
			return Status{}, nil, fmt.Errorf("goatest: unsafe cache entry %q", directory.Name())
		}
		if directory.Type()&os.ModeSymlink != 0 || !directory.IsDir() {
			return Status{}, nil, fmt.Errorf("goatest: cache entry %q is not a confined directory", directory.Name())
		}
		path := filepath.Join(versionRoot, directory.Name())
		size, modified, walkErr := entryMetadata(path)
		if walkErr != nil {
			return Status{}, nil, walkErr
		}
		entry := cacheEntry{name: directory.Name(), size: size, modified: modified, version: versionInfo}
		if ttl > 0 && !now.IsZero() && !modified.IsZero() {
			entry.expired = !modified.Add(ttl).After(now)
		}
		entries = append(entries, entry)
		status.Entries++
		status.Bytes += size
		if status.Oldest.IsZero() || modified.Before(status.Oldest) {
			status.Oldest = modified
		}
		if status.Newest.IsZero() || modified.After(status.Newest) {
			status.Newest = modified
		}
	}
	return status, entries, nil
}

func entryMetadata(root string) (int64, time.Time, error) {
	var size int64
	var modified time.Time
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("goatest: cache entry crosses symbolic link %s", path)
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("goatest: cache entry contains irregular file %s", path)
		}
		size += info.Size()
		if info.ModTime().After(modified) {
			modified = info.ModTime()
		}
		return nil
	})
	return size, modified, err
}

// removeEntries validates the whole batch, then removes it relative to an open
// descriptor for the inspected v1 directory.
func removeEntries(cacheRoot string, entries []cacheEntry, beforeRemove func()) error {
	if len(entries) == 0 {
		return nil
	}
	versionRoot, err := openCacheVersionRoot(cacheRoot, entries[0].version)
	if err != nil {
		return err
	}
	defer func() { _ = versionRoot.Close() }()
	// Validate every final component before removing any of them. If an
	// uncooperative process replaces one afterwards, Root.RemoveAll unlinks a
	// final symlink without traversing it and confines every intermediate
	// component to the open v1 descriptor.
	for _, entry := range entries {
		if !safeEntryName(entry.name) {
			return fmt.Errorf("goatest: refusing unconfined cache removal %q", entry.name)
		}
		info, err := versionRoot.Lstat(entry.name)
		if err != nil {
			return fmt.Errorf("goatest: inspect cache entry before removal: %w", err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("goatest: cache entry %q is not a confined directory", entry.name)
		}
	}
	if beforeRemove != nil {
		beforeRemove()
	}
	for _, entry := range entries {
		if err := removeEntry(versionRoot, entry.name); err != nil {
			return err
		}
	}
	return nil
}

// openCacheVersionRoot binds removals to the same v1 directory inspection
// observed. Opening first at cacheRoot prevents a replacement symlink from
// escaping it; comparing identities prevents a replacement directory inside
// cacheRoot from being mistaken for the inspected store.
func openCacheVersionRoot(cacheRoot string, expected os.FileInfo) (*os.Root, error) {
	confined, err := os.OpenRoot(cacheRoot)
	if err != nil {
		return nil, fmt.Errorf("goatest: open cache root for removal: %w", err)
	}
	defer func() { _ = confined.Close() }()
	current, err := confined.Lstat("v1")
	if err != nil {
		return nil, fmt.Errorf("goatest: inspect cache v1 root before removal: %w", err)
	}
	if current.Mode()&os.ModeSymlink != 0 || !current.IsDir() || expected == nil || !os.SameFile(expected, current) {
		return nil, errors.New("goatest: cache v1 root changed before removal")
	}
	versionRoot, err := confined.OpenRoot("v1")
	if err != nil {
		return nil, fmt.Errorf("goatest: open cache v1 root for removal: %w", err)
	}
	opened, err := versionRoot.Stat(".")
	if err != nil || !os.SameFile(current, opened) {
		_ = versionRoot.Close()
		if err != nil {
			return nil, fmt.Errorf("goatest: verify cache v1 root for removal: %w", err)
		}
		return nil, errors.New("goatest: cache v1 root changed while opening it for removal")
	}
	return versionRoot, nil
}

// removeEntry recursively removes one direct cache child through versionRoot.
func removeEntry(versionRoot *os.Root, name string) error {
	if err := versionRoot.RemoveAll(name); err != nil {
		return fmt.Errorf("goatest: remove cache entry: %w", err)
	}
	return nil
}

func safeEntryName(name string) bool {
	return name != "" && name != "." && name != ".." && filepath.Base(name) == name && !strings.ContainsAny(name, `/\\`)
}
