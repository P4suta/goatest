// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package trace

const SchemaV1 = "goatest-trace-v1"

const (
	PackageSuiteProbePrefix    = "package-suite:"
	PackageSuiteCoveragePrefix = "package-suite-coverage:"
	MutationControlProbePrefix = "mutation-control:"
)

const (
	TypeRunStart   = "run-start"
	TypePhaseStart = "phase-start"
	TypePhaseEnd   = "phase-end"
	TypePrepare    = "prepare"
	TypeExec       = "exec"
	TypeMutantExec = "mutant-exec"
	TypeRoute      = "route"
	TypeProbeExec  = "probe-exec"
	TypeProgress   = "progress"
	TypeArtifact   = "artifact"
	TypeRunEnd     = "run-end"
)

const (
	PrepareStateStarted  = "started"
	PrepareStateFinished = "finished"
)

const (
	PreparePhaseDiscovery          = "discovery"
	PreparePhaseProbeSnapshot      = "probe_snapshot"
	PreparePhaseMainValidation     = "main_validation"
	PreparePhaseMainRestoration    = "main_restoration"
	PreparePhaseVerification       = "verification"
	PreparePhaseBinaryBuild        = "binary_build"
	PreparePhaseProbeValidation    = "probe_validation"
	PreparePhaseProbeCoverageBuild = "probe_coverage_build"
	PreparePhaseProbeRestoration   = "probe_restoration"
)

const (
	PrepareResultSucceeded = "succeeded"
	PrepareResultFailed    = "failed"
	PrepareResultSkipped   = "skipped"
)

const (
	ReasonCoverageReaching = "coverage-reaching"
	ReasonProbeReaching    = "probe-reaching"
	ReasonUnreached        = "unreached"
)

const (
	GranularityBlock = "block"
	GranularityFile  = "file"
)

const (
	FallbackPositionUnknown = "position-unknown"
	FallbackOutsideBlocks   = "outside-blocks"
)

const (
	DischargeBranchNeverTaken = "branch-never-taken"
	DischargeNeverInfected    = "never-infected"
)

const (
	ProbeOutcomeMeasured    = "measured"
	ProbeOutcomeTestFailed  = "test-failed"
	ProbeOutcomeTimedOut    = "timed-out"
	ProbeOutcomeUnavailable = "unavailable"
)

const (
	WholeTreeStaticUnobservable = "static-unobservable"
	WholeTreeLogUnavailable     = "log-unavailable"
	WholeTreeLogAmbiguous       = "log-ambiguous"
	WholeTreeDirectoryAccess    = "directory-access"
	WholeTreeOutsideInput       = "outside-input"
)

type Event struct {
	Seq       int64  `json:"seq"`
	Type      string `json:"type"`
	Schema    string `json:"schema,omitempty"`
	Timestamp string `json:"timestamp"`
	ElapsedMS int64  `json:"elapsed_ms"`

	Phase    *PhaseRecord    `json:"phase,omitempty"`
	Prepare  *PrepareRecord  `json:"prepare,omitempty"`
	Exec     *ExecRecord     `json:"exec,omitempty"`
	Mutant   *MutantRecord   `json:"mutant,omitempty"`
	Route    *RouteRecord    `json:"route,omitempty"`
	Probe    *ProbeRecord    `json:"probe,omitempty"`
	Progress *ProgressRecord `json:"progress,omitempty"`
	Artifact *ArtifactRecord `json:"artifact,omitempty"`
	Run      *RunRecord      `json:"run,omitempty"`
}

type PhaseRecord struct {
	Name       string `json:"name"`
	DurationMS int64  `json:"duration_ms,omitempty"`
}

type PrepareRecord struct {
	Phase      string `json:"phase"`
	State      string `json:"state"`
	Result     string `json:"result,omitempty"`
	DurationMS *int64 `json:"duration_ms,omitempty"`
}

