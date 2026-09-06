// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package assure

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	gomutants "github.com/P4suta/go-mutants"
	goanalysis "github.com/P4suta/goatest/internal/golang"
	"github.com/P4suta/goatest/internal/report"
	"github.com/P4suta/goatest/internal/trace"
)

const (
	mutationPlanIndividual   = "individual:"
	mutationPlanBatch        = "batch:"
	mutationPlanPackageSuite = "package-suite"
	mutationPlanReused       = "reused"
)

const (
	mutationSurvivedSummary         = "all reaching tests passed with this mutation active"
	mutationFullyDischargedOpening  = "no reaching test was run: every one was discharged because "
	mutationPartlyDischargedOpening = mutationSurvivedSummary + "; %d more discharged without running because "

	mutationBranchDischargeClause    = "none takes the branch this mutation narrows"
	mutationInfectionDischargeClause = "none makes the mutated value differ"
	mutationMixedDischargeClause     = "%d take no branch this mutation narrows and %d never make the mutated value differ"

	mutationFullyDischargedSummary  = mutationFullyDischargedOpening + mutationBranchDischargeClause
	mutationPartlyDischargedSummary = mutationPartlyDischargedOpening + mutationBranchDischargeClause
)

const (
	mutationUnreachedSummary          = "no measured top-level target reached this mutation; its package suite survived"
	mutationSuiteUnreachedSummary     = "the measured package suite never reached this mutation"
	mutationSuiteUninfectedSummary    = "no measured top-level target reached this mutation; its measured package suite never made the mutated value differ"
	mutationSuiteTimeoutSummary       = "package suite exhausted its execution budget; the mutation outcome is unknown"
	mutationSuiteInconclusiveSummary  = "package suite could not establish an outcome for an unreached mutation"
	mutationSuiteControlTimeout       = "the original package suite timed out before the mutation could be evaluated"
	mutationSuiteControlFailure       = "the original package suite failed before the mutation could be evaluated"
	mutationTargetTimeoutSummary      = "compatible execution group exhausted its execution budget while this mutation was active"
	mutationTargetInconclusiveSummary = "compatible execution group could not establish a deterministic mutation outcome"
	mutationTargetControlTimeout      = "the exact original execution group timed out before the mutation could be evaluated"
	mutationTargetControlFailure      = "the exact original execution group failed before the mutation could be evaluated"
	mutationAggregateUnknownSummary   = "%d of %d compatible execution groups were inconclusive: %s"
	mutationControlUnavailable        = "no positive clean observation is available to bound mutation execution"
)

type MutationSession interface {
	Catalog() gomutants.Catalog
	Exec(context.Context, gomutants.ExecRequest) (gomutants.MutantResult, error)
	Probe(context.Context, gomutants.ProbeRequest) (gomutants.ProbeResult, error)
}

type TargetEvidence struct {
	Target       goanalysis.Target
	CoveredFiles []string
	Covered      []goanalysis.FileCoverage

	Instrumented []goanalysis.FileCoverage
	Environment  []string
	Duration     time.Duration

	ProbeDuration time.Duration

	WholeTree bool
	Probed    bool
	Infected  []uint32
}

func (evidence TargetEvidence) infects(index uint32) bool {
	if !evidence.Probed {
		return true
	}
	_, found := slices.BinarySearch(evidence.Infected, index)
	return found
}

type MutationOptions struct {
	ReplayMutantID string
	TestArgs       []string

	Timeout  time.Duration
	Jobs     int
	Accepted map[string]bool
	Progress func(completed, total int)
	Resume   map[string]MutationEvaluation

	Checkpoint      func(string, MutationEvaluation)
	OriginalControl func(context.Context, gomutants.ExecRequest) (gomutants.CommandResult, error)

	freshControl func(context.Context, gomutants.ExecRequest) (gomutants.CommandResult, error)

	Trace *trace.Recorder

	Instrumented []goanalysis.FileCoverage

	Evidence *MutationEvidence

	RepositoryObserver *RepositoryObserver

	SuiteProbes map[string]PackageProbeEvidence

	SuiteCoverage map[string]PackageSuiteCoverage

	SuiteEnvironment []string
}

type MutationEvaluation struct {
	Evidence   []report.Evidence
	Findings   []report.Finding
	Accounting report.MutantAccounting
	Mutants    []report.MutantDisposition

	Provenance string
}

