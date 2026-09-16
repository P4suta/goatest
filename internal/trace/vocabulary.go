// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package trace

import "slices"

// The closed vocabularies of goatest-trace-v1, as the lists a reader can walk.
//
// Every one of these sets is written down in at least three places: the
// constants above, the enum in schema.json, and a table or sentence in
// docs/trace-v1.md. Two of them were written down in five, because reader.go
// and internal/devtools/tracesummary each carried their own copy of the
// prepare phases as a switch.
//
// A ledger that compares three of five copies is green while two drift, and
// that is not a hypothetical: the copies existed and nothing compared them.
// These functions exist so that the number of places a vocabulary is spelled
// out goes back down to two - the constants and the schema - with everything
// else derived, and so that a ledger has something to compare the schema and
// the documentation against.
//
// Each returns a fresh slice, because a caller that sorted the vocabulary in
// place would change what every later caller reads.

// Types are the event types a recording may hold, in the order a run emits
// them for the first time.
func Types() []string {
	return []string{
		TypeRunStart, TypePhaseStart, TypePhaseEnd, TypePrepare, TypeExec,
		TypeMutantExec, TypeRoute, TypeProbeExec, TypeProgress, TypeArtifact,
		TypeRunEnd,
	}
}

// PreparePhases are the stages of one mutation-session preparation.
//
// The vocabulary is go-mutants', not this module's: the engine names the phase
// and goatest records what it was told. Keeping it closed here is a decision
// with a cost - an engine that adds a phase makes this list wrong - and the
// ledger beside it is what turns that cost into a failing test rather than a
// failing run.
func PreparePhases() []string {
	return []string{
		PreparePhaseDiscovery, PreparePhaseProbeSnapshot, PreparePhaseMainValidation,
		PreparePhaseMainRestoration, PreparePhaseVerification, PreparePhaseBinaryBuild,
		PreparePhaseProbeValidation, PreparePhaseProbeCoverageBuild, PreparePhaseProbeRestoration,
	}
}

// PrepareStates are the two edges of a preparation stage.
func PrepareStates() []string {
	return []string{PrepareStateStarted, PrepareStateFinished}
}

// PrepareResults are the ways a finished preparation stage ended.
func PrepareResults() []string {
	return []string{PrepareResultSucceeded, PrepareResultFailed, PrepareResultSkipped}
}

// RouteReasons are why a target is in a mutant's reaching set.
func RouteReasons() []string {
	return []string{ReasonCoverageReaching, ReasonProbeReaching, ReasonUnreached}
}

// Granularities are how precisely a mutant was placed in the coverage profile.
func Granularities() []string {
	return []string{GranularityBlock, GranularityFile}
}

// RouteFallbacks are why block routing gave way to routing by whole file.
func RouteFallbacks() []string {
	return []string{FallbackPositionUnknown, FallbackOutsideBlocks}
}

// DischargeReasons are why a target that covers a mutant was proved unable to
// observe it.
func DischargeReasons() []string {
	return []string{DischargeBranchNeverTaken, DischargeNeverInfected}
}

// ProbeOutcomes are how one infection probe ended.
//
// Only the first carries an infection set. The other three are separate
// reasons for there being none, and a reader that folds them into "not
// infected" has thrown away the difference between a probe that saw nothing
// and a probe that never got far enough to see.
func ProbeOutcomes() []string {
	return []string{
		ProbeOutcomeMeasured, ProbeOutcomeTestFailed, ProbeOutcomeTimedOut,
		ProbeOutcomeUnavailable,
	}
}

// WholeTreeReasons are why a behaviour key had to widen to the whole tree.
func WholeTreeReasons() []string {
	return []string{
		WholeTreeStaticUnobservable, WholeTreeLogUnavailable, WholeTreeLogAmbiguous,
		WholeTreeDirectoryAccess, WholeTreeOutsideInput,
	}
}

// knownPreparePhase reports whether a recorded phase is one this contract
// names.
func knownPreparePhase(phase string) bool {
	return slices.Contains(PreparePhases(), phase)
}
