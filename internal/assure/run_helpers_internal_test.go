// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package assure

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	gomutants "github.com/P4suta/go-mutants"
	"github.com/P4suta/goatest/internal/config"
	"github.com/P4suta/goatest/internal/evidence"
	goanalysis "github.com/P4suta/goatest/internal/golang"
	"github.com/P4suta/goatest/internal/report"
)

func TestCommandFreezesArgvAndSetsSafetyLimits(t *testing.T) {
	argv := []string{"go", "test", "./..."}
	got := command(argv, 7*time.Second)
	argv[0] = "mutated"
	if !slices.Equal(got.Argv, []string{"go", "test", "./..."}) || got.Timeout != 7*time.Second || got.OutputLimit != 32<<20 {
		t.Fatalf("command = %+v", got)
	}
}

func TestRunRejectsMissingRepositoryMalformedConfigAndUnknownContractWithoutStartingWork(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")
	if result, err := Run(t.Context(), Options{Root: missing}); err == nil || !reflect.DeepEqual(result, report.Report{}) {
		t.Fatalf("missing repository = (%+v, %v)", result, err)
	}
	malformed := t.TempDir()
	writeRunHelperFile(t, malformed, config.FileName, "unknown = true\n")
	if result, err := Run(t.Context(), Options{Root: malformed}); err == nil || !reflect.DeepEqual(result, report.Report{}) {
		t.Fatalf("malformed config = (%+v, %v)", result, err)
	}
	if result, err := Run(t.Context(), Options{Root: t.TempDir(), Contract: "unknown"}); err == nil || !reflect.DeepEqual(result, report.Report{}) || !strings.Contains(err.Error(), "contract") {
		t.Fatalf("unknown contract = (%+v, %v)", result, err)
	}
}

func TestCommandErrorDistinguishesInfrastructureAndCommandFailure(t *testing.T) {
	cause := errors.New("process failed")
	if err := commandError("go list", gomutants.CommandResult{}, cause); !errors.Is(err, cause) || err.Error() != "goatest: go list: process failed" {
		t.Fatalf("infrastructure error = %v", err)
	}
	err := commandError("go test", gomutants.CommandResult{ExitCode: 7, TimedOut: true, Output: []byte("failure\n")}, nil)
	if err == nil || !strings.Contains(err.Error(), "go test failed (exit=7 timeout=true): failure") {
		t.Fatalf("command failure = %v", err)
	}
}

func TestInspectWorkspaceReturnsCompleteMetadataAndExactCommands(t *testing.T) {
	moduleRoot := t.TempDir()
	listed := listedPackageJSON(t, moduleRoot)
	modules := moduleGraphJSON(t,
		listedModule{Path: "fixture.example/module", Main: true},
		listedModule{Path: "example.com/dependency", Version: "v1.2.3", Sum: "h1:sum", GoModSum: "h1:mod"},
	)
	workspace := &scriptedValidationWorkspace{results: []gomutants.CommandResult{
		{Output: []byte("go version go1.26.6 windows/amd64\n")},
		{Output: listed},
		{Output: modules},
	}}
	metadata, err := inspectWorkspace(t.Context(), workspace)
	if err != nil || metadata.toolchain != "go version go1.26.6 windows/amd64" || metadata.model.ModulePath != "fixture.example/module" || len(metadata.dependencies) != 2 {
		t.Fatalf("inspectWorkspace = (%+v, %v)", metadata, err)
	}
	want := []struct {
		argv    []string
		timeout time.Duration
	}{
		{[]string{"go", "version"}, 30 * time.Second},
		{[]string{"go", "list", "-json", "./..."}, 5 * time.Minute},
		{[]string{"go", "list", "-m", "-json", "all"}, 5 * time.Minute},
	}
	if len(workspace.commands) != len(want) {
		t.Fatalf("commands = %+v", workspace.commands)
	}
	for index, command := range workspace.commands {
		if !slices.Equal(command.Argv, want[index].argv) || command.Timeout != want[index].timeout || command.OutputLimit != 32<<20 {
			t.Errorf("command %d = %+v", index, command)
		}
	}
}

