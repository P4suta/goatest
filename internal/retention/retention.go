// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package retention bounds directories of diagnostic exhaust by age and size.
package retention

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

type Result struct {
	Before         Status
	After          Status
	RemovedEntries int
	RemovedBytes   int64
}

type entry struct {
	name, path string
	size       int64
	modified   time.Time
	expired    bool
}

func Inspect(root string) (Status, error) {
	status, _, err := inspect(root, 0, time.Time{})
	return status, err
}

// Collect removes expired recording directories first, then oldest
// directories until maxBytes is met. It never follows a symbolic link.
func Collect(root string, maxBytes int64, ttl time.Duration, now time.Time) (Result, error) {
	if maxBytes < 0 || ttl < 0 {
		return Result{}, errors.New("goatest: retention policy must not be negative")
	}
	before, entries, err := inspect(root, ttl, now)
	if err != nil {
		return Result{}, err
	}
	result := Result{Before: before}
	slices.SortFunc(entries, func(a, b entry) int {
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
	for _, candidate := range entries {
		if !candidate.expired && (maxBytes <= 0 || remaining <= maxBytes) {
			continue
		}
		if err := remove(root, candidate.path); err != nil {
			return Result{}, err
		}
		result.RemovedEntries++
		result.RemovedBytes += candidate.size
		remaining -= candidate.size
	}
	result.After, _, err = inspect(root, 0, time.Time{})
	return result, err
}

func inspect(root string, ttl time.Duration, now time.Time) (Status, []entry, error) {
	directories, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return Status{}, []entry{}, nil
	}
	if err != nil {
		return Status{}, nil, fmt.Errorf("goatest: inspect retained artifacts: %w", err)
	}
	var status Status
	entries := make([]entry, 0, len(directories))
	for _, directory := range directories {
		if !safeName(directory.Name()) || directory.Type()&os.ModeSymlink != 0 || !directory.IsDir() {
			return Status{}, nil, fmt.Errorf("goatest: retained artifact %q is not a confined directory", directory.Name())
		}
		path := filepath.Join(root, directory.Name())
		size, modified, err := metadata(path)
		if err != nil {
			return Status{}, nil, err
		}
		candidate := entry{name: directory.Name(), path: path, size: size, modified: modified}
		if ttl > 0 && !now.IsZero() && !modified.IsZero() {
			candidate.expired = !modified.Add(ttl).After(now)
		}
		entries = append(entries, candidate)
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

func metadata(root string) (int64, time.Time, error) {
	var size int64
	var modified time.Time
	err := filepath.WalkDir(root, func(path string, item fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if item.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("goatest: retained artifact crosses symbolic link %s", path)
		}
		if item.IsDir() {
			return nil
		}
		info, err := item.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("goatest: retained artifact contains irregular file %s", path)
		}
		size += info.Size()
		if info.ModTime().After(modified) {
			modified = info.ModTime()
		}
		return nil
	})
	return size, modified, err
}

func remove(root, target string) error {
	relative, err := filepath.Rel(root, target)
	if err != nil || !filepath.IsLocal(relative) || strings.Contains(relative, string(filepath.Separator)) {
		return fmt.Errorf("goatest: refusing unconfined retained artifact removal %q", target)
	}
	var paths []string
	if err := filepath.WalkDir(target, func(path string, item fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if item.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("goatest: refusing symbolic link in retained artifact %s", path)
		}
		paths = append(paths, path)
		return nil
	}); err != nil {
		return err
	}
	for index := len(paths) - 1; index >= 0; index-- {
		if err := os.Remove(paths[index]); err != nil {
			return fmt.Errorf("goatest: remove retained artifact: %w", err)
		}
	}
	return nil
}

func safeName(name string) bool {
	return name != "" && name != "." && name != ".." && filepath.Base(name) == name && !strings.ContainsAny(name, `/\\`)
}
