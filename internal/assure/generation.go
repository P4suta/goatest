// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package assure

import (
	"context"
	"fmt"
	"path"
	"slices"
	"strings"

	"github.com/P4suta/goatest/internal/config"
	"github.com/P4suta/goatest/internal/provider"
	"github.com/P4suta/goatest/internal/repair"
	"github.com/P4suta/goatest/internal/report"
)

type GenerationEvaluation struct {
	Findings []report.Finding
	Repairs  []report.Repair
	Applied  bool
}

type GenerationOptions struct {
	Snapshot            string
	NoApply             bool
	Generate            func(context.Context, provider.Request) (provider.Response, error)
	Command             []string
	AllowedPaths        []string
	Validator           repair.Validator
	RepositoryValidator RepositoryValidatorOptions
}

var (
	generationFromCommand = func(command []string) func(context.Context, provider.Request) (provider.Response, error) {
		client := provider.Client{Command: command}
		return client.Generate
	}
	newGenerationValidator = func(options RepositoryValidatorOptions) repair.Validator {
		return NewRepositoryValidator(options)
	}
)

func AttemptGeneratedRepairs(ctx context.Context, root string, findings []report.Finding, options GenerationOptions) (GenerationEvaluation, error) {
	evaluation := GenerationEvaluation{Findings: slices.Clone(findings)}
	if options.NoApply || len(findings) == 0 {
		return evaluation, nil
	}
	generate := options.Generate
	if generate == nil && len(options.Command) != 0 {
		generate = generationFromCommand(slices.Clone(options.Command))
	}
	if generate == nil {
		return evaluation, nil
	}
	validator := options.Validator
	if validator == nil {
		validator = newGenerationValidator(options.RepositoryValidator)
	}
	for _, finding := range findings {
		response, err := generate(ctx, provider.Request{
			Version: provider.ProtocolVersion, Finding: finding,
			AllowedPaths: slices.Clone(options.AllowedPaths), Snapshot: options.Snapshot,
		})
		if err != nil {
			return GenerationEvaluation{}, fmt.Errorf("goatest: generate repair for %s: %w", finding.ID, err)
		}
		if response.Version != provider.ProtocolVersion || response.FindingID != finding.ID {
			return GenerationEvaluation{}, fmt.Errorf("goatest: generation response identity mismatch for %s", finding.ID)
		}
		for _, candidate := range response.Candidates {
			repairID := report.FindingID("generated", finding.ID, candidate.Kind, candidate.Path, candidate.PreimageSHA256)
			if !generationPathAllowed(candidate.Path, options.AllowedPaths) {
				evaluation.Repairs = append(evaluation.Repairs, report.Repair{
					ID: repairID, Finding: finding.ID, Path: candidate.Path, Status: "rejected",
				})
				continue
			}
			applied, err := repair.ValidateAndApply(ctx, root, finding, candidate, validator)
			if err != nil {
				evaluation.Repairs = append(evaluation.Repairs, report.Repair{
					ID: repairID, Finding: finding.ID, Path: candidate.Path, Status: "rejected",
				})
				continue
			}
			evaluation.Repairs = append(evaluation.Repairs, report.Repair{
				ID: repairID, Finding: finding.ID, Path: applied.Path, Status: string(applied.Status),
			})
			if applied.Status == repair.StatusApplied {
				evaluation.Applied = true
				return evaluation, nil
			}
		}
	}
	return evaluation, nil
}

func generationPaths(options Options, loaded config.Config) []string {
	if len(options.AllowedGenerationPaths) != 0 {
		return slices.Clone(options.AllowedGenerationPaths)
	}
	if len(loaded.Generation.AllowedPaths) != 0 {
		return slices.Clone(loaded.Generation.AllowedPaths)
	}
	return []string{"**/*_test.go", "**/testdata/fuzz/**"}
}

func generationPathAllowed(candidate string, allowed []string) bool {
	if !repair.AllowedPath(candidate) {
		return false
	}
	normalized := strings.ReplaceAll(candidate, `\`, "/")
	for _, pattern := range allowed {
		pattern = strings.ReplaceAll(pattern, `\`, "/")
		switch {
		case pattern == "**/*_test.go" && strings.HasSuffix(normalized, "_test.go"):
			return true
		case pattern == "**/testdata/fuzz/**" && (strings.HasPrefix(normalized, "testdata/fuzz/") || strings.Contains(normalized, "/testdata/fuzz/")):
			return true
		case strings.HasSuffix(pattern, "/**") && strings.HasPrefix(normalized, strings.TrimSuffix(pattern, "**")):
			return true
		}
		if matched, err := path.Match(pattern, normalized); err == nil && matched {
			return true
		}
	}
	return false
}
