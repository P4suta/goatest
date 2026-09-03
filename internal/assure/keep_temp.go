// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package assure

import (
	"fmt"
	"time"

	gomutants "github.com/P4suta/go-mutants"
	"github.com/P4suta/goatest/internal/keptledger"
	"github.com/P4suta/goatest/internal/tempowner"
)

// The kinds of temporary directory a run keeps when it was asked to keep them.
// A kind names what the directory was for, because a developer reading a list
// of preserved paths chooses between them rather than opening all of them.
const (
	artifactBaselineScratch   = "baseline-scratch"
	artifactCandidateTree     = "candidate-tree"
	artifactBuildCacheScratch = "build-cache-scratch"
	artifactRunScratch        = "run-scratch"
	artifactMutationWorkspace = "mutation-workspace"
)

// releaseRunScratch removes the directory a run made everything else below, or
// keeps it when the run was asked to keep its temporaries.
//
// Nothing here can fail a run. The run has ended, and a directory that could
// not be removed costs the disk and never the verdict — which is also why what
// went wrong is reported through the progress stream rather than returned: the
// only caller is a deferred close with nowhere to put an error.
func releaseRunScratch(options Options, remove func(string) error, scratch runScratch, now time.Time) {
	if scratch.dir == "" {
		return
	}
	if options.KeepTemp {
		// Marked before it is named, so that the next sweep reads a directory
		// somebody kept on purpose rather than one whose lock nobody holds.
		if err := scratch.owner.Keep(); err != nil {
			emit(options, "temp-unavailable", err.Error())
		}
		recordKept(options, scratch, artifactRunScratch, []string{scratch.dir}, now)
		return
	}
	// The lock is closed before the removal and not after it: on Windows an
	// open handle inside a directory is what makes the removal fail.
	if err := scratch.owner.Release(); err != nil {
		emit(options, "temp-unavailable", err.Error())
	}
	if err := remove(scratch.dir); err != nil {
		emit(options, "temp-unavailable", err.Error())
	}
}

// releaseBaselineScratch removes the scratch directory a round collected its
// baseline in, or keeps it when the run was asked to keep its temporaries.
//
// Keeping is the whole of the change to the run: the directory stays where it
// was made, and the artifact event is the run's account of having left it
// there. That account is what carries the path into a trace and into the
// preserved paths of a failed run's diagnostics bundle.
func releaseBaselineScratch(options Options, remove func(string) error, directory string) error {
	if options.KeepTemp {
		options.Trace.Artifact(artifactBaselineScratch, directory)
		return nil
	}
	return remove(directory)
}

// releaseBuildCache removes the scratch layer of the run's build cache, or
// keeps it when the run was asked to keep its temporaries. It is kept and named
// for the same reason the baseline scratch is: what a run compiled and where it
// went is exactly what a developer investigating a slow run wants to open.
func releaseBuildCache(options Options, cache runBuildCache) error {
	if !cache.serves() {
		return nil
	}
	if options.KeepTemp {
		options.Trace.Artifact(artifactBuildCacheScratch, cache.scratch)
	}
	return cache.close(options.KeepTemp)
}

// releaseCandidate removes the isolated tree a candidate was validated in, or
// keeps it when the validator was asked to keep its temporaries. The tree a
// rejected candidate was rejected in is the one a developer reads, so it is
// kept whole and recorded the same way a kept scratch directory is.
func (validator *repositoryValidator) releaseCandidate(root string) {
	if validator.options.KeepTemp {
		validator.options.Trace.Artifact(artifactCandidateTree, root)
		return
	}
	_ = removeCandidateTemp(root)
}

// recordKept names the directories a run left on the disk on purpose: as
// artifacts of the recording, which is what a developer reading a trace and the
// bundle of a failed run sees, and in the repository's ledger, which is what
// outlives the run and what `goatest cache status` and `goatest cache gc` read.
//
// A ledger that cannot be written is a note and never a failure. The
// directories are on the disk either way; what is lost is the record that would
// have collected them later, and the run has already established everything it
// claims.
func recordKept(options Options, scratch runScratch, kind string, paths []string, now time.Time) {
	if len(paths) == 0 {
		return
	}
	entries := make([]keptledger.Entry, 0, len(paths))
	for _, path := range paths {
		options.Trace.Artifact(kind, path)
		entries = append(entries, keptledger.Entry{
			Path: path, RunID: scratch.id, KeptAt: now, Bytes: tempowner.Size(path),
		})
	}
	if err := keptledger.Append(keptledger.Path(scratch.root), entries...); err != nil {
		emit(options, "kept-temp-unrecorded", err.Error())
	}
}

// reportMutationSweep notes what the mutation engine collected before it copied
// anything, which it does whenever a workspace is opened.
//
// A sweep that reclaimed nothing says nothing, for the reason
// sweepRunTemporaries says nothing: a note in the progress stream of every run
// of every repository that never crashes is a note a reader learns to skip. A
// sweep that could not finish says so, because a temporary directory nobody can
// collect is exactly what a person watching a disk fill up needs to be told.
func reportMutationSweep(options Options, swept gomutants.SweepResult) {
	if len(swept.Removed) == 0 && swept.Err == nil {
		return
	}
	detail := fmt.Sprintf("removed=%d bytes=%d live=%d kept=%d",
		len(swept.Removed), swept.RemovedBytes, swept.Live, swept.Kept)
	if swept.Err != nil {
		detail += " error=" + swept.Err.Error()
	}
	emit(options, "mutation-temp-sweep", detail)
}
