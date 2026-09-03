// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package assure

// The kinds of temporary directory a run keeps when it was asked to keep them.
// A kind names what the directory was for, because a developer reading a list
// of preserved paths chooses between them rather than opening all of them.
const (
	artifactBaselineScratch   = "baseline-scratch"
	artifactCandidateTree     = "candidate-tree"
	artifactBuildCacheScratch = "build-cache-scratch"
)

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
