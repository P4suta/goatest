// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package assure

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/P4suta/goatest/internal/tempowner"
)

const (
	// runScratchPrefix names the one directory a run makes for itself. It
	// carries the tool's name because it sits among everybody else's temporary
	// directories, and the sweep recognizes its own work by exactly this name.
	runScratchPrefix = "goatest-run-"

	// The directories below a run scratch. Their names say what each one is
	// for and nothing else: which tool made them is already answered by the
	// parent they are in.
	baselineScratchName = "baseline-"
	candidateTreeName   = "candidate-"
	controlFuzzName     = "control-fuzz-"

	// buildScratchName is the build cache layer that dies with the run. It is
	// the one fixed name below a run scratch, because a run has exactly one of
	// them and a random suffix would only make it harder to find.
	buildScratchName = "build"

	// legacyPrefix is what the same directories are called when a run could not
	// make a scratch to put them in, and what every directory made before this
	// convention is called. A sweep knows them by these names alone.
	legacyPrefix = "goatest-"

	// legacyBuildCachePrefix is the build cache layer under that rule. It is
	// spelled out rather than derived, because the fixed name below a run
	// scratch and the name beside one are two different conventions that only
	// look related.
	legacyBuildCachePrefix = legacyPrefix + "build-cache-"
)

// TemporaryPrefixes are the names goatest makes directly under the temporary
// root, and so the only names its sweep may collect. Everything else in that
// directory belongs to somebody else, including go-mutants' own snapshots and
// scratch directories: those carry owner files of their own and are collected
// by go-mutants' sweep, which runs whenever a workspace is opened.
//
// It is exported for `goatest cache status` and `goatest cache gc`, which sweep
// the same directory on demand and must sweep exactly what a run sweeps: two
// lists that could disagree would be two conventions.
func TemporaryPrefixes() []string {
	return []string{
		runScratchPrefix,
		legacyPrefix + baselineScratchName,
		legacyPrefix + candidateTreeName,
		legacyPrefix + controlFuzzName,
		legacyBuildCachePrefix,
	}
}

// runScratch is the one directory below which everything a run writes outside
// the repository lives: the scratch layer of its build cache, the artifacts of
// each baseline round, the tree each candidate is validated in, and the fuzz
// cache of an original-control execution.
//
// Its zero value is a run that has none, which is what a run gets when the
// directory could not be made. Everything below then falls back to the
// temporary root and the names it used before this directory existed, because a
// run that cannot make one directory must still be able to run.
type runScratch struct {
	// dir is the scratch itself, or empty when it could not be made.
	dir string
	// fallback is the temporary root the run was configured with, which is
	// where its directories go when there is no scratch to put them in.
	fallback string
	// root is the repository the run is verifying. The ledger of what the run
	// kept lives there, and the marker names it so that a person who finds the
	// directory knows which project it belongs to.
	root string
	// id names the run in the marker and in every ledger entry it writes.
	id string
	// owner holds the lock and the marker for as long as the run is using the
	// directory. It is nil when the pair could not be written, which costs the
	// next run's sweep its liveness signal and costs this run nothing.
	owner *tempowner.Owner
}

// openRunScratch makes the directory of one run and claims it in that run's
// name.
//
// It answers with the scratch and the failure together on purpose: a run that
// could not make or claim one still has somewhere to write, which is beside it
// under the names the sweep knows, and neither failure is a reason to stop.
//
// A directory that was made but could not be claimed is deliberately not used.
// Nothing in it says who is working there, so the next sweep would judge it by
// age alone and could remove it with a live run's work inside. The run takes
// back the empty directory it just made — unless the claim failed because
// somebody else holds the lock, in which case, however implausibly the run came
// by that path, it is not the run's to remove.
func openRunScratch(makeScratch func(string, string) (string, error), removeScratch func(string) error, fallback, root string, now time.Time) (runScratch, error) {
	scratch := runScratch{fallback: fallback, root: root, id: unownedRunID(now)}
	directory, err := makeScratch(fallback, runScratchPrefix)
	if err != nil {
		return scratch, fmt.Errorf("goatest: create run scratch: %w", err)
	}
	// The name mkdtemp made is the run's identity from here on: it is unique by
	// construction, it is the name a person sees on the disk, and it is what
	// ties a marker, a trace artifact and a ledger entry to one another.
	identity := filepath.Base(directory)
	owner, err := tempowner.Claim(directory, tempowner.Marker{RunID: identity, Root: root}, now)
	if err != nil {
		failure := fmt.Errorf("goatest: claim run scratch: %w", err)
		if errors.Is(err, tempowner.ErrOwned) {
			return scratch, failure
		}
		return scratch, errors.Join(failure, removeScratch(directory))
	}
	scratch.dir, scratch.id, scratch.owner = directory, identity, owner
	return scratch, nil
}

// unownedRunID names a run that has no scratch directory to be named after. It
// follows the convention a recording uses for a run that never reached a report
// identity — the moment it ran and the process it ran in — because that is the
// only identity available before anything of the run exists.
func unownedRunID(now time.Time) string {
	return runScratchPrefix + now.UTC().Format("20060102T150405Z") + "-" + strconv.Itoa(os.Getpid())
}

// subdirectory names the parent and the prefix one kind of the run's temporary
// directories is made with: below the run scratch under the short name, or,
// where there is no scratch, beside it under the name the sweep knows.
func (scratch runScratch) subdirectory(name string) (string, string) {
	if scratch.dir == "" {
		return scratch.fallback, legacyPrefix + name
	}
	return scratch.dir, name
}

// buildCacheLayer makes the layer of the build cache that dies with the run.
func (scratch runScratch) buildCacheLayer() (string, error) {
	if scratch.dir == "" {
		return os.MkdirTemp(scratch.fallback, legacyBuildCachePrefix)
	}
	directory := filepath.Join(scratch.dir, buildScratchName)
	if err := os.Mkdir(directory, 0o700); err != nil {
		return "", err
	}
	return directory, nil
}

// sweepRunTemporaries collects what runs that died before they could tidy up
// left in the temporary root, before this run writes anything into it.
//
// A clean machine says nothing: a note that reports having reclaimed nothing
// would be in the progress stream of every run of every repository that never
// crashes, and a reader learns to skip exactly that kind of line.
func sweepRunTemporaries(options Options, sweep func(string, []string, time.Time) (tempowner.Result, error), now time.Time) {
	// Only where the caller named a directory. A run whose temporary root is
	// empty still makes its scratch where the operating system puts one —
	// creating a directory there is what every temporary directory has always
	// done — but collecting there is another thing entirely: it would reach the
	// directories of every goatest on the machine, live runs included, on the
	// strength of a value nobody set.
	if options.TempDirectory == "" {
		return
	}
	result, err := sweep(options.TempDirectory, TemporaryPrefixes(), now)
	if err != nil {
		result.Errors = append(result.Errors, err)
	}
	if len(result.Removed) == 0 && len(result.Errors) == 0 {
		return
	}
	emit(options, "temp-sweep", result.Detail("removed"))
}
