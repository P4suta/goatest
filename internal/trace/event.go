// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package trace

// SchemaV1 is the first trace contract. It identifies the event format in the
// run-start event and in the embedded JSON Schema. Pre-release trace shapes are
// replaced in place instead of consuming a public version.
const SchemaV1 = "goatest-trace-v1"

// Event types. Every recorded event carries exactly one of these discriminators
// and at most one payload record, named after the same concept.
const (
	TypeRunStart   = "run-start"
	TypePhaseStart = "phase-start"
	TypePhaseEnd   = "phase-end"
	TypeExec       = "exec"
	TypeMutantExec = "mutant-exec"
	TypeRoute      = "route"
	TypeProgress   = "progress"
	TypeArtifact   = "artifact"
	TypeRunEnd     = "run-end"
)

// Routing reasons explain why a mutant was given the execution plan it was
// given. A mutant no test reaches is unreached; every other plan is derived
// from the coverage of the targets that reach it.
const (
	ReasonCoverageReaching = "coverage-reaching"
	ReasonUnreached        = "unreached"
)

// Routing granularities name the evidence a route was decided on: the coverage
// blocks that contain the mutated position, or the whole file when the
// position could not be placed in one.
const (
	GranularityBlock = "block"
	GranularityFile  = "file"
)

// Routing fallbacks name why a route that would have been decided by block
// dropped back to the file. A mutant whose position the engine did not report
// cannot be placed in any block; a position no instrumented block contains is
// outside the coverage the toolchain measured, which is a gap in the evidence
// rather than an absence of tests. Both fall back to the file, which is the
// conservative side.
const (
	FallbackPositionUnknown = "position-unknown"
	FallbackOutsideBlocks   = "outside-blocks"
)

// Discharge reasons name the proof that removed a target from a reaching set.
// A target whose coverage reaches the mutated position through a branch it
// never takes cannot observe the mutation, so the proof discharges it instead
// of the run executing it. Only the reasons a proof produces are named here;
// the rest arrive with their producers.
const (
	DischargeBranchNeverTaken = "branch-never-taken"
)

// Event is one line of a trace. Its JSON field order is part of the contract
// consumers read, so the declaration order below is the wire order: the
// identity of the event first, then the single payload it carries.
//
// Everything in an event is deterministic except the timestamp and the
// durations, which are the only fields a second identical run may change.
type Event struct {
	Seq       int64  `json:"seq"`
	Type      string `json:"type"`
	Schema    string `json:"schema,omitempty"`
	Timestamp string `json:"timestamp"`
	ElapsedMS int64  `json:"elapsed_ms"`

	Phase    *PhaseRecord    `json:"phase,omitempty"`
	Exec     *ExecRecord     `json:"exec,omitempty"`
	Mutant   *MutantRecord   `json:"mutant,omitempty"`
	Route    *RouteRecord    `json:"route,omitempty"`
	Progress *ProgressRecord `json:"progress,omitempty"`
	Artifact *ArtifactRecord `json:"artifact,omitempty"`
	Run      *RunRecord      `json:"run,omitempty"`
}

// PhaseRecord names a phase of a run. A phase is only timed when it ends, so
// DurationMS is absent from the phase-start event.
type PhaseRecord struct {
	Name       string `json:"name"`
	DurationMS int64  `json:"duration_ms,omitempty"`
}

// ExecRecord describes one command a run executed.
//
// EnvNames holds variable names alone, sorted and deduplicated: a trace records
// which part of the environment a command could see and never what it held.
// Output is the captured combined output, digested into the record and
// preserved beside the trace by a sink that can store it; it is never
// serialised into the event itself.
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

	// Output is the captured bytes themselves, preserved beside the trace
	// instead of inside it.
	Output []byte `json:"-"`
}

// MutantRecord describes one mutant execution: which mutant ran, how it ran,
// and what became of it.
type MutantRecord struct {
	ID         string   `json:"id"`
	DisplayID  string   `json:"display_id,omitempty"`
	Package    string   `json:"package,omitempty"`
	Args       []string `json:"args,omitempty"`
	TimeoutMS  int64    `json:"timeout_ms,omitempty"`
	Outcome    string   `json:"outcome,omitempty"`
	KilledBy   string   `json:"killed_by,omitempty"`
	DurationMS int64    `json:"duration_ms,omitempty"`
	Error      string   `json:"error,omitempty"`
}

// RouteRecord explains how a mutant was routed: the targets coverage says reach
// it, the plan derived from them, and the reason that plan was chosen.
//
// Granularity, Fallback, FileCandidates and Column describe how the reaching
// set was narrowed. They are additive: a recording made before they existed
// carries none of them, so every one of them is omitted when it is empty.
//
// Granularity is what marks a route as carrying that metadata at all. On a
// route that names one, an absent FileCandidates is a count of zero — a file
// no test binary was ever linked against — rather than a missing measurement;
// on a route that names none, the metadata was never recorded. A Fallback is
// why a decision by block dropped back to the file, so a route carrying one is
// a route of GranularityFile.
//
// Discharged is the other half of the reaching measurement: on a route of
// GranularityBlock, ReachingTargets together with the targets of Discharged
// are the targets whose covered blocks contain the mutated position. A proof
// removed the discharged ones, so a discharged target never appears in
// ReachingTargets as well.
type RouteRecord struct {
	MutantID        string      `json:"mutant_id,omitempty"`
	Rule            string      `json:"rule,omitempty"`
	Path            string      `json:"path"`
	Line            int         `json:"line,omitempty"`
	Column          int         `json:"column,omitempty"`
	ReachingTargets []string    `json:"reaching_targets,omitempty"`
	Plan            []string    `json:"plan,omitempty"`
	Reason          string      `json:"reason"`
	Granularity     string      `json:"granularity,omitempty"`
	Fallback        string      `json:"fallback,omitempty"`
	FileCandidates  int         `json:"file_candidates,omitempty"`
	Discharged      []Discharge `json:"discharged,omitempty"`
}

// Discharge is one target a proof removed from a reaching set, and the proof
// that removed it. Discharging a target is a claim that executing it would
// prove nothing about the mutant, so the trace records which proof made the
// claim rather than the removal alone.
type Discharge struct {
	Target string `json:"target"`
	Reason string `json:"reason"`
}

// ProgressRecord carries a human readable progress note forwarded from the run.
type ProgressRecord struct {
	Kind   string `json:"kind"`
	Detail string `json:"detail,omitempty"`
}

// ArtifactRecord names a file a run wrote, relative to the repository.
type ArtifactRecord struct {
	Kind string `json:"kind"`
	Path string `json:"path"`
}

// RunRecord closes a trace with the verdict, the error that ended the run if
// there was one, and the event accounting. The accounting is never optional: a
// reader must be able to tell a complete trace from a lossy one.
type RunRecord struct {
	Verdict       string `json:"verdict,omitempty"`
	Error         string `json:"error,omitempty"`
	EventsEmitted int64  `json:"events_emitted"`
	EventsDropped int64  `json:"events_dropped"`
}
