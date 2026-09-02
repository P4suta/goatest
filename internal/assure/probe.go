// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package assure

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	gomutants "github.com/P4suta/go-mutants"
	goanalysis "github.com/P4suta/goatest/internal/golang"
	"github.com/P4suta/goatest/internal/trace"
)

// ProbeOptions configure one probe pass. Everything here is taken from the
// mutation phase the pass measures for, because a measurement of a target under
// other flags, another environment or another timeout is a measurement of
// another execution.
type ProbeOptions struct {
	// Contract is the contract the mutation phase runs under; it calibrates the
	// per-target timeout exactly as the mutation phase calibrates its own.
	Contract string
	// Timeout is the run's CommandTimeout override. Zero calibrates from the
	// target's baseline duration.
	Timeout time.Duration
	// TestArgs are the run's extra test flags, appended after -test.run exactly
	// as the mutation requests append them.
	TestArgs []string
	// Jobs bounds concurrent probes. Anything below one runs them one at a time.
	Jobs int
	// Trace records what each target measured. A nil recorder records nothing.
	Trace *trace.Recorder
	// Progress reports completions against the number of targets the pass
	// probes, which is every target but the fuzz ones.
	Progress func(completed, total int)
}

// ProbeEvaluation is what one pass established. Targets are the targets it was
// given, in the order it was given them, with the facts of the pass filled in;
// Measured and Unmeasured count the targets the pass could and could not speak
// for. A fuzz target is neither: the pass never probes one.
type ProbeEvaluation struct {
	Targets    []TargetEvidence
	Measured   int
	Unmeasured int
}

// ProbeTargets measures each baseline target against the session's probe tree
// and records which mutants it made differ.
//
// The probe tree runs the program the user wrote with no mutant active, so the
// pass costs about one baseline and changes nothing about the run's evidence:
// it only says, per target, which mutants that target could ever observe. The
// answer is a licence not to execute a pair, so it is taken only from a
// measured pass. Every other outcome, and every error the pass survives, leaves
// its target without facts, which routing reads as infecting everything it
// reaches - the answer a run gave before the pass existed.
//
// Two failures stop the pass instead: a cancelled run, which will not use the
// measurements it is still asking for, and a session prepared without a probe
// tree, which is a programming error rather than a failed measurement.
func ProbeTargets(ctx context.Context, session MutationSession, targets []TargetEvidence, options ProbeOptions) (ProbeEvaluation, error) {
	if session == nil {
		return ProbeEvaluation{}, fmt.Errorf("goatest: nil mutation session")
	}
	evaluation := ProbeEvaluation{Targets: slices.Clone(targets)}
	positions := probedTargetPositions(evaluation.Targets)
	if len(positions) == 0 {
		return evaluation, nil
	}
	identities := probeMutantIdentities(session.Catalog())
	measurements := make([]probeMeasurement, len(positions))
	jobs := min(max(options.Jobs, 1), len(positions))
	indexes := make(chan int, len(positions))
	var workers sync.WaitGroup
	var progress sync.Mutex
	completed := 0
	workers.Add(jobs)
	for range jobs {
		go func() {
			defer workers.Done()
			for index := range indexes {
				measurement := probeTarget(ctx, session, evaluation.Targets[positions[index]], identities, options)
				measurements[index] = measurement
				if measurement.recorded {
					// The recorder serialises the workers, so the stream holds
					// one complete line per execution in completion order.
					options.Trace.ProbeExec(measurement.record)
				}
				progress.Lock()
				completed++
				if options.Progress != nil {
					options.Progress(completed, len(positions))
				}
				progress.Unlock()
			}
		}()
	}
	for index := range positions {
		indexes <- index
	}
	close(indexes)
	workers.Wait()
	for index, measurement := range measurements {
		if measurement.fatal != nil {
			return ProbeEvaluation{}, measurement.fatal
		}
		target := &evaluation.Targets[positions[index]]
		target.Probed, target.Infected = measurement.measured, measurement.infected
		if measurement.measured {
			evaluation.Measured++
		} else {
			evaluation.Unmeasured++
		}
	}
	return evaluation, nil
}

// probeMeasurement is what one target's probe established: the facts it left
// behind, the record that describes it, and the failure that stops the pass
// rather than costing one target its facts.
type probeMeasurement struct {
	measured bool
	infected []uint32
	record   trace.ProbeRecord
	recorded bool
	fatal    error
}

