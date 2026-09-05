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
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	gomutants "github.com/P4suta/go-mutants"
	"github.com/P4suta/goatest/internal/checkpoint"
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

func TestCollectBaselineMergesInstrumentationAcrossTargetsInTargetOrder(t *testing.T) {
	profiles := map[string]string{
		"TestOne": "mode: set\n" +
			"fixture.example/module/shared.go:1.1,2.1 1 1\n" +
			"fixture.example/module/one.go:3.1,4.1 1 0\n",
		"TestTwo": "mode: set\n" +
			"fixture.example/module/shared.go:1.1,2.1 1 0\n" +
			"fixture.example/module/two.go:5.1,6.1 1 1\n",
	}
	collect := func(names ...string) []goanalysis.FileCoverage {
		t.Helper()
		workspace := &baselineFakeWorkspace{exec: func(command gomutants.Command) (gomutants.CommandResult, error) {
			for name, contents := range profiles {
				if !slices.Contains(command.Argv, "-test.run=^"+name+"$") {
					continue
				}
				if err := os.WriteFile(coverageProfileArgument(command), []byte(contents), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			return gomutants.CommandResult{Duration: time.Millisecond}, nil
		}}
		targets := make([]BaselineTarget, 0, len(names))
		for _, name := range names {
			targets = append(targets, BaselineTarget{Target: baselineTestTarget(name)})
		}
		result, err := CollectBaseline(t.Context(), workspace, baselineModel(), targets, BaselineOptions{ArtifactDirectory: t.TempDir()})
		if err != nil || len(result.Targets) != len(names) {
			t.Fatalf("CollectBaseline(%v) = (%+v, %v)", names, result, err)
		}
		return result.Instrumented
	}
	want := []goanalysis.FileCoverage{
		{Path: "one.go", Blocks: []goanalysis.CoverageBlock{{StartLine: 3, StartColumn: 1, EndLine: 4, EndColumn: 1}}},
		{Path: "shared.go", Blocks: []goanalysis.CoverageBlock{{StartLine: 1, StartColumn: 1, EndLine: 2, EndColumn: 1}}},
		{Path: "two.go", Blocks: []goanalysis.CoverageBlock{{StartLine: 5, StartColumn: 1, EndLine: 6, EndColumn: 1}}},
	}
	forward := collect("TestOne", "TestTwo")
	if !reflect.DeepEqual(forward, want) {
		t.Fatalf("instrumented = %+v, want %+v", forward, want)
	}
	if reversed := collect("TestTwo", "TestOne"); !reflect.DeepEqual(reversed, forward) {
		t.Fatalf("reversed instrumented = %+v, want %+v", reversed, forward)
	}
}

func TestPackageSuiteCoverageMeasuresTheExactFallbackAndFailsClosed(t *testing.T) {
	t.Parallel()
	block := "fixture.example/module/value.go:7.2,9.3 1 1\n"
	targets := []TargetEvidence{
		{Target: baselineTestTarget("TestOne"), Duration: 100 * time.Millisecond},
		{Target: baselineTestTarget("TestTwo"), Duration: 200 * time.Millisecond},
	}
	t.Run("passing suite", func(t *testing.T) {
		workspace := &baselineFakeWorkspace{exec: func(command gomutants.Command) (gomutants.CommandResult, error) {
			if command.Dir != "internal/example" || command.Timeout != 1300*time.Millisecond ||
				!slices.Equal(command.Env, []string{"DB=ready"}) ||
				!slices.Contains(command.Argv, "-test.count=1") ||
				!slices.Contains(command.Argv, "-test.short=true") || baselineCommandTarget(command) != "" {
				t.Fatalf("suite command = %+v", command)
			}
			if err := os.WriteFile(coverageProfileArgument(command), []byte("mode: set\n"+block), 0o600); err != nil {
				t.Fatal(err)
			}
			return gomutants.CommandResult{Duration: 275 * time.Millisecond}, nil
		}}
		got, measured, err := collectPackageSuiteCoverage(
			t.Context(), workspace, "fixture.example/module", "fixture.example/module",
			"internal/example", "/tmp/example.test", 5*time.Second, targets,
			BaselineOptions{
				ArtifactDirectory: t.TempDir(), Contract: "standard-v1",
				TestArgs: []string{"-test.short=true"}, SuiteEnvironment: []string{"DB=ready"},
			},
		)
		want := []goanalysis.FileCoverage{{Path: "value.go", Blocks: []goanalysis.CoverageBlock{infectionBlock()}}}
		if err != nil || !measured || got.Duration != 275*time.Millisecond || !reflect.DeepEqual(got.Covered, want) || !reflect.DeepEqual(got.Instrumented, want) {
			t.Fatalf("package suite = (%+v, %t, %v), want measured coverage %+v", got, measured, err, want)
		}
	})
	t.Run("failed suite supplies no fact", func(t *testing.T) {
		workspace := &baselineFakeWorkspace{exec: func(gomutants.Command) (gomutants.CommandResult, error) {
			return gomutants.CommandResult{ExitCode: 1}, nil
		}}
		got, measured, err := collectPackageSuiteCoverage(
			t.Context(), workspace, "fixture.example/module", "fixture.example/module",
			".", "/tmp/example.test", 5*time.Second, targets,
			BaselineOptions{ArtifactDirectory: t.TempDir(), Contract: "standard-v1"},
		)
		if err != nil || measured || !reflect.DeepEqual(got, PackageSuiteCoverage{}) {
			t.Fatalf("failed package suite = (%+v, %t, %v), want no fact", got, measured, err)
		}
	})
}

type parallelPackageSuiteWorkspace struct {
	mu       sync.Mutex
	active   int
	maximum  int
	started  chan string
	finished chan string
	gates    map[string]chan struct{}
	failures map[string]error
}

func (workspace *parallelPackageSuiteWorkspace) Exec(_ context.Context, command gomutants.Command) (gomutants.CommandResult, error) {
	name := command.Dir
	workspace.mu.Lock()
	workspace.active++
	workspace.maximum = max(workspace.maximum, workspace.active)
	workspace.mu.Unlock()
	workspace.started <- name
	<-workspace.gates[name]
	err := workspace.failures[name]
	if err == nil {
		err = os.WriteFile(coverageProfileArgument(command), []byte(
			"mode: set\nfixture.example/module/value.go:1.1,2.1 1 1\n",
		), 0o600)
	}
	workspace.mu.Lock()
	workspace.active--
	workspace.mu.Unlock()
	workspace.finished <- name
	return gomutants.CommandResult{Duration: 50 * time.Millisecond}, err
}

func TestPackageSuiteCoverageRunsAcrossPackagesAndPublishesInInputOrder(t *testing.T) {
	t.Parallel()
	controls := []packageSuiteControl{
		{importPath: "fixture.example/module/a", relativeDir: "a", binary: "/tmp/a.test"},
		{importPath: "fixture.example/module/b", relativeDir: "b", binary: "/tmp/b.test"},
	}
	firstErr := errors.New("first package failed")
	workspace := &parallelPackageSuiteWorkspace{
		started: make(chan string, len(controls)), finished: make(chan string, len(controls)),
		gates:    map[string]chan struct{}{"a": make(chan struct{}), "b": make(chan struct{})},
		failures: map[string]error{"a": firstErr},
	}
	artifactDirectory := t.TempDir()
	answers := make(chan []packageSuiteCoverageRun, 1)
	go func() {
		answers <- collectPackageSuiteCoverages(
			t.Context(), workspace, "fixture.example/module", controls, time.Second, nil,
			BaselineOptions{ArtifactDirectory: artifactDirectory, Contract: "standard-v1", Jobs: 2},
		)
	}()
	for range controls {
		select {
		case <-workspace.started:
		case <-time.After(10 * time.Second):
			t.Fatal("package-suite controls did not start concurrently")
		}
	}
	close(workspace.gates["b"])
	if finished := <-workspace.finished; finished != "b" {
		t.Fatalf("first completion = %q, want b", finished)
	}
	close(workspace.gates["a"])
	if finished := <-workspace.finished; finished != "a" {
		t.Fatalf("second completion = %q, want a", finished)
	}
	runs := <-answers
	workspace.mu.Lock()
	maximum := workspace.maximum
	workspace.mu.Unlock()
	if maximum != 2 || len(runs) != 2 {
		t.Fatalf("maximum concurrent controls = %d, runs = %+v", maximum, runs)
	}
	if runs[0].importPath != controls[0].importPath || !errors.Is(runs[0].err, firstErr) ||
		runs[1].importPath != controls[1].importPath || runs[1].err != nil || !runs[1].measured {
		t.Fatalf("ordered suite results = %+v", runs)
	}
}

type parallelBaselineWorkspace struct {
	mu       sync.Mutex
	active   int
	maximum  int
	started  chan string
	finished chan string
	gates    map[string]chan struct{}
	failures map[string]error
}

func (workspace *parallelBaselineWorkspace) Exec(_ context.Context, command gomutants.Command) (gomutants.CommandResult, error) {
	name := baselineCommandTarget(command)
	if name == "" {
		return gomutants.CommandResult{Duration: 1250 * time.Millisecond}, nil
	}
	workspace.mu.Lock()
	workspace.active++
	workspace.maximum = max(workspace.maximum, workspace.active)
	workspace.mu.Unlock()
	workspace.started <- name
	<-workspace.gates[name]
	err := workspace.failures[name]
	if err == nil {
		profile := coverageProfileArgument(command)
		contents := "mode: set\nfixture.example/module/value.go:1.1,2.1 1 1\n"
		err = os.WriteFile(profile, []byte(contents), 0o600)
	}
	workspace.mu.Lock()
	workspace.active--
	workspace.mu.Unlock()
	workspace.finished <- name
	return gomutants.CommandResult{Duration: 1250 * time.Millisecond}, err
}

func TestCollectBaselineSelectsTheFirstTargetErrorAfterConcurrentCompletion(t *testing.T) {
	names := []string{"TestOne", "TestTwo"}
	targets := make([]BaselineTarget, len(names))
	gates := make(map[string]chan struct{}, len(names))
	firstErr := errors.New("first target failed")
	secondErr := errors.New("second target failed")
	for index, name := range names {
		targets[index] = BaselineTarget{Target: baselineTestTarget(name)}
		gates[name] = make(chan struct{})
	}
	workspace := &parallelBaselineWorkspace{
		started: make(chan string, len(names)), finished: make(chan string, len(names)), gates: gates,
		failures: map[string]error{"TestOne": firstErr, "TestTwo": secondErr},
	}
	type answer struct{ err error }
	answers := make(chan answer, 1)
	go func() {
		_, err := CollectBaseline(t.Context(), workspace, baselineModel(), targets, BaselineOptions{
			ArtifactDirectory: t.TempDir(), Jobs: len(names),
		})
		answers <- answer{err: err}
	}()
	for range names {
		select {
		case <-workspace.started:
		case <-time.After(10 * time.Second):
			t.Fatal("concurrent failing baseline controls did not start")
		}
	}
	close(gates["TestTwo"])
	if finished := <-workspace.finished; finished != "TestTwo" {
		t.Fatalf("first completion = %s, want TestTwo", finished)
	}
	close(gates["TestOne"])
	if finished := <-workspace.finished; finished != "TestOne" {
		t.Fatalf("second completion = %s, want TestOne", finished)
	}
	got := (<-answers).err
	if !errors.Is(got, firstErr) || errors.Is(got, secondErr) || !strings.Contains(got.Error(), "baseline target TestOne") {
		t.Fatalf("concurrent baseline error = %v, want first target error", got)
	}
}

func baselineCommandTarget(command gomutants.Command) string {
	for _, argument := range command.Argv {
		if name, found := strings.CutPrefix(argument, "-test.run=^"); found {
			return strings.TrimSuffix(name, "$")
		}
	}
	return ""
}

// TestCollectBaselineMeasuresInParallelAndCommitsInTargetOrder holds both
// halves of baseline concurrency: three controls really overlap, while their
// reverse completion order cannot change the serial result or checkpoint
// sequence.
func TestCollectBaselineMeasuresInParallelAndCommitsInTargetOrder(t *testing.T) {
	names := []string{"TestOne", "TestTwo", "TestThree"}
	targets := make([]BaselineTarget, len(names))
	for index, name := range names {
		targets[index] = BaselineTarget{Target: baselineTestTarget(name)}
	}
	serialWorkspace := &baselineFakeWorkspace{exec: passingBaselineExec(t, "fixture.example/module", true)}
	serial, err := CollectBaseline(t.Context(), serialWorkspace, baselineModel(), targets, BaselineOptions{ArtifactDirectory: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}

	gates := make(map[string]chan struct{}, len(names))
	for _, name := range names {
		gates[name] = make(chan struct{})
	}
	workspace := &parallelBaselineWorkspace{
		started: make(chan string, len(names)), finished: make(chan string, len(names)), gates: gates,
	}
	checkpointSizes := make([]int, 0, len(names)+2)
	type answer struct {
		result BaselineResult
		err    error
	}
	answers := make(chan answer, 1)
	artifactDirectory := t.TempDir()
	go func() {
		result, collectErr := CollectBaseline(t.Context(), workspace, baselineModel(), targets, BaselineOptions{
			ArtifactDirectory: artifactDirectory,
			Jobs:              len(names),
			Checkpoint: func(state checkpoint.Baseline) {
				checkpointSizes = append(checkpointSizes, len(state.Targets))
			},
		})
		answers <- answer{result: result, err: collectErr}
	}()

	started := make(map[string]bool, len(names))
	for range names {
		select {
		case name := <-workspace.started:
			started[name] = true
		case <-time.After(10 * time.Second):
			t.Fatalf("only %d of %d baseline controls started concurrently", len(started), len(names))
		}
	}
	for index := len(names) - 1; index >= 0; index-- {
		close(gates[names[index]])
		select {
		case finished := <-workspace.finished:
			if finished != names[index] {
				t.Fatalf("finished %s, want released target %s", finished, names[index])
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("baseline target %s did not finish", names[index])
		}
	}
	var parallel answer
	select {
	case parallel = <-answers:
	case <-time.After(10 * time.Second):
		t.Fatal("parallel baseline did not return")
	}
	if parallel.err != nil {
		t.Fatal(parallel.err)
	}
	workspace.mu.Lock()
	maximum := workspace.maximum
	workspace.mu.Unlock()
	if maximum != len(names) || len(started) != len(names) {
		t.Fatalf("maximum concurrent controls = %d, started = %v", maximum, started)
	}
	if !reflect.DeepEqual(parallel.result, serial) {
		t.Fatalf("parallel result = %+v, want serial %+v", parallel.result, serial)
	}
	if want := []int{0, 1, 2, 3, 3}; !slices.Equal(checkpointSizes, want) {
		t.Fatalf("checkpoint target counts = %v, want %v", checkpointSizes, want)
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
