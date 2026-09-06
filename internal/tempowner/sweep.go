// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package tempowner

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const UnclaimedMaxAge = 24 * time.Hour

type Result struct {
	Removed []string

	RemovedBytes int64

	Live int

	Kept int

	Errors []error
}

func (result Result) Detail(removed string) string {
	detail := fmt.Sprintf("%s=%d bytes=%d live=%d kept=%d",
		removed, len(result.Removed), result.RemovedBytes, result.Live, result.Kept)
	if len(result.Errors) != 0 {
		detail += fmt.Sprintf(" errors=%d", len(result.Errors))
	}
	return detail
}

func Sweep(parent string, prefixes []string, now time.Time) (Result, error) {
	return sweeper{now: now, remove: os.RemoveAll}.sweep(parent, prefixes)
}

func Inspect(parent string, prefixes []string, now time.Time) (Result, error) {
	return sweeper{now: now, remove: func(string) error { return nil }}.sweep(parent, prefixes)
}

type sweeper struct {
	now    time.Time
	remove func(string) error
}

func (sweep sweeper) sweep(parent string, prefixes []string) (Result, error) {
	if parent == "" {
		return Result{}, nil
	}
	entries, err := os.ReadDir(parent)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Result{}, nil
		}
		return Result{}, fmt.Errorf("reading %s: %w", parent, err)
	}
	var result Result
	for _, entry := range entries {
		if !entry.IsDir() || !hasAnyPrefix(entry.Name(), prefixes) {
			continue
		}
		dir := filepath.Join(parent, entry.Name())
		decision, err := sweep.classify(dir, entry)
		if err != nil {
			result.Errors = append(result.Errors, err)
			continue
		}
		switch decision {
		case verdictLive:
			result.Live++
			continue
		case verdictKept:
			result.Kept++
			continue
		case verdictSpared:
			continue
		case verdictAbandoned:
		}
		size := Size(dir)
		if err := sweep.remove(dir); err != nil {
			result.Errors = append(result.Errors, fmt.Errorf("removing %s: %w", dir, err))
			continue
		}
		result.Removed = append(result.Removed, dir)
		result.RemovedBytes += size
	}
	return result, nil
}

type verdict int

const (
	verdictAbandoned verdict = iota

	verdictLive

	verdictKept

	verdictSpared
)

func (sweep sweeper) classify(dir string, entry fs.DirEntry) (verdict, error) {
	marker, err := ReadMarker(dir)
	switch {
	case err == nil && marker.Kept:
		return verdictKept, nil
	case errors.Is(err, fs.ErrNotExist):
		return sweep.unclaimed(dir, entry)
	}
	lock, held, lockErr := acquire(LockPath(dir))
	if lockErr != nil {
		return verdictSpared, fmt.Errorf("locking %s: %w", dir, lockErr)
	}
	if !held {
		return verdictLive, nil
	}

	if releaseErr := release(lock); releaseErr != nil {
		return verdictSpared, fmt.Errorf("releasing %s: %w", dir, releaseErr)
	}
	return verdictAbandoned, nil
}

func (sweep sweeper) unclaimed(dir string, entry fs.DirEntry) (verdict, error) {
	info, err := entry.Info()
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return verdictSpared, nil
		}
		return verdictSpared, fmt.Errorf("reading %s: %w", dir, err)
	}
	if sweep.now.Sub(info.ModTime()) < UnclaimedMaxAge {
		return verdictSpared, nil
	}
	return verdictAbandoned, nil
}

func hasAnyPrefix(name string, prefixes []string) bool {
	for _, prefix := range prefixes {
		if prefix != "" && strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

func Size(dir string) int64 {
	var total int64
	_ = filepath.WalkDir(dir, func(_ string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return nil
		}
		total += info.Size()
		return nil
	})
	return total
}
