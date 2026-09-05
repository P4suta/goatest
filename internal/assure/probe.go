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
	// Contract is the contract the mutation phase runs under; it supplies the
	// hard ceiling around each control-relative probe budget.
	Contract string
	// Timeout is the run's CommandTimeout ceiling. Zero leaves only the
	// contract ceiling around calibration from same-run baseline durations.
	Timeout time.Duration
	// TestArgs are the run's extra test flags, appended after -test.run exactly
	// as the mutation requests append them.
	TestArgs []string
	// Jobs bounds concurrent probes. Anything below one runs them one at a time.
	Jobs int
	// Trace records what each target measured. A nil recorder records nothing.
	Trace *trace.Recorder
	// Progress reports completions against all requested controls: every target
	// but the fuzz ones, plus package suites when PackageSuites is set.
	Progress func(completed, total int)
	// PackageSuites asks for one additional probe of every package that owns an
	// executable mutant. A suite measurement preserves TestMain, package setup,
	// and cross-target interactions while asking whether a probed mutant could
	// change that exact fallback execution.
	PackageSuites bool
	// SuitePackages narrows package-suite probes to these import paths. It is
	// used when whole-suite coverage has already discharged other fallbacks;
	// PackageSuites remains the direct API's request for every package.
	SuitePackages []string
	// SuiteEnvironment is the union of acquired resource environments, matching
	// the whole-package mutant request this control may replace.
	SuiteEnvironment []string
	// RepositoryObserver describes package-level reads for a suite verdict's
	// behaviour key. It is nil outside the guarded full-run evidence path.
	RepositoryObserver *RepositoryObserver
}

// ProbeEvaluation is what one pass established. Targets are the targets it was
// given, in the order it was given them, with the facts of the pass filled in.
// The target and suite counters say separately which controls the pass could
// and could not speak for. A fuzz target is neither: the pass never probes one.
type ProbeEvaluation struct {
	Targets          []TargetEvidence
	Measured         int
	Unmeasured       int
	Suites           map[string]PackageProbeEvidence
	SuitesMeasured   int
	SuitesUnmeasured int
}

// PackageProbeEvidence is one execution of a package's whole test suite on
// the semantics-preserving probe tree. Only Measured makes Infected a fact.
// Duration is the current-machine control used to bound a later mutant run;
// WholeTree records the conservative suite-key variant selected by runtime
// repository observation.
type PackageProbeEvidence struct {
	Measured  bool
	Infected  []uint32
	Duration  time.Duration
	WholeTree bool
}

