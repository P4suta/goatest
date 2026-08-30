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
	"time"
	"unicode/utf8"

	gomutants "github.com/P4suta/go-mutants"
	goanalysis "github.com/P4suta/goatest/internal/golang"
	"github.com/P4suta/goatest/internal/report"
)

type baselineFakeWorkspace struct {
	commands []gomutants.Command
	exec     func(gomutants.Command) (gomutants.CommandResult, error)
}

func (workspace *baselineFakeWorkspace) Exec(_ context.Context, command gomutants.Command) (gomutants.CommandResult, error) {
	workspace.commands = append(workspace.commands, command)
	return workspace.exec(command)
}

func TestCollectBaselineRejectsInvalidInputsAndArtifactCreationFailure(t *testing.T) {
	options := BaselineOptions{ArtifactDirectory: t.TempDir()}
	if result, err := CollectBaseline(t.Context(), nil, goanalysis.Model{}, nil, options); err == nil || !reflect.DeepEqual(result, BaselineResult{}) || err.Error() != "goatest: nil baseline workspace" {
		t.Fatalf("nil workspace = (%+v, %v)", result, err)
	}
	workspace := &baselineFakeWorkspace{exec: func(gomutants.Command) (gomutants.CommandResult, error) {
		t.Fatal("workspace called for invalid baseline options")
		return gomutants.CommandResult{}, nil
	}}
	if result, err := CollectBaseline(t.Context(), workspace, goanalysis.Model{}, nil, BaselineOptions{}); err == nil || !reflect.DeepEqual(result, BaselineResult{}) || err.Error() != "goatest: baseline requires an artifact directory" {
		t.Fatalf("empty artifact directory = (%+v, %v)", result, err)
	}
	file := filepath.Join(t.TempDir(), "artifact-file")
	if err := os.WriteFile(file, []byte("blocked"), 0o600); err != nil {
		t.Fatal(err)
	}
	if result, err := CollectBaseline(t.Context(), workspace, goanalysis.Model{}, nil, BaselineOptions{ArtifactDirectory: file}); err == nil || !reflect.DeepEqual(result, BaselineResult{}) || !strings.HasPrefix(err.Error(), "goatest: create baseline artifact directory: ") {
		t.Fatalf("artifact creation failure = (%+v, %v)", result, err)
	}
}

func TestCollectBaselineHonorsDefaultAndExplicitTimeoutsAndClonesEnvironment(t *testing.T) {
	for _, test := range []struct {
		name        string
		command     time.Duration
		target      time.Duration
		wantCommand time.Duration
		wantTarget  time.Duration
	}{
		{name: "defaults at zero", wantCommand: 10 * time.Minute, wantTarget: 10 * time.Minute},
		{name: "defaults below zero", command: -time.Second, target: -time.Second, wantCommand: 10 * time.Minute, wantTarget: 10 * time.Minute},
		{name: "explicit", command: 2 * time.Second, target: 3 * time.Second, wantCommand: 2 * time.Second, wantTarget: 3 * time.Second},
	} {
		t.Run(test.name, func(t *testing.T) {
			artifactDirectory := t.TempDir()
			workspace := &baselineFakeWorkspace{}
			workspace.exec = passingBaselineExec(t, "fixture.example/module", true)
			environment := []string{"RESOURCE=ready"}
			result, err := CollectBaseline(t.Context(), workspace, baselineModel(), []BaselineTarget{{
				Target: baselineTestTarget("TestValue"), Environment: environment,
			}}, BaselineOptions{ArtifactDirectory: artifactDirectory, CommandTimeout: test.command, TargetTimeout: test.target})
			if err != nil || len(result.Targets) != 1 || len(result.Evidence) != 3 || len(result.Findings) != 0 {
				t.Fatalf("CollectBaseline = (%+v, %v)", result, err)
			}
			if len(workspace.commands) != 4 {
				t.Fatalf("commands = %d, want 4", len(workspace.commands))
			}
			for index, command := range workspace.commands[:3] {
				if command.Timeout != test.wantCommand {
					t.Errorf("command %d timeout = %s, want %s", index, command.Timeout, test.wantCommand)
				}
			}
			targetCommand := workspace.commands[3]
			if targetCommand.Timeout != test.wantTarget || !slices.Equal(targetCommand.Env, environment) {
				t.Fatalf("target command = %+v", targetCommand)
			}
			environment[0] = "MUTATED=yes"
			if targetCommand.Env[0] != "RESOURCE=ready" || result.Targets[0].Environment[0] != "RESOURCE=ready" {
				t.Fatal("baseline result or command aliases target environment")
			}
		})
	}
}

