// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package assure coordinates baseline, coverage, resource, mutation, and
// repair evidence into one fail-closed assurance result.
package assure

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
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
	"github.com/P4suta/goatest/internal/trace"
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

// The forms an entry of a recorded execution plan takes: one target run on its
// own, several related targets of one package run together, the fuzzing of one
// target, and the package suite a mutant no target reaches is left to.
const (
	mutationPlanIndividual   = "individual:"
	mutationPlanBatch        = "batch:"
	mutationPlanFuzz         = "fuzz:"
	mutationPlanPackageSuite = "package-suite"
)

// The summaries a surviving mutant is reported through: the one a run reached
// by executing every test that could observe the mutation, the one a branch
// proof answered for entirely, and the one a proof answered part of. A
// discharge is a test that was not run, so a summary that named none of them
// would describe a suite that never ran them at all.
const (
	mutationSurvivedSummary         = "all reaching tests passed with this mutation active"
	mutationFullyDischargedSummary  = "no reaching test was run: every one was discharged because none takes the branch this mutation narrows"
	mutationPartlyDischargedSummary = mutationSurvivedSummary +
		"; %d more were discharged without running because none takes the branch this mutation narrows"
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
//
// Covered narrows the same evidence to the coverage blocks the target ran. It
// lives in memory only: blocks are far too large to rewrite on every
// checkpoint, so a target restored from a checkpoint carries nil there and
// routing treats it as reaching everything in CoveredFiles.
type TargetEvidence struct {
	Target       goanalysis.Target
	CoveredFiles []string
	Covered      []goanalysis.FileCoverage
	Environment  []string
	Duration     time.Duration
}

type MutationOptions struct {
	Root           string
	Snapshot       string
	Contract       string
	NoApply        bool
	ReplayMutantID string
	TestArgs       []string
	FuzzExecutions int
	Timeout        time.Duration
	Jobs           int
	Accepted       map[string]bool
	Progress       func(completed, total int)
	Resume         map[string]MutationEvaluation
	// Checkpoint records one terminal mutant evaluation. Worker goroutines may
	// call it concurrently, so callers must provide a concurrency-safe callback.
	Checkpoint      func(string, MutationEvaluation)
	OriginalControl func(context.Context, gomutants.ExecRequest) (gomutants.CommandResult, error)
	// Trace records how each mutant was routed. A nil recorder is an
	// evaluation that records nothing and reaches the same result.
	Trace *trace.Recorder
	// Instrumented is every coverage block the baseline compiled
	// instrumentation for. Routing reads it to tell a position no test ran
	// from a position no profile describes, and never writes to it, so the
	// same slice is shared by every worker.
	Instrumented []goanalysis.FileCoverage
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
	options.OriginalControl = memoizedOriginalControl(options.OriginalControl)
	executions := fuzzExecutions(options.Contract, options.FuzzExecutions)
	catalog := session.Catalog()
	if err := validateMutationCatalog(catalog); err != nil {
		return MutationEvaluation{}, err
	}
	var evaluation MutationEvaluation
	mutants := make([]gomutants.Mutant, 0, len(catalog.Mutants))
	resumed := make(map[string]bool, len(options.Resume))
	replayPresent := options.ReplayMutantID == ""
	for _, mutant := range catalog.Mutants {
		if !mutant.Accepted || options.ReplayMutantID != "" && mutant.ID != options.ReplayMutantID {
			continue
		}
		replayPresent = true
		if saved, ok := options.Resume[mutant.ID]; ok {
			evaluation.append(saved)
			resumed[mutant.ID] = true
			continue
		}
		mutants = append(mutants, mutant)
	}
	for _, rejection := range catalog.Rejections {
		if options.ReplayMutantID != "" && rejection.ID != options.ReplayMutantID {
			continue
		}
		replayPresent = true
		if saved, ok := options.Resume[rejection.ID]; ok {
			if !resumed[rejection.ID] {
				evaluation.append(saved)
				resumed[rejection.ID] = true
			}
			continue
		}
		unit := MutationEvaluation{Evidence: []report.Evidence{{
			Kind: "mutation", ID: rejection.ID, Status: "compile-rejected", Detail: rejection.Diagnostic,
		}}}
		evaluation.append(unit)
		checkpointMutation(options, rejection.ID, unit)
	}
	if !replayPresent {
		return MutationEvaluation{}, fmt.Errorf("goatest: replay mutant %s is absent from prepared catalog", options.ReplayMutantID)
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

		unit := seed.evaluation
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
					unit.addFinding(seed.mutant, confirmFinding.kind, confirmFinding.summary, options.Accepted)
					blocked = true
					break
				}
				result = confirmedResult
				if options.NoApply {
					stored, storeErr := storeTargetArtifacts(options.Root, options.Snapshot, seed.mutant, target.Target.Name, result.Artifacts, &unit)
					if storeErr != nil {
						return MutationEvaluation{}, storeErr
					}
					summary := "targeted fuzzing found a killing input, but no promotable standard corpus artifact was returned"
					if stored {
						summary = "targeted fuzzing found a killing input; a validated corpus candidate was stored for explicit fix --apply"
					}
					unit.addFinding(seed.mutant, "unpersisted-fuzz-kill", summary, options.Accepted)
					killed = true
					break
				}
				promoted, err := promoteTargetArtifacts(options.Root, seed.mutant, target.Target.Name, result.Artifacts, &unit)
				if err != nil {
					return MutationEvaluation{}, err
				}
				if !promoted {
					unit.addFinding(seed.mutant, "unpersisted-fuzz-kill", "targeted fuzzing killed the mutant without a promotable standard corpus input", options.Accepted)
				} else {
					unit.Applied = true
				}
				killed = true
			case gomutants.OutcomeSurvived:
				// Try another covering fuzz target, if present.
			case gomutants.OutcomeTimedOut:
				unit.addFinding(seed.mutant, "fuzz-timeout", "targeted fuzzing reached its safety timeout", options.Accepted)
				blocked = true
			case gomutants.OutcomeInconclusive, gomutants.OutcomeErrored, gomutants.OutcomeNotRun:
				unit.addFinding(seed.mutant, "fuzz-inconclusive", "targeted fuzzing could not establish a deterministic outcome", options.Accepted)
				blocked = true
			default:
				return MutationEvaluation{}, fmt.Errorf("goatest: mutant %s returned unknown fuzz outcome %q", seed.mutant.DisplayID, result.Outcome)
			}
			if killed || blocked {
				break
			}
		}
		if killed || blocked {
			evaluation.append(unit)
			checkpointMutation(options, seed.mutant.ID, unit)
			continue
		}

		unit.addFinding(seed.mutant, "surviving-mutant", mutationSurvivedSummary, options.Accepted)
		evaluation.append(unit)
		checkpointMutation(options, seed.mutant.ID, unit)
	}
	evaluation.Accounting, evaluation.Mutants = mutationAccounting(catalog, options.ReplayMutantID, evaluation)
	return evaluation, nil
}

