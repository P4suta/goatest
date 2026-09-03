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

// LegacyMaxAge is how long a directory with no marker must have been untouched
// before [Sweep] treats it as a leftover. It is generous because the cost of
// waiting is disk and the cost of being wrong is deleting the temporary
// directory of a run that is still using it.
//
// The rule covers two windows. One is a release: every directory made before
// the owner pair existed is unowned, and age is the only evidence available
// about it. The other is a moment, and it is permanent — between the mkdir of a
// run's scratch and the [Claim] that follows it, the directory carries nothing
// at all.
const LegacyMaxAge = 24 * time.Hour

// A Result is what one sweep did or, for [Inspect], would have done. It is
// diagnostic: no verdict, no schema and no exit code depends on it, because the
// job of a run is to measure mutants and collecting somebody else's leftovers
// is housekeeping it does on the way.
type Result struct {
	// Removed holds the path of every directory the sweep collected, or, for an
	// inspection, of every one it would have collected.
	Removed []string
	// RemovedBytes is what they held, as far as the walk could measure.
	RemovedBytes int64
	// Live is how many directories a running process still holds the lock of.
	Live int
	// Kept is how many were preserved on purpose.
	Kept int
	// Errors is one entry per directory the sweep could not judge or could not
	// remove. They are carried rather than returned because a sweep that
	// stopped at the first of them would leave the rest of the disk full.
	Errors []error
}

// Detail renders one result for a progress note or a line of report evidence.
// The first count is named by the caller because a sweep removed what an
// inspection only found.
func (result Result) Detail(removed string) string {
	detail := fmt.Sprintf("%s=%d bytes=%d live=%d kept=%d",
		removed, len(result.Removed), result.RemovedBytes, result.Live, result.Kept)
	if len(result.Errors) != 0 {
		detail += fmt.Sprintf(" errors=%d", len(result.Errors))
	}
	return detail
}

// Sweep collects every abandoned goatest directory directly under parent. An
// empty parent is the operating system's temporary directory, which is where
// os.MkdirTemp puts a directory nobody named a parent for.
//
// A parent that does not exist is not a failure: it is a machine on which
// nothing has run yet.
func Sweep(parent string, prefixes []string, now time.Time) (Result, error) {
	return sweeper{now: now, remove: os.RemoveAll}.sweep(parent, prefixes)
}

// Inspect classifies exactly as [Sweep] does and removes nothing, so that
// `goatest cache status` can report what a `goatest cache gc` would reclaim.
func Inspect(parent string, prefixes []string, now time.Time) (Result, error) {
	return sweeper{now: now, remove: func(string) error { return nil }}.sweep(parent, prefixes)
}

// A sweeper is a sweep with its removal exposed, which is what makes an
// inspection the same code as a collection rather than a second classifier that
// has to be kept in step with the first.
type sweeper struct {
	now    time.Time
	remove func(string) error
}

func (sweep sweeper) sweep(parent string, prefixes []string) (Result, error) {
	if parent == "" {
		parent = os.TempDir()
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
		// A symbolic link is skipped here: ReadDir reports the link itself, so
		// a link is never a directory, and removing one through its name would
		// take a tree nothing in this parent owns.
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

// A verdict is what a sweep decided about one directory.
type verdict int

const (
	// verdictAbandoned is the only one that removes anything.
	verdictAbandoned verdict = iota
	// verdictLive is a directory whose lock somebody holds.
	verdictLive
	// verdictKept is a directory whose marker says it was preserved.
	verdictKept
	// verdictSpared is left alone without being counted as either: an unowned
	// directory too young to judge, and one a failure was reported about.
	// Neither is a fact about a live owner, and reporting one as though it were
	// would put a number in a Result that nothing on the disk backs up.
	verdictSpared
)

// classify decides whether one directory is the sweep's to remove.
//
// A marker that cannot be read at all is treated as a marker that does not say
// kept, deliberately: the lock has already answered the only question that
// matters, and a half-written marker must not make a dead directory immortal.
func (sweep sweeper) classify(dir string, entry fs.DirEntry) (verdict, error) {
	marker, err := ReadMarker(dir)
	switch {
	case err == nil && marker.Kept:
		return verdictKept, nil
	case errors.Is(err, fs.ErrNotExist):
		return sweep.legacy(dir, entry)
	}
	lock, held, lockErr := acquire(LockPath(dir))
	if lockErr != nil {
		return verdictSpared, fmt.Errorf("locking %s: %w", dir, lockErr)
	}
	if !held {
		return verdictLive, nil
	}
	// Closed before the removal rather than after it: on Windows the open
	// handle inside the directory is itself what would refuse the delete.
	if releaseErr := release(lock); releaseErr != nil {
		return verdictSpared, fmt.Errorf("releasing %s: %w", dir, releaseErr)
	}
	return verdictAbandoned, nil
}

// legacy decides about a directory with no marker at all. Age is the only
// evidence there is, and a young one is left alone because it may be a run in
// progress: either one under a binary from before the owner pair existed, or
// one in the moment between its own mkdir and its claim.
func (sweep sweeper) legacy(dir string, entry fs.DirEntry) (verdict, error) {
	info, err := entry.Info()
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return verdictSpared, nil
		}
		return verdictSpared, fmt.Errorf("reading %s: %w", dir, err)
	}
	if sweep.now.Sub(info.ModTime()) < LegacyMaxAge {
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

// Size adds up the regular files under dir, best effort: the number is for a
// person reading a progress line or a status report, and no run may fail to
// reclaim a directory because it could not measure one file inside it.
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