func TestInspectSelectedPackagesUsesConfiguredCommandTimeout(t *testing.T) {
	t.Parallel()
	timeout := 17 * time.Second
	for _, test := range []struct {
		name     string
		patterns []string
		tags     []string
		wantArgv []string
	}{
		{name: "explicit packages", patterns: []string{"./pkg/..."}, tags: []string{"integration", "sqlite"}, wantArgv: []string{"go", "list", "-json", "-tags=integration,sqlite", "./pkg/..."}},
		{name: "build tags default to all packages", tags: []string{"integration"}, wantArgv: []string{"go", "list", "-json", "-tags=integration", "./..."}},
	} {
		t.Run(test.name, func(t *testing.T) {
			workspace := &scriptedValidationWorkspace{results: []gomutants.CommandResult{{
				Output: listedPackageJSON(t, t.TempDir()),
			}}}
			model, err := inspectSelectedPackages(t.Context(), workspace, test.patterns, test.tags, timeout)
			if err != nil || model.ModulePath != "fixture.example/module" || len(workspace.commands) != 1 {
				t.Fatalf("inspectSelectedPackages = (%+v, %v), commands=%+v", model, err, workspace.commands)
			}
			command := workspace.commands[0]
			if !slices.Equal(command.Argv, test.wantArgv) || command.Timeout != timeout || command.OutputLimit != 32<<20 {
				t.Fatalf("selected package command = %+v, want argv=%v timeout=%s", command, test.wantArgv, timeout)
			}
		})
	}
}

func TestInspectWorkspaceRejectsEveryCommandFailureAndMalformedOutput(t *testing.T) {
	moduleRoot := t.TempDir()
	validList := listedPackageJSON(t, moduleRoot)
	validModules := moduleGraphJSON(t, listedModule{Path: "fixture.example/module", Main: true})
	cause := errors.New("exec failed")
	for _, test := range []struct {
		name       string
		results    []gomutants.CommandResult
		errors     []error
		wantCalls  int
		wantCause  bool
		wantDetail string
	}{
		{name: "version infrastructure", results: []gomutants.CommandResult{{}}, errors: []error{cause}, wantCalls: 1, wantCause: true},
		{name: "version exit", results: []gomutants.CommandResult{{ExitCode: 1}}, wantCalls: 1, wantDetail: "go version failed"},
		{name: "version timeout", results: []gomutants.CommandResult{{TimedOut: true}}, wantCalls: 1, wantDetail: "go version failed"},
		{name: "list infrastructure", results: []gomutants.CommandResult{{Output: []byte("version")}, {}}, errors: []error{nil, cause}, wantCalls: 2, wantCause: true},
		{name: "list exit", results: []gomutants.CommandResult{{Output: []byte("version")}, {ExitCode: 1}}, wantCalls: 2, wantDetail: "go list failed"},
		{name: "list timeout", results: []gomutants.CommandResult{{Output: []byte("version")}, {TimedOut: true}}, wantCalls: 2, wantDetail: "go list failed"},
		{name: "malformed packages", results: []gomutants.CommandResult{{Output: []byte("version")}, {Output: []byte("{")}}, wantCalls: 2, wantDetail: "decode go list package"},
		{name: "modules infrastructure", results: []gomutants.CommandResult{{Output: []byte("version")}, {Output: validList}, {}}, errors: []error{nil, nil, cause}, wantCalls: 3, wantCause: true},
		{name: "modules exit", results: []gomutants.CommandResult{{Output: []byte("version")}, {Output: validList}, {ExitCode: 1}}, wantCalls: 3, wantDetail: "go list -m failed"},
		{name: "modules timeout", results: []gomutants.CommandResult{{Output: []byte("version")}, {Output: validList}, {TimedOut: true}}, wantCalls: 3, wantDetail: "go list -m failed"},
		{name: "malformed modules", results: []gomutants.CommandResult{{Output: []byte("version")}, {Output: validList}, {Output: []byte("{")}}, wantCalls: 3, wantDetail: "decode module graph"},
		{name: "empty module path", results: []gomutants.CommandResult{{Output: []byte("version")}, {Output: validList}, {Output: moduleGraphJSON(t, listedModule{})}}, wantCalls: 3, wantDetail: "empty path"},
		{name: "no main module", results: []gomutants.CommandResult{{Output: []byte("version")}, {Output: validList}, {Output: moduleGraphJSON(t, listedModule{Path: "fixture.example/module"})}}, wantCalls: 3, wantDetail: "no main module"},
		{name: "different main module", results: []gomutants.CommandResult{{Output: []byte("version")}, {Output: validList}, {Output: moduleGraphJSON(t, listedModule{Path: "other.example/module", Main: true})}}, wantCalls: 3, wantDetail: "does not match main module"},
		{name: "multiple main modules", results: []gomutants.CommandResult{{Output: []byte("version")}, {Output: validList}, {Output: moduleGraphJSON(t, listedModule{Path: "fixture.example/module", Main: true}, listedModule{Path: "other.example/module", Main: true})}}, wantCalls: 3, wantDetail: "refusing partial assurance"},
		{name: "valid control", results: []gomutants.CommandResult{{Output: []byte("version")}, {Output: validList}, {Output: validModules}}, wantCalls: 3},
	} {
		t.Run(test.name, func(t *testing.T) {
			workspace := &scriptedValidationWorkspace{results: test.results, errors: test.errors}
			metadata, err := inspectWorkspace(t.Context(), workspace)
			wantErr := test.wantCause || test.wantDetail != ""
			if (err != nil) != wantErr || len(workspace.commands) != test.wantCalls {
				t.Fatalf("inspectWorkspace = (%+v, %v), calls=%d", metadata, err, len(workspace.commands))
			}
			if test.wantCause && !errors.Is(err, cause) {
				t.Fatalf("error = %v, want cause %v", err, cause)
			}
			if test.wantDetail != "" && !strings.Contains(err.Error(), test.wantDetail) {
				t.Fatalf("error = %v, want detail %q", err, test.wantDetail)
			}
		})
	}
}