func checkpointMutation(options MutationOptions, id string, evaluation MutationEvaluation) {
	if options.Checkpoint != nil {
		options.Checkpoint(id, evaluation)
	}
}

// MutationCatalogFingerprint identifies the catalog facts that decide whether
// saved mutant dispositions still name the same source mutations.
func MutationCatalogFingerprint(catalog gomutants.Catalog) string {
	type identity struct {
		id, path, pkg, rule string
		line                int
	}
	items := make([]identity, 0, len(catalog.Mutants))
	for _, mutant := range catalog.Mutants {
		items = append(items, identity{mutant.ID, filepath.ToSlash(mutant.Path), mutant.Package, mutant.Rule, mutant.Line})
	}
	slices.SortFunc(items, func(a, b identity) int {
		if compared := strings.Compare(a.id, b.id); compared != 0 {
			return compared
		}
		if compared := strings.Compare(a.path, b.path); compared != 0 {
			return compared
		}
		if compared := strings.Compare(a.pkg, b.pkg); compared != 0 {
			return compared
		}
		if compared := strings.Compare(a.rule, b.rule); compared != 0 {
			return compared
		}
		return cmp.Compare(a.line, b.line)
	})
	hash := sha256.New()
	_, _ = hash.Write([]byte("goatest-mutation-catalog-v1\x00"))
	for _, item := range items {
		for _, field := range []string{item.id, item.path, item.pkg, item.rule} {
			var length [4]byte
			binary.BigEndian.PutUint32(length[:], uint32(len(field)))
			_, _ = hash.Write(length[:])
			_, _ = hash.Write([]byte(field))
		}
		var line [8]byte
		binary.BigEndian.PutUint64(line[:], uint64(int64(item.line)))
		_, _ = hash.Write(line[:])
	}
	return hex.EncodeToString(hash.Sum(nil))
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

// mutationSeedExecution is one planned execution of a mutant: the request that
// runs it, the detail that names it in the evidence, and the plan entry that
// names it in a trace.
type mutationSeedExecution struct {
	request gomutants.ExecRequest
	detail  string
	plan    string
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
				if results[index].err == nil && results[index].resolved {
					// A finished mutant is saved here, not once every seed has
					// finished: a checkpoint written only at the end of the
					// phase is a checkpoint a dying run never writes.
					checkpointMutation(options, mutants[index].ID, results[index].evaluation)
				}
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
	route := routeMutant(mutant, targets, options.Instrumented)
	seed := mutationSeed{mutant: mutant, reaching: route.reaching}
	if len(seed.reaching) == 0 && len(route.discharged) > 0 {
		// Coverage reached this mutation and a proof answered for every target
		// that did: nothing is left that could observe it, and running the
		// package suite would only run those same discharged tests again. That
		// no test takes the branch the mutation narrows is the finding.
		options.Trace.Route(mutationSeedRoute(mutant, route, nil))
		seed.evaluation.addFinding(mutant, "surviving-mutant", mutationFullyDischargedSummary, options.Accepted)
		seed.resolved = true
		return seed
	}
	if len(seed.reaching) == 0 {
		options.Trace.Route(mutationSeedRoute(mutant, route, nil))
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
	executions := mutationSeedExecutions(mutant, seed.reaching, options)
	options.Trace.Route(mutationSeedRoute(mutant, route, executions))
	for _, execution := range executions {
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
	// Every reaching test passed. Unless a fuzz target can still kill this
	// mutant, nothing is left to learn about it, so it is finalised here and
	// the worker checkpoints it like any other terminal mutant. Waiting for
	// the serial pass would mean the survivors — the mutants that cost the
	// most to execute again — are exactly the results a dying run loses.
	if !reachedByFuzz(seed.reaching) {
		seed.evaluation.addFinding(mutant, "surviving-mutant", mutationSurvivalSummary(len(route.discharged)), options.Accepted)
		seed.resolved = true
	}
	return seed
}

// mutationSurvivalSummary describes a mutant every test that could observe it
// passed on. The discharged tests are counted in the summary because a reader
// comparing the finding against the coverage of the file would otherwise go
// looking for the tests that never ran.
func mutationSurvivalSummary(discharged int) string {
	if discharged == 0 {
		return mutationSurvivedSummary
	}
	return fmt.Sprintf(mutationPartlyDischargedSummary, discharged)
}

// reachedByFuzz reports whether any target that reaches a mutant can fuzz it,
// which is the work a surviving mutant may still have ahead of it.
func reachedByFuzz(targets []TargetEvidence) bool {
	return slices.ContainsFunc(targets, func(target TargetEvidence) bool {
		return target.Target.Kind == goanalysis.KindFuzz
	})
}

type confirmationFinding struct {
	kind    string
	summary string
}

// controlOutcome is one remembered original-control run: what it printed and
// how it ended, or the infrastructure error that stopped it from ending.
type controlOutcome struct {
	once   sync.Once
	result gomutants.CommandResult
	err    error
}

// memoizedOriginalControl runs each distinct control command once per
// evaluation and hands every later kill the remembered outcome. The command is
// determined by the package, the arguments, and the environment of the request
// — never by the mutant, which the original code does not contain — and the
// snapshot an evaluation runs against is frozen, so within one evaluation a
// deterministic control cannot answer differently twice. A control that failed
// is remembered exactly like one that passed: every kill sharing the command
// reports the same flaky-mutation-control evidence without watching the
// control fail again.
func memoizedOriginalControl(control func(context.Context, gomutants.ExecRequest) (gomutants.CommandResult, error)) func(context.Context, gomutants.ExecRequest) (gomutants.CommandResult, error) {
	if control == nil {
		return nil
	}
	var mutex sync.Mutex
	outcomes := make(map[string]*controlOutcome)
	return func(ctx context.Context, request gomutants.ExecRequest) (gomutants.CommandResult, error) {
		key := request.Package + "\x00" + strings.Join(request.Args, "\x00") + "\x00" + strings.Join(request.Env, "\x00")
		mutex.Lock()
		outcome, remembered := outcomes[key]
		if !remembered {
			outcome = &controlOutcome{}
			outcomes[key] = outcome
		}
		mutex.Unlock()
		outcome.once.Do(func() { outcome.result, outcome.err = control(ctx, request) })
		return outcome.result, outcome.err
	}
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
			plan:    mutationPlanIndividual + target.Target.Name,
		})
	}
	for _, batch := range mutationTargetBatches(targets[individual:]) {
		duration := batchMutationDuration(batch)
		request := batchSeedRequest(mutant, batch, calibratedMutationTimeout(options.Contract, duration, options.Timeout))
		request.Args = append(request.Args, options.TestArgs...)
		executions = append(executions, mutationSeedExecution{
			request: request,
			detail:  batchMutationDetail(batch),
			plan:    batchMutationPlan(batch),
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

// batchMutationPlan names a batched execution in a trace. A batch of one is an
// individual run, which is exactly how it is requested.
func batchMutationPlan(targets []TargetEvidence) string {
	if len(targets) == 1 {
		return mutationPlanIndividual + targets[0].Target.Name
	}
	return fmt.Sprintf("%s%s(%d)", mutationPlanBatch, targets[0].Target.Package, len(targets))
}

// mutationSeedRoute describes how a mutant was routed: the targets baseline
// coverage proves reach it, the evidence that decided them, the executions
// that routing planned for them, and the reason the plan is what it is. A
// mutant no target reaches has no coverage to route by and is left to its
// package suite, and a mutant whose reaching targets a proof discharged has
// coverage that reaches it and nothing left to run: its plan is empty rather
// than the package suite, because the suite would run the very tests the proof
// just ruled out.
//
// The reaching targets are named in the order they will be executed, which is
// cheapest first, and the plan follows the same order: the individual runs,
// the batches of related targets behind them, and the fuzzing that a
// surviving mutant falls through to last. The position is clamped because a
// trace records a position or nothing, never a negative one.
func mutationSeedRoute(mutant gomutants.Mutant, route mutationRoute, executions []mutationSeedExecution) trace.RouteRecord {
	record := trace.RouteRecord{
		MutantID: mutant.ID, Rule: mutant.Rule, Path: mutant.Path,
		Line: max(mutant.Line, 0), Column: max(mutant.Column, 0),
		Reason:      trace.ReasonCoverageReaching,
		Granularity: route.granularity, Fallback: route.fallback, FileCandidates: route.fileCandidates,
		Discharged: route.discharged,
	}
	if len(route.reaching) == 0 && len(route.discharged) == 0 {
		record.Plan, record.Reason = []string{mutationPlanPackageSuite}, trace.ReasonUnreached
		return record
	}
	if len(route.reaching) == 0 {
		return record
	}
	record.ReachingTargets = make([]string, len(route.reaching))
	for index, target := range route.reaching {
		record.ReachingTargets[index] = target.Target.ID
	}
	record.Plan = make([]string, 0, len(executions)+len(route.reaching))
	for _, execution := range executions {
		record.Plan = append(record.Plan, execution.plan)
	}
	for _, target := range route.reaching {
		if target.Target.Kind == goanalysis.KindFuzz {
			record.Plan = append(record.Plan, mutationPlanFuzz+target.Target.Name)
		}
	}
	return record
}

func (evaluation *MutationEvaluation) append(other MutationEvaluation) {
	evaluation.Evidence = append(evaluation.Evidence, other.Evidence...)
	evaluation.Findings = append(evaluation.Findings, other.Findings...)
	evaluation.Repairs = append(evaluation.Repairs, other.Repairs...)
	evaluation.Applied = evaluation.Applied || other.Applied
}

// mutationRoute is the decision routing reached for one mutant: the targets
// that will run it, the evidence granularity that decided them, the fallback
// that widened the decision if one did, and how many targets the file alone
// would have named. The last three are diagnostics; only the reaching set
// decides what runs.
type mutationRoute struct {
	reaching       []TargetEvidence
	discharged     []trace.Discharge
	granularity    string
	fallback       string
	fileCandidates int
}

// routeMutant chooses the targets that must run one mutant.
//
// Coverage blocks narrow the file the mutant lives in to the positions each
// target actually ran, so a target that ran another part of the same file is
// left out. The narrowing is abandoned, fail-closed, whenever the evidence
// cannot support it: a mutant whose position the catalog could not report is
// routed by file, and so is a position no instrumented block contains, which
// is a gap between the blocks cmd/cover cut rather than proof that nothing
// runs it. A target restored from a checkpoint carries no blocks and is kept
// for the whole file for the same reason.
//
// A position that instrumentation does describe and no target ran reaches
// nobody. That is not a fallback but the answer: the mutation lives in code
// the measured targets never execute, and the package suite settles it.
//
// A reaching set decided by block is then narrowed once more, by the proof the
// engine may attach to the mutation itself: see dischargeNarrowedBranch.
func routeMutant(mutant gomutants.Mutant, targets []TargetEvidence, instrumented []goanalysis.FileCoverage) mutationRoute {
	path := filepath.ToSlash(mutant.Path)
	candidates := make([]TargetEvidence, 0, len(targets))
	for _, target := range targets {
		if slices.Contains(target.CoveredFiles, path) {
			candidates = append(candidates, target)
		}
	}
	if mutant.Line <= 0 || mutant.Column <= 0 {
		return fileMutationRoute(candidates, trace.FallbackPositionUnknown)
	}
	blocks, _ := goanalysis.FindFileCoverage(instrumented, path)
	if !blocks.Contains(mutant.Line, mutant.Column) {
		return fileMutationRoute(candidates, trace.FallbackOutsideBlocks)
	}
	reaching := make([]TargetEvidence, 0, len(candidates))
	for _, target := range candidates {
		covered, _ := goanalysis.FindFileCoverage(target.Covered, path)
		if target.Covered == nil || covered.Contains(mutant.Line, mutant.Column) {
			reaching = append(reaching, target)
		}
	}
	kept, discharged := dischargeNarrowedBranch(mutant, orderReachingTargets(reaching), blocks, path)
	return mutationRoute{
		reaching: kept, discharged: discharged,
		granularity: trace.GranularityBlock, fileCandidates: len(candidates),
	}
}

// dischargeNarrowedBranch removes the targets a branch proof shows cannot
// observe the mutation, and names each of them with the proof that removed it.
//
// go-mutants attaches the proof to an edit that can only make the condition of
// an `if` or a `for` less often true, and reports the span of the body that
// condition gates. Write C for the original condition and C' for the mutated
// one: C' implies C, and the whole condition is inert, so a target during which
// no statement of the gated body ran evaluated C to false every time it was
// evaluated, evaluated C' to false there too, took the same branch on every
// evaluation, and therefore ran identically on both programs. Executing it
// against the mutant would establish nothing that has not already been
// established, so it is discharged instead of run.
//
// The proof is used only where it is a proof. A fuzz target explores inputs
// beyond the corpus its coverage was measured on, so its blocks do not bound
// what it will execute. A target restored from a checkpoint carries no blocks
// at all, and silence is not evidence of absence. Neither is the body itself:
// unless some instrumented block begins inside the span, the body was never
// measured, and every target's silence about it means nothing. Everything the
// proof does not cover is kept, which is what the run did before it existed.
func dischargeNarrowedBranch(mutant gomutants.Mutant, reaching []TargetEvidence, instrumented goanalysis.FileCoverage, path string) ([]TargetEvidence, []trace.Discharge) {
	span, proved := narrowedBranchSpan(mutant)
	if !proved || !instrumented.StartsWithin(span) {
		return reaching, nil
	}
	kept := make([]TargetEvidence, 0, len(reaching))
	var discharged []trace.Discharge
	for _, target := range reaching {
		covered, _ := goanalysis.FindFileCoverage(target.Covered, path)
		if target.Target.Kind == goanalysis.KindFuzz || target.Covered == nil || covered.StartsWithin(span) {
			kept = append(kept, target)
			continue
		}
		discharged = append(discharged, trace.Discharge{
			Target: target.Target.ID, Reason: trace.DischargeBranchNeverTaken,
		})
	}
	if len(discharged) == 0 {
		return reaching, nil
	}
	return kept, discharged
}

// narrowedBranchSpan is the gated body of a mutant's branch proof, when the
// proof is one routing may act on. Everything the span is claimed to be is
// checked before anything is concluded from it, because a discharge is a
// decision not to run a test: the coordinates are the 1-based positions of a
// source span, the body does not end before it starts, and the edit precedes
// the body it gates, which is what makes it the condition rather than part of
// the body. A proof that fails any of it discharges nothing.
func narrowedBranchSpan(mutant gomutants.Mutant) (goanalysis.CoverageSpan, bool) {
	proof := mutant.Branch
	if proof == nil {
		return goanalysis.CoverageSpan{}, false
	}
	span := goanalysis.CoverageSpan{
		StartLine: proof.BodyStartLine, StartColumn: proof.BodyStartColumn,
		EndLine: proof.BodyEndLine, EndColumn: proof.BodyEndColumn,
	}
	if span.StartLine < 1 || span.StartColumn < 1 || span.EndLine < 1 || span.EndColumn < 1 {
		return goanalysis.CoverageSpan{}, false
	}
	if span.EndLine < span.StartLine || span.EndLine == span.StartLine && span.EndColumn < span.StartColumn {
		return goanalysis.CoverageSpan{}, false
	}
	if mutant.Line > span.StartLine || mutant.Line == span.StartLine && mutant.Column >= span.StartColumn {
		return goanalysis.CoverageSpan{}, false
	}
	return span, true
}

// fileMutationRoute is the route taken when the blocks cannot decide: every
// target that ran the file runs the mutant, exactly as routing did before
// blocks were read at all.
func fileMutationRoute(candidates []TargetEvidence, fallback string) mutationRoute {
	return mutationRoute{
		reaching: orderReachingTargets(candidates), granularity: trace.GranularityFile,
		fallback: fallback, fileCandidates: len(candidates),
	}
}

// orderReachingTargets puts the reaching targets in the order they will run:
// the measured ones cheapest first, so that a kill is found for the least
// time, and the unmeasured ones behind them in the order they were given.
func orderReachingTargets(targets []TargetEvidence) []TargetEvidence {
	measured := make([]TargetEvidence, 0, len(targets))
	unmeasured := make([]TargetEvidence, 0)
	for _, target := range targets {
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
