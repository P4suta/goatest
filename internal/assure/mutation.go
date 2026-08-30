// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package assure coordinates baseline, coverage, resource, mutation, and
// repair evidence into one fail-closed assurance result.
package assure

import (
	"cmp"
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	gomutants "github.com/P4suta/go-mutants"
	goanalysis "github.com/P4suta/goatest/internal/golang"
	"github.com/P4suta/goatest/internal/mutationbridge"
	"github.com/P4suta/goatest/internal/report"
)

const (
	standardFuzzExecutions          = 10_000
	deepFuzzExecutions              = 100_000
	minimumMutationTimeout          = 30 * time.Second
	mutationTimeoutOverhead         = 5 * time.Second
	standardMutationTimeoutLimit    = 30 * time.Minute
	deepMutationTimeoutLimit        = 5 * time.Hour
	mutationTimeoutMultiplier       = 5
	individualMutationTargetLimit   = 8
	maximumMutationBatchTargets     = 64
	maximumMutationRunArgumentBytes = 8 << 10
	maximumMutationBatchDuration    = time.Second
)

// MutationSession is the narrow reusable part of the go-mutants bridge used
// by the assurance evaluator. The interface also keeps scheduler tests free of
// subprocesses.
type MutationSession interface {
	Catalog() gomutants.Catalog
	Exec(context.Context, gomutants.ExecRequest) (gomutants.MutantResult, error)
}

// TargetEvidence is one top-level Go test/fuzz target and the source files it
// demonstrably reached during its baseline execution.
type TargetEvidence struct {
	Target       goanalysis.Target
	CoveredFiles []string
	Environment  []string
	Duration     time.Duration
}

type MutationOptions struct {
	Root           string
	Contract       string
	NoApply        bool
	ReplayMutantID string
	FuzzExecutions int
	Timeout        time.Duration
	Jobs           int
	Accepted       map[string]bool
	Progress       func(completed, total int)
}

type MutationEvaluation struct {
	Evidence []report.Evidence
	Findings []report.Finding
	Repairs  []report.Repair
	Applied  bool
}

// EvaluateMutations selects only targets that baseline coverage proves can
// reach a mutant. A fuzz-only kill is useful evidence only after its standard
// corpus artifact is durably promoted; callers must then start a fresh round.
func EvaluateMutations(ctx context.Context, session MutationSession, targets []TargetEvidence, options MutationOptions) (MutationEvaluation, error) {
	if session == nil {
		return MutationEvaluation{}, fmt.Errorf("goatest: nil mutation session")
	}
	if options.Root == "" {
		return MutationEvaluation{}, fmt.Errorf("goatest: mutation evaluation requires a repository root")
	}
	executions := fuzzExecutions(options.Contract, options.FuzzExecutions)
	catalog := session.Catalog()
	mutants := make([]gomutants.Mutant, 0, len(catalog.Mutants))
	for _, mutant := range catalog.Mutants {
		if !mutant.Accepted || options.ReplayMutantID != "" && mutant.ID != options.ReplayMutantID {
			continue
		}
		mutants = append(mutants, mutant)
	}
	if options.ReplayMutantID != "" && len(mutants) == 0 {
		return MutationEvaluation{}, fmt.Errorf("goatest: replay mutant %s is absent from prepared catalog", options.ReplayMutantID)
	}
	var evaluation MutationEvaluation
	for _, rejection := range catalog.Rejections {
		if options.ReplayMutantID != "" && rejection.ID != options.ReplayMutantID {
			continue
		}
		evaluation.Evidence = append(evaluation.Evidence, report.Evidence{
			Kind: "mutation", ID: rejection.ID, Status: "compile-equivalent", Detail: rejection.Diagnostic,
		})
	}
	seeds := evaluateMutationSeeds(ctx, session, mutants, targets, options)
	for _, seed := range seeds {
		if seed.err != nil {
			return MutationEvaluation{}, seed.err
		}
		evaluation.append(seed.evaluation)
		if seed.resolved {
			continue
		}

		var killed, blocked bool
		for _, target := range seed.reaching {
			if target.Target.Kind != goanalysis.KindFuzz {
				continue
			}
			result, err := session.Exec(ctx, fuzzRequest(seed.mutant, target, executions, calibratedMutationTimeout(options.Contract, target.Duration, options.Timeout)))
			if err != nil {
				return MutationEvaluation{}, fmt.Errorf("goatest: fuzz mutant %s with %s: %w", seed.mutant.DisplayID, target.Target.Name, err)
			}
			switch result.Outcome {
			case gomutants.OutcomeKilled:
				if options.NoApply {
					evaluation.addFinding(seed.mutant, "unpersisted-fuzz-kill", "targeted fuzzing found a killing input, but --no-apply left it outside the corpus", options.Accepted)
					killed = true
					break
				}
				promoted, err := promoteTargetArtifacts(options.Root, seed.mutant, target.Target.Name, result.Artifacts, &evaluation)
				if err != nil {
					return MutationEvaluation{}, err
				}
				if !promoted {
					evaluation.addFinding(seed.mutant, "unpersisted-fuzz-kill", "targeted fuzzing killed the mutant without a promotable standard corpus input", options.Accepted)
				} else {
					evaluation.Applied = true
				}
				killed = true
			case gomutants.OutcomeSurvived:
				// Try another covering fuzz target, if present.
			case gomutants.OutcomeTimedOut:
				evaluation.addFinding(seed.mutant, "fuzz-timeout", "targeted fuzzing reached its safety timeout", options.Accepted)
				blocked = true
			case gomutants.OutcomeInconclusive, gomutants.OutcomeErrored, gomutants.OutcomeNotRun:
				evaluation.addFinding(seed.mutant, "fuzz-inconclusive", "targeted fuzzing could not establish a deterministic outcome", options.Accepted)
				blocked = true
			default:
				return MutationEvaluation{}, fmt.Errorf("goatest: mutant %s returned unknown fuzz outcome %q", seed.mutant.DisplayID, result.Outcome)
			}
			if killed || blocked {
				break
			}
		}
		if killed || blocked {
			continue
		}

		evaluation.addFinding(seed.mutant, "surviving-mutant", "all reaching tests passed with this mutation active", options.Accepted)
	}
	return evaluation, nil
}