type ExecRecord struct {
	Argv            []string `json:"argv"`
	Dir             string   `json:"dir,omitempty"`
	EnvNames        []string `json:"env_names,omitempty"`
	TimeoutMS       int64    `json:"timeout_ms,omitempty"`
	ExitCode        int      `json:"exit_code"`
	TimedOut        bool     `json:"timed_out,omitempty"`
	DurationMS      int64    `json:"duration_ms,omitempty"`
	OutputBytes     int      `json:"output_bytes,omitempty"`
	OutputSHA256    string   `json:"output_sha256,omitempty"`
	OutputTruncated bool     `json:"output_truncated,omitempty"`
	OutputPath      string   `json:"output_path,omitempty"`
	Error           string   `json:"error,omitempty"`

	Output []byte `json:"-"`
}

type MutantRecord struct {
	ID         string   `json:"id"`
	DisplayID  string   `json:"display_id,omitempty"`
	Package    string   `json:"package,omitempty"`
	Args       []string `json:"args,omitempty"`
	TimeoutMS  int64    `json:"timeout_ms,omitempty"`
	Outcome    string   `json:"outcome,omitempty"`
	KilledBy   string   `json:"killed_by,omitempty"`
	DurationMS int64    `json:"duration_ms,omitempty"`

	WholeTree       bool   `json:"whole_tree,omitempty"`
	WholeTreeReason string `json:"whole_tree_reason,omitempty"`

	Error string `json:"error,omitempty"`
}

type RouteRecord struct {
	MutantID        string      `json:"mutant_id,omitempty"`
	Rule            string      `json:"rule,omitempty"`
	Path            string      `json:"path"`
	Line            int         `json:"line,omitempty"`
	Column          int         `json:"column,omitempty"`
	ReachingTargets []string    `json:"reaching_targets,omitempty"`
	Plan            []string    `json:"plan,omitempty"`
	Reason          string      `json:"reason"`
	Granularity     string      `json:"granularity"`
	Fallback        string      `json:"fallback,omitempty"`
	FileCandidates  int         `json:"file_candidates,omitempty"`
	Discharged      []Discharge `json:"discharged,omitempty"`
	ProbeReaching   []string    `json:"probe_reaching,omitempty"`
	SuiteCoverage   string      `json:"suite_coverage,omitempty"`
	SuiteReached    bool        `json:"suite_reached,omitempty"`
	SuiteProbe      string      `json:"suite_probe,omitempty"`
	Probed          bool        `json:"probed,omitempty"`
	Reused          bool        `json:"reused,omitempty"`
}

type Discharge struct {
	Target string `json:"target"`
	Reason string `json:"reason"`
}

type ProbeRecord struct {
	Target     string   `json:"target"`
	Package    string   `json:"package,omitempty"`
	Suite      bool     `json:"suite,omitempty"`
	Control    bool     `json:"control,omitempty"`
	Args       []string `json:"args,omitempty"`
	TimeoutMS  int64    `json:"timeout_ms,omitempty"`
	Outcome    string   `json:"outcome,omitempty"`
	ExitCode   int      `json:"exit_code"`
	DurationMS int64    `json:"duration_ms,omitempty"`
	Infected   []string `json:"infected,omitempty"`

	WholeTree       bool   `json:"whole_tree,omitempty"`
	WholeTreeReason string `json:"whole_tree_reason,omitempty"`

	Error string `json:"error,omitempty"`
}

type ProgressRecord struct {
	Kind   string `json:"kind"`
	Detail string `json:"detail,omitempty"`
}

type ArtifactRecord struct {
	Kind string `json:"kind"`
	Path string `json:"path"`
}

type RunRecord struct {
	Verdict       string `json:"verdict,omitempty"`
	Error         string `json:"error,omitempty"`
	EventsEmitted int64  `json:"events_emitted"`
	EventsDropped int64  `json:"events_dropped"`
}
