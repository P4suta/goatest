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
	path     string
	size     int64
	modified time.Time
	expired  bool
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
	for _, entry := range entries {
		if !entry.expired && (maxBytes <= 0 || remaining <= maxBytes) {
			continue
		}
		if err := removeEntry(root, entry.path); err != nil {
			return GCResult{}, err
		}
		result.RemovedEntries++
		result.RemovedBytes += entry.size
		remaining -= entry.size
	}
	result.After, _, err = inspectUnlocked(root, 0, time.Time{})
	return result, err
}

func inspectUnlocked(root string, ttl time.Duration, now time.Time) (Status, []cacheEntry, error) {
	versionRoot := filepath.Join(root, "v1")
	directories, err := os.ReadDir(versionRoot)
	if errors.Is(err, os.ErrNotExist) {
		return Status{}, []cacheEntry{}, nil
	}
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
		entry := cacheEntry{name: directory.Name(), path: path, size: size, modified: modified}
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

func removeEntry(cacheRoot, target string) error {
	versionRoot := filepath.Join(cacheRoot, "v1")
	relative, err := filepath.Rel(versionRoot, target)
	if err != nil || !filepath.IsLocal(relative) || strings.Contains(relative, string(filepath.Separator)) {
		return fmt.Errorf("goatest: refusing unconfined cache removal %q", target)
	}
	var paths []string
	if err := filepath.WalkDir(target, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("goatest: refusing symlink in cache entry %s", path)
		}
		paths = append(paths, path)
		return nil
	}); err != nil {
		return err
	}
	for index := len(paths) - 1; index >= 0; index-- {
		if err := os.Remove(paths[index]); err != nil {
			return fmt.Errorf("goatest: remove cache entry: %w", err)
		}
	}
	return nil
}

func safeEntryName(name string) bool {
	return name != "" && name != "." && name != ".." && filepath.Base(name) == name && !strings.ContainsAny(name, `/\\`)
}
