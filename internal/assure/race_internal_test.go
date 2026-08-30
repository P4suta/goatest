// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package assure

import (
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	gomutants "github.com/P4suta/go-mutants"
	goanalysis "github.com/P4suta/goatest/internal/golang"
	"github.com/P4suta/goatest/internal/report"
)

func TestRelevantRacePackagesCoversOwnerDependencyAndCoveredFileIndependently(t *testing.T) {
	t.Parallel()
	model := goanalysis.Model{Packages: []goanalysis.Package{
		{ImportPath: "fixture/app", RelativeDir: "app"},
		{ImportPath: "fixture/gateway", RelativeDir: "gateway"},
		{ImportPath: "fixture/plain", RelativeDir: "plain"},
		{ImportPath: "fixture/worker", RelativeDir: "worker"},
	}}
	targets := []TargetEvidence{
		{Target: goanalysis.Target{Package: "fixture/worker"}, CoveredFiles: []string{"plain/plain.go"}},
		{Target: goanalysis.Target{Package: "fixture/app", Dependencies: []string{"fixture/worker"}}, CoveredFiles: []string{"plain/plain.go"}},
		{Target: goanalysis.Target{Package: "fixture/gateway"}, CoveredFiles: []string{"plain/plain.go", "worker/worker.go"}},
		{Target: goanalysis.Target{Package: "fixture/plain"}, CoveredFiles: []string{"plain/plain.go"}},
	}
	got := RelevantRacePackages(model, []string{"fixture/worker", "fixture/worker"}, targets)
	want := []string{"fixture/app", "fixture/gateway", "fixture/worker"}
	if !slices.Equal(got, want) {
		t.Fatalf("RelevantRacePackages = %v, want %v", got, want)
	}
}

func TestPackageForFileChoosesRootOrLongestNestedPackage(t *testing.T) {
	t.Parallel()
	packages := []goanalysis.Package{
		{ImportPath: "fixture/root", RelativeDir: "."},
		{ImportPath: "fixture/pkg", RelativeDir: "pkg"},
		{ImportPath: "fixture/nested", RelativeDir: "pkg/nested"},
	}
	for _, test := range []struct {
		path string
		want string
	}{
		{path: "root.go", want: "fixture/root"},
		{path: "./root.go", want: "fixture/root"},
		{path: `pkg\value.go`, want: "fixture/pkg"},
		{path: "pkg/nested/value.go", want: "fixture/nested"},
		{path: "pkg/other/value.go", want: "fixture/pkg"},
		{path: "unknown/value.go", want: ""},
	} {
		if got := packageForFile(packages, test.path); got != test.want {
			t.Errorf("packageForFile(%q) = %q, want %q", test.path, got, test.want)
		}
	}
	duplicates := []goanalysis.Package{
		{ImportPath: "fixture/first", RelativeDir: "pkg"},
		{ImportPath: "fixture/second", RelativeDir: "pkg"},
	}
	if got := packageForFile(duplicates, "pkg/value.go"); got != "fixture/first" {
		t.Fatalf("duplicate directory package = %q, want first", got)
	}
}