func TestDependencyDigestsIncludesRemoteReplacementAndLocalContentIdentity(t *testing.T) {
	local := t.TempDir()
	writeRunHelperFile(t, local, "dependency.go", "package dependency\nconst Value = 1\n")
	writeRunHelperFile(t, local, "testdata/fuzz/FuzzValue/seed", "go test fuzz v1\nint(1)\n")
	modules := []listedModule{
		{Path: "main.example/module", Main: true},
		{Path: "remote.example/module", Version: "v1.2.3", Sum: "h1:sum", GoModSum: "h1:mod"},
		{Path: "replace.example/module", Version: "v1.0.0", Replace: &listedModule{Path: "fork.example/module", Version: "v1.1.0", Sum: "h1:fork", GoModSum: "h1:forkmod"}},
		{Path: "local.example/module", Version: "v0.0.0", Replace: &listedModule{Path: local, Dir: local}},
	}
	got, err := dependencyDigests(moduleGraphJSON(t, modules...))
	if err != nil || len(got) != len(modules) {
		t.Fatalf("dependencyDigests = (%v, %v)", got, err)
	}
	if got["remote.example/module"] != digestText("v1.2.3\x00h1:sum\x00h1:mod") {
		t.Fatalf("remote digest = %q", got["remote.example/module"])
	}
	wantReplacement := "v1.0.0\x00\x00\x00replace\x00fork.example/module\x00v1.1.0\x00h1:fork\x00h1:forkmod"
	if got["replace.example/module"] != digestText(wantReplacement) {
		t.Fatalf("replacement digest = %q", got["replace.example/module"])
	}
	before := got["local.example/module"]
	writeRunHelperFile(t, local, "dependency.go", "package dependency\nconst Value = 2\n")
	after, err := dependencyDigests(moduleGraphJSON(t, modules...))
	if err != nil || after["local.example/module"] == before {
		t.Fatalf("local digest did not change: before=%q after=%q err=%v", before, after["local.example/module"], err)
	}
}

