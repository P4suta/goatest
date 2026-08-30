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
	"github.com/P4suta/goatest/internal/provider"
	"github.com/P4suta/goatest/internal/repair"
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
// by the assurance evaluator. Exec must permit concurrent calls, matching the
// go-mutants Session contract. The interface also keeps scheduler tests free
// of subprocesses.
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
	Root            string
	Snapshot        string
	Contract        string
	NoApply         bool
	ReplayMutantID  string
	TestArgs        []string
	FuzzExecutions  int
	Timeout         time.Duration
	Jobs            int
	Accepted        map[string]bool
	Progress        func(completed, total int)
	OriginalControl func(context.Context, gomutants.ExecRequest) (gomutants.CommandResult, error)
}

type MutationEvaluation struct {
	Evidence   []report.Evidence
	Findings   []report.Finding
	Repairs    []report.Repair
	Accounting report.MutantAccounting
	Mutants    []report.MutantDisposition
	Applied    bool
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
	if err := validateMutationCatalog(catalog); err != nil {
		return MutationEvaluation{}, err
	}
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
			Kind: "mutation", ID: rejection.ID, Status: "compile-rejected", Detail: rejection.Diagnostic,
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
			request := fuzzRequest(seed.mutant, target, executions, calibratedMutationTimeout(options.Contract, target.Duration, options.Timeout))
			request.Args = append(request.Args, options.TestArgs...)
			result, err := session.Exec(ctx, request)
			if err != nil {
				return MutationEvaluation{}, fmt.Errorf("goatest: fuzz mutant %s with %s: %w", seed.mutant.DisplayID, target.Target.Name, err)
			}
			switch result.Outcome {
			case gomutants.OutcomeKilled:
				confirmed, confirmFinding, confirmedResult, confirmErr := confirmMutationKill(ctx, session, seed.mutant, request, result, options)
				if confirmErr != nil {
					return MutationEvaluation{}, confirmErr
				}
				if !confirmed {
					evaluation.addFinding(seed.mutant, confirmFinding.kind, confirmFinding.summary, options.Accepted)
					blocked = true
					break
				}
				result = confirmedResult
				if options.NoApply {
					stored, storeErr := storeTargetArtifacts(options.Root, options.Snapshot, seed.mutant, target.Target.Name, result.Artifacts, &evaluation)
					if storeErr != nil {
						return MutationEvaluation{}, storeErr
					}
					summary := "targeted fuzzing found a killing input, but no promotable standard corpus artifact was returned"
					if stored {
						summary = "targeted fuzzing found a killing input; a validated corpus candidate was stored for explicit fix --apply"
					}
					evaluation.addFinding(seed.mutant, "unpersisted-fuzz-kill", summary, options.Accepted)
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
	evaluation.Accounting, evaluation.Mutants = mutationAccounting(catalog, options.ReplayMutantID, evaluation)
	return evaluation, nil
}

func validateMutationCatalog(catalog gomutants.Catalog) error {
	mutants := make(map[string]gomutants.Mutant, len(catalog.Mutants))
	for _, mutant := range catalog.Mutants {
		if mutant.ID == "" {
			return fmt.Errorf("goatest: mutation catalog contains an empty mutant ID")
		}
		if _, duplicate := mutants[mutant.ID]; duplicate {
			return fmt.Errorf("goatest: mutation catalog contains duplicate mutant %s", mutant.ID)
		}
		mutants[mutant.ID] = mutant
	}
	rejections := make(map[string]bool, len(catalog.Rejections))
	for _, rejection := range catalog.Rejections {
		mutant, exists := mutants[rejection.ID]
		if !exists {
			return fmt.Errorf("goatest: compile rejection %s is absent from the mutation catalog", rejection.ID)
		}
		if rejections[rejection.ID] {
			return fmt.Errorf("goatest: mutation catalog contains duplicate rejection %s", rejection.ID)
		}
		if mutant.Accepted {
			return fmt.Errorf("goatest: mutant %s is both executable and compile-rejected", rejection.ID)
		}
		rejections[rejection.ID] = true
	}
	for _, mutant := range catalog.Mutants {
		if !mutant.Accepted && !rejections[mutant.ID] {
			return fmt.Errorf("goatest: non-executable mutant %s has no compile rejection", mutant.ID)
		}
	}
	return nil
}

func mutationAccounting(catalog gomutants.Catalog, replayID string, evaluation MutationEvaluation) (report.MutantAccounting, []report.MutantDisposition) {
	accounting := report.MutantAccounting{Discovered: len(catalog.Mutants)}
	selected := make(map[string]bool)
	for _, mutant := range catalog.Mutants {
		if mutant.Accepted && (replayID == "" || mutant.ID == replayID) {
			selected[mutant.ID] = true
		}
	}
	for _, rejection := range catalog.Rejections {
		if replayID == "" || rejection.ID == replayID {
			selected[rejection.ID] = true
		}
	}
	accounting.Selected = len(selected)
	accounting.OutOfScope = accounting.Discovered - accounting.Selected
	statuses := make(map[string]report.MutantStatus, len(selected))
	details := make(map[string]string, len(selected))
	for _, item := range evaluation.Evidence {
		if !selected[item.ID] || item.Kind != "mutation" {
			continue
		}
		switch item.Status {
		case "killed", "compile-rejected", "accepted":
			statuses[item.ID] = report.MutantStatus(item.Status)
			details[item.ID] = item.Detail
		}
	}
	for _, finding := range evaluation.Findings {
		if !selected[finding.MutantID] {
			continue
		}
		if finding.Kind == "surviving-mutant" || finding.Kind == "unreached-mutant" {
			statuses[finding.MutantID] = report.MutantSurvived
		} else {
			statuses[finding.MutantID] = report.MutantInconclusive
		}
		details[finding.MutantID] = finding.Summary
	}
	dispositions := make([]report.MutantDisposition, 0, len(catalog.Mutants))
	for _, mutant := range catalog.Mutants {
		status := report.MutantOutOfScope
		detail := "outside the resolved mutation scope"
		if selected[mutant.ID] {
			status = statuses[mutant.ID]
			detail = details[mutant.ID]
			if status == "" {
				status = report.MutantUnknown
				detail = "selected mutant has no terminal disposition"
			}
		}
		dispositions = append(dispositions, report.MutantDisposition{
			ID: mutant.ID, Status: status, Path: mutant.Path, Line: mutant.Line,
			Package: mutant.Package, Rule: mutant.Rule, Detail: detail,
		})
		switch status {
		case report.MutantKilled:
			accounting.Killed++
			accounting.Executed++
		case report.MutantSurvived:
			accounting.Survived++
			accounting.Executed++
		case report.MutantInconclusive:
			accounting.Inconclusive++
			accounting.Executed++
		case report.MutantCompileRejected:
			accounting.CompileRejected++
		case report.MutantAccepted:
			accounting.Accepted++
		case report.MutantUnknown:
			accounting.Unknown++
		}
	}
	return accounting, dispositions
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
		request := gomutants.ExecRequest{
			Mutant: mutant.ID, Package: mutant.Package, Args: slices.Clone(options.TestArgs),
			Timeout: calibratedMutationTimeout(options.Contract, 0, options.Timeout),
		}
		result, err := session.Exec(ctx, request)
		if err != nil {
			seed.err = fmt.Errorf("goatest: execute unreached mutant %s: %w", mutant.DisplayID, err)
			return seed
		}
		switch result.Outcome {
		case gomutants.OutcomeKilled:
			confirmed, finding, _, confirmErr := confirmMutationKill(ctx, session, mutant, request, result, options)
			if confirmErr != nil {
				seed.err = confirmErr
				return seed
			}
			if confirmed {
				seed.evaluation.addKill(mutant, mutant.Package+" package suite")
			} else {
				seed.evaluation.addFinding(mutant, finding.kind, finding.summary, options.Accepted)
			}
		case gomutants.OutcomeSurvived:
			seed.evaluation.addFinding(mutant, "unreached-mutant", "no measured top-level target reached this mutation; its package suite survived", options.Accepted)
		case gomutants.OutcomeTimedOut:
			seed.evaluation.addFinding(mutant, "mutation-timeout", "package suite timed out while confirming an unreached mutation", options.Accepted)
		case gomutants.OutcomeInconclusive, gomutants.OutcomeErrored, gomutants.OutcomeNotRun:
			seed.evaluation.addFinding(mutant, "mutation-inconclusive", "package suite could not establish an outcome for an unreached mutation", options.Accepted)
		default:
			seed.err = fmt.Errorf("goatest: mutant %s returned unknown outcome %q", mutant.DisplayID, result.Outcome)
		}
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
			confirmed, finding, _, confirmErr := confirmMutationKill(ctx, session, mutant, execution.request, result, options)
			if confirmErr != nil {
				seed.err = confirmErr
				return seed
			}
			if confirmed {
				detail := execution.detail
				if options.OriginalControl != nil {
					detail += " (paired confirmation)"
				}
				seed.evaluation.addKill(mutant, detail)
			} else {
				seed.evaluation.addFinding(mutant, finding.kind, finding.summary, options.Accepted)
			}
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

type confirmationFinding struct {
	kind    string
	summary string
}

func confirmMutationKill(ctx context.Context, session MutationSession, mutant gomutants.Mutant, request gomutants.ExecRequest, first gomutants.MutantResult, options MutationOptions) (bool, confirmationFinding, gomutants.MutantResult, error) {
	if options.OriginalControl == nil {
		return true, confirmationFinding{}, first, nil
	}
	control, err := options.OriginalControl(ctx, request)
	if err != nil {
		return false, confirmationFinding{}, gomutants.MutantResult{}, fmt.Errorf("goatest: original control for mutant %s: %w", mutant.DisplayID, err)
	}
	if control.TimedOut || control.ExitCode != 0 {
		return false, confirmationFinding{
			kind: "flaky-mutation-control", summary: "the original control failed immediately before kill confirmation: " + summarize(control.Output),
		}, gomutants.MutantResult{}, nil
	}
	second, err := session.Exec(ctx, request)
	if err != nil {
		return false, confirmationFinding{}, gomutants.MutantResult{}, fmt.Errorf("goatest: confirm mutant %s: %w", mutant.DisplayID, err)
	}
	if second.Outcome != gomutants.OutcomeKilled {
		return false, confirmationFinding{
			kind: "flaky-mutation-kill", summary: "the mutation kill did not reproduce in paired confirmation",
		}, second, nil
	}
	return true, confirmationFinding{}, second, nil
}

func mutationSeedExecutions(mutant gomutants.Mutant, targets []TargetEvidence, options MutationOptions) []mutationSeedExecution {
	individual := min(len(targets), individualMutationTargetLimit)
	executions := make([]mutationSeedExecution, 0, individual+len(targets[individual:]))
	for _, target := range targets[:individual] {
		request := seedRequest(mutant, target, calibratedMutationTimeout(options.Contract, target.Duration, options.Timeout))
		request.Args = append(request.Args, options.TestArgs...)
		executions = append(executions, mutationSeedExecution{
			request: request,
			detail:  target.Target.Name,
		})
	}
	for _, batch := range mutationTargetBatches(targets[individual:]) {
		duration := batchMutationDuration(batch)
		request := batchSeedRequest(mutant, batch, calibratedMutationTimeout(options.Contract, duration, options.Timeout))
		request.Args = append(request.Args, options.TestArgs...)
		executions = append(executions, mutationSeedExecution{
			request: request,
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
	evaluation.Accounting.Discovered += other.Accounting.Discovered
	evaluation.Accounting.Selected += other.Accounting.Selected
	evaluation.Accounting.Executed += other.Accounting.Executed
	evaluation.Accounting.Killed += other.Accounting.Killed
	evaluation.Accounting.Survived += other.Accounting.Survived
	evaluation.Accounting.Inconclusive += other.Accounting.Inconclusive
	evaluation.Accounting.CompileRejected += other.Accounting.CompileRejected
	evaluation.Accounting.Accepted += other.Accounting.Accepted
	evaluation.Accounting.OutOfScope += other.Accounting.OutOfScope
	evaluation.Accounting.Unknown += other.Accounting.Unknown
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

func storeTargetArtifacts(root, snapshot string, mutant gomutants.Mutant, targetName string, artifacts []gomutants.Artifact, evaluation *MutationEvaluation) (bool, error) {
	var stored bool
	marker := "/testdata/fuzz/" + targetName + "/"
	rootMarker := "testdata/fuzz/" + targetName + "/"
	finding := mutationFinding(mutant, "unpersisted-fuzz-kill", "targeted fuzzing found a killing input")
	for _, artifact := range artifacts {
		artifactPath := filepath.ToSlash(artifact.Path)
		if !strings.HasPrefix(artifactPath, rootMarker) && !strings.Contains(artifactPath, marker) {
			continue
		}
		path, data, err := mutationbridge.CorpusCandidate(artifact)
		if err != nil {
			return false, fmt.Errorf("goatest: preserve fuzz artifact for mutant %s: %w", mutant.DisplayID, err)
		}
		candidate := provider.Candidate{Kind: "corpus", Path: path, Content: data}
		identifier := generatedRepairID(snapshot, finding, candidate)
		if _, err := repair.StoreCandidate(root, repair.CandidateRecord{
			Version: repair.CandidateVersion, ID: identifier, Snapshot: snapshot,
			Finding: finding, Candidate: candidate, Validation: "paired-fuzz-confirmed",
		}); err != nil {
			return false, err
		}
		evaluation.Repairs = append(evaluation.Repairs, report.Repair{
			ID: identifier, Finding: finding.ID, Path: path, Status: string(repair.StatusCandidate),
			Validation: "paired-fuzz-confirmed", Provenance: "snapshot=" + snapshot,
		})
		stored = true
	}
	return stored, nil
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