func EvaluateMutations(ctx context.Context, session MutationSession, targets []TargetEvidence, options MutationOptions) (MutationEvaluation, error) {
	if session == nil {
		return MutationEvaluation{}, fmt.Errorf("goatest: nil mutation session")
	}
	options.freshControl = options.OriginalControl
	options.OriginalControl = memoizedOriginalControl(options.OriginalControl)
	catalog := session.Catalog()
	if err := validateMutationCatalog(catalog); err != nil {
		return MutationEvaluation{}, err
	}
	var evaluation MutationEvaluation
	mutants := make([]gomutants.Mutant, 0, len(catalog.Mutants))
	resumed := make(map[string]bool, len(options.Resume))

	resumedProvenance := make(map[string]string, len(options.Resume))
	replayPresent := options.ReplayMutantID == ""
	for _, mutant := range catalog.Mutants {
		if !mutant.Accepted || options.ReplayMutantID != "" && mutant.ID != options.ReplayMutantID {
			continue
		}
		replayPresent = true
		if saved, ok := options.Resume[mutant.ID]; ok {
			evaluation.append(saved)
			resumed[mutant.ID] = true
			if saved.Provenance != "" {
				resumedProvenance[mutant.ID] = saved.Provenance
			}
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
	}
	evaluation.Accounting, evaluation.Mutants = mutationAccounting(catalog, options.ReplayMutantID, evaluation, options.Evidence, resumedProvenance)
	return evaluation, nil
}

func checkpointMutation(options MutationOptions, id string, evaluation MutationEvaluation) {
	if options.Checkpoint != nil {
		options.Checkpoint(id, evaluation)
	}
}

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
		return cmp.Or(
			strings.Compare(a.id, b.id),
			strings.Compare(a.path, b.path),
			strings.Compare(a.pkg, b.pkg),
			strings.Compare(a.rule, b.rule),
			cmp.Compare(a.line, b.line),
		)
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

func mutationAccounting(catalog gomutants.Catalog, replayID string, evaluation MutationEvaluation, collected *MutationEvidence, resumedProvenance map[string]string) (report.MutantAccounting, []report.MutantDisposition) {
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
		if !selected[item.ID] {
			continue
		}
		if item.Kind != "mutation" {
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

		reused, provenance := collected.disposition(mutant.ID)
		if !reused {
			provenance = resumedProvenance[mutant.ID]
			reused = provenance != ""
		}
		dispositions = append(dispositions, report.MutantDisposition{
			ID: mutant.ID, Status: status, Path: mutant.Path, Line: mutant.Line,
			Package: mutant.Package, Rule: mutant.Rule, Detail: detail,
			Reused: reused, Provenance: provenance,
		})
		switch status {
		case report.MutantKilled:
			accounting.Killed++
			accounting.Executed++
			if reused {
				accounting.ReusedKilled++
			}
		case report.MutantSurvived:
			accounting.Survived++
			accounting.Executed++
			if reused {
				accounting.ReusedSurvived++
			}
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
	mutant   gomutants.Mutant
	reaching []TargetEvidence

	discharged []trace.Discharge
	evaluation MutationEvaluation
	resolved   bool
	err        error
}

type mutationSeedExecution struct {
	request gomutants.ExecRequest
	detail  string
	plan    string
	targets []TargetEvidence
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
	route = applySuiteCoverageRouting(mutant, route, options.SuiteCoverage)
	route = applyProbeRouting(mutant, targets, route, options.SuiteProbes)
	seed := mutationSeed{mutant: mutant, reaching: route.reaching, discharged: route.discharged}
	if killer, provenance, reused := options.Evidence.reuseKill(mutant, route); reused {
		options.Trace.Route(reusedMutationRoute(mutant, route))
		seed.evaluation.addKill(mutant, killer)
		seed.evaluation.Provenance = provenance
		seed.resolved = true
		return seed
	}
	if len(seed.reaching) == 0 && len(route.discharged) == 0 && route.suiteCoverage != "" && !route.suiteReached {
		options.Trace.Route(mutationSeedRoute(mutant, route, nil))
		seed.evaluation.addFinding(mutant, "unreached-mutant", mutationSuiteUnreachedSummary, options.Accepted)
		options.Evidence.recordUnreached(mutant, route.suiteWholeTree,
			"unreached-mutant", mutationSuiteUnreachedSummary)
		seed.resolved = true
		return seed
	}
	if len(seed.reaching) == 0 && len(route.discharged) == 0 && route.suiteProbe != "" && !route.suiteInfected {
		options.Trace.Route(mutationSeedRoute(mutant, route, nil))
		seed.evaluation.addFinding(mutant, "unreached-mutant", mutationSuiteUninfectedSummary, options.Accepted)
		options.Evidence.recordUnreached(mutant, route.suiteWholeTree,
			"unreached-mutant", mutationSuiteUninfectedSummary)
		seed.resolved = true
		return seed
	}
	if finding, provenance, reused := options.Evidence.reuseVerdict(mutant, route); reused {
		options.Trace.Route(reusedMutationRoute(mutant, route))
		seed.evaluation.addFinding(mutant, finding.Kind, finding.Summary, options.Accepted)
		seed.evaluation.Provenance = provenance
		seed.resolved = true
		return seed
	}
	if len(seed.reaching) == 0 && len(route.discharged) > 0 {
		options.Trace.Route(mutationSeedRoute(mutant, route, nil))
		seed.evaluation.addFinding(mutant, "surviving-mutant", mutationDischargedSummary(route.discharged), options.Accepted)
		seed.resolved = true
		return seed
	}
	if len(seed.reaching) == 0 {
		options.Trace.Route(mutationSeedRoute(mutant, route, nil))
		request := gomutants.ExecRequest{
			Mutant: mutant.ID, Package: mutant.Package, Args: slices.Clone(options.TestArgs),
			Env:     slices.Clone(options.SuiteEnvironment),
			Timeout: mutationExecutionTimeout(options.Timeout, route.suiteDuration),
		}
		var finding controlFinding
		var controlErr error
		var control time.Duration
		request, control, finding, controlErr = prepareMutationRequest(
			ctx, mutant, request, options,
			"mutation-control-failure", mutationSuiteControlFailure,
			"mutation-control-timeout", mutationSuiteControlTimeout,
		)
		if controlErr != nil {
			seed.err = fmt.Errorf("goatest: original package-suite control for mutant %s: %w", mutant.DisplayID, controlErr)
			return seed
		}
		if finding.kind != "" {
			seed.evaluation.addFinding(mutant, finding.kind, finding.summary, options.Accepted)
			seed.resolved = true
			return seed
		}
		result, observation, err := executeMutationUnderMeasuredBudget(ctx, session, request, control, options)
		if err != nil {
			seed.err = fmt.Errorf("goatest: execute unreached mutant %s: %w", mutant.DisplayID, err)
			return seed
		}
		suiteWholeTree := options.RepositoryObserver.wholeTreeSuite(mutant.Package, observation)
		switch result.Outcome {
		case gomutants.OutcomeKilled:
			seed.evaluation.addKill(mutant, mutant.Package+" package suite")
		case gomutants.OutcomeSurvived:
			seed.evaluation.addFinding(mutant, "unreached-mutant", mutationUnreachedSummary, options.Accepted)

			options.Evidence.recordUnreached(mutant, suiteWholeTree,
				"unreached-mutant", mutationUnreachedSummary)
		case gomutants.OutcomeTimedOut:
			seed.evaluation.addFinding(mutant, "mutation-timeout", mutationSuiteTimeoutSummary, options.Accepted)
		case gomutants.OutcomeInconclusive:
			seed.evaluation.addFinding(mutant, "mutation-inconclusive", mutationSuiteInconclusiveSummary, options.Accepted)
		case gomutants.OutcomeErrored, gomutants.OutcomeNotRun:
			seed.err = mutationOutcomeProtocolError(mutant, result.Outcome)
		default:
			seed.err = fmt.Errorf("goatest: mutant %s returned unknown outcome %q", mutant.DisplayID, result.Outcome)
		}
		seed.resolved = true
		return seed
	}
	executions := mutationSeedExecutions(mutant, seed.reaching, options)
	options.Trace.Route(mutationSeedRoute(mutant, route, executions))

	executed := make([]TargetEvidence, 0, len(seed.reaching))
	unknown := make([]mutationGroupUnknown, 0)
	for _, execution := range executions {
		var finding controlFinding
		var controlErr error
		var control time.Duration
		execution.request, control, finding, controlErr = prepareMutationRequest(
			ctx, mutant, execution.request, options,
			"mutation-control-failure", mutationTargetControlFailure,
			"mutation-control-timeout", mutationTargetControlTimeout,
		)
		if controlErr != nil {
			seed.err = controlErr
			return seed
		}
		if finding.kind != "" {
			unknown = append(unknown, mutationGroupUnknown{detail: execution.detail, finding: finding})
			continue
		}
		result, observation, err := executeMutationUnderMeasuredBudget(ctx, session, execution.request, control, options)
		if err != nil {
			seed.err = fmt.Errorf("goatest: execute mutant %s with %s: %w", mutant.DisplayID, execution.detail, err)
			return seed
		}
		observedTargets := applyRepositoryObservation(execution.targets, observation, options.RepositoryObserver)
		switch result.Outcome {
		case gomutants.OutcomeKilled:
			seed.evaluation.addKill(mutant, execution.detail)
			options.Evidence.recordKill(mutant, observedTargets)
			seed.resolved = true
			return seed
		case gomutants.OutcomeSurvived:

			executed = append(executed, observedTargets...)
		case gomutants.OutcomeTimedOut:
			unknown = append(unknown, mutationGroupUnknown{detail: execution.detail, finding: controlFinding{
				kind: "mutation-timeout", summary: mutationTargetTimeoutSummary,
			}})
		case gomutants.OutcomeInconclusive:
			unknown = append(unknown, mutationGroupUnknown{detail: execution.detail, finding: controlFinding{
				kind: "mutation-inconclusive", summary: mutationTargetInconclusiveSummary,
			}})
		case gomutants.OutcomeErrored, gomutants.OutcomeNotRun:
			seed.err = mutationOutcomeProtocolError(mutant, result.Outcome)
			return seed
		default:
			seed.err = fmt.Errorf("goatest: mutant %s returned unknown outcome %q", mutant.DisplayID, result.Outcome)
			return seed
		}
	}
	if len(unknown) != 0 {
		finding := aggregateMutationUnknown(unknown, len(executions))
		seed.evaluation.addFinding(mutant, finding.kind, finding.summary, options.Accepted)
		seed.resolved = true
		return seed
	}

	summary := mutationSurvivalSummary(seed.discharged)
	seed.evaluation.addFinding(mutant, "surviving-mutant", summary, options.Accepted)
	options.Evidence.recordSurvived(mutant, executed, "surviving-mutant", summary)
	seed.resolved = true
	return seed
}

type mutationDischargeCounts struct {
	branch    int
	infection int
}

func countMutationDischarges(discharged []trace.Discharge) mutationDischargeCounts {
	var counts mutationDischargeCounts
	for _, discharge := range discharged {
		if discharge.Reason == trace.DischargeNeverInfected {
			counts.infection++
			continue
		}
		counts.branch++
	}
	return counts
}

func (counts mutationDischargeCounts) total() int { return counts.branch + counts.infection }

func (counts mutationDischargeCounts) clause() string {
	switch {
	case counts.infection == 0:
		return mutationBranchDischargeClause
	case counts.branch == 0:
		return mutationInfectionDischargeClause
	default:
		return fmt.Sprintf(mutationMixedDischargeClause, counts.branch, counts.infection)
	}
}

func mutationSurvivalSummary(discharged []trace.Discharge) string {
	counts := countMutationDischarges(discharged)
	switch {
	case counts.total() == 0:
		return mutationSurvivedSummary
	case counts.infection == 0:
		return fmt.Sprintf(mutationPartlyDischargedSummary, counts.total())
	default:
		return fmt.Sprintf(mutationPartlyDischargedOpening, counts.total()) + counts.clause()
	}
}

func mutationDischargedSummary(discharged []trace.Discharge) string {
	counts := countMutationDischarges(discharged)
	if counts.infection == 0 {
		return mutationFullyDischargedSummary
	}
	return mutationFullyDischargedOpening + counts.clause()
}

type controlFinding struct {
	kind    string
	summary string
}

type mutationGroupUnknown struct {
	detail  string
	finding controlFinding
}

func aggregateMutationUnknown(unknown []mutationGroupUnknown, total int) controlFinding {
	if len(unknown) == 1 {
		return unknown[0].finding
	}
	details := make([]string, len(unknown))
	for index, observation := range unknown {
		details[index] = observation.detail + ": " + observation.finding.summary
	}
	return controlFinding{
		kind:    "mutation-inconclusive",
		summary: fmt.Sprintf(mutationAggregateUnknownSummary, len(unknown), total, strings.Join(details, "; ")),
	}
}

func mutationOutcomeProtocolError(mutant gomutants.Mutant, outcome gomutants.Outcome) error {
	return fmt.Errorf("goatest: mutant %s returned %q without an execution error", mutant.DisplayID, outcome)
}

type controlOutcome struct {
	once   sync.Once
	result gomutants.CommandResult
	err    error
}

func memoizedOriginalControl(control func(context.Context, gomutants.ExecRequest) (gomutants.CommandResult, error)) func(context.Context, gomutants.ExecRequest) (gomutants.CommandResult, error) {
	if control == nil {
		return nil
	}
	var mutex sync.Mutex
	outcomes := make(map[string]*controlOutcome)
	return func(ctx context.Context, request gomutants.ExecRequest) (gomutants.CommandResult, error) {
		key := request.Package + "\x00" + strings.Join(request.Args, "\x00") + "\x00" + strings.Join(request.Env, "\x00") + "\x00" + strconv.FormatInt(int64(request.Timeout), 10)
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

func prepareMutationRequest(
	ctx context.Context,
	mutant gomutants.Mutant,
	request gomutants.ExecRequest,
	options MutationOptions,
	failureKind, failureSummary, timeoutKind, timeoutSummary string,
) (gomutants.ExecRequest, time.Duration, controlFinding, error) {
	unavailable := controlFinding{
		kind: "mutation-control-unavailable", summary: mutationControlUnavailable,
	}
	if options.OriginalControl == nil || request.Timeout <= 0 {
		return request, 0, unavailable, nil
	}
	controlRequest := request
	controlRequest.Timeout = options.Timeout
	control, err := runOriginalControl(ctx, controlRequest, options)
	if err != nil {
		return gomutants.ExecRequest{}, 0, controlFinding{},
			fmt.Errorf("goatest: original budget control for mutant %s: %w", mutant.DisplayID, err)
	}
	if control.TimedOut {
		return request, 0, controlFinding{kind: timeoutKind, summary: timeoutSummary}, nil
	}
	if control.ExitCode != 0 {
		summary := failureSummary
		if output := summarize(control.Output); output != "no output" {
			summary += ": " + output
		}
		return request, 0, controlFinding{kind: failureKind, summary: summary}, nil
	}
	request.Timeout = mutationExecutionTimeout(options.Timeout, request.Timeout, control.Duration)
	if request.Timeout <= 0 {
		return request, 0, unavailable, nil
	}
	return request, control.Duration, controlFinding{}, nil
}

func executeMutationUnderMeasuredBudget(
	ctx context.Context,
	session MutationSession,
	request gomutants.ExecRequest,
	control time.Duration,
	options MutationOptions,
) (gomutants.MutantResult, repositoryObservation, error) {
	result, observation, err := executeMutation(ctx, session, request, options)
	if err != nil || result.Outcome != gomutants.OutcomeTimedOut {
		return result, observation, err
	}
	widened, slower := budgetAfterMeasuredSlowdown(ctx, request, control, options)
	if !slower {
		return result, observation, nil
	}
	request.Timeout = widened
	return executeMutation(ctx, session, request, options)
}

func budgetAfterMeasuredSlowdown(
	ctx context.Context,
	request gomutants.ExecRequest,
	control time.Duration,
	options MutationOptions,
) (time.Duration, bool) {
	if options.freshControl == nil || control <= 0 || request.Timeout <= 0 {
		return 0, false
	}
	controlRequest := request
	controlRequest.Timeout = options.Timeout
	fresh, err := options.freshControl(ctx, controlRequest)
	if err != nil || fresh.TimedOut || fresh.ExitCode != 0 || fresh.Duration <= 0 {
		return 0, false
	}
	widened := max(
		mutationExecutionTimeout(options.Timeout, request.Timeout, fresh.Duration),
		scaledMutationTimeout(options.Timeout, request.Timeout, control, fresh.Duration),
	)
	if widened <= request.Timeout {
		return 0, false
	}
	return widened, true
}

func scaledMutationTimeout(limit, budget, before, after time.Duration) time.Duration {
	if budget <= 0 || before <= 0 || after <= before {
		return budget
	}
	maximum := time.Duration(math.MaxInt64)
	scaled := maximum
	if budget <= maximum/after {
		scaled = budget * after / before
	}
	if limit > 0 && scaled > limit {
		return limit
	}
	return scaled
}

func executeMutation(ctx context.Context, session MutationSession, request gomutants.ExecRequest, options MutationOptions) (gomutants.MutantResult, repositoryObservation, error) {
	instrumented := request
	var finish func() repositoryObservation
	instrumented.Args, finish = options.RepositoryObserver.instrumentPackage(request.Package, request.Args)
	result, err := session.Exec(ctx, instrumented)
	observation := finish()
	if err == nil && repositoryTestLogFailure(result.OutputTail, instrumented.Args) {
		observation = repositoryObservation{reason: wholeTreeLogUnavailable}
		err = fmt.Errorf("goatest: repository observation for mutant %s failed", request.Mutant)
	}
	options.Trace.MutantExec(mutantExecutionRecord(request, result, err))
	return result, observation, err
}

func runOriginalControl(ctx context.Context, request gomutants.ExecRequest, options MutationOptions) (gomutants.CommandResult, error) {
	return options.OriginalControl(ctx, request)
}

func applyRepositoryObservation(targets []TargetEvidence, observation repositoryObservation, observer *RepositoryObserver) []TargetEvidence {
	result := slices.Clone(targets)
	for index := range result {
		result[index].WholeTree = result[index].WholeTree || observer.wholeTree(result[index].Target, observation)
	}
	return result
}

func mutationSeedExecutions(mutant gomutants.Mutant, targets []TargetEvidence, options MutationOptions) []mutationSeedExecution {
	groups := mutationTargetGroups(targets)
	executions := make([]mutationSeedExecution, 0, len(groups))
	for _, group := range groups {
		executions = append(executions, mutationExecutionForTargets(mutant, group, options))
	}
	return executions
}

func mutationPlanningDuration(target TargetEvidence) time.Duration {
	if target.ProbeDuration > 0 {
		return target.ProbeDuration
	}
	return target.Duration
}

func mutationExecutionForTargets(mutant gomutants.Mutant, targets []TargetEvidence, options MutationOptions) mutationSeedExecution {
	request := batchSeedRequest(mutant, targets, aggregateMutationTimeout(targets, options))
	request.Args = append(request.Args, options.TestArgs...)
	return mutationSeedExecution{
		request: request, detail: batchMutationDetail(targets),
		plan: batchMutationPlan(targets), targets: targets,
	}
}

func mutationTargetGroups(targets []TargetEvidence) [][]TargetEvidence {
	groups := make([][]TargetEvidence, 0)
	byExecutionEnvironment := make(map[string]int)
	for _, target := range targets {
		key := mutationExecutionEnvironment(target)
		index, ok := byExecutionEnvironment[key]
		if !ok {
			index = len(groups)
			byExecutionEnvironment[key] = index
			groups = append(groups, nil)
		}
		groups[index] = append(groups[index], target)
	}
	for _, group := range groups {
		slices.SortFunc(group, compareMutationGroupTargets)
	}
	slices.SortFunc(groups, func(first, second []TargetEvidence) int {
		if order := cmp.Compare(mutationGroupPlanningDuration(first), mutationGroupPlanningDuration(second)); order != 0 {
			return order
		}
		return strings.Compare(mutationExecutionEnvironment(first[0]), mutationExecutionEnvironment(second[0]))
	})
	return groups
}

func compareMutationGroupTargets(first, second TargetEvidence) int {
	if order := strings.Compare(first.Target.Package, second.Target.Package); order != 0 {
		return order
	}
	if order := strings.Compare(first.Target.Name, second.Target.Name); order != 0 {
		return order
	}
	if order := strings.Compare(string(first.Target.Kind), string(second.Target.Kind)); order != 0 {
		return order
	}
	return strings.Compare(first.Target.ID, second.Target.ID)
}

func mutationGroupPlanningDuration(targets []TargetEvidence) time.Duration {
	var duration time.Duration
	for _, target := range targets {
		duration = saturatingDurationSum(duration, mutationPlanningDuration(target))
	}
	return duration
}

func mutationExecutionEnvironment(target TargetEvidence) string {
	return target.Target.Package + "\x00" + strings.Join(targetBehaviorEnvironment(nil, target.Environment), "\x00")
}

func batchMutationControlDurations(targets []TargetEvidence) (time.Duration, time.Duration) {
	var baseline, probe time.Duration
	for _, target := range targets {
		baseline = saturatingDurationSum(baseline, target.Duration)
		probe = saturatingDurationSum(probe, target.ProbeDuration)
	}
	return baseline, probe
}

func aggregateMutationTimeout(targets []TargetEvidence, options MutationOptions) time.Duration {
	baseline, probe := batchMutationControlDurations(targets)
	targetDeadline := mutationExecutionTimeout(options.Timeout, baseline, probe)
	pkg := targets[0].Target.Package
	var suiteSamples []time.Duration
	if suite, measured := options.SuiteCoverage[pkg]; measured && suite.Duration > 0 {
		suiteSamples = append(suiteSamples, suite.Duration)
	}
	if suite, measured := options.SuiteProbes[pkg]; measured && suite.Measured && suite.Duration > 0 {
		suiteSamples = append(suiteSamples, suite.Duration)
	}
	if len(suiteSamples) == 0 {
		return targetDeadline
	}
	suiteDeadline := mutationExecutionTimeout(options.Timeout, suiteSamples...)
	return mutationExecutionTimeout(options.Timeout, targetDeadline, suiteDeadline)
}

func saturatingDurationSum(total, duration time.Duration) time.Duration {
	if duration <= 0 {
		return total
	}
	maximum := time.Duration(math.MaxInt64)
	if duration > maximum-total {
		return maximum
	}
	return total + duration
}

func batchMutationDetail(targets []TargetEvidence) string {
	if len(targets) == 1 {
		return targets[0].Target.Name
	}
	return fmt.Sprintf("%s (%d related targets)", targets[0].Target.Package, len(targets))
}

func batchMutationPlan(targets []TargetEvidence) string {
	if len(targets) == 1 {
		return mutationPlanIndividual + targets[0].Target.Name
	}
	return fmt.Sprintf("%s%s(%d)", mutationPlanBatch, targets[0].Target.Package, len(targets))
}

func mutationSeedRoute(mutant gomutants.Mutant, route mutationRoute, executions []mutationSeedExecution) trace.RouteRecord {
	record := trace.RouteRecord{
		MutantID: mutant.ID, Rule: mutant.Rule, Path: mutant.Path,
		Line: max(mutant.Line, 0), Column: max(mutant.Column, 0),
		Reason:      trace.ReasonCoverageReaching,
		Granularity: route.granularity, Fallback: route.fallback, FileCandidates: route.fileCandidates,
		Discharged: route.discharged, ProbeReaching: slices.Clone(route.probeReaching),
		SuiteCoverage: route.suiteCoverage, SuiteReached: route.suiteReached,
		SuiteProbe: route.suiteProbe, Probed: mutant.Probed,
	}
	if len(route.reaching) == 0 && len(route.discharged) == 0 {
		if (route.suiteCoverage == "" || route.suiteReached) &&
			(route.suiteProbe == "" || route.suiteInfected) {
			record.Plan = []string{mutationPlanPackageSuite}
		}
		record.Reason = trace.ReasonUnreached
		return record
	}
	if len(route.reaching) == 0 {
		return record
	}
	record.ReachingTargets = make([]string, len(route.reaching))
	for index, target := range route.reaching {
		record.ReachingTargets[index] = target.Target.ID
	}
	if len(route.probeReaching) != 0 {
		record.Reason = trace.ReasonProbeReaching
	}
	record.Plan = make([]string, 0, len(executions))
	for _, execution := range executions {
		record.Plan = append(record.Plan, execution.plan)
	}
	return record
}

func reusedMutationRoute(mutant gomutants.Mutant, route mutationRoute) trace.RouteRecord {
	record := mutationSeedRoute(mutant, route, nil)
	record.Plan = []string{mutationPlanReused}
	record.Reused = true
	return record
}

func (evaluation *MutationEvaluation) append(other MutationEvaluation) {
	evaluation.Evidence = append(evaluation.Evidence, other.Evidence...)
	evaluation.Findings = append(evaluation.Findings, other.Findings...)
}

type mutationRoute struct {
	reaching       []TargetEvidence
	discharged     []trace.Discharge
	probeReaching  []string
	suiteCoverage  string
	suiteReached   bool
	suiteProbe     string
	suiteInfected  bool
	suiteDuration  time.Duration
	suiteWholeTree bool
	granularity    string
	fallback       string
	fileCandidates int
}

func applySuiteCoverageRouting(mutant gomutants.Mutant, route mutationRoute, suites map[string]PackageSuiteCoverage) mutationRoute {
	if len(route.reaching) != 0 || len(route.discharged) != 0 {
		return route
	}
	suite, measured := suites[mutant.Package]
	if !measured {
		return route
	}
	route.suiteDuration = suite.Duration
	if mutant.Line <= 0 || mutant.Column <= 0 {
		return route
	}
	path := filepath.ToSlash(mutant.Path)
	instrumented, _ := goanalysis.FindFileCoverage(suite.Instrumented, path)
	if !instrumented.Contains(mutant.Line, mutant.Column) {
		return route
	}
	covered, _ := goanalysis.FindFileCoverage(suite.Covered, path)
	route.suiteCoverage = packageSuiteCoverageTarget(mutant.Package)
	route.suiteReached = covered.Contains(mutant.Line, mutant.Column)
	route.suiteWholeTree = suite.WholeTree
	return route
}

func neededProbeSuitePackages(catalog gomutants.Catalog, targets []TargetEvidence, instrumented []goanalysis.FileCoverage, suites map[string]PackageSuiteCoverage) []string {
	needed := make(map[string]bool)
	for _, mutant := range catalog.Mutants {
		if !mutant.Accepted || !mutant.Probed || mutant.Package == "" {
			continue
		}
		route := routeMutant(mutant, targets, instrumented)
		if len(route.reaching) != 0 || len(route.discharged) != 0 {
			continue
		}
		route = applySuiteCoverageRouting(mutant, route, suites)
		if route.suiteCoverage != "" && !route.suiteReached {
			continue
		}
		needed[mutant.Package] = true
	}
	packages := make([]string, 0, len(needed))
	for pkg := range needed {
		packages = append(packages, pkg)
	}
	slices.Sort(packages)
	return packages
}

func applyProbeRouting(mutant gomutants.Mutant, targets []TargetEvidence, route mutationRoute, suites map[string]PackageProbeEvidence) mutationRoute {
	coverageEmpty := len(route.reaching) == 0 && len(route.discharged) == 0
	if coverageEmpty && mutant.Probed {
		if suite, measured := suites[mutant.Package]; measured && suite.Measured {
			route.suiteProbe = packageSuiteProbeTarget(mutant.Package)
			route.suiteInfected = packageProbeInfects(suite, mutant.Index)
			if route.suiteDuration == 0 {
				route.suiteDuration = suite.Duration
			}
			route.suiteWholeTree = suite.WholeTree
		}
	}
	if route.suiteCoverage != "" && !route.suiteReached && route.suiteInfected {
		route.suiteCoverage = ""
	}

	checkPositiveCounterexample := route.suiteCoverage != "" && !route.suiteReached
	suitePositivelyReached := route.suiteProbe == "" && route.suiteCoverage != "" && route.suiteReached
	if coverageEmpty && !checkPositiveCounterexample && (suitePositivelyReached || route.suiteInfected) {
		return route
	}
	known := make(map[string]bool, len(route.reaching)+len(route.discharged))
	for _, target := range route.reaching {
		known[target.Target.ID] = true
	}
	for _, discharge := range route.discharged {
		known[discharge.Target] = true
	}
	for _, target := range targets {
		if known[target.Target.ID] || !mutant.Probed || !target.Probed || !target.infects(mutant.Index) {
			continue
		}
		route.reaching = append(route.reaching, target)
		route.probeReaching = append(route.probeReaching, target.Target.ID)
		known[target.Target.ID] = true
	}
	if len(route.probeReaching) != 0 {
		route.reaching = orderReachingTargets(mutant, route.reaching)
	}
	return route
}

func packageProbeInfects(evidence PackageProbeEvidence, index uint32) bool {
	return slices.Contains(evidence.Infected, index)
}

func routeMutant(mutant gomutants.Mutant, targets []TargetEvidence, instrumented []goanalysis.FileCoverage) mutationRoute {
	path := filepath.ToSlash(mutant.Path)
	candidates := make([]TargetEvidence, 0, len(targets))
	for _, target := range targets {
		if slices.Contains(target.CoveredFiles, path) {
			candidates = append(candidates, target)
		}
	}
	if mutant.Line <= 0 || mutant.Column <= 0 {
		return fileMutationRoute(mutant, candidates, trace.FallbackPositionUnknown)
	}
	blocks, _ := goanalysis.FindFileCoverage(instrumented, path)
	if !blocks.Contains(mutant.Line, mutant.Column) {
		return fileMutationRoute(mutant, candidates, trace.FallbackOutsideBlocks)
	}
	reaching := make([]TargetEvidence, 0, len(candidates))
	for _, target := range candidates {
		covered, _ := goanalysis.FindFileCoverage(target.Covered, path)
		if target.Covered == nil || covered.Contains(mutant.Line, mutant.Column) {
			reaching = append(reaching, target)
		}
	}
	kept, discharged := dischargeReachingTargets(mutant, orderReachingTargets(mutant, reaching), blocks, path)
	return mutationRoute{
		reaching: kept, discharged: discharged,
		granularity: trace.GranularityBlock, fileCandidates: len(candidates),
	}
}

func dischargeReachingTargets(mutant gomutants.Mutant, ordered []TargetEvidence, instrumented goanalysis.FileCoverage, path string) ([]TargetEvidence, []trace.Discharge) {
	unbranched, branch := dischargeNarrowedBranch(mutant, ordered, instrumented, path)
	kept, infection := dischargeNeverInfected(mutant, unbranched)
	if len(branch) == 0 && len(infection) == 0 {
		return ordered, nil
	}
	proofs := make(map[string]string, len(branch)+len(infection))
	for _, discharge := range slices.Concat(branch, infection) {
		proofs[discharge.Target] = discharge.Reason
	}
	discharged := make([]trace.Discharge, 0, len(proofs))
	for _, target := range ordered {
		if reason, removed := proofs[target.Target.ID]; removed {
			discharged = append(discharged, trace.Discharge{Target: target.Target.ID, Reason: reason})
		}
	}
	return kept, discharged
}

func dischargeNeverInfected(mutant gomutants.Mutant, reaching []TargetEvidence) ([]TargetEvidence, []trace.Discharge) {
	if !mutant.Probed {
		return reaching, nil
	}
	kept := make([]TargetEvidence, 0, len(reaching))
	var discharged []trace.Discharge
	for _, target := range reaching {
		if target.infects(mutant.Index) {
			kept = append(kept, target)
			continue
		}
		discharged = append(discharged, trace.Discharge{
			Target: target.Target.ID, Reason: trace.DischargeNeverInfected,
		})
	}
	if len(discharged) == 0 {
		return reaching, nil
	}
	return kept, discharged
}

func dischargeNarrowedBranch(mutant gomutants.Mutant, reaching []TargetEvidence, instrumented goanalysis.FileCoverage, path string) ([]TargetEvidence, []trace.Discharge) {
	span, proved := narrowedBranchSpan(mutant)
	if !proved || !instrumented.StartsWithin(span) {
		return reaching, nil
	}
	kept := make([]TargetEvidence, 0, len(reaching))
	var discharged []trace.Discharge
	for _, target := range reaching {
		covered, _ := goanalysis.FindFileCoverage(target.Covered, path)
		if target.Covered == nil || covered.StartsWithin(span) {
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

func fileMutationRoute(mutant gomutants.Mutant, candidates []TargetEvidence, fallback string) mutationRoute {
	return mutationRoute{
		reaching: orderReachingTargets(mutant, candidates), granularity: trace.GranularityFile,
		fallback: fallback, fileCandidates: len(candidates),
	}
}

func orderReachingTargets(mutant gomutants.Mutant, targets []TargetEvidence) []TargetEvidence {
	measured := make([]TargetEvidence, 0, len(targets))
	unmeasured := make([]TargetEvidence, 0)
	for _, target := range targets {
		if mutationPlanningDuration(target) > 0 {
			measured = append(measured, target)
		} else {
			unmeasured = append(unmeasured, target)
		}
	}
	slices.SortStableFunc(measured, func(first, second TargetEvidence) int {
		return compareMutationWitnesses(mutant, first, second)
	})
	return append(measured, unmeasured...)
}

func compareMutationWitnesses(mutant gomutants.Mutant, first, second TargetEvidence) int {
	if mutant.Probed && first.Probed != second.Probed {
		if first.Probed {
			return -1
		}
		return 1
	}
	if mutant.Probed && first.Probed {
		if order := cmp.Compare(len(first.Infected), len(second.Infected)); order != 0 {
			return order
		}
	}
	if order := cmp.Compare(mutationPlanningDuration(first), mutationPlanningDuration(second)); order != 0 {
		return order
	}
	return 0
}

func seedRequest(mutant gomutants.Mutant, target TargetEvidence, timeout time.Duration) gomutants.ExecRequest {
	return gomutants.ExecRequest{
		Mutant: mutant.ID, Package: target.Target.Package,
		Args: []string{targetRunArgument(target)},
		Env:  slices.Clone(target.Environment), Timeout: timeout,
	}
}

func targetRunArgument(target TargetEvidence) string {
	return "-test.run=^" + regexp.QuoteMeta(target.Target.Name) + "$"
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

func mutationExecutionTimeout(limit time.Duration, samples ...time.Duration) time.Duration {
	var timeout time.Duration
	for _, sample := range samples {
		timeout = saturatingDurationSum(timeout, sample)
		if limit > 0 && timeout >= limit {
			return limit
		}
	}
	return timeout
}

func controlExecutionTimeout(limit time.Duration, samples ...time.Duration) time.Duration {
	timeout := mutationExecutionTimeout(limit, samples...)
	if timeout == 0 {
		return limit
	}
	return timeout
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