func TestDependencyDigestsRejectsMalformedEmptyAndUnreadableLocalReplacement(t *testing.T) {
	for _, test := range []struct {
		name   string
		input  []byte
		detail string
	}{
		{name: "malformed", input: []byte("{"), detail: "decode module graph"},
		{name: "empty path", input: moduleGraphJSON(t, listedModule{}), detail: "empty path"},
		{name: "missing local replacement", input: moduleGraphJSON(t, listedModule{Path: "dependency", Replace: &listedModule{Path: "missing", Dir: filepath.Join(t.TempDir(), "missing")}}), detail: "digest local replacement"},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := dependencyDigests(test.input)
			if err == nil || got != nil || !strings.Contains(err.Error(), test.detail) {
				t.Fatalf("dependencyDigests = (%v, %v)", got, err)
			}
		})
	}
	for _, replacement := range []*listedModule{
		{Path: "versioned", Version: "v1", Dir: filepath.Join(t.TempDir(), "missing")},
		{Path: "summed", Sum: "h1:sum", Dir: filepath.Join(t.TempDir(), "missing")},
		{Path: "no-dir"},
	} {
		if got, err := dependencyDigests(moduleGraphJSON(t, listedModule{Path: "dependency", Replace: replacement})); err != nil || got["dependency"] == "" {
			t.Fatalf("non-local replacement = (%v, %v)", got, err)
		}
	}
}

func TestAssuranceInputsCapturesEveryIdentityDimensionDeterministically(t *testing.T) {
	root := t.TempDir()
	writeRunHelperFile(t, root, "value.go", "package fixture\n")
	writeRunHelperFile(t, root, "testdata/fuzz/FuzzValue/seed", "go test fuzz v1\nint(1)\n")
	loaded := config.Config{
		Execution: config.Execution{Environment: []string{"A", "B"}},
		Resources: map[string]config.Resource{
			"postgres": {Command: []string{"provider", "postgres"}, Timeout: 7 * time.Second, Shared: true},
		},
	}
	options := Options{NoApply: true, Changed: true, ChangedRef: "HEAD~1", Environment: []string{
		"B=2", "a=1", "GOFLAGS=-trimpath", "UNDECLARED_SECRET=must-not-be-hashed",
		"TEMP=unstable", "GO_MUTANTS_INTERNAL=unstable",
	}}
	metadata := roundMetadata{
		toolchain: "go version go1.26.6", dependencies: map[string]string{"dependency": "digest"},
	}
	inputs, digest, err := assuranceInputs(root, "deep-v1", options, loaded, metadata)
	if err != nil || digest != evidence.Digest(inputs) || inputs.Toolchain != metadata.toolchain || inputs.Platform != runtime.GOOS+"/"+runtime.GOARCH ||
		inputs.Contract != "deep-v1;apply=false;changed=true;ref=HEAD~1" || inputs.GoatestVersion != GoatestVersion || inputs.GoMutantsVersion != GoMutantsVersion ||
		inputs.Dependencies["dependency"] != "digest" || inputs.Resources["postgres"] == "" || len(inputs.Files) == 0 || len(inputs.Corpus) == 0 {
		t.Fatalf("assuranceInputs = (%+v, %q, %v)", inputs, digest, err)
	}
	for _, expected := range []string{"B=2", "a=1", "GOFLAGS=-trimpath", "GOTOOLCHAIN=local"} {
		if !slices.Contains(inputs.Environment, expected) {
			t.Fatalf("selected environment missing %q: %v", expected, inputs.Environment)
		}
	}
	for _, unstable := range []string{"TEMP=unstable", "GO_MUTANTS_INTERNAL=unstable", "UNDECLARED_SECRET=must-not-be-hashed"} {
		if slices.Contains(inputs.Environment, unstable) {
			t.Fatalf("unstable environment retained: %v", inputs.Environment)
		}
	}
	second, secondDigest, err := assuranceInputs(root, "deep-v1", options, loaded, metadata)
	if err != nil || secondDigest != digest || !reflect.DeepEqual(second, inputs) {
		t.Fatalf("nondeterministic assurance inputs = (%+v, %q, %v)", second, secondDigest, err)
	}

	variants := []config.Resource{
		{Command: []string{"different"}, Timeout: 7 * time.Second, Shared: true},
		{Command: []string{"provider", "postgres"}, Timeout: 8 * time.Second, Shared: true},
		{Command: []string{"provider", "postgres"}, Timeout: 7 * time.Second, Shared: false},
		{Command: []string{"provider", "postgres"}, Timeout: 7 * time.Second, Shared: true, Exclusive: true},
		{Command: []string{"provider", "postgres"}, Timeout: 7 * time.Second, Shared: true, Environment: []string{"PROVIDER_MODE"}},
	}
	for _, variant := range variants {
		changed := config.Config{Resources: map[string]config.Resource{"postgres": variant}}
		_, changedDigest, err := assuranceInputs(root, "deep-v1", options, changed, metadata)
		if err != nil || changedDigest == digest {
			t.Errorf("resource variant did not invalidate digest: %+v %v", variant, err)
		}
	}
	if _, _, err := assuranceInputs(filepath.Join(root, "missing"), "deep-v1", options, loaded, metadata); err == nil {
		t.Fatal("missing repository scan succeeded")
	}
}

