// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package assure_test

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	gomutants "github.com/P4suta/go-mutants"
	"github.com/P4suta/goatest/internal/assure"
	"github.com/P4suta/goatest/internal/filemode"
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
				profile := "mode: set\n" +
					"fixture.example/module/boundary.go:5.29,6.16 1 1\n" +
					"fixture.example/module/boundary.go:6.16,8.3 1 1\n" +
					"fixture.example/module/boundary.go:9.2,9.10 1 1\n" +
					"fixture.example/module/unused.go:3.14,5.2 1 0\n"
				if err := os.WriteFile(path, []byte(profile), filemode.ReadableFile); err != nil {
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
	wantBlocks := []goanalysis.FileCoverage{{Path: "boundary.go", Blocks: []goanalysis.CoverageBlock{
		{StartLine: 5, StartColumn: 29, EndLine: 6, EndColumn: 16},
		{StartLine: 6, StartColumn: 16, EndLine: 8, EndColumn: 3},
		{StartLine: 9, StartColumn: 2, EndLine: 9, EndColumn: 10},
	}}}
	for _, target := range result.Targets {
		if strings.Join(target.CoveredFiles, ",") != "boundary.go" {
			t.Errorf("coverage for %s = %v", target.Target.Name, target.CoveredFiles)
		}
		if !reflect.DeepEqual(target.Covered, wantBlocks) {
			t.Errorf("covered blocks for %s = %+v, want %+v", target.Target.Name, target.Covered, wantBlocks)
		}
		if target.Duration != 1375*time.Millisecond {
			t.Errorf("baseline duration for %s = %s", target.Target.Name, target.Duration)
		}
	}
	if paths := goanalysis.CoveredPaths(result.Instrumented); !slices.Equal(paths, []string{"boundary.go", "unused.go"}) {
		t.Errorf("instrumented files = %v", paths)
	}
	compileCount := 0
	invocationCount := 0
	commands := workspace.Calls()
	for _, command := range commands {
		if len(command.Argv) >= 3 && command.Argv[0] == "go" && command.Argv[1] == "test" && command.Argv[2] == "-c" {
			compileCount++
			if !slices.Contains(command.Argv, "-coverpkg=fixture.example/module") {
				t.Fatalf("compile command = %q, want only the test binary's module import closure", command.Argv)
			}
		}
		if len(command.Argv) > 0 && strings.HasSuffix(command.Argv[0], testBinarySuffix()) {
			invocationCount++
		}
	}
	if compileCount != 1 || invocationCount != 2 {
		t.Fatalf("compile=%d invoke=%d commands=%+v", compileCount, invocationCount, commands)
	}
}

func TestCollectBaselineReportsTheFirstFailureWithoutRetry(t *testing.T) {
	invocations := 0
	workspace := testkit.NewWorkspace()
	workspace.On().Do(func(command gomutants.Command) (gomutants.CommandResult, error) {
		if len(command.Argv) > 0 && strings.HasSuffix(command.Argv[0], testBinarySuffix()) {
			invocations++
			return gomutants.CommandResult{ExitCode: 1, Output: []byte("boom")}, nil
		}
		return gomutants.CommandResult{}, nil
	})
	model := goanalysis.Model{ModulePath: "fixture.example/module", Packages: []goanalysis.Package{{ImportPath: "fixture.example/module", RelativeDir: "."}}}
	result, err := assure.CollectBaseline(t.Context(), workspace, model, []assure.BaselineTarget{{Target: target("TestOne", goanalysis.KindTest)}}, assure.BaselineOptions{ArtifactDirectory: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Findings) != 1 || result.Findings[0].Kind != "baseline-failure" || result.Executed != 1 || result.Skipped != 0 || invocations != 1 {
		t.Fatalf("baseline = %+v, invocations = %d", result, invocations)
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
