// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package report

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// Validate checks the fail-closed invariants of the public report contract.
// It intentionally does not require invocation metadata; use
// ValidateForPersistence at the durable report boundary.
func Validate(input Report) error {
	if input.Schema != "" && input.Schema != SchemaV1 {
		return fmt.Errorf("goatest: report schema %q is unsupported", input.Schema)
	}
	if !knownVerdict(input.Verdict) {
		return fmt.Errorf("goatest: report verdict %q is unknown", input.Verdict)
	}
	if err := validateCount("targets", input.Accounting.Targets); err != nil {
		return err
	}
	if err := validateCount("race", input.Accounting.Race); err != nil {
		return err
	}
	if err := validateTargets(input.Targets); err != nil {
		return err
	}
	if input.Resume != nil {
		if input.Resume.Attempts < 1 || input.Resume.ReusedTargets < 0 || input.Resume.ReusedRacePackages < 0 || input.Resume.ReusedMutants < 0 {
			return errors.New("goatest: resume metadata contains an invalid count")
		}
		if input.Resume.ReusedTargets > input.Accounting.Targets.Selected || input.Resume.ReusedRacePackages > input.Accounting.Race.Selected || input.Resume.ReusedMutants > input.Accounting.Mutants.Selected {
			return errors.New("goatest: resume metadata exceeds selected work")
		}
	}
	if err := validateMutants(input.Accounting.Mutants, input.Mutants, input.Verdict); err != nil {
		return err
	}
	for _, item := range input.Evidence {
		if item.Kind == "mutation" && item.Status == "compile-equivalent" {
			return errors.New("goatest: mutation status compile-equivalent is invalid; use compile-rejected")
		}
	}
	if err := validateAcceptances(input); err != nil {
		return err
	}
	return validateVerdictScope(input)
}

func validateTargets(targets []TargetDisposition) error {
	seen := make(map[string]struct{}, len(targets))
	for _, target := range targets {
		if strings.TrimSpace(target.ID) == "" || strings.TrimSpace(target.Name) == "" || strings.TrimSpace(target.Kind) == "" || strings.TrimSpace(target.Package) == "" || strings.TrimSpace(target.Status) == "" {
			return errors.New("goatest: target inventory contains an incomplete identity")
		}
		if target.Line < 0 || target.DurationMS < 0 {
			return fmt.Errorf("goatest: target %s contains a negative line or duration", target.ID)
		}
		if _, duplicate := seen[target.ID]; duplicate {
			return fmt.Errorf("goatest: target inventory contains duplicate ID %s", target.ID)
		}
		seen[target.ID] = struct{}{}
	}
	return nil
}

func validateAcceptances(input Report) error {
	accepted := make(map[string]struct{}, len(input.Acceptances))
	for _, item := range input.Acceptances {
		if strings.TrimSpace(item.ID) == "" || strings.TrimSpace(item.Reason) == "" {
			return errors.New("goatest: report acceptance requires id and reason")
		}
		if _, err := time.Parse(time.RFC3339, item.Expires); err != nil {
			return fmt.Errorf("goatest: report acceptance %q expiry: %w", item.ID, err)
		}
		if _, duplicate := accepted[item.ID]; duplicate {
			return fmt.Errorf("goatest: report acceptance %q is duplicated", item.ID)
		}
		accepted[item.ID] = struct{}{}
	}
	for _, item := range input.Evidence {
		if item.Kind != "mutation" || item.Status != "accepted" {
			continue
		}
		if _, exists := accepted[item.Detail]; !exists {
			return fmt.Errorf("goatest: accepted mutation %q has no acceptance metadata for %q", item.ID, item.Detail)
		}
	}
	for _, mutant := range input.Mutants {
		if mutant.Status != MutantAccepted {
			continue
		}
		if _, exists := accepted[mutant.Detail]; !exists {
			return fmt.Errorf("goatest: accepted mutant %q has no acceptance metadata for %q", mutant.ID, mutant.Detail)
		}
	}
	return nil
}