func TestTraceTakesNoPartInCacheIdentity(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeRunHelperFile(t, root, "value.go", "package fixture\n")
	loaded := config.Config{Execution: config.Execution{Environment: []string{"A"}}}
	metadata := roundMetadata{toolchain: "go version go1.26.6", dependencies: map[string]string{"dependency": "digest"}}
	untraced := Options{
		NoApply: true, Changed: true, ChangedRef: "HEAD~1", Packages: []string{"./internal/..."},
		CommandTimeout: time.Minute, Environment: []string{"A=1", "GOFLAGS=-trimpath"},
	}
	_, recorder := newTraceRecording()
	traced := untraced
	traced.Trace = recorder
	if traced.Trace == nil {
		t.Fatal("the traced options carry no recorder")
	}
	if modeIdentity(traced) != modeIdentity(untraced) {
		t.Fatalf("traced mode identity = %q, untraced = %q", modeIdentity(traced), modeIdentity(untraced))
	}
	if modeIdentity(Options{Trace: recorder}) != modeIdentity(Options{}) {
		t.Fatalf("traced default mode identity = %q", modeIdentity(Options{Trace: recorder}))
	}
	untracedInputs, untracedDigest, err := assuranceInputs(root, "deep-v1", untraced, loaded, metadata)
	if err != nil {
		t.Fatal(err)
	}
	tracedInputs, tracedDigest, err := assuranceInputs(root, "deep-v1", traced, loaded, metadata)
	if err != nil || tracedDigest != untracedDigest || !reflect.DeepEqual(tracedInputs, untracedInputs) {
		t.Fatalf("traced assurance inputs = (%+v, %q, %v), want (%+v, %q)",
			tracedInputs, tracedDigest, err, untracedInputs, untracedDigest)
	}
}

