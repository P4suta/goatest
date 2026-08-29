// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package assure coordinates baseline, coverage, resource, mutation, and
// repair evidence into one fail-closed assurance result.
package assure

import (
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
	standardFuzzExecutions       = 10_000
	deepFuzzExecutions           = 100_000
	minimumMutationTimeout       = 30 * time.Second
	mutationTimeoutOverhead      = 5 * time.Second
	standardMutationTimeoutLimit = 30 * time.Minute
	deepMutationTimeoutLimit     = 5 * time.Hour
	mutationTimeoutMultiplier    = 5
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
	var evaluation MutationEvaluation
	catalog := session.Catalog()
	for _, rejection := range catalog.Rejections {
		evaluation.Evidence = append(evaluation.Evidence, report.Evidence{
			Kind: "mutation", ID: rejection.ID, Status: "compile-equivalent", Detail: rejection.Diagnostic,
		})
	}
	mutants := make([]gomutants.Mutant, 0, len(catalog.Mutants))
	for _, mutant := range catalog.Mutants {
		if mutant.Accepted {
			mutants = append(mutants, mutant)
		}
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

		killed := false
		blocked := false
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

func evaluateMutationSeeds(ctx context.Context, session MutationSession, mutants []gomutants.Mutant, targets []TargetEvidence, options MutationOptions) []mutationSeed {
	results := make([]mutationSeed, len(mutants))
	if len(mutants) == 0 {
		return results
	}
	jobs := options.Jobs
	if jobs <= 0 {
		jobs = 1
	}
	if jobs > len(mutants) {
		jobs = len(mutants)
	}
	indexes := make(chan int)
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
	for _, target := range seed.reaching {
		result, err := session.Exec(ctx, seedRequest(mutant, target, calibratedMutationTimeout(options.Contract, target.Duration, options.Timeout)))
		if err != nil {
			seed.err = fmt.Errorf("goatest: execute mutant %s with %s: %w", mutant.DisplayID, target.Target.Name, err)
			return seed
		}
		switch result.Outcome {
		case gomutants.OutcomeKilled:
			seed.evaluation.addKill(mutant, target.Target.Name)
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

func (evaluation *MutationEvaluation) append(other MutationEvaluation) {
	evaluation.Evidence = append(evaluation.Evidence, other.Evidence...)
	evaluation.Findings = append(evaluation.Findings, other.Findings...)
	evaluation.Repairs = append(evaluation.Repairs, other.Repairs...)
	evaluation.Applied = evaluation.Applied || other.Applied
}

func reachingTargets(path string, targets []TargetEvidence) []TargetEvidence {
	normalized := filepath.ToSlash(path)
	result := make([]TargetEvidence, 0)
	for _, target := range targets {
		if slices.Contains(target.CoveredFiles, normalized) {
			result = append(result, target)
		}
	}
	return result
}

func seedRequest(mutant gomutants.Mutant, target TargetEvidence, timeout time.Duration) gomutants.ExecRequest {
	return gomutants.ExecRequest{
		Mutant: mutant.ID, Package: target.Target.Package,
		Args: []string{"-test.run=^" + regexp.QuoteMeta(target.Target.Name) + "$"},
		Env:  slices.Clone(target.Environment), Timeout: timeout,
	}
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
	if requested <= 0 || requested > maximum {
		return maximum
	}
	return requested
}

func calibratedMutationTimeout(contract string, baseline, override time.Duration) time.Duration {
	if override != 0 {
		return override
	}
	maximum := standardMutationTimeoutLimit
	if contract == "deep-v1" {
		maximum = deepMutationTimeoutLimit
	}
	if baseline <= 0 {
		return minimumMutationTimeout
	}
	if baseline > (maximum-mutationTimeoutOverhead)/mutationTimeoutMultiplier {
		return maximum
	}
	timeout := baseline*mutationTimeoutMultiplier + mutationTimeoutOverhead
	if timeout < minimumMutationTimeout {
		return minimumMutationTimeout
	}
	return min(timeout, maximum)
}

func promoteTargetArtifacts(root string, mutant gomutants.Mutant, targetName string, artifacts []gomutants.Artifact, evaluation *MutationEvaluation) (bool, error) {
	promoted := false
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