// ValidateForPersistence requires the audit metadata that every completed run
// must carry before it can replace a latest index.
func ValidateForPersistence(input Report) error {
	if err := Validate(input); err != nil {
		return err
	}
	if input.Schema != SchemaV1 {
		return fmt.Errorf("goatest: persisted report schema %q: expected %s", input.Schema, SchemaV1)
	}
	if input.RunID == "" {
		return errors.New("goatest: persisted report is missing run_id")
	}
	if input.RunKind == "" {
		return errors.New("goatest: persisted report is missing run_kind")
	}
	if !knownRunKind(input.RunKind) {
		return fmt.Errorf("goatest: persisted report run_kind %q is unknown", input.RunKind)
	}
	if input.Verdict == "" {
		return errors.New("goatest: persisted report is missing verdict")
	}
	if strings.TrimSpace(input.Contract) == "" {
		return errors.New("goatest: persisted report is missing contract")
	}
	if strings.TrimSpace(input.Snapshot) == "" {
		return errors.New("goatest: persisted report is missing snapshot identity")
	}
	if input.Scope.Requested.Kind == "" || input.Scope.Resolved.Kind == "" {
		return errors.New("goatest: persisted report is missing requested or resolved scope")
	}
	if input.Scope.Requested.Project == "" || input.Scope.Resolved.Project == "" {
		return errors.New("goatest: persisted report is missing requested or resolved project boundary")
	}
	if input.Repository.Module == "" {
		return errors.New("goatest: persisted report is missing repository module identity")
	}
	if !validSHA256(input.Configuration.Digest) {
		return errors.New("goatest: persisted report configuration digest is not a lowercase SHA-256")
	}
	if input.Toolchain.Go == "" || input.Toolchain.Goatest == "" || input.Toolchain.GoMutants == "" || input.Toolchain.OS == "" || input.Toolchain.Arch == "" {
		return errors.New("goatest: persisted report is missing toolchain or platform identity")
	}
	if input.Repository.Git.Commit == "" || input.Repository.Git.MergeBase == "" {
		return errors.New("goatest: persisted report is missing explicit Git identity")
	}
	if !input.Repository.Git.Available {
		if input.Repository.Git.Commit != "unavailable" || input.Repository.Git.MergeBase != "unavailable" || input.Repository.Git.Dirty || len(input.Repository.Git.ChangedFiles) != 0 {
			return errors.New("goatest: unavailable Git metadata contains an ambiguous partial identity")
		}
		if !hasLimitation(input.Limitations, "git-metadata-unavailable") {
			return errors.New("goatest: unavailable Git metadata is missing its limitation")
		}
	} else if input.Repository.Git.Commit == "unavailable" || input.Repository.Git.MergeBase == "unavailable" {
		return errors.New("goatest: available Git metadata uses the unavailable sentinel")
	}
	started, err := time.Parse(time.RFC3339Nano, input.Timing.StartedAt)
	if err != nil {
		return fmt.Errorf("goatest: report started_at: %w", err)
	}
	finished, err := time.Parse(time.RFC3339Nano, input.Timing.FinishedAt)
	if err != nil {
		return fmt.Errorf("goatest: report finished_at: %w", err)
	}
	if finished.Before(started) || input.Timing.DurationMS < 0 {
		return errors.New("goatest: report timing is internally inconsistent")
	}
	for _, acceptance := range input.Acceptances {
		expires, _ := time.Parse(time.RFC3339, acceptance.Expires)
		if !expires.After(started) {
			return fmt.Errorf("goatest: report acceptance %q was expired when the run started", acceptance.ID)
		}
	}
	if input.Cache.Derived && input.Cache.SourceRunID == "" {
		return errors.New("goatest: cache-derived report is missing source_run_id")
	}
	if !input.Cache.Derived && input.Cache.SourceRunID != "" {
		return errors.New("goatest: non-cache report unexpectedly names a cache source_run_id")
	}
	return nil
}

func knownRunKind(kind RunKind) bool {
	switch kind {
	case RunFull, RunChangeset, RunPackage, RunReplay, RunOperation:
		return true
	default:
		return false
	}
}

func validSHA256(input string) bool {
	if len(input) != 64 {
		return false
	}
	for _, character := range input {
		if character < '0' || character > '9' {
			if character < 'a' || character > 'f' {
				return false
			}
		}
	}
	return true
}

func hasLimitation(limitations []Limitation, code string) bool {
	for _, limitation := range limitations {
		if limitation.Code == code {
			return true
		}
	}
	return false
}

func knownVerdict(verdict Verdict) bool {
	switch verdict {
	case "", VerdictAssured, VerdictChangeAssured, VerdictScopeAssured, VerdictDefect,
		VerdictInsufficient, VerdictError, VerdictReproduced, VerdictResolved:
		return true
	case VerdictCompleted:
		return true
	default:
		return false
	}
}

func validateCount(name string, count CountAccounting) error {
	if count.Discovered < 0 || count.Selected < 0 || count.Executed < 0 || count.Skipped < 0 || count.Excluded < 0 {
		return fmt.Errorf("goatest: %s accounting contains a negative count", name)
	}
	if count.Discovered == 0 && count.Selected == 0 && count.Executed == 0 && count.Skipped == 0 && count.Excluded == 0 {
		return nil
	}
	if count.Discovered != count.Selected+count.Excluded {
		return fmt.Errorf("goatest: %s accounting mismatch: discovered=%d selected=%d excluded=%d", name, count.Discovered, count.Selected, count.Excluded)
	}
	if count.Selected != count.Executed+count.Skipped {
		return fmt.Errorf("goatest: %s accounting mismatch: selected=%d executed=%d skipped=%d", name, count.Selected, count.Executed, count.Skipped)
	}
	return nil
}

