// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/P4suta/goatest/internal/assure"
	"github.com/P4suta/goatest/internal/cli"
	"github.com/P4suta/goatest/internal/config"
	envselect "github.com/P4suta/goatest/internal/environment"
	"github.com/P4suta/goatest/internal/provider"
	"github.com/P4suta/goatest/internal/repair"
	"github.com/P4suta/goatest/internal/report"
	"github.com/P4suta/goatest/internal/resource"
)

func (service Service) fix(ctx context.Context, root string, request cli.Request) (report.Report, error) {
	records, err := selectedCandidateRecords(root, request.IDs)
	if err != nil {
		return report.Report{}, err
	}
	result := report.Report{
		Schema: report.SchemaV1, RunKind: report.RunOperation, Verdict: report.VerdictCompleted,
		Evidence: []report.Evidence{{Kind: "fix", ID: "candidates", Status: "loaded", Detail: fmt.Sprintf("count=%d apply=%t", len(records), request.Apply)}},
	}
	for _, record := range records {
		result.Repairs = append(result.Repairs, candidateRepair(root, record, "candidate", record.Validation, ""))
	}
	if !request.Apply || len(records) == 0 {
		return result, nil
	}
	loaded, err := config.Load(root)
	if err != nil {
		return report.Report{}, err
	}
	result.Contract = loaded.Contract
	validator := service.FixValidator
	closeResources := func() error { return nil }
	if validator == nil {
		environment, close, resourceErr := fixEnvironment(ctx, loaded, service.Environment)
		if resourceErr != nil {
			return report.Report{}, resourceErr
		}
		closeResources = close
		defer func() { _ = closeResources() }()
		// The command layer refuses --contract, packages, and test-binary
		// arguments on fix, so the loaded configuration is the one authority.
		validator = assure.NewRepositoryValidator(assure.RepositoryValidatorOptions{
			Root: root, Contract: loaded.Contract, GoBinary: service.GoBinary, TempDirectory: service.TempDirectory,
			Environment: environment, Packages: slices.Clone(loaded.Project.Packages), BuildTags: loaded.Execution.BuildTags,
			TestArgs: slices.Clone(loaded.Execution.TestBinaryArgs), Timeout: loaded.Execution.Timeout,
		})
	}
	validated := make([]provider.Candidate, len(records))
	rejected := false
	for index, record := range records {
		candidate, validationErr := repair.ValidateCandidate(ctx, root, record.Finding, record.Candidate, validator)
		if validationErr != nil {
			rejected = true
			result.Repairs[index] = candidateRepair(root, record, "rejected", "failed", validationErr.Error())
			continue
		}
		validated[index] = candidate
		result.Repairs[index].Validation = "passed-fresh"
	}
	if rejected {
		result.Verdict = report.VerdictInsufficient
		result.Limitations = []report.Limitation{{Code: "repair-validation-rejected", Summary: "no candidates were applied because at least one fresh validation failed"}}
		return result, nil
	}
	applications := make([]repair.Application, len(validated))
	for index, candidate := range validated {
		applications[index] = repair.Application{Finding: records[index].Finding, Candidate: candidate}
	}
	applied, applyErr := repair.ApplyCandidates(root, applications)
	if applyErr != nil {
		result.Verdict = report.VerdictError
		return result, applyErr
	}
	allApplied := true
	for index, item := range applied {
		result.Repairs[index].Status = string(item.Status)
		allApplied = allApplied && item.Status == repair.StatusApplied
		if item.Artifact != "" {
			result.Repairs[index].Reason = "preimage changed after fresh validation; candidate preserved at " + item.Artifact
		}
	}
	if !allApplied {
		result.Verdict = report.VerdictInsufficient
		result.Limitations = append(result.Limitations, report.Limitation{
			Code: "repair-preimage-changed", Summary: "the candidate batch was not applied because at least one preimage changed",
		})
	}
	if closeErr := closeResources(); closeErr != nil {
		return result, closeErr
	}
	closeResources = func() error { return nil }
	return result, nil
}

func selectedCandidateRecords(root string, ids []string) ([]repair.CandidateRecord, error) {
	if len(ids) == 0 {
		return repair.ListCandidates(root)
	}
	seen := make(map[string]bool, len(ids))
	records := make([]repair.CandidateRecord, 0, len(ids))
	for _, id := range ids {
		if seen[id] {
			return nil, fmt.Errorf("goatest: duplicate repair candidate ID %q", id)
		}
		seen[id] = true
		record, err := repair.LoadCandidate(root, id)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, nil
}

func candidateRepair(root string, record repair.CandidateRecord, status, validation, reason string) report.Repair {
	return report.Repair{
		ID: record.ID, Finding: record.Finding.ID, Path: record.Candidate.Path, Status: status,
		Diff: candidateDiff(root, record.Candidate), Validation: validation, Reason: reason,
		Provenance: "snapshot=" + record.Snapshot,
	}
}

func candidateDiff(root string, candidate provider.Candidate) string {
	current, exists, err := repair.CurrentContent(root, candidate.Path)
	if err != nil {
		return "diff unavailable: " + err.Error()
	}
	if exists && bytes.Equal(current, candidate.Content) {
		return ""
	}
	if !utf8.Valid(current) || !utf8.Valid(candidate.Content) || bytes.IndexByte(current, 0) >= 0 || bytes.IndexByte(candidate.Content, 0) >= 0 {
		return fmt.Sprintf("binary change: old=%d bytes new=%d bytes", len(current), len(candidate.Content))
	}
	oldText := boundedDiffText(current)
	newText := boundedDiffText(candidate.Content)
	return "--- a/" + candidate.Path + "\n+++ b/" + candidate.Path + "\n@@\n-" +
		strings.ReplaceAll(oldText, "\n", "\n-") + "\n+" + strings.ReplaceAll(newText, "\n", "\n+")
}

func boundedDiffText(data []byte) string {
	const limit = 32 << 10
	if len(data) <= limit {
		return strings.TrimSuffix(string(data), "\n")
	}
	return strings.TrimSuffix(string(data[:limit]), "\n") + "\n[goatest: diff truncated]"
}

func fixEnvironment(ctx context.Context, loaded config.Config, base []string) ([]string, func() error, error) {
	if base == nil {
		base = os.Environ()
	}
	specs := make(map[string]resource.Spec, len(loaded.Resources))
	names := make([]string, 0, len(loaded.Resources))
	for name, item := range loaded.Resources {
		specs[name] = resource.Spec{
			Command: slices.Clone(item.Command), Timeout: item.Timeout, Shared: item.Shared, Exclusive: item.Exclusive,
			Environment: envselect.Provider(base, item.Environment),
		}
		names = append(names, name)
	}
	slices.Sort(names)
	manager := resource.New(specs)
	overlay := []string{}
	for _, name := range names {
		lease, err := manager.Acquire(ctx, name)
		if err != nil {
			return nil, func() error { return nil }, errors.Join(err, manager.Close())
		}
		overlay = append(overlay, lease.Environment()...)
	}
	return mergeFixEnvironment(base, overlay), manager.Close, nil
}

func mergeFixEnvironment(base, overlay []string) []string {
	values := make(map[string]string)
	names := make(map[string]string)
	for _, entry := range append(slices.Clone(base), overlay...) {
		key, value, ok := strings.Cut(entry, "=")
		if !ok || key == "" {
			continue
		}
		upper := strings.ToUpper(key)
		values[upper], names[upper] = value, key
	}
	result := make([]string, 0, len(values))
	for key, value := range values {
		result = append(result, names[key]+"="+value)
	}
	slices.Sort(result)
	return result
}
