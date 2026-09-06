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
	"github.com/P4suta/goatest/internal/filemode"
	goanalysis "github.com/P4suta/goatest/internal/golang"
	"github.com/P4suta/goatest/internal/report"
	"github.com/P4suta/goatest/internal/trace"
)

const (
	parallelBaselineDeadline            = 10 * time.Second
	baselineCommandCount                = 4
	checkpointCompletionRecords         = 2
	combinedProbeExecutionCount         = 2
	freshPackageSuiteRunCount           = 2
	preparedProbeIndex           uint32 = 7
	preparedProbeDuration               = 1250 * time.Millisecond
	targetFindingSourceLine             = 17
	targetCommandFixtureDeadline        = 5 * time.Second
)

type baselineFakeWorkspace struct {
	mu       sync.Mutex
	commands []gomutants.Command
	exec     func(gomutants.Command) (gomutants.CommandResult, error)
}

func (workspace *baselineFakeWorkspace) Exec(_ context.Context, command gomutants.Command) (gomutants.CommandResult, error) {
	workspace.mu.Lock()
	workspace.commands = append(workspace.commands, command)
	workspace.mu.Unlock()
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
	if err := os.WriteFile(file, []byte("blocked"), filemode.PrivateFile); err != nil {
		t.Fatal(err)
	}
	if result, err := CollectBaseline(t.Context(), workspace, goanalysis.Model{}, nil, BaselineOptions{ArtifactDirectory: file}); err == nil || !reflect.DeepEqual(result, BaselineResult{}) || !strings.HasPrefix(err.Error(), "goatest: create baseline artifact directory: ") {
		t.Fatalf("artifact creation failure = (%+v, %v)", result, err)
	}
}

func TestBaselineCoveragePackagesAreTheExactModuleImportClosure(t *testing.T) {
	t.Parallel()
	pkg := goanalysis.Package{
		ImportPath: "fixture.example/module/subject",
		Dependencies: []string{
			"fixture.example/module/z", "example.net/external", "fixture.example/module/a",
			"fixture.example/module", "fixture.example/module/a", "fixture.example/module-other",
		},
	}
	want := []string{
		"fixture.example/module", "fixture.example/module/a", "fixture.example/module/subject", "fixture.example/module/z",
	}
	if got := baselineCoveragePackages("fixture.example/module", pkg); !slices.Equal(got, want) {
		t.Fatalf("baseline coverage packages = %q, want exact import closure %q", got, want)
	}
}

func TestCollectBaselineStopsAfterProjectChecks(t *testing.T) {
	workspace := &baselineFakeWorkspace{exec: func(gomutants.Command) (gomutants.CommandResult, error) {
		return gomutants.CommandResult{}, nil
	}}
	var states []checkpoint.Baseline
	result, err := CollectBaseline(t.Context(), workspace, baselineModel(), []BaselineTarget{{
		Target: baselineTestTarget("TestValue"),
	}}, BaselineOptions{
		ArtifactDirectory: t.TempDir(), StopAfterChecks: true,
		Checkpoint: func(state checkpoint.Baseline) { states = append(states, state) },
	})
	if err != nil || len(workspace.commands) != 2 || len(result.Targets) != 0 || len(states) != 1 ||
		!states[0].BuildVetComplete || states[0].Complete {
		t.Fatalf("checks = (%+v, %v), commands=%d states=%+v", result, err, len(workspace.commands), states)
	}
}

