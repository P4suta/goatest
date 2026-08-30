// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package assure

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	gomutants "github.com/P4suta/go-mutants"
	goanalysis "github.com/P4suta/goatest/internal/golang"
	"github.com/P4suta/goatest/internal/mutationbridge"
	"github.com/P4suta/goatest/internal/provider"
	"github.com/P4suta/goatest/internal/report"
)

type scriptedValidationWorkspace struct {
	commands []gomutants.Command
	results  []gomutants.CommandResult
	errors   []error
	closed   int
}

func (workspace *scriptedValidationWorkspace) Exec(_ context.Context, command gomutants.Command) (gomutants.CommandResult, error) {
	workspace.commands = append(workspace.commands, command)
	index := len(workspace.commands) - 1
	var result gomutants.CommandResult
	if index < len(workspace.results) {
		result = workspace.results[index]
	}
	if index < len(workspace.errors) {
		return result, workspace.errors[index]
	}
	return result, nil
}

func (workspace *scriptedValidationWorkspace) Close() error {
	workspace.closed++
	return nil
}

func TestDefaultPrepareValidationSessionRejectsUnsupportedWorkspace(t *testing.T) {
	session, err := defaultPrepareValidationSession(t.Context(), &scriptedValidationWorkspace{}, mutationbridge.PrepareOptions{})
	if session != nil || err == nil || !strings.Contains(err.Error(), "unsupported validation workspace") {
		t.Fatalf("defaultPrepareValidationSession = (%T, %v)", session, err)
	}
}

func TestNewRepositoryValidatorCopiesOptions(t *testing.T) {
	options := RepositoryValidatorOptions{
		Root: "root", Contract: "deep-v1", GoBinary: "go-custom", TempDirectory: "temp",
		Environment: []string{"A=1"}, MutationOperators: []string{"comparison"},
	}
	validator := NewRepositoryValidator(options)
	if validator == nil || !reflect.DeepEqual(validator.options, options) {
		t.Fatalf("NewRepositoryValidator = %+v", validator)
	}
	options.Environment[0] = "A=mutated"
	options.MutationOperators[0] = "mutated"
	if validator.options.Environment[0] != "A=1" || validator.options.MutationOperators[0] != "comparison" {
		t.Fatal("repository validator aliases caller-owned options")
	}
}

func TestRepositoryValidatorOpenPassesFrozenBridgeOptions(t *testing.T) {
	previous := openValidationWorkspace
	t.Cleanup(func() { openValidationWorkspace = previous })
	wantWorkspace := &scriptedValidationWorkspace{}
	var gotRoot string
	var gotOptions mutationbridge.Options
	openValidationWorkspace = func(_ context.Context, root string, options mutationbridge.Options) (validationWorkspace, error) {
		gotRoot, gotOptions = root, options
		options.Environment[0] = "MUTATED=yes"
		return wantWorkspace, nil
	}
	validator := NewRepositoryValidator(RepositoryValidatorOptions{
		GoBinary: "go-custom", TempDirectory: "temp", Environment: []string{"DB=ready"},
	})
	workspace, err := validator.open(t.Context(), "snapshot")
	if err != nil || workspace != wantWorkspace || gotRoot != "snapshot" || gotOptions.GoBinary != "go-custom" || gotOptions.TempDirectory != "temp" ||
		gotOptions.ReportDirectory != ".goatest" || !slices.Equal(gotOptions.Environment, []string{"MUTATED=yes"}) || validator.options.Environment[0] != "DB=ready" {
		t.Fatalf("open = (%T, %v), root=%q options=%+v validator=%+v", workspace, err, gotRoot, gotOptions, validator.options)
	}
}

func TestRunPassingCoversSuccessInfrastructureExitAndTimeout(t *testing.T) {
	cause := errors.New("exec failed")
	for _, test := range []struct {
		name    string
		result  gomutants.CommandResult
		err     error
		wantErr bool
	}{
		{name: "success"},
		{name: "infrastructure error", err: cause, wantErr: true},
		{name: "nonzero exit", result: gomutants.CommandResult{ExitCode: 7, Output: []byte("suite failed")}, wantErr: true},
		{name: "timeout", result: gomutants.CommandResult{TimedOut: true, Output: []byte("hung")}, wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			workspace := &scriptedValidationWorkspace{results: []gomutants.CommandResult{test.result}, errors: []error{test.err}}
			err := runPassing(t.Context(), workspace, []string{"go", "test", "./..."}, "candidate suite", 10*time.Minute)
			if (err != nil) != test.wantErr || len(workspace.commands) != 1 {
				t.Fatalf("runPassing = %v, commands=%+v", err, workspace.commands)
			}
			command := workspace.commands[0]
			if !slices.Equal(command.Argv, []string{"go", "test", "./..."}) || command.Timeout != 10*time.Minute {
				t.Fatalf("command = %+v", command)
			}
			if test.err != nil && !errors.Is(err, cause) {
				t.Fatalf("error = %v, want cause %v", err, cause)
			}
			if test.result.ExitCode != 0 && !strings.Contains(err.Error(), "exit=7") {
				t.Fatalf("exit error = %v", err)
			}
			if test.result.TimedOut && !strings.Contains(err.Error(), "timeout=true") {
				t.Fatalf("timeout error = %v", err)
			}
		})
	}
}

