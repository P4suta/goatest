// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package assure

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/P4suta/goatest/internal/config"
	"github.com/P4suta/goatest/internal/provider"
	"github.com/P4suta/goatest/internal/repair"
	"github.com/P4suta/goatest/internal/report"
)

type generationValidator struct {
	original func(context.Context, provider.Candidate) error
	kills    func(context.Context, report.Finding, provider.Candidate) error
	suite    func(context.Context, provider.Candidate) error
}

func (validator generationValidator) OriginalStable(ctx context.Context, candidate provider.Candidate) error {
	if validator.original == nil {
		return nil
	}
	return validator.original(ctx, candidate)
}

func (validator generationValidator) Kills(ctx context.Context, finding report.Finding, candidate provider.Candidate) error {
	if validator.kills == nil {
		return nil
	}
	return validator.kills(ctx, finding, candidate)
}

func (validator generationValidator) Suite(ctx context.Context, candidate provider.Candidate) error {
	if validator.suite == nil {
		return nil
	}
	return validator.suite(ctx, candidate)
}

func TestAttemptGeneratedRepairsSkipsDisabledEmptyAndUnconfiguredGeneration(t *testing.T) {
	finding := generationFinding("finding-a")
	for _, test := range []struct {
		name     string
		findings []report.Finding
		options  GenerationOptions
	}{
		{name: "empty", options: GenerationOptions{Generate: func(context.Context, provider.Request) (provider.Response, error) {
			t.Fatal("generator called without findings")
			return provider.Response{}, nil
		}}},
		{name: "unconfigured", findings: []report.Finding{finding}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if test.options.Generate == nil {
				test.options.Generate = func(context.Context, provider.Request) (provider.Response, error) {
					t.Fatal("generator called while generation is disabled or unconfigured")
					return provider.Response{}, nil
				}
				if test.name == "unconfigured" {
					test.options.Generate = nil
				}
			}
			evaluation, err := AttemptGeneratedRepairs(t.Context(), t.TempDir(), test.findings, test.options)
			if err != nil || !reflect.DeepEqual(evaluation, GenerationEvaluation{Findings: test.findings}) {
				t.Fatalf("AttemptGeneratedRepairs = (%+v, %v)", evaluation, err)
			}
			if len(test.findings) != 0 {
				test.findings[0].ID = "mutated"
				if evaluation.Findings[0].ID != finding.ID {
					t.Fatal("evaluation aliases input findings")
				}
			}
		})
	}
}