// ProbeTargets measures each baseline target against the session's probe tree
// and records which mutants it made differ.
//
// The probe tree runs the program the user wrote with no mutant active, so the
// pass changes nothing about the run's evidence: it says which mutants each
// target and requested package suite could ever observe. The answer is a
// licence not to execute a pair or an unchanged suite, so it is taken only from
// a measured pass. Every other outcome, and every error the pass survives,
// leaves the older conservative execution in place.
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
	catalog := session.Catalog()
	packages := slices.Clone(options.SuitePackages)
	slices.Sort(packages)
	packages = slices.Compact(packages)
	if len(packages) == 0 && options.PackageSuites {
		packages = probeSuitePackages(catalog)
	}
	if len(packages) != 0 {
		evaluation.Suites = make(map[string]PackageProbeEvidence, len(packages))
	}
	if len(positions) == 0 && len(packages) == 0 {
		return evaluation, nil
	}
	identities := probeMutantIdentities(catalog)
	work := make([]probeWork, 0, len(positions)+len(packages))
	for _, position := range positions {
		work = append(work, probeWork{targetPosition: position})
	}
	for _, pkg := range packages {
		work = append(work, probeWork{
			packageName: pkg,
			control:     packageSuiteControlDuration(evaluation.Targets, pkg),
		})
	}
	measurements := make([]probeWorkResult, len(work))
	jobs := min(max(options.Jobs, 1), len(work))
	indexes := make(chan int, len(work))
	var workers sync.WaitGroup
	var progress sync.Mutex
	completed := 0
	workers.Add(jobs)
	for range jobs {
		go func() {
			defer workers.Done()
			for index := range indexes {
				item := work[index]
				if item.packageName != "" {
					measurements[index].suite = probeSuite(ctx, session, item.packageName, item.control, identities, options)
				} else {
					measurements[index].target = probeTarget(ctx, session, evaluation.Targets[item.targetPosition], identities, options)
				}
				measurement := measurements[index].measurement()
				if measurement.recorded {
					// The recorder serialises the workers, so the stream holds
					// one complete line per execution in completion order.
					options.Trace.ProbeExec(measurement.record)
				}
				progress.Lock()
				completed++
				if options.Progress != nil {
					options.Progress(completed, len(work))
				}
				progress.Unlock()
			}
		}()
	}
	for index := range work {
		indexes <- index
	}
	close(indexes)
	workers.Wait()
	for index, result := range measurements {
		item := work[index]
		measurement := result.measurement()
		if measurement.fatal != nil {
			return ProbeEvaluation{}, measurement.fatal
		}
		if item.packageName != "" {
			suite := result.suite
			evaluation.Suites[item.packageName] = PackageProbeEvidence{
				Measured: suite.measured, Infected: slices.Clone(suite.infected),
				Duration: suite.duration, WholeTree: suite.wholeTree,
			}
			if suite.measured {
				evaluation.SuitesMeasured++
			} else {
				evaluation.SuitesUnmeasured++
			}
			continue
		}
		target := &evaluation.Targets[item.targetPosition]
		target.Probed, target.Infected = measurement.measured, measurement.infected
		if measurement.measured {
			target.ProbeDuration = measurement.duration
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
	duration time.Duration
	record   trace.ProbeRecord
	recorded bool
	fatal    error
}

type probeSuiteMeasurement struct {
	probeMeasurement
	wholeTree bool
}

type probeWork struct {
	targetPosition int
	packageName    string
	control        time.Duration
}

type probeWorkResult struct {
	target probeMeasurement
	suite  probeSuiteMeasurement
}

func (result probeWorkResult) measurement() probeMeasurement {
	if result.suite.recorded || result.suite.fatal != nil {
		return result.suite.probeMeasurement
	}
	return result.target
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
	return probeMeasurement{measured: true, infected: infected, duration: result.Duration, record: record, recorded: true}
}

const packageSuiteProbePrefix = "package-suite:"

func packageSuiteProbeTarget(pkg string) string { return packageSuiteProbePrefix + pkg }

func probeSuitePackages(catalog gomutants.Catalog) []string {
	seen := make(map[string]bool)
	for _, mutant := range catalog.Mutants {
		if mutant.Accepted && mutant.Package != "" {
			seen[mutant.Package] = true
		}
	}
	packages := make([]string, 0, len(seen))
	for pkg := range seen {
		packages = append(packages, pkg)
	}
	slices.Sort(packages)
	return packages
}

func probeSuite(ctx context.Context, session MutationSession, pkg string, control time.Duration, identities map[uint32]string, options ProbeOptions) probeSuiteMeasurement {
	request := gomutants.ProbeRequest{
		Package: pkg, Args: slices.Clone(options.TestArgs), Env: slices.Clone(options.SuiteEnvironment),
		Timeout: controlRelativeMutationTimeout(options.Contract, options.Timeout, control),
	}
	record := trace.ProbeRecord{
		Target: packageSuiteProbeTarget(pkg), Package: pkg, Args: slices.Clone(request.Args),
		TimeoutMS: traceMilliseconds(request.Timeout), Suite: true,
	}
	if err := ctx.Err(); err != nil {
		return probeSuiteMeasurement{probeMeasurement: probeMeasurement{fatal: err}}
	}
	instrumented := request
	arguments, finish := options.RepositoryObserver.instrumentPackage(pkg, request.Args)
	instrumented.Args = arguments
	result, err := session.Probe(ctx, instrumented)
	observation := finish()
	if err == nil && repositoryTestLogFailure(result.OutputTail, instrumented.Args) {
		result, err = session.Probe(ctx, request)
		observation = repositoryObservation{unknown: true}
	}
	if err != nil {
		if ctx.Err() != nil || errors.Is(err, gomutants.ErrProbeNotPrepared) {
			return probeSuiteMeasurement{probeMeasurement: probeMeasurement{fatal: fmt.Errorf("goatest: probe package suite %s: %w", pkg, err)}}
		}
		record.Error = err.Error()
		return probeSuiteMeasurement{probeMeasurement: probeMeasurement{record: record, recorded: true}}
	}
	record.Outcome = string(result.Outcome)
	record.ExitCode = result.ExitCode
	record.DurationMS = traceMilliseconds(result.Duration)
	measurement := probeSuiteMeasurement{probeMeasurement: probeMeasurement{
		duration: result.Duration, record: record, recorded: true,
	}}
	if result.Outcome != gomutants.ProbeMeasured {
		return measurement
	}
	infected := slices.Clone(result.Infected)
	if !slices.IsSorted(infected) {
		slices.Sort(infected)
		infected = slices.Compact(infected)
	}
	identifiers := make([]string, 0, len(infected))
	for _, index := range infected {
		identity, known := identities[index]
		if !known {
			measurement.record.Outcome, measurement.record.Infected = "", nil
			measurement.record.Error = fmt.Sprintf("probe reported an unknown mutant index %d", index)
			return measurement
		}
		identifiers = append(identifiers, identity)
	}
	if len(identifiers) != 0 {
		measurement.record.Infected = identifiers
	}
	measurement.measured = true
	measurement.infected = infected
	measurement.wholeTree = options.RepositoryObserver.wholeTreeSuite(pkg, observation)
	return measurement
}

func packageSuiteControlDuration(targets []TargetEvidence, pkg string) time.Duration {
	var duration time.Duration
	for _, target := range targets {
		if target.Target.Package == pkg {
			duration = boundedDurationSum(duration, target.Duration)
		}
	}
	return duration
}

// probeRequest is the request the mutation phase will send for this target,
// minus the mutant a probe tree never activates. It shares the selection and
// environment; its deadline is the first control-relative budget, while the
// later mutant request also incorporates the duration this probe measured.
func probeRequest(target TargetEvidence, options ProbeOptions) gomutants.ProbeRequest {
	return gomutants.ProbeRequest{
		Package: target.Target.Package,
		Args:    append([]string{targetRunArgument(target)}, options.TestArgs...),
		Env:     slices.Clone(target.Environment),
		Timeout: controlRelativeMutationTimeout(options.Contract, options.Timeout, target.Duration),
	}
}
