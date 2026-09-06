// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package assure

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	gomutants "github.com/P4suta/go-mutants"
	"github.com/P4suta/goatest/internal/checkpoint"
	"github.com/P4suta/goatest/internal/trace"
)

const (
	probeIndexAcceptedFlag byte = 1 << iota
	probeIndexMeasuredFlag
)

type ProbeOptions struct {
	Contract string

	Timeout time.Duration

	TestArgs []string

	Jobs int

	Trace *trace.Recorder

	Progress func(completed, total int)

	PackageSuites bool

	SuitePackages []string
	Suites        map[string]PackageProbeEvidence

	SuiteEnvironment []string

	SuiteCoverage map[string]PackageSuiteCoverage

	RepositoryObserver *RepositoryObserver
}

type ProbeEvaluation struct {
	Targets          []TargetEvidence
	Measured         int
	Unmeasured       int
	Suites           map[string]PackageProbeEvidence
	SuitesMeasured   int
	SuitesUnmeasured int
}

type PackageProbeEvidence struct {
	Measured  bool
	Infected  []uint32
	Duration  time.Duration
	WholeTree bool
}

func ProbeTargets(ctx context.Context, session MutationSession, targets []TargetEvidence, options ProbeOptions) (ProbeEvaluation, error) {
	if session == nil {
		return ProbeEvaluation{}, fmt.Errorf("goatest: nil mutation session")
	}
	evaluation := ProbeEvaluation{Targets: slices.Clone(targets)}
	for _, target := range evaluation.Targets {
		if target.Probed {
			evaluation.Measured++
		}
	}
	if len(options.Suites) != 0 {
		evaluation.Suites = make(map[string]PackageProbeEvidence, len(options.Suites))
		for pkg, suite := range options.Suites {
			suite.Infected = slices.Clone(suite.Infected)
			evaluation.Suites[pkg] = suite
			if suite.Measured {
				evaluation.SuitesMeasured++
			} else {
				evaluation.SuitesUnmeasured++
			}
		}
	}
	positions := probedTargetPositions(evaluation.Targets)
	catalog := session.Catalog()
	packages := slices.Clone(options.SuitePackages)
	slices.Sort(packages)
	packages = slices.Compact(packages)
	if len(packages) == 0 && options.PackageSuites {
		packages = probeSuitePackages(catalog)
	}
	packages = slices.DeleteFunc(packages, func(pkg string) bool {
		suite, ok := options.Suites[pkg]
		return ok && suite.Measured
	})
	if len(packages) != 0 {
		if evaluation.Suites == nil {
			evaluation.Suites = make(map[string]PackageProbeEvidence, len(packages))
		}
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

func probedTargetPositions(targets []TargetEvidence) []int {
	positions := make([]int, 0, len(targets))
	for position, target := range targets {
		if target.Probed {
			continue
		}
		positions = append(positions, position)
	}
	return positions
}

func probeTargetCount(targets []TargetEvidence) int {
	return len(probedTargetPositions(targets))
}

func probeMutantIdentities(catalog gomutants.Catalog) map[uint32]string {
	identities := make(map[uint32]string, len(catalog.Mutants))
	for _, mutant := range catalog.Mutants {
		identities[mutant.Index] = mutant.ID
	}
	return identities
}

func probeTarget(ctx context.Context, session MutationSession, target TargetEvidence, identities map[uint32]string, options ProbeOptions) probeMeasurement {
	request := probeRequest(target, options)
	record := probeRequestRecord(target.Target.ID, false, request)
	if err := ctx.Err(); err != nil {
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
	record, infected, measured := probeResultRecord(target.Target.ID, false, request, result, identities)
	if !measured {
		return probeMeasurement{record: record, recorded: true}
	}
	return probeMeasurement{measured: true, infected: infected, duration: result.Duration, record: record, recorded: true}
}

func probeRequestRecord(target string, suite bool, request gomutants.ProbeRequest) trace.ProbeRecord {
	return trace.ProbeRecord{
		Target: target, Package: request.Package, Suite: suite,
		Args: slices.Clone(request.Args), TimeoutMS: traceMilliseconds(request.Timeout),
	}
}

func probeResultRecord(
	target string,
	suite bool,
	request gomutants.ProbeRequest,
	result gomutants.ProbeResult,
	identities map[uint32]string,
) (trace.ProbeRecord, []uint32, bool) {
	record := probeRequestRecord(target, suite, request)
	record.Outcome = string(result.Outcome)
	record.ExitCode = result.ExitCode
	record.DurationMS = traceMilliseconds(result.Duration)
	if result.Outcome != gomutants.ProbeMeasured {
		return record, nil, false
	}
	infected := slices.Clone(result.Infected)
	slices.Sort(infected)
	infected = slices.Compact(infected)
	identifiers := make([]string, 0, len(infected))
	for _, index := range infected {
		identity, known := identities[index]
		if !known {
			record.Outcome = ""
			record.Error = fmt.Sprintf("probe reported an unknown mutant index %d", index)
			return record, nil, false
		}
		identifiers = append(identifiers, identity)
	}
	if len(identifiers) != 0 {
		record.Infected = identifiers
	}
	return record, infected, true
}

func packageSuiteProbeTarget(pkg string) string { return trace.PackageSuiteProbePrefix + pkg }

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
		Timeout: controlExecutionTimeout(options.Timeout, control, suiteCoverageControlDuration(options.SuiteCoverage, pkg)),
	}
	record := probeRequestRecord(packageSuiteProbeTarget(pkg), true, request)
	if err := ctx.Err(); err != nil {
		return probeSuiteMeasurement{probeMeasurement: probeMeasurement{fatal: err}}
	}
	instrumented := request
	arguments, finish := options.RepositoryObserver.instrumentPackage(pkg, request.Args)
	instrumented.Args = arguments
	result, err := session.Probe(ctx, instrumented)
	observation := finish()
	if err == nil && repositoryTestLogFailure(string(result.Output), instrumented.Args) {
		err = fmt.Errorf("goatest: repository observation for probe package suite %s failed", pkg)
		record.Error = err.Error()
		return probeSuiteMeasurement{probeMeasurement: probeMeasurement{record: record, recorded: true, fatal: err}}
	}
	if err != nil {
		if ctx.Err() != nil || errors.Is(err, gomutants.ErrProbeNotPrepared) {
			return probeSuiteMeasurement{probeMeasurement: probeMeasurement{fatal: fmt.Errorf("goatest: probe package suite %s: %w", pkg, err)}}
		}
		record.Error = err.Error()
		return probeSuiteMeasurement{probeMeasurement: probeMeasurement{record: record, recorded: true}}
	}
	record, infected, measured := probeResultRecord(packageSuiteProbeTarget(pkg), true, request, result, identities)
	record.WholeTreeReason = string(options.RepositoryObserver.wholeTreeSuiteReason(pkg, observation))
	record.WholeTree = record.WholeTreeReason != string(wholeTreeObserved)
	measurement := probeSuiteMeasurement{probeMeasurement: probeMeasurement{
		duration: result.Duration, record: record, recorded: true,
	}}
	if !measured {
		return measurement
	}
	measurement.measured = true
	measurement.infected = infected
	measurement.wholeTree = record.WholeTree
	return measurement
}

func packageSuiteControlDuration(targets []TargetEvidence, pkg string) time.Duration {
	var duration time.Duration
	for _, target := range targets {
		if target.Target.Package == pkg {
			duration = saturatingDurationSum(duration, target.Duration)
		}
	}
	return duration
}

func probeRequest(target TargetEvidence, options ProbeOptions) gomutants.ProbeRequest {
	return gomutants.ProbeRequest{
		Package: target.Target.Package,
		Args:    append([]string{targetRunArgument(target)}, options.TestArgs...),
		Env:     slices.Clone(target.Environment),
		Timeout: controlExecutionTimeout(
			options.Timeout, target.Duration,
			suiteCoverageControlDuration(options.SuiteCoverage, target.Target.Package),
		),
	}
}

func suiteCoverageControlDuration(suites map[string]PackageSuiteCoverage, pkg string) time.Duration {
	suite, measured := suites[pkg]
	if !measured {
		return 0
	}
	return suite.Duration
}

func checkpointMutationProbe(catalog gomutants.Catalog, evaluation ProbeEvaluation) *checkpoint.MutationProbe {
	result := &checkpoint.MutationProbe{
		IndexFingerprint: mutationProbeIndexFingerprint(catalog),
		Targets:          make([]checkpoint.TargetProbe, len(evaluation.Targets)),
	}
	for index, target := range evaluation.Targets {
		result.Targets[index] = checkpoint.TargetProbe{ID: target.Target.ID, Measured: target.Probed}
		if target.Probed {
			result.Targets[index].DurationNS = int64(target.ProbeDuration)
			result.Targets[index].Infected = slices.Clone(target.Infected)
			slices.Sort(result.Targets[index].Infected)
			result.Targets[index].Infected = slices.Compact(result.Targets[index].Infected)
		}
	}
	packages := make([]string, 0, len(evaluation.Suites))
	for pkg := range evaluation.Suites {
		packages = append(packages, pkg)
	}
	slices.Sort(packages)
	result.Suites = make([]checkpoint.SuiteProbe, 0, len(packages))
	for _, pkg := range packages {
		suite := evaluation.Suites[pkg]
		saved := checkpoint.SuiteProbe{Package: pkg, Measured: suite.Measured}
		if suite.Measured {
			saved.DurationNS = int64(suite.Duration)
			saved.Infected = slices.Clone(suite.Infected)
			slices.Sort(saved.Infected)
			saved.Infected = slices.Compact(saved.Infected)
			saved.WholeTree = suite.WholeTree
		}
		result.Suites = append(result.Suites, saved)
	}
	return result
}

func restoreMutationProbe(catalog gomutants.Catalog, targets []TargetEvidence, packages []string, saved checkpoint.MutationProbe) (ProbeEvaluation, bool) {
	if saved.IndexFingerprint != mutationProbeIndexFingerprint(catalog) {
		return ProbeEvaluation{}, false
	}
	evaluation := ProbeEvaluation{Targets: slices.Clone(targets)}
	byID := make(map[string]int, len(targets))
	for index := range evaluation.Targets {
		target := &evaluation.Targets[index]
		target.Probed, target.ProbeDuration, target.Infected = false, 0, nil
		if target.Target.ID == "" {
			return ProbeEvaluation{}, false
		}
		if _, duplicate := byID[target.Target.ID]; duplicate {
			return ProbeEvaluation{}, false
		}
		byID[target.Target.ID] = index
	}
	if len(saved.Targets) != len(targets) {
		return ProbeEvaluation{}, false
	}
	knownIndices := make(map[uint32]bool, len(catalog.Mutants))
	for _, mutant := range catalog.Mutants {
		knownIndices[mutant.Index] = true
	}
	seenTargets := make(map[string]bool, len(saved.Targets))
	for _, measured := range saved.Targets {
		position, found := byID[measured.ID]
		if !found || seenTargets[measured.ID] || !validCheckpointProbeFact(measured.Measured, measured.DurationNS, measured.Infected, false) {
			return ProbeEvaluation{}, false
		}
		seenTargets[measured.ID] = true
		for _, infected := range measured.Infected {
			if !knownIndices[infected] {
				return ProbeEvaluation{}, false
			}
		}
		target := &evaluation.Targets[position]
		if measured.Measured {
			target.Probed = true
			target.ProbeDuration = time.Duration(measured.DurationNS)
			target.Infected = slices.Clone(measured.Infected)
		}
	}
	for _, target := range evaluation.Targets {
		if target.Probed {
			evaluation.Measured++
		} else {
			evaluation.Unmeasured++
		}
	}

	wantPackages := slices.Clone(packages)
	slices.Sort(wantPackages)
	wantPackages = slices.Compact(wantPackages)
	gotPackages := make([]string, 0, len(saved.Suites))
	for _, suite := range saved.Suites {
		gotPackages = append(gotPackages, suite.Package)
	}
	slices.Sort(gotPackages)
	if !slices.Equal(gotPackages, wantPackages) {
		return ProbeEvaluation{}, false
	}
	if len(saved.Suites) != 0 {
		evaluation.Suites = make(map[string]PackageProbeEvidence, len(saved.Suites))
	}
	for _, measured := range saved.Suites {
		if measured.Package == "" || !validCheckpointProbeFact(measured.Measured, measured.DurationNS, measured.Infected, measured.WholeTree) {
			return ProbeEvaluation{}, false
		}
		for _, infected := range measured.Infected {
			if !knownIndices[infected] {
				return ProbeEvaluation{}, false
			}
		}
		evaluation.Suites[measured.Package] = PackageProbeEvidence{
			Measured: measured.Measured, Duration: time.Duration(measured.DurationNS),
			Infected: slices.Clone(measured.Infected), WholeTree: measured.WholeTree,
		}
		if measured.Measured {
			evaluation.SuitesMeasured++
		} else {
			evaluation.SuitesUnmeasured++
		}
	}
	return evaluation, true
}

func validCheckpointProbeFact(measured bool, durationNS int64, infected []uint32, wholeTree bool) bool {
	if durationNS < 0 || !measured && (durationNS != 0 || len(infected) != 0 || wholeTree) {
		return false
	}
	for index := 1; index < len(infected); index++ {
		if infected[index-1] >= infected[index] {
			return false
		}
	}
	return true
}

func mutationProbeIndexFingerprint(catalog gomutants.Catalog) string {
	type entry struct {
		index    uint32
		id       string
		accepted bool
		probed   bool
	}
	entries := make([]entry, 0, len(catalog.Mutants))
	for _, mutant := range catalog.Mutants {
		entries = append(entries, entry{
			index: mutant.Index, id: mutant.ID, accepted: mutant.Accepted, probed: mutant.Probed,
		})
	}
	slices.SortFunc(entries, func(left, right entry) int {
		if left.index < right.index {
			return -1
		}
		if left.index > right.index {
			return 1
		}
		return compareText(left.id, right.id)
	})
	hash := sha256.New()
	_, _ = hash.Write([]byte("goatest-mutation-probe-index-v1\x00"))
	for _, item := range entries {
		var index [4]byte
		binary.BigEndian.PutUint32(index[:], item.index)
		_, _ = hash.Write(index[:])
		var length [4]byte
		binary.BigEndian.PutUint32(length[:], uint32(len(item.id)))
		_, _ = hash.Write(length[:])
		_, _ = hash.Write([]byte(item.id))
		flags := byte(0)
		if item.accepted {
			flags |= probeIndexAcceptedFlag
		}
		if item.probed {
			flags |= probeIndexMeasuredFlag
		}
		_, _ = hash.Write([]byte{flags})
	}
	return hex.EncodeToString(hash.Sum(nil))
}