func validateMutants(count MutantAccounting, dispositions []MutantDisposition, verdict Verdict) error {
	values := []int{
		count.Discovered, count.Selected, count.Executed, count.Killed, count.Survived,
		count.Inconclusive, count.CompileRejected, count.Accepted, count.OutOfScope, count.Unknown,
	}
	for _, value := range values {
		if value < 0 {
			return errors.New("goatest: mutant accounting contains a negative count")
		}
	}
	if count.Discovered != count.Executed+count.CompileRejected+count.Accepted+count.OutOfScope+count.Unknown {
		return fmt.Errorf("goatest: mutant disposition mismatch: discovered=%d executed=%d compile-rejected=%d accepted=%d out-of-scope=%d unknown=%d",
			count.Discovered, count.Executed, count.CompileRejected, count.Accepted, count.OutOfScope, count.Unknown)
	}
	if count.Selected != count.Executed+count.CompileRejected+count.Accepted+count.Unknown {
		return fmt.Errorf("goatest: mutant selection mismatch: selected=%d", count.Selected)
	}
	if count.Executed != count.Killed+count.Survived+count.Inconclusive {
		return fmt.Errorf("goatest: mutant execution mismatch: executed=%d killed=%d survived=%d inconclusive=%d",
			count.Executed, count.Killed, count.Survived, count.Inconclusive)
	}
	if count.Unknown != 0 && verdict != VerdictError {
		return errors.New("goatest: unexplained mutants require an ERROR verdict")
	}
	if len(dispositions) != count.Discovered {
		return fmt.Errorf("goatest: mutant inventory mismatch: discovered=%d inventory=%d", count.Discovered, len(dispositions))
	}
	observed := MutantAccounting{Discovered: len(dispositions)}
	seen := make(map[string]struct{}, len(dispositions))
	for _, disposition := range dispositions {
		if disposition.ID == "" {
			return errors.New("goatest: mutant inventory contains an empty ID")
		}
		if disposition.Line < 0 {
			return fmt.Errorf("goatest: mutant %s has a negative source line", disposition.ID)
		}
		if _, duplicate := seen[disposition.ID]; duplicate {
			return fmt.Errorf("goatest: mutant inventory contains duplicate ID %s", disposition.ID)
		}
		seen[disposition.ID] = struct{}{}
		switch disposition.Status {
		case MutantKilled:
			observed.Selected++
			observed.Executed++
			observed.Killed++
		case MutantSurvived:
			observed.Selected++
			observed.Executed++
			observed.Survived++
		case MutantInconclusive:
			observed.Selected++
			observed.Executed++
			observed.Inconclusive++
		case MutantCompileRejected:
			observed.Selected++
			observed.CompileRejected++
		case MutantAccepted:
			observed.Selected++
			observed.Accepted++
		case MutantOutOfScope:
			observed.OutOfScope++
		case MutantUnknown:
			observed.Selected++
			observed.Unknown++
		default:
			return fmt.Errorf("goatest: mutant %s has unknown disposition %q", disposition.ID, disposition.Status)
		}
	}
	if observed != count {
		return fmt.Errorf("goatest: mutant inventory does not match accounting: inventory=%+v accounting=%+v", observed, count)
	}
	return nil
}

func validateVerdictScope(input Report) error {
	if input.RunKind == "" {
		return nil
	}
	resolved := input.Scope.Resolved.Kind
	switch input.Verdict {
	case VerdictAssured:
		if resolved != string(RunFull) {
			return fmt.Errorf("goatest: ASSURED is reserved for resolved full scope, got %q", resolved)
		}
	case VerdictChangeAssured:
		if input.RunKind != RunChangeset || resolved != string(RunChangeset) {
			return errors.New("goatest: CHANGE_ASSURED requires a resolved changeset run")
		}
	case VerdictScopeAssured:
		if input.RunKind != RunPackage || resolved != string(RunPackage) {
			return errors.New("goatest: SCOPE_ASSURED requires a resolved package run")
		}
	case VerdictReproduced, VerdictResolved:
		if input.RunKind != RunReplay {
			return errors.New("goatest: replay outcomes require a replay run")
		}
	case VerdictCompleted:
		if input.RunKind != RunOperation {
			return errors.New("goatest: COMPLETED is reserved for non-assurance operations")
		}
	}
	return nil
}
