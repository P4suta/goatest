// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package report

import "slices"

// Limitation codes.
//
// A code is the identifier a reader greps for. It appears in plain output as
// `LIMITATION <code> <summary>`, in HTML as a `<code>` element and in JSON as
// `limitations[].code`, so it is a name this project has published whether or
// not it meant to. Codes were string literals at their raising sites until this
// ledger; the summaries beside them still are, because a summary is prose for a
// person and a code is a key for a search.
//
// These are not the numbered error codes. An error code names a failure that
// ended something; a limitation names a caveat on a run that reached a verdict
// anyway, which is why a report can carry several and still be ASSURED.
const (
	// LimitationAssuranceIncomplete marks a run that stopped on an
	// infrastructure failure rather than on a verdict about the code.
	LimitationAssuranceIncomplete = "assurance-incomplete"
	// LimitationAssuranceInterrupted marks a run that was stopped by a signal.
	// Its report records the run and not its results; the diagnostics bundle
	// written beside it holds how far the run got.
	LimitationAssuranceInterrupted = "assurance-interrupted"
	// LimitationConfigurationMetadataUnavailable marks a report whose effective
	// configuration could not be read while it was being finalized.
	LimitationConfigurationMetadataUnavailable = "configuration-metadata-unavailable"
	// LimitationContractMetadataUnavailable marks a report whose assurance
	// contract could not be resolved before execution stopped.
	LimitationContractMetadataUnavailable = "contract-metadata-unavailable"
	// LimitationGitMetadataUnavailable marks a report whose Git identity or
	// changeset metadata could not be resolved. A report whose Git metadata is
	// unavailable must carry it; ValidateForPersistence refuses one that does
	// not, because an absent identity and an unstated absent identity read the
	// same in JSON.
	LimitationGitMetadataUnavailable = "git-metadata-unavailable"
	// LimitationGoMutantsMetadataUnavailable marks a report whose go-mutants
	// version could not be resolved from build information.
	LimitationGoMutantsMetadataUnavailable = "go-mutants-metadata-unavailable"
	// LimitationGoToolchainMetadataUnavailable marks a report whose Go toolchain
	// identity could not be resolved before execution stopped.
	LimitationGoToolchainMetadataUnavailable = "go-toolchain-metadata-unavailable"
	// LimitationLaterPhasesNotRun marks a run whose later phases were skipped
	// because an earlier one did not pass.
	LimitationLaterPhasesNotRun = "later-phases-not-run"
	// LimitationModuleMetadataUnavailable marks a report whose Go module
	// identity could not be resolved before execution stopped.
	LimitationModuleMetadataUnavailable = "module-metadata-unavailable"
	// LimitationPlanCostEstimate marks a plan whose cost excludes
	// target-specific runtime, resource startup and the race pass.
	LimitationPlanCostEstimate = "plan-cost-estimate"
	// LimitationProjectExclude marks a run whose configured boundary put some
	// paths outside what was assured.
	LimitationProjectExclude = "project-exclude"
	// LimitationRaceScopeStaticEstimate marks a race scope counted statically
	// rather than from what the pass executed.
	LimitationRaceScopeStaticEstimate = "race-scope-static-estimate"
	// LimitationRepairPreimageChanged marks a repair batch left unapplied
	// because at least one preimage had changed.
	LimitationRepairPreimageChanged = "repair-preimage-changed"
	// LimitationRepairValidationRejected marks a repair batch left unapplied
	// because at least one fresh validation failed.
	LimitationRepairValidationRejected = "repair-validation-rejected"
	// LimitationResourceCacheDisabled marks a run whose exact cache reuse was
	// disabled because a configured resource carries runtime state.
	LimitationResourceCacheDisabled = "resource-cache-disabled"
	// LimitationSnapshotMetadataUnavailable marks a report whose source
	// snapshot identity could not be computed before execution stopped.
	LimitationSnapshotMetadataUnavailable = "snapshot-metadata-unavailable"
	// LimitationUnresolvedMutationGaps marks a run that finished with mutation
	// evidence gaps it could not resolve.
	LimitationUnresolvedMutationGaps = "unresolved-mutation-gaps"
	// LimitationWholeTreeBehaviourKeys marks a run whose behaviour keys were
	// taken from the whole tree rather than from the observed changeset.
	LimitationWholeTreeBehaviourKeys = "whole-tree-behaviour-keys"
)

// limitationCodes is every code a persisted report may carry, sorted.
var limitationCodes = []string{
	LimitationAssuranceIncomplete,
	LimitationAssuranceInterrupted,
	LimitationConfigurationMetadataUnavailable,
	LimitationContractMetadataUnavailable,
	LimitationGitMetadataUnavailable,
	LimitationGoMutantsMetadataUnavailable,
	LimitationGoToolchainMetadataUnavailable,
	LimitationLaterPhasesNotRun,
	LimitationModuleMetadataUnavailable,
	LimitationPlanCostEstimate,
	LimitationProjectExclude,
	LimitationRaceScopeStaticEstimate,
	LimitationRepairPreimageChanged,
	LimitationRepairValidationRejected,
	LimitationResourceCacheDisabled,
	LimitationSnapshotMetadataUnavailable,
	LimitationUnresolvedMutationGaps,
	LimitationWholeTreeBehaviourKeys,
}

// LimitationCodes returns every code a persisted report may carry, sorted.
//
// This is the set docs/limitations.md is pinned against, in both directions.
func LimitationCodes() []string { return slices.Clone(limitationCodes) }

// KnownLimitationCode reports whether a code is one a report may carry.
func KnownLimitationCode(code string) bool {
	return slices.Contains(limitationCodes, code)
}