func TestModeIdentityStableEnvironmentAndAcceptanceBoundaries(t *testing.T) {
	if got := modeIdentity(Options{}); got != ";apply=true;changed=false;ref=" {
		t.Fatalf("default mode identity = %q", got)
	}
	if got := modeIdentity(Options{NoApply: true, Changed: true, ChangedRef: "base"}); got != ";apply=false;changed=true;ref=base" {
		t.Fatalf("selected mode identity = %q", got)
	}
	if got := modeIdentity(Options{NoApply: true, ReplayMutantID: "mutant-a"}); got != ";apply=false;changed=false;ref=;replay=mutant-a" {
		t.Fatalf("replay mode identity = %q", got)
	}
	if got := modeIdentity(Options{NoApply: true, ReplayFindingID: "finding-a"}); got != ";apply=false;changed=false;ref=;replay-finding=finding-a" {
		t.Fatalf("finding replay mode identity = %q", got)
	}
	environment := stableEnvironment([]string{
		"B=2", "a=1", "bad", "TMP=x", "temp=y", "TmpDir=z", "go_mutants_x=1", "A=2",
		"STARSHIP_SESSION_KEY=volatile", "__mise_session=volatile",
	})
	if !slices.Equal(environment, []string{"A=2", "B=2", "a=1"}) {
		t.Fatalf("stableEnvironment = %v", environment)
	}
	selected := selectedEnvironment([]string{"B=2", "A=1", "SECRET=hidden", "GOFLAGS=-trimpath"}, []string{"B"})
	if !slices.Equal(selected, []string{"B=2", "GOFLAGS=-trimpath"}) {
		t.Fatalf("selectedEnvironment = %v", selected)
	}
	providerEnvironment := generationProviderEnvironment(
		[]string{"Path=C:/tools", "TEMP=C:/temp", "TOKEN=allowed", "SECRET=hidden"}, []string{"TOKEN"},
	)
	if !slices.Equal(providerEnvironment, []string{"Path=C:/tools", "TEMP=C:/temp", "TOKEN=allowed"}) {
		t.Fatalf("generationProviderEnvironment = %v", providerEnvironment)
	}

	now := time.Date(2026, 8, 30, 0, 0, 0, 0, time.UTC)
	loaded := config.Config{Acceptance: []config.Acceptance{
		{ID: "active", Expires: now.Add(time.Nanosecond)},
		{ID: "equal", Expires: now},
		{ID: "expired", Expires: now.Add(-time.Nanosecond)},
	}}
	accepted := activeAcceptance(loaded, now)
	if !reflect.DeepEqual(accepted, map[string]bool{"active": true}) {
		t.Fatalf("activeAcceptance = %v", accepted)
	}
	for _, test := range []struct {
		name     string
		report   report.Report
		accepted map[string]bool
		want     bool
	}{
		{name: "no acceptance", want: true},
		{name: "active", report: report.Report{Evidence: []report.Evidence{{Kind: "mutation", Status: "accepted", Detail: "active"}}}, accepted: accepted, want: true},
		{name: "expired", report: report.Report{Evidence: []report.Evidence{{Kind: "mutation", Status: "accepted", Detail: "expired"}}}, accepted: accepted},
		{name: "different kind", report: report.Report{Evidence: []report.Evidence{{Kind: "resource", Status: "accepted", Detail: "expired"}}}, want: true},
		{name: "different status", report: report.Report{Evidence: []report.Evidence{{Kind: "mutation", Status: "killed", Detail: "expired"}}}, want: true},
	} {
		if got := cachedAcceptanceValid(test.report, test.accepted); got != test.want {
			t.Errorf("%s cachedAcceptanceValid = %t, want %t", test.name, got, test.want)
		}
	}
}

func TestProjectExcludeMatchingAndLimitationsAreExplicit(t *testing.T) {
	patterns := []string{"generated/**", "**/*_generated.go", "exact_test.go"}
	for path, want := range map[string]bool{
		"generated/value_test.go": true,
		"pkg/value_generated.go":  true,
		"exact_test.go":           true,
		"pkg/value_test.go":       false,
	} {
		if got := projectPathExcluded(path, patterns); got != want {
			t.Errorf("projectPathExcluded(%q) = %t, want %t", path, got, want)
		}
	}
	targets := []goanalysis.Target{{Path: "generated/a_test.go"}, {Path: "pkg/a_test.go"}}
	if got := includedProjectTargets(targets, patterns); len(got) != 1 || got[0].Path != "pkg/a_test.go" {
		t.Fatalf("included targets = %+v", got)
	}
	packages := []goanalysis.Package{
		{ImportPath: "example/generated", RelativeDir: "generated"},
		{ImportPath: "example/pkg", RelativeDir: "pkg"},
	}
	if got := includedProjectPackages(packages, patterns); len(got) != 1 || got[0].ImportPath != "example/pkg" {
		t.Fatalf("included packages = %+v", got)
	}
	limitations := projectExcludeLimitations(patterns)
	if len(limitations) != len(patterns) || limitations[0].Code != "project-exclude" || !strings.Contains(limitations[0].Summary, patterns[0]) {
		t.Fatalf("exclude limitations = %+v", limitations)
	}
}