func TestCollectBaselineUsesPreparedProbeForCoverageAndInfection(t *testing.T) {
	workspace := &baselineFakeWorkspace{exec: func(gomutants.Command) (gomutants.CommandResult, error) {
		return gomutants.CommandResult{}, nil
	}}
	session := &mutationUnitSession{catalog: gomutants.Catalog{Mutants: []gomutants.Mutant{{
		Index: preparedProbeIndex, ID: "mutant", Accepted: true, Probed: true,
		Package: "fixture.example/module", Path: "other.go", Line: 1, Column: 1,
	}}}}
	session.probe = func(request gomutants.ProbeRequest) (gomutants.ProbeResult, error) {
		profile := coverageProfileArgument(gomutants.Command{Argv: request.Args})
		if profile == "" {
			t.Fatalf("probe request has no coverage profile: %+v", request)
		}
		contents := "mode: set\nfixture.example/module/value.go:1.1,2.1 1 1\n"
		if err := os.WriteFile(profile, []byte(contents), filemode.PrivateFile); err != nil {
			t.Fatal(err)
		}
		return gomutants.ProbeResult{
			Outcome: gomutants.ProbeMeasured, Infected: []uint32{preparedProbeIndex},
			Duration: preparedProbeDuration, Output: []byte("PASS\n"),
		}, nil
	}
	recording, recorder := newProbeRecording()
	result, err := CollectBaseline(t.Context(), workspace, baselineModel(), []BaselineTarget{{
		Target: baselineTestTarget("TestValue"), Environment: []string{"RESOURCE=ready"},
	}}, BaselineOptions{
		ArtifactDirectory: t.TempDir(), Contract: "standard-v1", PackageSuites: true,
		UseTestFraming: true, ProbeSession: session, Trace: recorder,
	})
	if err != nil || len(workspace.commands) != 2 || len(result.Targets) != 1 || len(session.probeRequests()) != combinedProbeExecutionCount {
		t.Fatalf("prepared baseline = (%+v, %v), commands=%d probes=%+v", result, err, len(workspace.commands), session.probeRequests())
	}
	target := result.Targets[0]
	if !target.Probed || !slices.Equal(target.Infected, []uint32{preparedProbeIndex}) || target.Duration != preparedProbeDuration ||
		target.ProbeDuration != 0 || !slices.Equal(target.Environment, []string{"RESOURCE=ready"}) {
		t.Fatalf("target = %+v", target)
	}
	suite, ok := result.ProbeSuites["fixture.example/module"]
	if !ok || !suite.Measured || !slices.Equal(suite.Infected, []uint32{preparedProbeIndex}) || suite.Duration != 0 {
		t.Fatalf("suite probe = %+v, found=%t", suite, ok)
	}
	for _, request := range session.probeRequests() {
		if request.Package != "fixture.example/module" {
			t.Fatalf("probe request = %+v", request)
		}
	}
	validateProbeLines(t, recording.Lines())
	records := probeRecords(t, recording)
	targetRecord := records[target.Target.ID]
	if targetRecord.Suite || targetRecord.Outcome != trace.ProbeOutcomeMeasured ||
		targetRecord.DurationMS != traceMilliseconds(preparedProbeDuration) || !slices.Equal(targetRecord.Infected, []string{"mutant"}) {
		t.Fatalf("target probe record = %+v", targetRecord)
	}
	suiteRecord := records[packageSuiteProbeTarget(target.Target.Package)]
	if !suiteRecord.Suite || suiteRecord.Outcome != trace.ProbeOutcomeMeasured ||
		suiteRecord.DurationMS != traceMilliseconds(preparedProbeDuration) || !slices.Equal(suiteRecord.Infected, []string{"mutant"}) {
		t.Fatalf("suite probe record = %+v", suiteRecord)
	}
}

func TestCollectBaselineSkipsPreparedSuiteWhenTargetRoutesEveryMutant(t *testing.T) {
	workspace := &baselineFakeWorkspace{exec: func(gomutants.Command) (gomutants.CommandResult, error) {
		return gomutants.CommandResult{}, nil
	}}
	session := &mutationUnitSession{catalog: gomutants.Catalog{Mutants: []gomutants.Mutant{{
		Index: preparedProbeIndex, ID: "mutant", Accepted: true, Probed: true,
		Package: "fixture.example/module", Path: "value.go", Line: 1, Column: 1,
	}}}}
	session.probe = func(request gomutants.ProbeRequest) (gomutants.ProbeResult, error) {
		profile := coverageProfileArgument(gomutants.Command{Argv: request.Args})
		if err := os.WriteFile(profile, []byte("mode: set\nfixture.example/module/value.go:1.1,2.1 1 1\n"), filemode.PrivateFile); err != nil {
			t.Fatal(err)
		}
		return gomutants.ProbeResult{
			Outcome: gomutants.ProbeMeasured, Infected: []uint32{preparedProbeIndex}, Duration: preparedProbeDuration,
		}, nil
	}
	result, err := CollectBaseline(t.Context(), workspace, baselineModel(), []BaselineTarget{{
		Target: baselineTestTarget("TestValue"),
	}}, BaselineOptions{
		ArtifactDirectory: t.TempDir(), Contract: "standard-v1", PackageSuites: true, ProbeSession: session,
	})
	if err != nil || len(session.probeRequests()) != 1 || len(result.Targets) != 1 || len(result.ProbeSuites) != 0 {
		t.Fatalf("prepared baseline = (%+v, %v), probes=%+v", result, err, session.probeRequests())
	}
}