func TestCollectBaselinePropagatesEveryInfrastructureAndCoverageFailure(t *testing.T) {
	for _, test := range []struct {
		stage   string
		message string
	}{
		{stage: "vet exec", message: "goatest: go vet:"},
		{stage: "build exec", message: "goatest: go build:"},
		{stage: "compile missing package", message: "target package fixture.example/module was absent from go list"},
		{stage: "compile exec", message: "compile test binary for fixture.example/module:"},
		{stage: "compile timeout", message: "compile test binary for fixture.example/module failed (exit=0 timeout=true): compile timeout"},
		{stage: "compile exit", message: "compile test binary for fixture.example/module failed (exit=2 timeout=false): compile failed"},
		{stage: "target exec", message: "baseline target TestValue:"},
		{stage: "retry exec", message: "repeat baseline target TestValue:"},
		{stage: "coverage read", message: "read coverage for TestValue:"},
		{stage: "coverage parse", message: "coverage for TestValue:"},
	} {
		t.Run(test.stage, func(t *testing.T) {
			sentinel := errors.New(test.stage + " sentinel")
			targetCalls := 0
			workspace := &baselineFakeWorkspace{}
			workspace.exec = func(command gomutants.Command) (gomutants.CommandResult, error) {
				switch {
				case len(command.Argv) > 1 && command.Argv[0] == "go" && command.Argv[1] == "vet":
					if test.stage == "vet exec" {
						return gomutants.CommandResult{}, sentinel
					}
				case len(command.Argv) > 1 && command.Argv[0] == "go" && command.Argv[1] == "build":
					if test.stage == "build exec" {
						return gomutants.CommandResult{}, sentinel
					}
				case len(command.Argv) > 2 && command.Argv[0] == "go" && command.Argv[1] == "test" && command.Argv[2] == "-c":
					switch test.stage {
					case "compile exec":
						return gomutants.CommandResult{}, sentinel
					case "compile timeout":
						return gomutants.CommandResult{TimedOut: true, Output: []byte("compile timeout")}, nil
					case "compile exit":
						return gomutants.CommandResult{ExitCode: 2, Output: []byte("compile failed")}, nil
					}
				default:
					targetCalls++
					if test.stage == "target exec" {
						return gomutants.CommandResult{}, sentinel
					}
					if test.stage == "retry exec" {
						if targetCalls == 1 {
							return gomutants.CommandResult{ExitCode: 1}, nil
						}
						return gomutants.CommandResult{}, sentinel
					}
					if test.stage != "coverage read" {
						profile := coverageProfileArgument(command)
						contents := "mode: set\nfixture.example/module/value.go:1.1,2.1 1 1\n"
						if test.stage == "coverage parse" {
							contents = "invalid coverage"
						}
						if err := os.WriteFile(profile, []byte(contents), 0o600); err != nil {
							t.Fatal(err)
						}
					}
				}
				return gomutants.CommandResult{}, nil
			}
			model := baselineModel()
			if test.stage == "compile missing package" {
				model.Packages = nil
			}
			result, err := CollectBaseline(t.Context(), workspace, model, []BaselineTarget{{Target: baselineTestTarget("TestValue")}}, BaselineOptions{ArtifactDirectory: t.TempDir()})
			if err == nil || !reflect.DeepEqual(result, BaselineResult{}) || !strings.Contains(err.Error(), test.message) {
				t.Fatalf("CollectBaseline(%s) = (%+v, %v), want message %q", test.stage, result, err, test.message)
			}
			if strings.Contains(test.stage, "exec") && !errors.Is(err, sentinel) {
				t.Fatalf("CollectBaseline(%s) lost cause: %v", test.stage, err)
			}
		})
	}
}

func TestClassifyTargetFailureDistinguishesFlakeTimeoutAndStableFailure(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name     string
		attempts []gomutants.CommandResult
		kind     string
		summary  string
	}{
		{
			name: "failure status changes", attempts: []gomutants.CommandResult{{ExitCode: 1}, {}, {ExitCode: 1}},
			kind: "flaky-test", summary: "baseline target produced inconsistent results across three attempts",
		},
		{
			name: "timeout changes", attempts: []gomutants.CommandResult{{ExitCode: 1}, {ExitCode: 1, TimedOut: true}, {ExitCode: 1}},
			kind: "flaky-test", summary: "baseline target produced inconsistent results across three attempts",
		},
		{
			name: "exit code changes", attempts: []gomutants.CommandResult{{ExitCode: 1}, {ExitCode: 2}, {ExitCode: 1}},
			kind: "flaky-test", summary: "baseline target produced inconsistent results across three attempts",
		},
		{
			name: "stable timeout", attempts: []gomutants.CommandResult{{TimedOut: true}, {TimedOut: true}, {TimedOut: true}},
			kind: "baseline-timeout", summary: "baseline target timed out in three consecutive attempts",
		},
		{
			name: "stable failure", attempts: []gomutants.CommandResult{{ExitCode: 1, Output: []byte(" boom\n")}, {ExitCode: 1}, {ExitCode: 1}},
			kind: "baseline-failure", summary: "baseline target failed in three consecutive attempts: boom",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			kind, summary := classifyTargetFailure(test.attempts)
			if kind != test.kind || summary != test.summary {
				t.Fatalf("classifyTargetFailure = (%q, %q), want (%q, %q)", kind, summary, test.kind, test.summary)
			}
		})
	}
}