func TestAttemptGeneratedRepairsNoApplyStoresValidatedCandidateWithoutChangingSource(t *testing.T) {
	root := t.TempDir()
	finding := generationFinding("finding-read-only")
	candidate := provider.Candidate{Kind: "patch", Path: "generated_test.go", Content: []byte("package fixture\n")}
	repairID := generatedRepairID("snapshot-read-only", finding, candidate)

	evaluation, err := AttemptGeneratedRepairs(t.Context(), root, []report.Finding{finding}, GenerationOptions{
		Snapshot: "snapshot-read-only", NoApply: true, AllowedPaths: []string{"generated_test.go"}, Validator: generationValidator{},
		Generate: func(context.Context, provider.Request) (provider.Response, error) {
			return provider.Response{Version: provider.ProtocolVersion, FindingID: finding.ID, Candidates: []provider.Candidate{candidate}}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	wantRepairs := []report.Repair{{ID: repairID, Finding: finding.ID, Path: candidate.Path, Status: "candidate"}}
	if evaluation.Applied || !reflect.DeepEqual(evaluation.Repairs, wantRepairs) {
		t.Fatalf("evaluation = %+v, want repairs %+v", evaluation, wantRepairs)
	}
	if _, err := os.Stat(filepath.Join(root, candidate.Path)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read-only verification changed source: %v", err)
	}
	record, err := repair.LoadCandidate(root, repairID)
	if err != nil {
		t.Fatal(err)
	}
	if record.Snapshot != "snapshot-read-only" || record.Finding != finding || !reflect.DeepEqual(record.Candidate, candidate) || record.Validation != "passed" {
		t.Fatalf("candidate record = %+v", record)
	}
}

func TestAttemptGeneratedRepairsUsesClonedCommandOnlyWithoutDirectGenerator(t *testing.T) {
	previous := generationFromCommand
	t.Cleanup(func() { generationFromCommand = previous })

	called := 0
	generationFromCommand = func(command, environment []string) func(context.Context, provider.Request) (provider.Response, error) {
		called++
		if !slices.Equal(command, []string{"generator", "--json"}) {
			t.Fatalf("command = %v", command)
		}
		if !slices.Equal(environment, []string{"PROVIDER_TOKEN=redacted-test-value"}) {
			t.Fatalf("environment = %v", environment)
		}
		command[0] = "mutated by factory"
		environment[0] = "MUTATED=yes"
		return func(_ context.Context, request provider.Request) (provider.Response, error) {
			return provider.Response{Version: provider.ProtocolVersion, FindingID: request.Finding.ID}, nil
		}
	}
	command := []string{"generator", "--json"}
	environment := []string{"PROVIDER_TOKEN=redacted-test-value"}
	finding := generationFinding("finding-command")
	evaluation, err := AttemptGeneratedRepairs(t.Context(), t.TempDir(), []report.Finding{finding}, GenerationOptions{
		Command: command, ProviderEnvironment: environment, Validator: generationValidator{},
	})
	if err != nil || called != 1 || command[0] != "generator" || environment[0] != "PROVIDER_TOKEN=redacted-test-value" || !reflect.DeepEqual(evaluation.Findings, []report.Finding{finding}) {
		t.Fatalf("command generation = (%+v, %v), calls=%d command=%v environment=%v", evaluation, err, called, command, environment)
	}

	called = 0
	directCalls := 0
	evaluation, err = AttemptGeneratedRepairs(t.Context(), t.TempDir(), []report.Finding{finding}, GenerationOptions{
		Command: command, Validator: generationValidator{},
		Generate: func(_ context.Context, request provider.Request) (provider.Response, error) {
			directCalls++
			return provider.Response{Version: provider.ProtocolVersion, FindingID: request.Finding.ID}, nil
		},
	})
	if err != nil || called != 0 || directCalls != 1 || !reflect.DeepEqual(evaluation.Findings, []report.Finding{finding}) {
		t.Fatalf("direct generation = (%+v, %v), factory=%d direct=%d", evaluation, err, called, directCalls)
	}
}

func TestAttemptGeneratedRepairsBuildsExactIndependentRequests(t *testing.T) {
	findings := []report.Finding{generationFinding("finding-a"), generationFinding("finding-b")}
	allowed := []string{"pkg/*_test.go"}
	var requests []provider.Request
	evaluation, err := AttemptGeneratedRepairs(t.Context(), t.TempDir(), findings, GenerationOptions{
		Snapshot: "snapshot-a", AllowedPaths: allowed, Validator: generationValidator{},
		Generate: func(_ context.Context, request provider.Request) (provider.Response, error) {
			requests = append(requests, request)
			request.AllowedPaths[0] = "mutated"
			return provider.Response{Version: provider.ProtocolVersion, FindingID: request.Finding.ID}, nil
		},
	})
	if err != nil || len(requests) != 2 || !reflect.DeepEqual(evaluation, GenerationEvaluation{Findings: findings}) {
		t.Fatalf("AttemptGeneratedRepairs = (%+v, %v), requests=%+v", evaluation, err, requests)
	}
	for index, request := range requests {
		if request.Version != provider.ProtocolVersion || request.Finding != findings[index] || request.Snapshot != "snapshot-a" || len(request.AllowedPaths) != 1 {
			t.Errorf("request %d = %+v", index, request)
		}
	}
	if allowed[0] != "pkg/*_test.go" || requests[1].AllowedPaths[0] != "mutated" {
		t.Fatalf("allowed path isolation = source %v requests %+v", allowed, requests)
	}
	findings[0].ID = "mutated"
	if evaluation.Findings[0].ID != "finding-a" {
		t.Fatal("evaluation aliases findings")
	}
}

func TestAttemptGeneratedRepairsRejectsProviderFailuresAndIdentityMismatches(t *testing.T) {
	cause := errors.New("provider failed")
	finding := generationFinding("finding-a")
	for _, test := range []struct {
		name     string
		response provider.Response
		err      error
	}{
		{name: "provider error", err: cause},
		{name: "wrong version", response: provider.Response{Version: provider.ProtocolVersion + 1, FindingID: finding.ID}},
		{name: "wrong finding", response: provider.Response{Version: provider.ProtocolVersion, FindingID: "finding-b"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			evaluation, err := AttemptGeneratedRepairs(t.Context(), t.TempDir(), []report.Finding{finding}, GenerationOptions{
				Validator: generationValidator{},
				Generate: func(context.Context, provider.Request) (provider.Response, error) {
					return test.response, test.err
				},
			})
			if err == nil || !reflect.DeepEqual(evaluation, GenerationEvaluation{}) {
				t.Fatalf("AttemptGeneratedRepairs = (%+v, %v)", evaluation, err)
			}
			if test.err != nil && !errors.Is(err, cause) {
				t.Fatalf("error = %v, want cause %v", err, cause)
			}
			if test.err == nil && !strings.Contains(err.Error(), "identity mismatch for "+finding.ID) {
				t.Fatalf("identity error = %v", err)
			}
		})
	}
}

func TestAttemptGeneratedRepairsUsesDefaultValidatorAndAppliesFirstPassingCandidate(t *testing.T) {
	previous := newGenerationValidator
	t.Cleanup(func() { newGenerationValidator = previous })

	var received RepositoryValidatorOptions
	originalCalls, killCalls, suiteCalls := 0, 0, 0
	newGenerationValidator = func(options RepositoryValidatorOptions) repair.Validator {
		received = options
		return generationValidator{
			original: func(context.Context, provider.Candidate) error { originalCalls++; return nil },
			kills:    func(context.Context, report.Finding, provider.Candidate) error { killCalls++; return nil },
			suite:    func(context.Context, provider.Candidate) error { suiteCalls++; return nil },
		}
	}
	root := t.TempDir()
	finding := generationFinding("finding-default-validator")
	repositoryOptions := RepositoryValidatorOptions{Root: root, Contract: "deep-v1", GoBinary: "go-custom"}
	candidate := provider.Candidate{Kind: "patch", Path: "generated_test.go", Content: []byte("package fixture\n")}
	evaluation, err := AttemptGeneratedRepairs(t.Context(), root, []report.Finding{finding}, GenerationOptions{
		AllowedPaths: []string{"generated_test.go"}, RepositoryValidator: repositoryOptions,
		Generate: func(context.Context, provider.Request) (provider.Response, error) {
			return provider.Response{Version: provider.ProtocolVersion, FindingID: finding.ID, Candidates: []provider.Candidate{candidate}}, nil
		},
	})
	wantRepair := report.Repair{
		ID:      generatedRepairID("", finding, candidate),
		Finding: finding.ID, Path: candidate.Path, Status: "applied",
	}
	contents, readErr := os.ReadFile(filepath.Join(root, candidate.Path))
	if err != nil || readErr != nil || !reflect.DeepEqual(received, repositoryOptions) || originalCalls != 3 || killCalls != 2 || suiteCalls != 1 ||
		!evaluation.Applied || !reflect.DeepEqual(evaluation.Repairs, []report.Repair{wantRepair}) || !slices.Equal(contents, candidate.Content) {
		t.Fatalf("applied evaluation = (%+v, %v), read=%v calls=(%d,%d,%d) options=%+v", evaluation, err, readErr, originalCalls, killCalls, suiteCalls, received)
	}
}

func TestAttemptGeneratedRepairsRecordsEveryRejectionAndArtifactBeforeApplying(t *testing.T) {
	root := t.TempDir()
	findings := []report.Finding{generationFinding("finding-a"), generationFinding("finding-late")}
	candidates := []provider.Candidate{
		{Kind: "patch", Path: "unsafe.go", Content: []byte("unsafe")},
		{Kind: "patch", Path: "rejected_test.go", Content: []byte("rejected")},
		{Kind: "patch", Path: "conflict_test.go", PreimageSHA256: strings.Repeat("a", 64), Content: []byte("artifact")},
		{Kind: "patch", Path: "applied_test.go", Content: []byte("applied")},
		{Kind: "patch", Path: "late_test.go", Content: []byte("late")},
	}
	generated := 0
	validatedLate := false
	validator := generationValidator{original: func(_ context.Context, candidate provider.Candidate) error {
		if candidate.Path == "rejected_test.go" {
			return errors.New("unstable")
		}
		if candidate.Path == "late_test.go" {
			validatedLate = true
		}
		return nil
	}}
	evaluation, err := AttemptGeneratedRepairs(t.Context(), root, findings, GenerationOptions{
		AllowedPaths: []string{"*_test.go"}, Validator: validator,
		Generate: func(_ context.Context, request provider.Request) (provider.Response, error) {
			generated++
			return provider.Response{Version: provider.ProtocolVersion, FindingID: request.Finding.ID, Candidates: candidates}, nil
		},
	})
	if err != nil || generated != 1 || validatedLate || !evaluation.Applied || len(evaluation.Repairs) != 4 {
		t.Fatalf("AttemptGeneratedRepairs = (%+v, %v), generated=%d late=%t", evaluation, err, generated, validatedLate)
	}
	wantStatuses := []string{"rejected", "rejected", "artifact", "applied"}
	wantPaths := []string{"unsafe.go", "rejected_test.go", "conflict_test.go", "applied_test.go"}
	for index, got := range evaluation.Repairs {
		candidate := candidates[index]
		wantID := generatedRepairID("", findings[0], candidate)
		if got.ID != wantID || got.Finding != findings[0].ID || got.Path != wantPaths[index] || got.Status != wantStatuses[index] {
			t.Errorf("repair %d = %+v", index, got)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "late_test.go")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("late candidate applied: %v", err)
	}
}

func TestGenerationPathsHonorsPrecedenceAndClonesEveryResult(t *testing.T) {
	options := Options{AllowedGenerationPaths: []string{"option_test.go"}}
	loaded := config.Config{Generation: config.Generation{AllowedPaths: []string{"config_test.go"}}}
	got := generationPaths(options, loaded)
	if !slices.Equal(got, options.AllowedGenerationPaths) {
		t.Fatalf("option paths = %v", got)
	}
	got[0] = "mutated"
	if options.AllowedGenerationPaths[0] != "option_test.go" {
		t.Fatal("option paths were not cloned")
	}

	options.AllowedGenerationPaths = nil
	got = generationPaths(options, loaded)
	if !slices.Equal(got, loaded.Generation.AllowedPaths) {
		t.Fatalf("config paths = %v", got)
	}
	got[0] = "mutated"
	if loaded.Generation.AllowedPaths[0] != "config_test.go" {
		t.Fatal("config paths were not cloned")
	}

	loaded.Generation.AllowedPaths = nil
	wantDefault := []string{"**/*_test.go", "**/testdata/fuzz/**"}
	got = generationPaths(options, loaded)
	if !slices.Equal(got, wantDefault) {
		t.Fatalf("default paths = %v", got)
	}
	got[0] = "mutated"
	if next := generationPaths(options, loaded); !slices.Equal(next, wantDefault) {
		t.Fatalf("default paths alias prior result: %v", next)
	}
}

func TestGenerationPathAllowedRequiresSafeCandidateAndMatchingPattern(t *testing.T) {
	for _, test := range []struct {
		name      string
		candidate string
		allowed   []string
		want      bool
	}{
		{name: "root test default", candidate: "value_test.go", allowed: []string{"**/*_test.go"}, want: true},
		{name: "nested test default", candidate: "pkg/value_test.go", allowed: []string{"**/*_test.go"}, want: true},
		{name: "root fuzz default", candidate: "testdata/fuzz/FuzzValue/seed", allowed: []string{"**/testdata/fuzz/**"}, want: true},
		{name: "nested fuzz default", candidate: "pkg/testdata/fuzz/FuzzValue/seed", allowed: []string{"**/testdata/fuzz/**"}, want: true},
		{name: "directory prefix", candidate: "pkg/nested/value_test.go", allowed: []string{"pkg/**"}, want: true},
		{name: "directory prefix boundary", candidate: "pkg2/value_test.go", allowed: []string{"pkg/**"}},
		{name: "backslash pattern", candidate: "pkg/value_test.go", allowed: []string{`pkg\**`}, want: true},
		{name: "path match", candidate: "pkg/value_test.go", allowed: []string{"pkg/*_test.go"}, want: true},
		{name: "valid nonmatch", candidate: "other/value_test.go", allowed: []string{"pkg/*_test.go"}},
		{name: "invalid pattern", candidate: "value_test.go", allowed: []string{"["}},
		{name: "empty patterns", candidate: "value_test.go"},
		{name: "unsafe extension despite broad pattern", candidate: "value.go", allowed: []string{"**"}},
		{name: "absolute", candidate: "/value_test.go", allowed: []string{"**/*_test.go"}},
		{name: "traversal", candidate: "../value_test.go", allowed: []string{"**/*_test.go"}},
		{name: "backslash candidate", candidate: `pkg\value_test.go`, allowed: []string{"**/*_test.go"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := generationPathAllowed(test.candidate, test.allowed); got != test.want {
				t.Fatalf("generationPathAllowed(%q, %v) = %t, want %t", test.candidate, test.allowed, got, test.want)
			}
		})
	}
}

func generationFinding(id string) report.Finding {
	return report.Finding{ID: id, Kind: "surviving-mutant", Path: "value.go", Line: 7, Summary: "mutant survived", MutantID: "mutant-a"}
}

var _ repair.Validator = generationValidator{}