func TestCollectBaselineRunsPreparedSuiteForAcceptedPackageWithoutTargets(t *testing.T) {
	workspace := &baselineFakeWorkspace{exec: func(gomutants.Command) (gomutants.CommandResult, error) {
		return gomutants.CommandResult{}, nil
	}}
	session := &mutationUnitSession{catalog: gomutants.Catalog{Mutants: []gomutants.Mutant{{
		Index: preparedProbeIndex, ID: "mutant", Accepted: true, Probed: true,
		Package: "fixture.example/module", Path: "value.go", Line: 1, Column: 1,
	}}}}
	session.probe = func(request gomutants.ProbeRequest) (gomutants.ProbeResult, error) {
		profile := coverageProfileArgument(gomutants.Command{Argv: request.Args})
		if err := os.WriteFile(profile, []byte("mode: set\nfixture.example/module/value.go:1.1,2.1 1 1\n"), filemode.PrivateFile); err != nil {
			t.Fatal(err)
		}
		return gomutants.ProbeResult{
			Outcome: gomutants.ProbeMeasured, Infected: []uint32{preparedProbeIndex}, Duration: preparedProbeDuration,
		}, nil
	}
	result, err := CollectBaseline(t.Context(), workspace, baselineModel(), nil, BaselineOptions{
		ArtifactDirectory: t.TempDir(), Contract: "standard-v1", PackageSuites: true, ProbeSession: session,
	})
	suite, measured := result.ProbeSuites["fixture.example/module"]
	if err != nil || len(session.probeRequests()) != 1 || !measured || !suite.Measured ||
		!slices.Equal(suite.Infected, []uint32{preparedProbeIndex}) {
		t.Fatalf("prepared baseline = (%+v, %v), probes=%+v", result, err, session.probeRequests())
	}
}