type mutationSeed struct {
	mutant     gomutants.Mutant
	reaching   []TargetEvidence
	evaluation MutationEvaluation
	resolved   bool
	err        error
}

type mutationSeedExecution struct {
	request gomutants.ExecRequest
	detail  string
}

func evaluateMutationSeeds(ctx context.Context, session MutationSession, mutants []gomutants.Mutant, targets []TargetEvidence, options MutationOptions) []mutationSeed {
	if len(mutants) == 0 {
		return nil
	}
	results := make([]mutationSeed, len(mutants))
	jobs := min(max(options.Jobs, 1), len(mutants))
	indexes := make(chan int, len(mutants))
	var workers sync.WaitGroup
	var progress sync.Mutex
	completed := 0
	workers.Add(jobs)
	for range jobs {
		go func() {
			defer workers.Done()
			for index := range indexes {
				results[index] = evaluateMutationSeed(ctx, session, mutants[index], targets, options)
				progress.Lock()
				completed++
				if options.Progress != nil {
					options.Progress(completed, len(mutants))
				}
				progress.Unlock()
			}
		}()
	}
	for index := range mutants {
		indexes <- index
	}
	close(indexes)
	workers.Wait()
	return results
}

func evaluateMutationSeed(ctx context.Context, session MutationSession, mutant gomutants.Mutant, targets []TargetEvidence, options MutationOptions) mutationSeed {
	seed := mutationSeed{mutant: mutant, reaching: reachingTargets(mutant.Path, targets)}
	if len(seed.reaching) == 0 {
		seed.evaluation.addFinding(mutant, "unreached-mutant", "no top-level test or fuzz target reaches this mutation", options.Accepted)
		seed.resolved = true
		return seed
	}
	for _, execution := range mutationSeedExecutions(mutant, seed.reaching, options) {
		result, err := session.Exec(ctx, execution.request)
		if err != nil {
			seed.err = fmt.Errorf("goatest: execute mutant %s with %s: %w", mutant.DisplayID, execution.detail, err)
			return seed
		}
		switch result.Outcome {
		case gomutants.OutcomeKilled:
			seed.evaluation.addKill(mutant, execution.detail)
			seed.resolved = true
		case gomutants.OutcomeSurvived:
			// Continue through every demonstrably relevant target.
		case gomutants.OutcomeTimedOut:
			seed.evaluation.addFinding(mutant, "mutation-timeout", "target timed out while this mutation was active", options.Accepted)
			seed.resolved = true
		case gomutants.OutcomeInconclusive, gomutants.OutcomeErrored, gomutants.OutcomeNotRun:
			seed.evaluation.addFinding(mutant, "mutation-inconclusive", "target could not establish a deterministic mutation outcome", options.Accepted)
			seed.resolved = true
		default:
			seed.err = fmt.Errorf("goatest: mutant %s returned unknown outcome %q", mutant.DisplayID, result.Outcome)
			return seed
		}
		if seed.resolved {
			return seed
		}
	}
	return seed
}