func TestRepositoryValidatorOriginalStableUsesIsolatedCandidateAndClosesWorkspace(t *testing.T) {
	root, candidate := validationFixture(t)
	workspace := &scriptedValidationWorkspace{}
	installValidationOpener(t, func(_ context.Context, snapshot string, _ mutationbridge.Options) (validationWorkspace, error) {
		contents, err := os.ReadFile(filepath.Join(snapshot, candidate.Path))
		if err != nil || !slices.Equal(contents, candidate.Content) || snapshot == root {
			t.Fatalf("candidate snapshot = %q contents=%q err=%v", snapshot, contents, err)
		}
		return workspace, nil
	})
	validator := NewRepositoryValidator(RepositoryValidatorOptions{Root: root, TempDirectory: t.TempDir()})
	if err := validator.OriginalStable(t.Context(), candidate); err != nil {
		t.Fatal(err)
	}
	if workspace.closed != 1 || len(workspace.commands) != 1 || !slices.Equal(workspace.commands[0].Argv, []string{"go", "test", "-count=1", "./..."}) {
		t.Fatalf("workspace = %+v", workspace)
	}
}

func TestRepositoryValidatorOriginalStablePropagatesOpenAndCommandErrors(t *testing.T) {
	root, candidate := validationFixture(t)
	cause := errors.New("open failed")
	installValidationOpener(t, func(context.Context, string, mutationbridge.Options) (validationWorkspace, error) {
		return nil, cause
	})
	validator := NewRepositoryValidator(RepositoryValidatorOptions{Root: root, TempDirectory: t.TempDir()})
	if err := validator.OriginalStable(t.Context(), candidate); !errors.Is(err, cause) {
		t.Fatalf("open error = %v", err)
	}

	workspace := &scriptedValidationWorkspace{results: []gomutants.CommandResult{{ExitCode: 1}}}
	openValidationWorkspace = func(context.Context, string, mutationbridge.Options) (validationWorkspace, error) {
		return workspace, nil
	}
	if err := validator.OriginalStable(t.Context(), candidate); err == nil || workspace.closed != 1 {
		t.Fatalf("command error = %v, closed=%d", err, workspace.closed)
	}
}