func TestMutationJobLimitAndProgressCoverEveryBoundary(t *testing.T) {
	previous := runtime.GOMAXPROCS(2)
	t.Cleanup(func() { runtime.GOMAXPROCS(previous) })
	if got := mutationJobLimit(Options{MutationJobs: 4}, config.Config{Resources: map[string]config.Resource{"db": {Exclusive: true}}}); got != 1 {
		t.Fatalf("exclusive jobs = %d", got)
	}
	for _, test := range []struct{ requested, want int }{{-1, 2}, {0, 2}, {1, 1}, {4, 4}, {5, 4}} {
		if got := mutationJobLimit(Options{MutationJobs: test.requested}, config.Config{}); got != test.want {
			t.Errorf("mutationJobLimit(%d) = %d, want %d", test.requested, got, test.want)
		}
	}
	var events []Event
	progress := mutationProgress(Options{Progress: func(event Event) { events = append(events, event) }})
	for completed := 1; completed <= 101; completed++ {
		progress(completed, 101)
	}
	var want []Event
	for completed := 1; completed <= 101; completed++ {
		if completed == 1 || completed == 101 || completed%2 == 0 {
			want = append(want, Event{Kind: "mutation-progress", Detail: strconv.Itoa(completed) + "/101"})
		}
	}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("progress events = %v, want %v", events, want)
	}
	mutationProgress(Options{})(1, 1)
}

func TestMergeAndValidationEnvironmentHandleCaseConflictsAndOverlay(t *testing.T) {
	merged, err := mergeEnvironment([]string{"b=2", "A=1", "a=1"}, []string{"C=3", "B=2"})
	if err != nil || !slices.Equal(merged, []string{"A=1", "B=2", "C=3"}) {
		t.Fatalf("mergeEnvironment = (%v, %v)", merged, err)
	}
	for _, entries := range [][]string{{"invalid"}, {"=empty"}, {"A=1", "a=2"}} {
		if got, err := mergeEnvironment(entries, nil); err == nil || got != nil {
			t.Errorf("mergeEnvironment(%v) = (%v, %v)", entries, got, err)
		}
	}
	validated := validationEnvironment([]string{"Path=one", "A=1", "invalid", "=empty"}, []string{"PATH=two", "B=2", "bad"})
	if !slices.Equal(validated, []string{"A=1", "B=2", "PATH=two"}) {
		t.Fatalf("validationEnvironment = %v", validated)
	}
	if got := validationEnvironment([]string{}, nil); len(got) != 0 {
		t.Fatalf("explicit empty validation environment = %v", got)
	}
	t.Setenv("GOATEST_VALIDATION_ENV_MARKER", "ready")
	if got := validationEnvironment(nil, nil); !containsEnvironment(got, "GOATEST_VALIDATION_ENV_MARKER", "ready") {
		t.Fatalf("process validation environment missing marker: %v", got)
	}
}

