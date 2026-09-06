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

const (
	artifactBaselineScratch    = "baseline-scratch"
	artifactCandidateTree      = "candidate-tree"
	artifactBuildCacheScratch  = "build-cache-scratch"
	artifactNativeCacheScratch = "native-build-cache-scratch"
	artifactRunScratch         = "run-scratch"
	artifactMutationWorkspace  = "mutation-workspace"
)

func releaseRunScratch(options Options, remove func(string) error, scratch runScratch, now time.Time) {
	if scratch.dir == "" {
		return
	}
	if options.KeepTemp {
		if err := scratch.owner.Keep(); err != nil {
			emit(options, "temp-unavailable", err.Error())
		}
		recordKept(options, scratch, artifactRunScratch, []string{scratch.dir}, now)
		return
	}

	if err := scratch.owner.Release(); err != nil {
		emit(options, "temp-unavailable", err.Error())
	}
	if err := remove(scratch.dir); err != nil {
		emit(options, "temp-unavailable", err.Error())
	}
}

func releaseBaselineScratch(options Options, remove func(string) error, directory string) error {
	if options.KeepTemp {
		options.Trace.Artifact(artifactBaselineScratch, directory)
		return nil
	}
	return remove(directory)
}

func releaseBuildCache(options Options, cache runBuildCache, scratch runScratch, now time.Time) error {
	if !cache.serves() {
		return nil
	}
	if options.KeepTemp {
		if err := cache.close(true); err != nil {
			return err
		}
		options.Trace.Artifact(artifactBuildCacheScratch, cache.scratch)
		if cache.native != "" {
			if scratch.root != "" && scratch.id != "" {
				recordKept(options, scratch, artifactNativeCacheScratch, []string{cache.native}, now)
			} else {
				options.Trace.Artifact(artifactNativeCacheScratch, cache.native)
			}
		}
		return nil
	}
	return cache.close(false)
}

func (validator *repositoryValidator) releaseCandidate(root string) {
	if validator.options.KeepTemp {
		validator.options.Trace.Artifact(artifactCandidateTree, root)
		return
	}
	_ = removeCandidateTemp(root)
}

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

func recordTemporaryArtifacts(options Options, kind string, paths []string) {
	for _, path := range paths {
		options.Trace.Artifact(kind, path)
	}
}

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