func TestRepositoryValidatorKillsRequiresIdentityAndCoversSessionOutcomes(t *testing.T) {
	root, candidate := validationFixture(t)
	validator := NewRepositoryValidator(RepositoryValidatorOptions{
		Root: root, TempDirectory: t.TempDir(), Contract: "deep-v1", MutationOperators: []string{"comparison"},
	})
	opened := 0
	installValidationOpener(t, func(context.Context, string, mutationbridge.Options) (validationWorkspace, error) {
		opened++
		return &scriptedValidationWorkspace{}, nil
	})
	if err := validator.Kills(t.Context(), report.Finding{}, candidate); err == nil || opened != 0 {
		t.Fatalf("missing identity error = %v, opened=%d", err, opened)
	}

	openCause := errors.New("open failed")
	openValidationWorkspace = func(context.Context, string, mutationbridge.Options) (validationWorkspace, error) {
		return nil, openCause
	}
	if err := validator.Kills(t.Context(), report.Finding{MutantID: "target"}, candidate); !errors.Is(err, openCause) {
		t.Fatalf("open error = %v", err)
	}

	prepareCause := errors.New("prepare failed")
	workspace := &scriptedValidationWorkspace{}
	openValidationWorkspace = func(context.Context, string, mutationbridge.Options) (validationWorkspace, error) {
		return workspace, nil
	}
	previousPrepare := prepareValidationSession
	t.Cleanup(func() { prepareValidationSession = previousPrepare })
	prepareValidationSession = func(context.Context, validationWorkspace, mutationbridge.PrepareOptions) (MutationSession, error) {
		return nil, prepareCause
	}
	if err := validator.Kills(t.Context(), report.Finding{MutantID: "target"}, candidate); !errors.Is(err, prepareCause) || workspace.closed != 1 {
		t.Fatalf("prepare error = %v, closed=%d", err, workspace.closed)
	}

	for _, test := range []struct {
		name    string
		catalog gomutants.Catalog
		outcome gomutants.Outcome
		execErr error
		wantErr bool
	}{
		{name: "matching unaccepted", catalog: gomutants.Catalog{Mutants: []gomutants.Mutant{{ID: "target", Accepted: false}, {ID: "other", Accepted: true}}}, wantErr: true},
		{name: "absent", catalog: gomutants.Catalog{Mutants: []gomutants.Mutant{{ID: "other", Accepted: true}}}, wantErr: true},
		{name: "execution error", catalog: validationCatalog(), execErr: errors.New("mutation exec failed"), wantErr: true},
		{name: "survived", catalog: validationCatalog(), outcome: gomutants.OutcomeSurvived, wantErr: true},
		{name: "killed", catalog: validationCatalog(), outcome: gomutants.OutcomeKilled},
	} {
		t.Run(test.name, func(t *testing.T) {
			workspace := &scriptedValidationWorkspace{}
			openValidationWorkspace = func(context.Context, string, mutationbridge.Options) (validationWorkspace, error) {
				return workspace, nil
			}
			session := &mutationUnitSession{catalog: test.catalog, exec: func(request gomutants.ExecRequest) (gomutants.MutantResult, error) {
				if request.Mutant != "target" || request.Package != "fixture.example/module" || request.Timeout != 30*time.Second {
					t.Fatalf("mutation request = %+v", request)
				}
				return gomutants.MutantResult{Outcome: test.outcome}, test.execErr
			}}
			var prepared mutationbridge.PrepareOptions
			prepareValidationSession = func(_ context.Context, got validationWorkspace, options mutationbridge.PrepareOptions) (MutationSession, error) {
				if got != workspace {
					t.Fatal("prepared a different workspace")
				}
				prepared = options
				return session, nil
			}
			err := validator.Kills(t.Context(), report.Finding{MutantID: "target"}, candidate)
			if (err != nil) != test.wantErr || workspace.closed != 1 {
				t.Fatalf("Kills = %v, closed=%d", err, workspace.closed)
			}
			if prepared.Contract != "deep-v1" || !slices.Equal(prepared.Operators, []string{"comparison"}) || !slices.Equal(prepared.VerifyArgv, []string{"go", "test", "-run=^$", "./..."}) {
				t.Fatalf("prepare options = %+v", prepared)
			}
			if test.execErr != nil && !errors.Is(err, test.execErr) {
				t.Fatalf("exec error = %v, want %v", err, test.execErr)
			}
			if test.wantErr && test.execErr == nil && len(session.requests) == 0 && !strings.Contains(err.Error(), "absent") {
				t.Fatalf("absence error = %v", err)
			}
		})
	}
}