func TestCollectRaceHandlesNoPackagesNilWorkspaceExecutionErrorAndTimeout(t *testing.T) {
	t.Run("not applicable", func(t *testing.T) {
		result, err := CollectRace(t.Context(), nil, goanalysis.Model{}, nil, "standard-v1", nil)
		if err != nil || len(result.Findings) != 0 || !reflect.DeepEqual(result.Evidence, []report.Evidence{{Kind: "race", ID: "packages", Status: "not-applicable"}}) {
			t.Fatalf("CollectRace(no packages) = (%+v, %v)", result, err)
		}
	})

	t.Run("nil workspace", func(t *testing.T) {
		result, err := CollectRace(t.Context(), nil, goanalysis.Model{}, []string{"fixture/worker"}, "standard-v1", nil)
		if err == nil || !reflect.DeepEqual(result, RaceResult{}) || err.Error() != "goatest: nil race workspace" {
			t.Fatalf("CollectRace(nil workspace) = (%+v, %v)", result, err)
		}
	})

	t.Run("execution error", func(t *testing.T) {
		sentinel := errors.New("exec failed")
		workspace := &baselineFakeWorkspace{exec: func(gomutants.Command) (gomutants.CommandResult, error) {
			return gomutants.CommandResult{}, sentinel
		}}
		result, err := CollectRace(t.Context(), workspace, goanalysis.Model{}, []string{"fixture/worker"}, "standard-v1", nil)
		if !reflect.DeepEqual(result, RaceResult{}) || !errors.Is(err, sentinel) || !strings.HasPrefix(err.Error(), "goatest: race verification: ") {
			t.Fatalf("CollectRace(exec error) = (%+v, %v)", result, err)
		}
	})

	t.Run("timeout", func(t *testing.T) {
		workspace := &baselineFakeWorkspace{exec: func(gomutants.Command) (gomutants.CommandResult, error) {
			return gomutants.CommandResult{TimedOut: true}, nil
		}}
		result, err := CollectRace(t.Context(), workspace, goanalysis.Model{}, []string{"fixture/worker"}, "standard-v1", nil)
		if !reflect.DeepEqual(result, RaceResult{}) || err == nil || err.Error() != "goatest: race verification timed out" {
			t.Fatalf("CollectRace(timeout) = (%+v, %v)", result, err)
		}
	})
}

func TestCollectRaceCommandIsDeterministicClonedAndDeepIncludesEveryPackage(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name       string
		contract   string
		concurrent []string
		model      goanalysis.Model
		packages   []string
	}{
		{name: "standard", contract: "standard-v1", concurrent: []string{"fixture/z", "fixture/a", "fixture/z"}, packages: []string{"fixture/a", "fixture/z"}},
		{name: "deep", contract: "deep-v1", concurrent: []string{"ignored"}, model: goanalysis.Model{Packages: []goanalysis.Package{{ImportPath: "fixture/z"}, {ImportPath: "fixture/a"}, {ImportPath: "fixture/a"}}}, packages: []string{"fixture/a", "fixture/z"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			environment := []string{"DB=ready"}
			workspace := &baselineFakeWorkspace{exec: func(command gomutants.Command) (gomutants.CommandResult, error) {
				wantArgv := append([]string{"go", "test", "-race", "-count=1"}, test.packages...)
				if !slices.Equal(command.Argv, wantArgv) || !slices.Equal(command.Env, environment) || command.Timeout != raceVerificationTimeout {
					t.Fatalf("race command = %+v, want argv %v", command, wantArgv)
				}
				return gomutants.CommandResult{}, nil
			}}
			result, err := CollectRace(t.Context(), workspace, test.model, test.concurrent, test.contract, environment)
			if err != nil || len(result.Findings) != 0 || !reflect.DeepEqual(result.Evidence, []report.Evidence{{Kind: "race", ID: strings.Join(test.packages, ","), Status: "passed"}}) {
				t.Fatalf("CollectRace = (%+v, %v)", result, err)
			}
			environment[0] = "MUTATED=yes"
			if workspace.commands[0].Env[0] != "DB=ready" {
				t.Fatal("race command aliases environment")
			}
		})
	}
}

func TestCollectRaceClassifiesPanicAndUnexpectedExitPrecisely(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name    string
		output  string
		kind    string
		wantErr string
	}{
		{name: "panic", output: "test output\npanic: corrupted state", kind: "race-test-failure"},
		{name: "unexpected", output: "go: -race requires cgo", wantErr: "goatest: race verification failed (exit=2): go: -race requires cgo"},
	} {
		t.Run(test.name, func(t *testing.T) {
			workspace := &baselineFakeWorkspace{exec: func(gomutants.Command) (gomutants.CommandResult, error) {
				return gomutants.CommandResult{ExitCode: 2, Output: []byte(test.output)}, nil
			}}
			result, err := CollectRace(t.Context(), workspace, goanalysis.Model{}, []string{"fixture/worker"}, "standard-v1", nil)
			if test.wantErr != "" {
				if !reflect.DeepEqual(result, RaceResult{}) || err == nil || err.Error() != test.wantErr {
					t.Fatalf("CollectRace = (%+v, %v)", result, err)
				}
				return
			}
			if err != nil || len(result.Findings) != 1 || result.Findings[0].Kind != test.kind {
				t.Fatalf("CollectRace = (%+v, %v)", result, err)
			}
		})
	}
}
