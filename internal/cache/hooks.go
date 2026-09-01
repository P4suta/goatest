// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package cache

import (
	"os"
	"time"
)

// storeHooks is the filesystem a cache store reads and commits through, plus
// the collector a bounded store runs after a committed write. Its zero value is
// the os package and the store's own collection, so production passes it unset
// and a test fills in only the operation it drives, keeping the real behaviour
// for the rest.
//
// The hooks travel as an argument rather than as package-level variables. That
// is what lets the cache tests run in parallel: what one test installs is
// reachable only from the call it passed it to.
type storeHooks struct {
	// read reads a stored report.
	read func(path string) ([]byte, error)
	// mkdirAll creates the directory a report is stored in.
	mkdirAll func(path string, perm os.FileMode) error
	// createTemporary opens the temporary file a report is written to before it
	// replaces the stored one.
	createTemporary func(directory, pattern string) (cacheWritableFile, error)
	// remove deletes a temporary file, or the destination a rename refused.
	remove func(path string) error
	// rename publishes a written temporary file as the stored report.
	rename func(oldPath, newPath string) error
	// collect trims the cache to its policy once a write is committed. It runs
	// while the commit still holds the write lock, so the default is the
	// unlocked collector rather than the exported Collect.
	collect func(root string, maxBytes int64, ttl time.Duration, now time.Time) (GCResult, error)
}

// resolved returns the hooks with every unset operation filled in from the os
// package and this package's own unlocked collector.
func (hooks storeHooks) resolved() storeHooks {
	if hooks.read == nil {
		hooks.read = os.ReadFile
	}
	if hooks.mkdirAll == nil {
		hooks.mkdirAll = os.MkdirAll
	}
	if hooks.createTemporary == nil {
		hooks.createTemporary = func(directory, pattern string) (cacheWritableFile, error) {
			return os.CreateTemp(directory, pattern)
		}
	}
	if hooks.remove == nil {
		hooks.remove = os.Remove
	}
	if hooks.rename == nil {
		hooks.rename = os.Rename
	}
	if hooks.collect == nil {
		hooks.collect = collectUnlocked
	}
	return hooks
}