func TestRepositoryValidatorSuiteCoversEveryFailureAndRaceOutcome(t *testing.T) {
	root, candidate := validationFixture(t)
	validator := NewRepositoryValidator(RepositoryValidatorOptions{
		Root: root, TempDirectory: t.TempDir(), Contract: "deep-v1",
		Environment: []string{"FEATURE=enabled"}, BuildTags: []string{"integration"}, TestArgs: []string{"-test.short=true"},
	})
	openCause := errors.New("open failed")
	installValidationOpener(t, func(context.Context, string, mutationbridge.Options) (validationWorkspace, error) {
		return nil, openCause
	})
	if err := validator.Suite(t.Context(), candidate); !errors.Is(err, openCause) {
		t.Fatalf("open error = %v", err)
	}

	for _, test := range []struct {
		name             string
		results          []gomutants.CommandResult
		execErrors       []error
		decodeErr        error
		concurrencyErr   error
		raceResult       RaceResult
		raceErr          error
		wantErr          bool
		wantCommands     int
		wantCollectCalls int
	}{
		{name: "success", results: successfulSuiteResults(), wantCommands: 2, wantCollectCalls: 1},
		{name: "test failure", results: []gomutants.CommandResult{{ExitCode: 1}}, wantErr: true, wantCommands: 1},
		{name: "list infrastructure", results: successfulSuiteResults(), execErrors: []error{nil, errors.New("list failed")}, wantErr: true, wantCommands: 2},
		{name: "list exit", results: []gomutants.CommandResult{{}, {ExitCode: 3}}, wantErr: true, wantCommands: 2},
		{name: "list timeout", results: []gomutants.CommandResult{{}, {TimedOut: true}}, wantErr: true, wantCommands: 2},
		{name: "decode failure", results: successfulSuiteResults(), decodeErr: errors.New("decode failed"), wantErr: true, wantCommands: 2},
		{name: "concurrency failure", results: successfulSuiteResults(), concurrencyErr: errors.New("scan failed"), wantErr: true, wantCommands: 2},
		{name: "race infrastructure", results: successfulSuiteResults(), raceErr: errors.New("race failed"), wantErr: true, wantCommands: 2, wantCollectCalls: 1},
		{name: "race finding", results: successfulSuiteResults(), raceResult: RaceResult{Findings: []report.Finding{{ID: "race"}}}, wantErr: true, wantCommands: 2, wantCollectCalls: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			workspace := &scriptedValidationWorkspace{results: test.results, errors: test.execErrors}
			openValidationWorkspace = func(context.Context, string, mutationbridge.Options) (validationWorkspace, error) {
				return workspace, nil
			}
			previousDecode, previousConcurrency, previousRace := decodeValidationPackages, concurrencyValidationPackages, collectValidationRace
			t.Cleanup(func() {
				decodeValidationPackages, concurrencyValidationPackages, collectValidationRace = previousDecode, previousConcurrency, previousRace
			})
			model := goanalysis.Model{ModulePath: "fixture.example/module", Packages: []goanalysis.Package{{ImportPath: "fixture.example/module", RelativeDir: "."}}}
			decodeValidationPackages = func(io.Reader) (goanalysis.Model, error) { return model, test.decodeErr }
			concurrencyValidationPackages = func(gotRoot string, packages []goanalysis.Package) ([]string, error) {
				if gotRoot == "" || !reflect.DeepEqual(packages, model.Packages) {
					t.Fatalf("concurrency input = %q %+v", gotRoot, packages)
				}
				return []string{"fixture.example/module"}, test.concurrencyErr
			}
			collectCalls := 0
			collectValidationRace = func(_ context.Context, got CommandWorkspace, gotModel goanalysis.Model, concurrent []string, contract string, options RaceOptions) (RaceResult, error) {
				collectCalls++
				if got != workspace || !reflect.DeepEqual(gotModel, model) || !slices.Equal(concurrent, []string{"fixture.example/module"}) || contract != "deep-v1" ||
					!slices.Equal(options.Environment, []string{"FEATURE=enabled"}) || !slices.Equal(options.BuildTags, []string{"integration"}) ||
					!slices.Equal(options.TestArgs, []string{"-test.short=true"}) {
					t.Fatalf("race input = (%T, %+v, %v, %q, %+v)", got, gotModel, concurrent, contract, options)
				}
				return test.raceResult, test.raceErr
			}
			err := validator.Suite(t.Context(), candidate)
			if (err != nil) != test.wantErr || len(workspace.commands) != test.wantCommands || workspace.closed != 1 || collectCalls != test.wantCollectCalls {
				t.Fatalf("Suite = %v, commands=%+v closed=%d collect=%d", err, workspace.commands, workspace.closed, collectCalls)
			}
			if len(workspace.commands) == 2 && !slices.Equal(workspace.commands[1].Argv, []string{"go", "list", "-json", "-tags=integration", "./..."}) {
				t.Fatalf("list command = %+v", workspace.commands[1])
			}
		})
	}
}

func successfulSuiteResults() []gomutants.CommandResult {
	return []gomutants.CommandResult{{}, {Output: []byte("ignored by decoder seam")}}
}

func validationCatalog() gomutants.Catalog {
	return gomutants.Catalog{Mutants: []gomutants.Mutant{
		{ID: "other", Package: "wrong", Accepted: true},
		{ID: "target", Package: "fixture.example/module", Accepted: true},
	}}
}

func validationFixture(t *testing.T) (string, provider.Candidate) {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "value.go"), []byte("package fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return root, provider.Candidate{Kind: "patch", Path: "value_test.go", Content: []byte("package fixture\n")}
}

func installValidationOpener(t *testing.T, opener func(context.Context, string, mutationbridge.Options) (validationWorkspace, error)) {
	t.Helper()
	previous := openValidationWorkspace
	t.Cleanup(func() { openValidationWorkspace = previous })
	openValidationWorkspace = opener
}

var _ validationWorkspace = (*scriptedValidationWorkspace)(nil)
