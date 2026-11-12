// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package assure

import "github.com/P4suta/goatest/internal/trace"

const (
	phaseSnapshot   = "snapshot"
	phaseCacheCheck = "cache-check"
	phaseDiscover   = "discover"
	phaseImpact     = "impact"
	phaseResources  = "resources"
	phaseBaseline   = "baseline"
	phaseGraph      = "graph"
	phaseRace       = "race"
	phaseProbe      = "probe"
	phaseMutation   = "mutation"
	phaseRepair     = "repair"
	phaseFinalize   = "finalize"
)

// runPhaseNames are the phases of a run, in the order a run enters them.
//
// A run uses them as a sequence rather than a nesting: entering one ends the
// one before it, and the last open phase ends when the run does, so every
// phase-start has a phase-end even on the cache-hit and error paths.
//
// The list exists so that docs/trace-v1.md can be pinned to it. The names are
// what a reader of a recording sees, and a recording whose phases are not the
// ones the page describes is a recording nobody can read.
func runPhaseNames() []string {
	return []string{
		phaseSnapshot, phaseCacheCheck, phaseDiscover, phaseImpact, phaseResources,
		phaseBaseline, phaseGraph, phaseRace, phaseProbe, phaseMutation, phaseRepair,
		phaseFinalize,
	}
}

type runPhases struct {
	recorder *trace.Recorder
	end      func()
}

func (phases *runPhases) enter(name string) {
	phases.leave()
	phases.end = phases.recorder.PhaseStart(name)
}

func (phases *runPhases) leave() {
	if phases.end != nil {
		phases.end()
		phases.end = nil
	}
}