func TestCollectBaselineTreatsFuzzSeedProbeAsMutationProof(t *testing.T) {
	workspace := &baselineFakeWorkspace{exec: func(gomutants.Command) (gomutants.CommandResult, error) {
		return gomutants.CommandResult{}, nil
	}}
	session := &mutationUnitSession{catalog: gomutants.Catalog{Mutants: []gomutants.Mutant{{
		Index: preparedProbeIndex, ID: "mutant", Accepted: true, Probed: true,
		Package: "fixture.example/module", Path: "value.go", Line: 1, Column: 1,
	}}}}
	session.probe = func(request gomutants.ProbeRequest) (gomutants.ProbeResult, error) {
		profile := coverageProfileArgument(gomutants.Command{Argv: request.Args})
		if err := os.WriteFile(profile, []byte("mode: set\nfixture.example/module/value.go:1.1,2.1 1 1\n"), filemode.PrivateFile); err != nil {
			t.Fatal(err)
		}
		return gomutants.ProbeResult{
			Outcome: gomutants.ProbeMeasured, Infected: []uint32{preparedProbeIndex}, Duration: preparedProbeDuration,
		}, nil
	}
	target := baselineTestTarget("FuzzValue")
	target.Kind = goanalysis.KindFuzz
	result, err := CollectBaseline(t.Context(), workspace, baselineModel(), []BaselineTarget{{Target: target}}, BaselineOptions{
		ArtifactDirectory: t.TempDir(), Contract: "standard-v1", PackageSuites: true, ProbeSession: session,
	})
	if err != nil || len(result.Targets) != 1 || !result.Targets[0].Probed || !slices.Equal(result.Targets[0].Infected, []uint32{preparedProbeIndex}) {
		t.Fatalf("fuzz baseline = (%+v, %v), want deterministic seed coverage and mutation proof", result, err)
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
			if len(workspace.commands) != baselineCommandCount {
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

func TestCollectBaselineUsesExactPackageScopeAndCompletesCheckpointAfterEveryControl(t *testing.T) {
	for _, test := range []struct {
		name     string
		packages []string
		want     []string
	}{
		{name: "default", want: []string{"./..."}},
		{name: "explicit", packages: []string{"./internal/assure"}, want: []string{"./internal/assure"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			workspace := &baselineFakeWorkspace{exec: passingBaselineExec(t, "fixture.example/module", true)}
			var checkpoints []checkpoint.Baseline
			result, err := CollectBaseline(t.Context(), workspace, baselineModel(), []BaselineTarget{{
				Target: baselineTestTarget("TestValue"),
			}}, BaselineOptions{
				ArtifactDirectory: t.TempDir(), Packages: test.packages,
				Checkpoint: func(state checkpoint.Baseline) { checkpoints = append(checkpoints, state) },
			})
			if err != nil || len(result.Targets) != 1 || len(checkpoints) < 2 {
				t.Fatalf("baseline = (%+v, %v), checkpoints=%+v", result, err, checkpoints)
			}
			for _, command := range workspace.commands[:2] {
				if !slices.Equal(command.Argv[len(command.Argv)-len(test.want):], test.want) {
					t.Fatalf("command scope = %q, want suffix %q", command.Argv, test.want)
				}
			}
			for _, state := range checkpoints[:len(checkpoints)-1] {
				if state.Complete {
					t.Fatalf("intermediate checkpoint was complete: %+v", state)
				}
			}
			if !checkpoints[len(checkpoints)-1].Complete {
				t.Fatalf("final checkpoint was incomplete: %+v", checkpoints[len(checkpoints)-1])
			}
		})
	}
}

func TestCollectBaselineMeasuresFreshAndPendingResumedPackageSuites(t *testing.T) {
	target := baselineTestTarget("TestValue")
	for _, test := range []struct {
		name   string
		resume *checkpoint.Baseline
	}{
		{name: "fresh"},
		{name: "pending resumed suite", resume: &checkpoint.Baseline{
			BuildVetComplete: true,
			Targets: []checkpoint.BaselineTarget{{
				ID: target.ID,
				Target: checkpointTargetEvidence(TargetEvidence{
					Target: target, CoveredFiles: []string{"value.go"},
					Covered: []goanalysis.FileCoverage{{Path: "value.go", Blocks: []goanalysis.CoverageBlock{infectionBlock()}}},
				}),
				Inventory: report.TargetDisposition{ID: target.ID, Name: target.Name, Kind: string(target.Kind), Package: target.Package, Status: "passed"},
			}},
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			workspace := &baselineFakeWorkspace{exec: passingBaselineExec(t, "fixture.example/module", true)}
			result, err := CollectBaseline(t.Context(), workspace, baselineModel(), []BaselineTarget{{Target: target}}, BaselineOptions{
				ArtifactDirectory: t.TempDir(), PackageSuites: true, Resume: test.resume,
			})
			if err != nil || len(result.Suites) != 1 {
				t.Fatalf("baseline = (%+v, %v)", result, err)
			}
			testBinaryRuns := 0
			for _, command := range workspace.commands {
				if len(command.Argv) > 0 && command.Argv[0] != "go" {
					testBinaryRuns++
				}
			}
			wantRuns := freshPackageSuiteRunCount
			if test.resume != nil {
				wantRuns = 1
			}
			if testBinaryRuns != wantRuns {
				t.Fatalf("test binary runs = %d, want %d: %+v", testBinaryRuns, wantRuns, workspace.commands)
			}
		})
	}
}

func TestCollectBaselinePublishesOneExactInstrumentationAnchorAndOneCompletedCheckpoint(t *testing.T) {
	names := []string{"TestOne", "TestTwo"}
	targets := make([]BaselineTarget, len(names))
	for index, name := range names {
		targets[index] = BaselineTarget{Target: baselineTestTarget(name)}
	}
	workspace := &baselineFakeWorkspace{exec: passingBaselineExec(t, "fixture.example/module", true)}
	var checkpoints []checkpoint.Baseline
	result, err := CollectBaseline(t.Context(), workspace, baselineModel(), targets, BaselineOptions{
		ArtifactDirectory: t.TempDir(), PackageSuites: true, Jobs: 1,
		Checkpoint: func(state checkpoint.Baseline) { checkpoints = append(checkpoints, state) },
	})
	if err != nil || result.Executed != len(targets) || result.Skipped != 0 {
		t.Fatalf("baseline = (%+v, %v)", result, err)
	}
	completed := 0
	suiteCheckpointed := false
	for _, state := range checkpoints {
		if state.Complete {
			completed++
			if state.Routing == nil {
				t.Fatalf("completed checkpoint has no routing: %+v", state)
			}
			for _, unit := range state.Targets {
				if unit.Target != nil && unit.Target.Instrumented != nil {
					t.Fatalf("completed checkpoint target retained instrumentation: %+v", unit)
				}
			}
			continue
		}
		if len(state.Suites) != 0 {
			suiteCheckpointed = true
		}
		owners := 0
		for _, unit := range state.Targets {
			if unit.Target == nil || unit.Target.Instrumented == nil {
				continue
			}
			owners++
			if unit.ID != targets[0].Target.ID {
				t.Fatalf("checkpoint instrumentation owner = %q, want %q", unit.ID, targets[0].Target.ID)
			}
		}
		if len(state.Targets) != 0 && owners != 1 {
			t.Fatalf("checkpoint instrumentation owners = %d, want one: %+v", owners, state)
		}
	}
	if completed != 1 || !suiteCheckpointed {
		t.Fatalf("completed checkpoints = %d, suite checkpointed = %t: %+v", completed, suiteCheckpointed, checkpoints)
	}
}

func TestBaselineJobLimitAndCheckpointEvidenceCoverEveryBoundary(t *testing.T) {
	for _, test := range []struct {
		requested int
		work      int
		automatic int
		want      int
	}{
		{requested: -1, work: 7, automatic: 3, want: 3},
		{requested: 0, work: 2, automatic: 4, want: 2},
		{requested: 8, work: 3, automatic: 2, want: 3},
		{requested: 4, work: 0, automatic: 2, want: 1},
	} {
		if got := baselineJobLimitFor(test.requested, test.work, test.automatic); got != test.want {
			t.Errorf("baselineJobLimitFor(%d, %d, %d) = %d, want %d", test.requested, test.work, test.automatic, got, test.want)
		}
	}
	items := []report.Evidence{
		{Kind: "target", ID: "target"},
		{Kind: "baseline", ID: "vet"},
		{Kind: "resource", ID: "database"},
		{Kind: "baseline", ID: "build"},
	}
	if got := baselineCheckEvidence(items); !reflect.DeepEqual(got, []report.Evidence{items[1], items[3]}) {
		t.Fatalf("baseline checkpoint evidence = %+v", got)
	}
}

func TestCompletedTargetEvidenceSelectsOnlyStoredTargetsInInputOrder(t *testing.T) {
	first := baselineTestTarget("TestFirst")
	missing := baselineTestTarget("TestMissing")
	empty := baselineTestTarget("TestEmpty")
	last := baselineTestTarget("TestLast")
	completed := map[string]checkpoint.BaselineTarget{
		first.ID: {Target: &checkpoint.TargetEvidence{Target: checkpoint.Target{ID: first.ID, Name: first.Name}}},
		empty.ID: {},
		last.ID:  {Target: &checkpoint.TargetEvidence{Target: checkpoint.Target{ID: last.ID, Name: last.Name}}},
	}
	got := completedTargetEvidence(
		[]BaselineTarget{{Target: first}, {Target: missing}, {Target: empty}, {Target: last}},
		completed,
	)
	if len(got) != 2 || got[0].Target.ID != first.ID || got[1].Target.ID != last.ID {
		t.Fatalf("completed target evidence = %+v", got)
	}
}

func TestCheckpointBaselineSuiteDistinguishesUnmeasuredAndEmptyCoverage(t *testing.T) {
	unmeasured := checkpointBaselineSuite(packageSuiteCoverageRun{importPath: "fixture.example/module"})
	if unmeasured.Measured || unmeasured.Covered != nil || unmeasured.Instrumented != nil {
		t.Fatalf("unmeasured suite = %+v", unmeasured)
	}
	measured := checkpointBaselineSuite(packageSuiteCoverageRun{
		importPath: "fixture.example/module", measured: true,
		suite: PackageSuiteCoverage{Duration: time.Second, WholeTree: true},
	})
	if !measured.Measured || measured.Covered == nil || measured.Instrumented == nil ||
		len(measured.Covered.Files) != 0 || len(measured.Instrumented.Files) != 0 ||
		measured.DurationNS != int64(time.Second) || !measured.WholeTree {
		t.Fatalf("measured empty suite = %+v", measured)
	}
}

func TestBaselineCompileCommandIncludesTagsAndCoverage(t *testing.T) {
	got := baselineCompileCommand(
		[]string{"fixture.example/module", "fixture.example/module/dependency"},
		"fixture.example/module", "module.test", []string{"integration", "sqlite"},
	)
	want := []string{
		"go", "test", "-tags=integration,sqlite", "-c",
		"-coverpkg=fixture.example/module,fixture.example/module/dependency", "-o", "module.test", "fixture.example/module",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("baseline compile command = %v, want %v", got, want)
	}
}

func TestTestFramedCommandAddsTheProtocolWithoutAliasingInput(t *testing.T) {
	input := gomutants.Command{Argv: []string{"module.test", "-test.run=^TestValue$"}}
	got := testFramedCommand(input)
	want := []string{"module.test", "-test.v=test2json", "-test.run=^TestValue$"}
	if !slices.Equal(got.Argv, want) || !slices.Equal(input.Argv, []string{"module.test", "-test.run=^TestValue$"}) {
		t.Fatalf("framed command = %v, input = %v", got.Argv, input.Argv)
	}
	if empty := testFramedCommand(gomutants.Command{}); len(empty.Argv) != 0 {
		t.Fatalf("framed empty command = %v", empty.Argv)
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
		{stage: "coverage read", message: "read coverage for TestValue:"},
		{stage: "coverage parse", message: "coverage for TestValue:"},
	} {
		t.Run(test.stage, func(t *testing.T) {
			sentinel := errors.New(test.stage + " sentinel")
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
					if test.stage == "target exec" {
						return gomutants.CommandResult{}, sentinel
					}
					if test.stage != "coverage read" {
						profile := coverageProfileArgument(command)
						contents := "mode: set\nfixture.example/module/value.go:1.1,2.1 1 1\n"
						if test.stage == "coverage parse" {
							contents = "invalid coverage"
						}
						if err := os.WriteFile(profile, []byte(contents), filemode.PrivateFile); err != nil {
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
				if err := os.WriteFile(coverageProfileArgument(command), []byte(contents), filemode.PrivateFile); err != nil {
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
			if command.Dir != "internal/example" || command.Timeout != 300*time.Millisecond ||
				!slices.Equal(command.Env, []string{"DB=ready"}) ||
				!slices.Contains(command.Argv, "-test.count=1") ||
				!slices.Contains(command.Argv, "-test.short=true") || baselineCommandTarget(command) != "" {
				t.Fatalf("suite command = %+v", command)
			}
			if err := os.WriteFile(coverageProfileArgument(command), []byte("mode: set\n"+block), filemode.PrivateFile); err != nil {
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
		), filemode.PrivateFile)
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
			BaselineOptions{ArtifactDirectory: artifactDirectory, Contract: "standard-v1", Jobs: 2}, nil,
		)
	}()
	for range controls {
		select {
		case <-workspace.started:
		case <-time.After(parallelBaselineDeadline):
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

func TestPackageSuiteCoverageKeepsANonemptyControl(t *testing.T) {
	workspace := &baselineFakeWorkspace{exec: passingBaselineExec(t, "fixture.example/module", true)}
	controls := []packageSuiteControl{{
		importPath: "fixture.example/module", relativeDir: ".", binary: "/tmp/module.test",
	}}
	runs := collectPackageSuiteCoverages(
		t.Context(), workspace, "fixture.example/module", controls, time.Second, nil,
		BaselineOptions{ArtifactDirectory: t.TempDir(), Contract: "standard-v1", Jobs: 1}, nil,
	)
	if len(runs) != 1 || runs[0].importPath != controls[0].importPath || !runs[0].measured || runs[0].err != nil {
		t.Fatalf("package suite runs = %+v", runs)
	}
}

func TestBaselineClassifiedUnitUsesTheExactNotRunEmptyEvidenceCase(t *testing.T) {
	target := BaselineTarget{Target: baselineTestTarget("TestValue")}
	existing := report.Evidence{Kind: "existing", ID: "evidence-a", Status: "kept"}
	for _, test := range []struct {
		name     string
		status   string
		evidence []report.Evidence
		want     []report.Evidence
	}{
		{name: "not run empty", status: "not-run", want: []report.Evidence{{
			Kind: "target", ID: target.Target.ID, Status: "not-run", Detail: "detail",
		}}},
		{name: "passed empty", status: "passed"},
		{name: "not run existing", status: "not-run", evidence: []report.Evidence{existing}, want: []report.Evidence{existing}},
		{name: "passed existing", status: "passed", evidence: []report.Evidence{existing}, want: []report.Evidence{existing}},
	} {
		t.Run(test.name, func(t *testing.T) {
			unit := baselineClassifiedUnit(target, test.status, "detail", 0, false, false, nil, test.evidence, nil)
			if !reflect.DeepEqual(unit.Evidence, test.want) {
				t.Fatalf("evidence = %+v, want %+v", unit.Evidence, test.want)
			}
		})
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
		err = os.WriteFile(profile, []byte(contents), filemode.PrivateFile)
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
		case <-time.After(parallelBaselineDeadline):
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

func TestCollectBaselineMeasuresInParallelAndNormalizesReportOrder(t *testing.T) {
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
	checkpointSizes := make([]int, 0, len(names)+checkpointCompletionRecords)
	progress := make([]int, 0, len(names)+1)
	var partialCheckpoint, completedCheckpoint checkpoint.Baseline
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
			Progress: func(completed, total int) {
				if total != len(names) {
					t.Errorf("baseline progress total = %d, want %d", total, len(names))
				}
				progress = append(progress, completed)
			},
			Checkpoint: func(state checkpoint.Baseline) {
				checkpointSizes = append(checkpointSizes, len(state.Targets))
				if state.Complete {
					completedCheckpoint = state
				} else {
					partialCheckpoint = state
				}
			},
		})
		answers <- answer{result: result, err: collectErr}
	}()

	started := make(map[string]bool, len(names))
	for range names {
		select {
		case name := <-workspace.started:
			started[name] = true
		case <-time.After(parallelBaselineDeadline):
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
		case <-time.After(parallelBaselineDeadline):
			t.Fatalf("baseline target %s did not finish", names[index])
		}
	}
	var parallel answer
	select {
	case parallel = <-answers:
	case <-time.After(parallelBaselineDeadline):
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
	for _, target := range parallel.result.Targets {
		if target.Instrumented != nil {
			t.Fatalf("fresh target %s retained instrumentation already owned by global routing", target.Target.Name)
		}
	}
	if len(parallel.result.Instrumented) == 0 {
		t.Fatal("fresh baseline lost its global instrumentation")
	}
	if want := []int{0, 3, 3}; !slices.Equal(checkpointSizes, want) {
		t.Fatalf("checkpoint target counts = %v, want %v", checkpointSizes, want)
	}
	if want := []int{0, 1, 2, 3}; !slices.Equal(progress, want) {
		t.Fatalf("baseline progress = %v, want %v", progress, want)
	}
	anchors := 0
	for _, unit := range partialCheckpoint.Targets {
		if unit.Target != nil && unit.Target.Instrumented != nil {
			anchors++
		}
	}
	if anchors != 1 {
		t.Fatalf("partial checkpoint instrumentation anchors = %d, want one per compiled package", anchors)
	}
	for _, unit := range completedCheckpoint.Targets {
		if unit.Target != nil && unit.Target.Instrumented != nil {
			t.Fatalf("completed target %s retained instrumentation already compacted into routing", unit.ID)
		}
	}
	if completedCheckpoint.Routing == nil {
		t.Fatal("completed checkpoint did not compact global instrumentation into routing")
	}
}

func TestCollectBaselineCheckpointsEveryOutOfOrderSuccessBeforeAnEarlierError(t *testing.T) {
	names := []string{"TestOne", "TestTwo", "TestThree"}
	targets := make([]BaselineTarget, len(names))
	gates := make(map[string]chan struct{}, len(names))
	for index, name := range names {
		targets[index] = BaselineTarget{Target: baselineTestTarget(name)}
		gates[name] = make(chan struct{})
	}
	firstErr := errors.New("first target interrupted")
	workspace := &parallelBaselineWorkspace{
		started: make(chan string, len(names)), finished: make(chan string, len(names)), gates: gates,
		failures: map[string]error{"TestOne": firstErr},
	}
	var saved checkpoint.Baseline
	answer := make(chan error, 1)
	go func() {
		_, err := CollectBaseline(t.Context(), workspace, baselineModel(), targets, BaselineOptions{
			ArtifactDirectory: t.TempDir(), Jobs: len(names),
			Checkpoint: func(state checkpoint.Baseline) { saved = state },
		})
		answer <- err
	}()
	for range names {
		select {
		case <-workspace.started:
		case <-time.After(parallelBaselineDeadline):
			t.Fatal("baseline controls did not start")
		}
	}
	for _, name := range []string{"TestThree", "TestTwo", "TestOne"} {
		close(gates[name])
		if finished := <-workspace.finished; finished != name {
			t.Fatalf("finished %s, want %s", finished, name)
		}
	}
	if err := <-answer; !errors.Is(err, firstErr) {
		t.Fatalf("baseline error = %v, want %v", err, firstErr)
	}
	got := make([]string, len(saved.Targets))
	for index, unit := range saved.Targets {
		got[index] = unit.ID
	}
	want := []string{"target-TestThree", "target-TestTwo"}
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("checkpointed targets = %v, want every terminal success %v", got, want)
	}
}

func TestClassifyTargetFailureDistinguishesTimeoutAndFailure(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name    string
		attempt gomutants.CommandResult
		kind    string
		summary string
	}{
		{
			name: "timeout", attempt: gomutants.CommandResult{TimedOut: true},
			kind: "baseline-timeout", summary: "baseline target exceeded its execution budget",
		},
		{
			name: "failure", attempt: gomutants.CommandResult{ExitCode: 1, Output: []byte(" boom\n")},
			kind: "baseline-failure", summary: "baseline target failed: boom",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			kind, summary := classifyTargetFailure(test.attempt)
			if kind != test.kind || summary != test.summary {
				t.Fatalf("classifyTargetFailure = (%q, %q), want (%q, %q)", kind, summary, test.kind, test.summary)
			}
		})
	}
}

func TestClassifyTestFramingAcceptsOnlyMarkedSelectedSkipReports(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name        string
		output      string
		wantSkipped bool
		wantKind    string
		wantSummary string
		wantError   bool
	}{
		{
			name: "top-level", output: "\x16--- SKIP: TestValue (0.00s)\r\n",
			wantSkipped: true, wantKind: "skipped-target", wantSummary: "the selected top-level target called Skip",
		},
		{
			name: "top-level before unframed output", output: "\x16--- SKIP: TestValue\nunframed output",
			wantSkipped: true, wantKind: "skipped-target", wantSummary: "the selected top-level target called Skip",
		},
		{
			name: "top-level after an empty frame", output: "\x16\x16--- SKIP: TestValue\n",
			wantSkipped: true, wantKind: "skipped-target", wantSummary: "the selected top-level target called Skip",
		},
		{
			name: "indented subtest after unterminated output", output: "user output without newline\x16    --- SKIP: TestValue/subtest (1.25s)\n",
			wantSkipped: true, wantKind: "skipped-subtest", wantSummary: "a selected subtest was skipped: TestValue/subtest",
		},
		{name: "unmarked lookalike", output: "--- SKIP: TestValue (0.00s)\n"},
		{name: "different target", output: "\x16--- SKIP: TestValuable (0.00s)\n"},
		{name: "large unframed output", output: strings.Repeat("x", (4<<20)+1)},
		{name: "truncated unknown", output: commandOutputTruncatedPrefix + ": the process produced 2000000 bytes, only the tail is kept\n\x16--- PASS: TestValue (0.00s)\n", wantError: true},
		{
			name: "skip retained in truncated tail", output: commandOutputTruncatedPrefix + ": the process produced 2000000 bytes, only the tail is kept\n\x16--- SKIP: TestValue/subtest (0.00s)\n",
			wantSkipped: true, wantKind: "skipped-subtest", wantSummary: "a selected subtest was skipped: TestValue/subtest",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			skipped, kind, summary, err := classifyTestFraming("TestValue", []byte(test.output))
			if skipped != test.wantSkipped || kind != test.wantKind || summary != test.wantSummary || (err != nil) != test.wantError {
				t.Fatalf("classifyTestFraming = (%t, %q, %q, %v)", skipped, kind, summary, err)
			}
		})
	}
}

func TestTrimTestDurationAcceptsOnlyGoTestDurations(t *testing.T) {
	for _, test := range []struct {
		input string
		want  string
	}{
		{input: "TestValue", want: "TestValue"},
		{input: "TestValue (1.25s)", want: "TestValue"},
		{input: "TestValue (1.25ms)", want: "TestValue (1.25ms)"},
		{input: "TestValue (quickly)", want: "TestValue (quickly)"},
	} {
		if got := trimTestDuration(test.input); got != test.want {
			t.Fatalf("trim test duration %q = %q, want %q", test.input, got, test.want)
		}
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
		{name: "exact boundary", output: strings.Repeat("a", maximumSummaryRunes), want: strings.Repeat("a", maximumSummaryRunes)},
		{name: "over boundary", output: strings.Repeat("a", maximumSummaryRunes+1), want: strings.Repeat("a", maximumSummaryRunes) + "…"},
		{name: "unicode boundary", output: strings.Repeat("界", maximumSummaryRunes+1), want: strings.Repeat("界", maximumSummaryRunes) + "…"},
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
	target.Path, target.Line = "value_test.go", targetFindingSourceLine
	finding := targetFinding(target, "baseline-failure", "boom")
	wantID := report.FindingID("target", target.ID, "baseline-failure")
	if finding.ID != wantID || finding.Kind != "baseline-failure" || finding.Path != target.Path || finding.Line != targetFindingSourceLine || finding.Summary != "boom" || finding.Replay != "goatest replay "+wantID {
		t.Fatalf("finding = %+v", finding)
	}
	environment := []string{"RESOURCE=ready"}
	command := targetCommand("baseline.test", "value.cover", "pkg", BaselineTarget{Target: target, Environment: environment}, targetCommandFixtureDeadline)
	wantArgv := []string{"baseline.test", "-test.run=^TestValue$", "-test.coverprofile=value.cover", "-test.count=1"}
	if !slices.Equal(command.Argv, wantArgv) || command.Dir != "pkg" || !slices.Equal(command.Env, environment) || command.Timeout != targetCommandFixtureDeadline {
		t.Fatalf("target command = %+v", command)
	}
	environment[0] = "MUTATED=yes"
	if command.Env[0] != "RESOURCE=ready" {
		t.Fatal("target command aliases environment")
	}
	if name := binaryName("fixture.example/module"); !strings.HasSuffix(name, testBinarySuffixInternal()) || len(strings.TrimSuffix(name, testBinarySuffixInternal())) != 16 {
		t.Fatalf("binaryName = %q", name)
	}
	if build := baselineBuildCommand([]string{"integration"}, []string{"./cmd/tool"}); !slices.Equal(build, []string{
		"go", "build", "-tags=integration", "-o", os.DevNull, "./cmd/tool",
	}) {
		t.Fatalf("baseline build command = %q", build)
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
			if err := os.WriteFile(profile, []byte(contents), filemode.PrivateFile); err != nil {
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
