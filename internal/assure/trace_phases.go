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