// probedTargetPositions are the positions of the targets a pass measures: every
// target the mutation phase runs under -test.run, which is the tests and the
// examples.
//
// A fuzz target is never probed. The mutation phase fuzzes it beyond the seed
// corpus a probe would measure, so a measurement of the corpus would license
// skipping executions that explore inputs it never saw; and a fuzz target run
// on the probe tree would write corpus files into that tree.
func probedTargetPositions(targets []TargetEvidence) []int {
	positions := make([]int, 0, len(targets))
	for position, target := range targets {
		if target.Target.Kind == goanalysis.KindFuzz {
			continue
		}
		positions = append(positions, position)
	}
	return positions
}

// probeTargetCount is how many targets a pass will measure, for the note that
// announces it.
func probeTargetCount(targets []TargetEvidence) int {
	return len(probedTargetPositions(targets))
}

// probeMutantIdentities maps the catalogue index a probe result names to the
// mutant identity everything else in goatest is keyed on. It is built once per
// pass because Catalog copies the whole catalogue on every call.
func probeMutantIdentities(catalog gomutants.Catalog) map[uint32]string {
	identities := make(map[uint32]string, len(catalog.Mutants))
	for _, mutant := range catalog.Mutants {
		identities[mutant.Index] = mutant.ID
	}
	return identities
}

// probeTarget measures one target and describes what became of it.
func probeTarget(ctx context.Context, session MutationSession, target TargetEvidence, identities map[uint32]string, options ProbeOptions) probeMeasurement {
	request := probeRequest(target, options)
	record := trace.ProbeRecord{
		Target: target.Target.ID, Package: request.Package,
		Args: slices.Clone(request.Args), TimeoutMS: traceMilliseconds(request.Timeout),
	}
	if err := ctx.Err(); err != nil {
		// A cancelled run will not use this measurement, so it is not taken.
		return probeMeasurement{fatal: err}
	}
	result, err := session.Probe(ctx, request)
	if err != nil {
		if ctx.Err() != nil || errors.Is(err, gomutants.ErrProbeNotPrepared) {
			return probeMeasurement{fatal: fmt.Errorf("goatest: probe %s: %w", target.Target.Name, err)}
		}
		record.Error = err.Error()
		return probeMeasurement{record: record, recorded: true}
	}
	record.Outcome = string(result.Outcome)
	record.ExitCode = result.ExitCode
	record.DurationMS = traceMilliseconds(result.Duration)
	if result.Outcome != gomutants.ProbeMeasured {
		// A pass that cannot be vouched for reports that it has no facts, which
		// is not the same sentence as "nothing was infected".
		return probeMeasurement{record: record, recorded: true}
	}
	infected := slices.Clone(result.Infected)
	if !slices.IsSorted(infected) {
		// The engine contract promises the indices ascending and distinct.
		// Routing will binary-search them, so a set that arrives otherwise is
		// repaired here rather than trusted into a wrong answer.
		slices.Sort(infected)
		infected = slices.Compact(infected)
	}
	identifiers := make([]string, 0, len(infected))
	for _, index := range infected {
		identity, known := identities[index]
		if !known {
			// An index outside the catalogue is a contract violation, and a
			// measurement naming a mutant nobody can identify is no
			// measurement: the target keeps no facts at all.
			record.Outcome, record.Infected = "", nil
			record.Error = fmt.Sprintf("probe reported an unknown mutant index %d", index)
			return probeMeasurement{record: record, recorded: true}
		}
		identifiers = append(identifiers, identity)
	}
	if len(identifiers) != 0 {
		// A measured execution that infected nothing says so with the outcome
		// alone: an empty list would read as a measurement of nothing.
		record.Infected = identifiers
	}
	return probeMeasurement{measured: true, infected: infected, record: record, recorded: true}
}

// probeRequest is the request the mutation phase would send for this target,
// minus the mutant a probe tree never activates. Sharing the selection, the
// environment and the calibrated timeout is what makes the measurement a
// statement about the execution the mutation phase will run.
func probeRequest(target TargetEvidence, options ProbeOptions) gomutants.ProbeRequest {
	return gomutants.ProbeRequest{
		Package: target.Target.Package,
		Args:    append([]string{targetRunArgument(target)}, options.TestArgs...),
		Env:     slices.Clone(target.Environment),
		Timeout: calibratedMutationTimeout(options.Contract, target.Duration, options.Timeout),
	}
}
