// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package buildcache

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/P4suta/goatest/internal/advisorylock"
)

// collectionMarkerPath is the file whose time records the last collection of
// this layer and whose lock serializes collections of it. One file carries both
// because they are one question: who collected this layer, and when.
func (layer Layer) collectionMarkerPath() string {
	return filepath.Join(layer.Dir, collectedName)
}

// HoldCollection takes this layer's collection lock and returns the release.
//
// It reports false when another process holds it, which is a process already
// collecting this layer. That is never an error: the base layer is shared by
// every repository on the machine, so yielding to whoever got there first
// leaves the layer bounded either way and costs the caller nothing. The release
// is always safe to call, whether or not the lock was taken.
func (layer Layer) HoldCollection() (func() error, bool, error) {
	if layer.Dir == "" {
		return func() error { return nil }, false, nil
	}
	file, err := os.OpenFile(layer.collectionMarkerPath(), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return func() error { return nil }, false, fmt.Errorf("goatest: open build cache collection lock: %w", err)
	}
	locked, err := advisorylock.Try(file)
	if err != nil {
		_ = file.Close()
		return func() error { return nil }, false, fmt.Errorf("goatest: lock build cache collection: %w", err)
	}
	if !locked {
		_ = file.Close()
		return func() error { return nil }, false, nil
	}
	return func() error {
		unlockErr := advisorylock.Release(file)
		closeErr := file.Close()
		if joined := errors.Join(unlockErr, closeErr); joined != nil {
			return fmt.Errorf("goatest: release build cache collection lock: %w", joined)
		}
		return nil
	}, true, nil
}

// CollectLocked collects the layer if it is this process's turn, and reports
// whether it ran.
//
// Two things can make it not this process's turn. Another process may be
// collecting the layer, in which case there is nothing to do. Or the layer may
// have been collected within interval, which is what keeps the cost of the
// bound off the hot path: a run's scratch layer is asked to collect once per go
// command and there are thousands of them, so it walks the layer once per
// interval instead. An interval of zero always collects, which is what a run
// asks for when it ends.
func (layer Layer) CollectLocked(policy Policy, interval time.Duration, now time.Time) (Collected, bool, error) {
	return layer.collectLockedWithHooks(policy, interval, now, layerHooks{})
}

// collectLockedWithHooks is CollectLocked against a filesystem the caller
// supplies.
func (layer Layer) collectLockedWithHooks(policy Policy, interval time.Duration, now time.Time, hooks layerHooks) (Collected, bool, error) {
	hooks = hooks.resolved()
	if err := policy.validate(); err != nil {
		return Collected{}, false, err
	}
	if layer.Dir == "" {
		return Collected{}, false, nil
	}
	// Reading the record before taking the lock is what makes the common answer
	// — no, not yet — cost one stat rather than an open and a syscall.
	if layer.collectedRecently(interval, now, hooks) {
		return Collected{}, false, nil
	}
	release, held, err := layer.HoldCollection()
	if errors.Is(err, os.ErrNotExist) {
		// A layer no machine has built yet holds nothing to collect. A run asks
		// for this on its first ever verification, and so does cache gc.
		return Collected{}, false, nil
	}
	if err != nil {
		return Collected{}, false, err
	}
	if !held {
		return Collected{}, false, nil
	}
	defer func() { _ = release() }()
	// Another process may have collected between the read above and the lock.
	if layer.collectedRecently(interval, now, hooks) {
		return Collected{}, false, nil
	}
	collected, err := layer.collectWithHooks(policy, now, hooks)
	if err != nil {
		return Collected{}, false, err
	}
	// The record is what the next collection reads to decide whether to run. A
	// record that cannot be written costs an extra walk and nothing else, so it
	// is never worth failing a collection that already happened.
	_ = hooks.chtimes(layer.collectionMarkerPath(), now, now)
	return collected, true, nil
}

// collectedRecently reports whether this layer was collected within interval.
// A record that cannot be read is no record: the collection runs, which is the
// fallback that keeps the layer bounded rather than the one that keeps it fast.
func (layer Layer) collectedRecently(interval time.Duration, now time.Time, hooks layerHooks) bool {
	if interval <= 0 || now.IsZero() {
		return false
	}
	info, err := hooks.stat(layer.collectionMarkerPath())
	if err != nil {
		return false
	}
	elapsed := now.Sub(info.ModTime())
	return elapsed >= 0 && elapsed < interval
}
