// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package assure_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gomutants "github.com/P4suta/go-mutants"
	"github.com/P4suta/goatest/internal/assure"
	goanalysis "github.com/P4suta/goatest/internal/golang"
	"github.com/P4suta/goatest/internal/testkit"
)

func TestCollectBaselineBuildsOneBinaryPerPackageAndMapsTopLevelCoverage(t *testing.T) {
	artifacts := t.TempDir()
	workspace := testkit.NewWorkspace()
	workspace.On().Do(func(command gomutants.Command) (gomutants.CommandResult, error) {
		for _, argument := range command.Argv {
			if strings.HasPrefix(argument, "-test.coverprofile=") {
				path := strings.TrimPrefix(argument, "-test.coverprofile=")
				profile := "mode: set\nfixture.example/module/boundary.go:1.1,3.1 1 1\nfixture.example/module/unused.go:1.1,2.1 1 0\n"
				if err := os.WriteFile(path, []byte(profile), 0o644); err != nil {
					t.Fatal(err)
				}
			}
		}
		return gomutants.CommandResult{Duration: 1375 * time.Millisecond}, nil
	})
	model := goanalysis.Model{ModulePath: "fixture.example/module", Packages: []goanalysis.Package{{
		ImportPath: "fixture.example/module", RelativeDir: ".", Dependencies: []string{"fmt"},
	}}}
	targets := []assure.BaselineTarget{
		{Target: target("TestOne", goanalysis.KindTest)},
		{Target: target("FuzzTwo", goanalysis.KindFuzz), Environment: []string{"RESOURCE=ready"}},
	}

	result, err := assure.CollectBaseline(t.Context(), workspace, model, targets, assure.BaselineOptions{ArtifactDirectory: artifacts})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Findings) != 0 || len(result.Targets) != 2 {
		t.Fatalf("result = %+v", result)
	}
	for _, target := range result.Targets {
		if strings.Join(target.CoveredFiles, ",") != "boundary.go" {
			t.Errorf("coverage for %s = %v", target.Target.Name, target.CoveredFiles)
		}
		if target.Duration != 1375*time.Millisecond {
			t.Errorf("baseline duration for %s = %s", target.Target.Name, target.Duration)
		}
	}
	compileCount := 0
	invocationCount := 0
	commands := workspace.Calls()
	for _, command := range commands {
		if len(command.Argv) >= 3 && command.Argv[0] == "go" && command.Argv[1] == "test" && command.Argv[2] == "-c" {
			compileCount++
		}
		if len(command.Argv) > 0 && strings.HasSuffix(command.Argv[0], testBinarySuffix()) {
			invocationCount++
		}
	}
	if compileCount != 1 || invocationCount != 2 {
		t.Fatalf("compile=%d invoke=%d commands=%+v", compileCount, invocationCount, commands)
	}
}

func TestCollectBaselineClassifiesRepeatableFailureAndFlake(t *testing.T) {
	for _, testCase := range []struct {
		name      string
		exitCodes []int
		wantKind  string
	}{
		{name: "repeatable", exitCodes: []int{1, 1, 1}, wantKind: "baseline-failure"},
		{name: "flaky", exitCodes: []int{1, 0, 1}, wantKind: "flaky-test"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			index := 0
			workspace := testkit.NewWorkspace()
			workspace.On().Do(func(command gomutants.Command) (gomutants.CommandResult, error) {
				if len(command.Argv) > 0 && strings.HasSuffix(command.Argv[0], testBinarySuffix()) {
					code := testCase.exitCodes[index]
					index++
					return gomutants.CommandResult{ExitCode: code, Output: []byte("boom")}, nil
				}
				return gomutants.CommandResult{}, nil
			})
			model := goanalysis.Model{ModulePath: "fixture.example/module", Packages: []goanalysis.Package{{ImportPath: "fixture.example/module", RelativeDir: "."}}}
			result, err := assure.CollectBaseline(t.Context(), workspace, model, []assure.BaselineTarget{{Target: target("TestOne", goanalysis.KindTest)}}, assure.BaselineOptions{ArtifactDirectory: t.TempDir()})
			if err != nil {
				t.Fatal(err)
			}
			if len(result.Findings) != 1 || result.Findings[0].Kind != testCase.wantKind {
				t.Fatalf("findings = %+v", result.Findings)
			}
			if index != 3 {
				t.Fatalf("target attempts = %d, want 3", index)
			}
		})
	}
}

func TestCollectBaselineFailsAsInfrastructureWhenVetOrBuildCannotComplete(t *testing.T) {
	workspace := testkit.NewWorkspace()
	workspace.On().Do(func(command gomutants.Command) (gomutants.CommandResult, error) {
		if len(command.Argv) > 1 && command.Argv[1] == "vet" {
			return gomutants.CommandResult{ExitCode: 2, Output: []byte("bad package")}, nil
		}
		return gomutants.CommandResult{}, nil
	})
	_, err := assure.CollectBaseline(t.Context(), workspace, goanalysis.Model{ModulePath: "fixture.example/module"}, nil, assure.BaselineOptions{ArtifactDirectory: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "go vet") {
		t.Fatalf("error = %v", err)
	}
}

func testBinarySuffix() string {
	if filepath.Separator == '\\' {
		return ".test.exe"
	}
	return ".test"
}
