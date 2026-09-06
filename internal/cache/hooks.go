// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package cache

import (
	"os"
	"time"
)

type storeHooks struct {
	read func(path string) ([]byte, error)

	mkdirAll func(path string, perm os.FileMode) error

	createTemporary func(directory, pattern string) (cacheWritableFile, error)

	remove func(path string) error

	rename func(oldPath, newPath string) error

	collect func(root string, maxBytes int64, ttl time.Duration, now time.Time) (GCResult, error)
}

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