func mutationSeedExecutions(mutant gomutants.Mutant, targets []TargetEvidence, options MutationOptions) []mutationSeedExecution {
	individual := min(len(targets), individualMutationTargetLimit)
	executions := make([]mutationSeedExecution, 0, individual+len(targets[individual:]))
	for _, target := range targets[:individual] {
		executions = append(executions, mutationSeedExecution{
			request: seedRequest(mutant, target, calibratedMutationTimeout(options.Contract, target.Duration, options.Timeout)),
			detail:  target.Target.Name,
		})
	}
	for _, batch := range mutationTargetBatches(targets[individual:]) {
		duration := batchMutationDuration(batch)
		executions = append(executions, mutationSeedExecution{
			request: batchSeedRequest(mutant, batch, calibratedMutationTimeout(options.Contract, duration, options.Timeout)),
			detail:  batchMutationDetail(batch),
		})
	}
	return executions
}

func mutationTargetBatches(targets []TargetEvidence) [][]TargetEvidence {
	batches := make([][]TargetEvidence, 0)
	byExecutionEnvironment := make(map[string]int)
	for _, target := range targets {
		key := target.Target.Package + "\x00" + strings.Join(target.Environment, "\x00")
		index, ok := byExecutionEnvironment[key]
		full := ok && len(batches[index]) == maximumMutationBatchTargets
		if ok && !full && len(batches[index]) != 0 {
			candidate := append(slices.Clone(batches[index]), target)
			full = len(batchRunArgument(candidate)) > maximumMutationRunArgumentBytes ||
				batchMutationDuration(candidate) > maximumMutationBatchDuration
		}
		if !ok || full {
			index = len(batches)
			byExecutionEnvironment[key] = index
			batches = append(batches, nil)
		}
		batches[index] = append(batches[index], target)
	}
	return batches
}

func batchMutationDuration(targets []TargetEvidence) time.Duration {
	var total time.Duration
	for _, target := range targets {
		duration := min(max(target.Duration, 0), deepMutationTimeoutLimit)
		total = min(total+duration, deepMutationTimeoutLimit)
	}
	return total
}

func batchMutationDetail(targets []TargetEvidence) string {
	if len(targets) == 1 {
		return targets[0].Target.Name
	}
	return fmt.Sprintf("%s (%d related targets)", targets[0].Target.Package, len(targets))
}

func (evaluation *MutationEvaluation) append(other MutationEvaluation) {
	evaluation.Evidence = append(evaluation.Evidence, other.Evidence...)
	evaluation.Findings = append(evaluation.Findings, other.Findings...)
	evaluation.Repairs = append(evaluation.Repairs, other.Repairs...)
	evaluation.Applied = evaluation.Applied || other.Applied
}

func reachingTargets(path string, targets []TargetEvidence) []TargetEvidence {
	normalized := filepath.ToSlash(path)
	measured := make([]TargetEvidence, 0)
	unmeasured := make([]TargetEvidence, 0)
	for _, target := range targets {
		if !slices.Contains(target.CoveredFiles, normalized) {
			continue
		}
		if target.Duration > 0 {
			measured = append(measured, target)
		} else {
			unmeasured = append(unmeasured, target)
		}
	}
	slices.SortStableFunc(measured, func(first, second TargetEvidence) int {
		return cmp.Compare(first.Duration, second.Duration)
	})
	return append(measured, unmeasured...)
}

func seedRequest(mutant gomutants.Mutant, target TargetEvidence, timeout time.Duration) gomutants.ExecRequest {
	return gomutants.ExecRequest{
		Mutant: mutant.ID, Package: target.Target.Package,
		Args: []string{"-test.run=^" + regexp.QuoteMeta(target.Target.Name) + "$"},
		Env:  slices.Clone(target.Environment), Timeout: timeout,
	}
}