func TestClassifyTest2JSONIgnoresMixedStderrButRetainsScannerFailures(t *testing.T) {
	output := []byte("plain stderr before JSON\n" +
		`{"Action":"output","Test":"TestValue","Output":"running"}` + "\n" +
		"another malformed line\n" +
		`{"Action":"skip","Test":"TestValue/subtest","Output":"skipped"}` + "\n")
	skipped, kind, summary, err := classifyTest2JSON("TestValue", output)
	if err != nil || !skipped || kind != "skipped-subtest" || summary != "a selected subtest was skipped: TestValue/subtest" {
		t.Fatalf("mixed test2json = (%t, %q, %q, %v)", skipped, kind, summary, err)
	}
	if skipped, kind, summary, err = classifyTest2JSON("TestValue", []byte(strings.Repeat("x", (4<<20)+1))); err == nil || skipped || kind != "" || summary != "" {
		t.Fatalf("oversized test2json = (%t, %q, %q, %v)", skipped, kind, summary, err)
	}
}

func TestSummarizeIsBoundedTrimmedAndValidUTF8(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		output string
		want   string
	}{
		{name: "empty", output: " \r\n\t", want: "no output"},
		{name: "short", output: "  useful output\n", want: "useful output"},
		{name: "exact boundary", output: strings.Repeat("a", 512), want: strings.Repeat("a", 512)},
		{name: "over boundary", output: strings.Repeat("a", 513), want: strings.Repeat("a", 512) + "…"},
		{name: "unicode boundary", output: strings.Repeat("界", 513), want: strings.Repeat("界", 512) + "…"},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := summarize([]byte(test.output))
			if got != test.want || !utf8.ValidString(got) {
				t.Fatalf("summarize = %q (valid=%t), want %q", got, utf8.ValidString(got), test.want)
			}
		})
	}
}

func TestTargetFindingAndCommandAreDeterministicAndDoNotAliasInputs(t *testing.T) {
	t.Parallel()
	target := baselineTestTarget("TestValue")
	target.Path, target.Line = "value_test.go", 17
	finding := targetFinding(target, "baseline-failure", "boom")
	wantID := report.FindingID("target", target.ID, "baseline-failure")
	if finding.ID != wantID || finding.Kind != "baseline-failure" || finding.Path != target.Path || finding.Line != 17 || finding.Summary != "boom" || finding.Replay != "goatest replay "+wantID {
		t.Fatalf("finding = %+v", finding)
	}
	environment := []string{"RESOURCE=ready"}
	command := targetCommand("baseline.test", "value.cover", "pkg", BaselineTarget{Target: target, Environment: environment}, 5*time.Second)
	wantArgv := []string{"baseline.test", "-test.run=^TestValue$", "-test.coverprofile=value.cover", "-test.count=1"}
	if !slices.Equal(command.Argv, wantArgv) || command.Dir != "pkg" || !slices.Equal(command.Env, environment) || command.Timeout != 5*time.Second {
		t.Fatalf("target command = %+v", command)
	}
	environment[0] = "MUTATED=yes"
	if command.Env[0] != "RESOURCE=ready" {
		t.Fatal("target command aliases environment")
	}
	if name := binaryName("fixture.example/module"); !strings.HasSuffix(name, testBinarySuffixInternal()) || len(strings.TrimSuffix(name, testBinarySuffixInternal())) != 16 {
		t.Fatalf("binaryName = %q", name)
	}
}

func baselineModel() goanalysis.Model {
	return goanalysis.Model{ModulePath: "fixture.example/module", Packages: []goanalysis.Package{{
		ImportPath: "fixture.example/module", RelativeDir: ".",
	}}}
}

func baselineTestTarget(name string) goanalysis.Target {
	return goanalysis.Target{ID: "target-" + name, Name: name, Kind: goanalysis.KindTest, Package: "fixture.example/module", RelativeDir: "."}
}

func passingBaselineExec(t *testing.T, module string, writeCoverage bool) func(gomutants.Command) (gomutants.CommandResult, error) {
	t.Helper()
	return func(command gomutants.Command) (gomutants.CommandResult, error) {
		if len(command.Argv) > 0 && command.Argv[0] != "go" && writeCoverage {
			profile := coverageProfileArgument(command)
			contents := "mode: set\n" + module + "/value.go:1.1,2.1 1 1\n"
			if err := os.WriteFile(profile, []byte(contents), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		return gomutants.CommandResult{Duration: 1250 * time.Millisecond}, nil
	}
}

func coverageProfileArgument(command gomutants.Command) string {
	for _, argument := range command.Argv {
		if strings.HasPrefix(argument, "-test.coverprofile=") {
			return strings.TrimPrefix(argument, "-test.coverprofile=")
		}
	}
	return ""
}

func testBinarySuffixInternal() string {
	if os.PathSeparator == '\\' {
		return ".test.exe"
	}
	return ".test"
}

var _ CommandWorkspace = (*baselineFakeWorkspace)(nil)
