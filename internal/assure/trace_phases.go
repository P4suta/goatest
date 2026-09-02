// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package assure

import "github.com/P4suta/goatest/internal/trace"

// The phases of a run, in the order a round passes through them. A round that
// repeats after a promoted repair passes through them again, and a round that
// reaches its verdict early stops partway: what a phase says is where the run
// was, never what it was obliged to do.
const (
	phaseSnapshot        = "snapshot"
	phaseCacheCheck      = "cache-check"
	phaseDiscover        = "discover"
	phaseImpact          = "impact"
	phaseResources       = "resources"
	phaseBaseline        = "baseline"
	phaseGraph           = "graph"
	phaseRace            = "race"
	phaseMutationPrepare = "mutation-prepare"
	phaseProbe           = "probe"
	phaseMutation        = "mutation"
	phaseRepair          = "repair"
	phaseFinalize        = "finalize"
)

// runPhases records which phase a run is in.
//
// The phases of a run are a sequence rather than a nesting: entering one ends
// the one before it, and leaving the sequence ends whichever is open. That is
// what pairs every phase-start with a phase-end, including on the cache-hit
// and error paths that leave a round early, from a single deferred leave
// rather than from a closer the coordinator has to pair by hand at each of its
// several dozen returns.
type runPhases struct {
	recorder *trace.Recorder
	end      func()
}

// enter ends the phase in progress and begins the named one.
func (phases *runPhases) enter(name string) {
	phases.leave()
	phases.end = phases.recorder.PhaseStart(name)
}

// leave ends the phase in progress, if there is one. It repeats harmlessly, so
// a deferred leave costs nothing once the sequence has already ended.
func (phases *runPhases) leave() {
	if phases.end != nil {
		phases.end()
		phases.end = nil
	}
}