func batchSeedRequest(mutant gomutants.Mutant, targets []TargetEvidence, timeout time.Duration) gomutants.ExecRequest {
	if len(targets) == 1 {
		return seedRequest(mutant, targets[0], timeout)
	}
	return gomutants.ExecRequest{
		Mutant: mutant.ID, Package: targets[0].Target.Package,
		Args: []string{batchRunArgument(targets)},
		Env:  slices.Clone(targets[0].Environment), Timeout: timeout,
	}
}

func batchRunArgument(targets []TargetEvidence) string {
	names := make([]string, len(targets))
	for index, target := range targets {
		names[index] = regexp.QuoteMeta(target.Target.Name)
	}
	return "-test.run=^(" + strings.Join(names, "|") + ")$"
}

func fuzzRequest(mutant gomutants.Mutant, target TargetEvidence, executions int, timeout time.Duration) gomutants.ExecRequest {
	return gomutants.ExecRequest{
		Mutant: mutant.ID, Package: target.Target.Package,
		Args: []string{
			"-test.run=^$",
			"-test.fuzz=^" + regexp.QuoteMeta(target.Target.Name) + "$",
			"-test.fuzztime=" + strconv.Itoa(executions) + "x",
		},
		Env: slices.Clone(target.Environment), Timeout: timeout,
	}
}

func fuzzExecutions(contract string, requested int) int {
	maximum := standardFuzzExecutions
	if contract == "deep-v1" {
		maximum = deepFuzzExecutions
	}
	if requested <= 0 {
		return maximum
	}
	return min(requested, maximum)
}

func calibratedMutationTimeout(contract string, baseline, override time.Duration) time.Duration {
	if override > 0 {
		return override
	}
	maximum := standardMutationTimeoutLimit
	if contract == "deep-v1" {
		maximum = deepMutationTimeoutLimit
	}
	boundedBaseline := min(max(baseline, 0), maximum)
	timeout := boundedBaseline*mutationTimeoutMultiplier + mutationTimeoutOverhead
	return min(max(timeout, minimumMutationTimeout), maximum)
}

func promoteTargetArtifacts(root string, mutant gomutants.Mutant, targetName string, artifacts []gomutants.Artifact, evaluation *MutationEvaluation) (bool, error) {
	var promoted bool
	marker := "/testdata/fuzz/" + targetName + "/"
	rootMarker := "testdata/fuzz/" + targetName + "/"
	for _, artifact := range artifacts {
		path := filepath.ToSlash(artifact.Path)
		if !strings.HasPrefix(path, rootMarker) && !strings.Contains(path, marker) {
			continue
		}
		appliedPath, added, err := mutationbridge.PromoteCorpus(root, artifact)
		if err != nil {
			return false, fmt.Errorf("goatest: promote fuzz artifact for mutant %s: %w", mutant.DisplayID, err)
		}
		if !added {
			// An identical pre-existing corpus is already durable evidence.
			promoted = true
			continue
		}
		promoted = true
		evaluation.Repairs = append(evaluation.Repairs, report.Repair{
			ID: report.FindingID("corpus", mutant.ID, appliedPath), Finding: report.FindingID("mutation", mutant.ID),
			Path: appliedPath, Status: "applied",
		})
	}
	return promoted, nil
}

func (evaluation *MutationEvaluation) addKill(mutant gomutants.Mutant, target string) {
	evaluation.Evidence = append(evaluation.Evidence, report.Evidence{
		Kind: "mutation", ID: mutant.ID, Status: "killed", Detail: target,
	})
}

func (evaluation *MutationEvaluation) addFinding(mutant gomutants.Mutant, kind, summary string, accepted map[string]bool) {
	finding := mutationFinding(mutant, kind, summary)
	if accepted[finding.ID] {
		evaluation.Evidence = append(evaluation.Evidence, report.Evidence{
			Kind: "mutation", ID: mutant.ID, Status: "accepted", Detail: finding.ID,
		})
		return
	}
	evaluation.Findings = append(evaluation.Findings, finding)
}

func mutationFinding(mutant gomutants.Mutant, kind, summary string) report.Finding {
	id := report.FindingID("mutation", mutant.ID)
	return report.Finding{
		ID: id, Kind: kind, Path: mutant.Path, Line: mutant.Line, Summary: summary,
		Replay: "goatest replay " + id,
		Mutant: fmt.Sprintf("%s: %s -> %s", mutant.Rule, mutant.Original, mutant.Replacement), MutantID: mutant.ID,
	}
}