func TestBaselineVerdictRepositoryRootExecutionEnvironmentAndEmit(t *testing.T) {
	if baselineVerdict(nil) != report.VerdictInsufficient || baselineVerdict([]report.Finding{{Kind: "other"}}) != report.VerdictInsufficient ||
		baselineVerdict([]report.Finding{{Kind: "other"}, {Kind: "baseline-failure"}}) != report.VerdictDefect ||
		baselineVerdict([]report.Finding{{Kind: "baseline-timeout"}}) != report.VerdictDefect ||
		baselineVerdict([]report.Finding{{Kind: "vet-failure"}}) != report.VerdictDefect ||
		baselineVerdict([]report.Finding{{Kind: "build-failure"}}) != report.VerdictDefect ||
		baselineVerdict([]report.Finding{{Kind: "test-binary-build-failure"}}) != report.VerdictDefect {
		t.Fatal("baselineVerdict matrix failed")
	}

	previousAbsolute, previousStat := absoluteRepositoryPath, statRepositoryPath
	t.Cleanup(func() { absoluteRepositoryPath, statRepositoryPath = previousAbsolute, previousStat })
	cause := errors.New("absolute failed")
	absoluteRepositoryPath = func(string) (string, error) { return "", cause }
	if got, err := repositoryRoot("root"); !errors.Is(err, cause) || got != "" {
		t.Fatalf("absolute root = (%q, %v)", got, err)
	}
	absoluteRepositoryPath = filepath.Abs
	statRepositoryPath = func(string) (os.FileInfo, error) { return nil, cause }
	if got, err := repositoryRoot("root"); !errors.Is(err, cause) || got != "" || !strings.Contains(err.Error(), "repository root") {
		t.Fatalf("stat root = (%q, %v)", got, err)
	}
	statRepositoryPath = os.Stat
	directory := t.TempDir()
	if got, err := repositoryRoot(directory); err != nil || got != directory {
		t.Fatalf("directory root = (%q, %v)", got, err)
	}
	file := filepath.Join(directory, "file")
	writeRunHelperFile(t, directory, "file", "file")
	if got, err := repositoryRoot(file); err == nil || got != "" || !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("file root = (%q, %v)", got, err)
	}
	if got, err := repositoryRoot(""); err != nil || !filepath.IsAbs(got) {
		t.Fatalf("default root = (%q, %v)", got, err)
	}

	input := []string{
		"b=2", "A=1", "a=last", "invalid", "=empty",
		"GOPROXY=network", "GOSUMDB=network", "GOTELEMETRY=on", "GOTOOLCHAIN=auto",
		"STARSHIP_SESSION_KEY=changes-every-shell", "__mise_session=changes-every-shell",
	}
	environment := executionEnvironment(input)
	if !slices.IsSorted(environment) || !containsEnvironment(environment, "a", "last") || !containsEnvironment(environment, "b", "2") ||
		!containsEnvironment(environment, "GOPROXY", "off") || !containsEnvironment(environment, "GOSUMDB", "off") ||
		!containsEnvironment(environment, "GOTELEMETRY", "off") || !containsEnvironment(environment, "GOTOOLCHAIN", "local") ||
		containsEnvironment(environment, "STARSHIP_SESSION_KEY", "changes-every-shell") ||
		containsEnvironment(environment, "__MISE_SESSION", "changes-every-shell") ||
		slices.Contains(environment, "invalid=") || slices.Contains(environment, "=empty") {
		t.Fatalf("executionEnvironment = %v", environment)
	}
	if input[0] != "b=2" {
		t.Fatal("executionEnvironment mutated input")
	}
	t.Setenv("GOATEST_EXECUTION_ENV_MARKER", "ready")
	if got := executionEnvironment(nil); !containsEnvironment(got, "GOATEST_EXECUTION_ENV_MARKER", "ready") {
		t.Fatalf("process execution environment missing marker: %v", got)
	}

	var events []Event
	emit(Options{}, "ignored", "ignored")
	emit(Options{Progress: func(event Event) { events = append(events, event) }}, "kind", "detail")
	if !reflect.DeepEqual(events, []Event{{Kind: "kind", Detail: "detail"}}) {
		t.Fatalf("emit events = %v", events)
	}
}

func listedPackageJSON(t *testing.T, root string) []byte {
	t.Helper()
	item := map[string]any{
		"ImportPath": "fixture.example/module", "Dir": root, "Deps": []string{"example.com/dependency"},
		"Module": map[string]any{"Path": "fixture.example/module", "Dir": root},
	}
	data, err := json.Marshal(item)
	if err != nil {
		t.Fatal(err)
	}
	return append(data, '\n')
}

func moduleGraphJSON(t *testing.T, modules ...listedModule) []byte {
	t.Helper()
	var output strings.Builder
	encoder := json.NewEncoder(&output)
	for _, module := range modules {
		if err := encoder.Encode(module); err != nil {
			t.Fatal(err)
		}
	}
	return []byte(output.String())
}

func digestText(input string) string {
	sum := sha256.Sum256([]byte(input))
	return hex.EncodeToString(sum[:])
}

func writeRunHelperFile(t *testing.T, root, relative, contents string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

func containsEnvironment(environment []string, key, value string) bool {
	for _, entry := range environment {
		name, got, ok := strings.Cut(entry, "=")
		if ok && strings.EqualFold(name, key) && got == value {
			return true
		}
	}
	return false
}
