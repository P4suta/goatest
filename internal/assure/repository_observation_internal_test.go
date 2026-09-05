// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package assure

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	gomutants "github.com/P4suta/go-mutants"
	"github.com/P4suta/goatest/internal/evidence"
	goanalysis "github.com/P4suta/goatest/internal/golang"
)

func TestCollectBaselineRecordsTheObservedRepositoryBoundary(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeObservationFile(t, root, "value.go", "package fixture\n")
	writeObservationFile(t, root, "docs/notes.md", "notes\n")
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("outside\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	const pkg = "fixture.example/module"
	model := goanalysis.Model{ModulePath: pkg, ModuleDir: root, Packages: []goanalysis.Package{{
		ImportPath: pkg, RelativeDir: ".",
	}}}
	inputs := evidence.Inputs{Files: map[string]string{"value.go": "value", "docs/notes.md": "notes"}}
	sources := newTargetKeySources(inputs, model, "standard-v1", Options{}, map[string]bool{pkg: true})

	for _, test := range []struct {
		name string
		path string
		want bool
	}{
		{name: "temporary input", path: outside},
		{name: "repository directory", path: root, want: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			observer := newRepositoryObserver(root, t.TempDir(), map[string]goanalysis.RepositoryReadCandidate{pkg: {}}, sources)
			workspace := &baselineFakeWorkspace{exec: func(command gomutants.Command) (gomutants.CommandResult, error) {
				for _, argument := range command.Argv {
					if log, found := strings.CutPrefix(argument, "-test.testlogfile="); found {
						if err := os.WriteFile(log, []byte("# test log\nopen "+test.path+"\n"), 0o600); err != nil {
							return gomutants.CommandResult{}, err
						}
						profile := coverageProfileArgument(command)
						if err := os.WriteFile(profile, []byte("mode: set\n"+pkg+"/value.go:1.1,1.16 1 1\n"), 0o600); err != nil {
							return gomutants.CommandResult{}, err
						}
					}
				}
				return gomutants.CommandResult{}, nil
			}}
			result, err := CollectBaseline(context.Background(), workspace, model, []BaselineTarget{{
				Target: baselineTestTarget("TestValue"),
			}}, BaselineOptions{ArtifactDirectory: t.TempDir(), RepositoryObserver: observer})
			if err != nil {
				t.Fatal(err)
			}
			if len(result.Targets) != 1 || result.Targets[0].WholeTree != test.want {
				t.Fatalf("baseline targets = %+v, want whole_tree=%t", result.Targets, test.want)
			}
		})
	}
}

func TestRepositoryObservationWidensOnlyForUnaccountedRepositoryInputs(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeObservationFile(t, root, "pkg/value.go", "package pkg\n")
	writeObservationFile(t, root, "docs/notes.md", "notes\n")
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("outside\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	const pkg = "fixture.example/module/pkg"
	inputs := evidence.Inputs{Files: map[string]string{
		"go.mod": "module", "pkg/value.go": "value", "docs/notes.md": "notes",
	}}
	model := goanalysis.Model{ModulePath: "fixture.example/module", ModuleDir: root, Packages: []goanalysis.Package{{
		ImportPath: pkg, RelativeDir: "pkg",
	}}}
	sources := newTargetKeySources(inputs, model, "standard-v1", Options{}, map[string]bool{pkg: true})
	observer := newRepositoryObserver(root, t.TempDir(), map[string]goanalysis.RepositoryReadCandidate{
		pkg: {},
	}, sources)
	target := goanalysis.Target{Package: pkg, RelativeDir: "pkg"}
	header := "# test log\n"

	for _, test := range []struct {
		name string
		log  string
		want bool
	}{
		{name: "no file access", log: header},
		{name: "temporary file", log: header + "open " + outside + "\n"},
		{name: "relative narrow file", log: header + "open value.go\n"},
		{name: "absolute narrow file", log: header + "stat " + filepath.Join(root, "pkg", "value.go") + "\n"},
		{name: "narrow external file", log: header + "open " + filepath.Join(root, "docs", "notes.md") + "\n", want: true},
		{name: "repository directory", log: header + "open " + filepath.Join(root, "pkg") + "\n", want: true},
		{name: "missing repository path", log: header + "stat " + filepath.Join(root, "later.txt") + "\n", want: true},
		{name: "repository chdir", log: header + "chdir " + root + "\n", want: true},
		{name: "unknown operation", log: header + "read pkg/value.go\n", want: true},
		{name: "truncated log", log: header + "open value.go", want: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			observation := parseRepositoryTestLog([]byte(test.log), root, filepath.Join(root, "pkg"))
			if got := observer.wholeTree(target, observation); got != test.want {
				t.Fatalf("whole tree = %t, want %t; observation=%+v", got, test.want, observation)
			}
		})
	}
}

func TestRepositoryObserverRefusesToNarrowPathsTheTestLogCannotEncode(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeObservationFile(t, root, "pkg/value.go", "package pkg\n")
	const pkg = "fixture.example/module/pkg"
	sources := newTargetKeySources(evidence.Inputs{Files: map[string]string{
		"pkg/value.go": "value", "docs/line\nbreak.md": "newline",
	}}, goanalysis.Model{ModuleDir: root, Packages: []goanalysis.Package{{
		ImportPath: pkg, RelativeDir: "pkg",
	}}}, "standard-v1", Options{}, map[string]bool{pkg: true})
	observer := newRepositoryObserver(root, t.TempDir(), map[string]goanalysis.RepositoryReadCandidate{pkg: {}}, sources)
	arguments, finish := observer.instrumentPackage(pkg, nil)
	if len(arguments) != 0 || !observer.wholeTree(goanalysis.Target{Package: pkg}, finish()) {
		t.Fatalf("newline-bearing tree was dynamically narrowed: args=%v", arguments)
	}
}

func TestRepositoryObserverUsesPrivateUniqueLogsAndCleansThem(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeObservationFile(t, root, "pkg/value.go", "package pkg\n")
	const pkg = "fixture.example/module/pkg"
	sources := newTargetKeySources(evidence.Inputs{Files: map[string]string{"pkg/value.go": "value"}},
		goanalysis.Model{ModuleDir: root, Packages: []goanalysis.Package{{ImportPath: pkg, RelativeDir: "pkg"}}},
		"standard-v1", Options{}, map[string]bool{pkg: true})
	observer := newRepositoryObserver(root, t.TempDir(), map[string]goanalysis.RepositoryReadCandidate{pkg: {}}, sources)

	firstArgs, firstFinish := observer.instrumentPackage(pkg, []string{"-test.run=^TestValue$"})
	secondArgs, secondFinish := observer.instrumentPackage(pkg, nil)
	firstPath := testLogArgument(t, firstArgs)
	secondPath := testLogArgument(t, secondArgs)
	if firstPath == secondPath {
		t.Fatalf("two executions shared test log %q", firstPath)
	}
	if err := os.WriteFile(firstPath, []byte("# test log\nopen value.go\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secondPath, []byte("# test log\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if firstFinish().unknown || secondFinish().unknown {
		t.Fatal("valid action logs were rejected")
	}
	for _, name := range []string{firstPath, secondPath} {
		if _, err := os.Stat(name); !os.IsNotExist(err) {
			t.Errorf("transient action log %q remains: %v", name, err)
		}
	}
	if !slices.Equal(firstArgs[:1], []string{"-test.run=^TestValue$"}) {
		t.Fatalf("test selection changed: %v", firstArgs)
	}
}

func TestRepositoryObserverKeepsKnownUnobservableCandidatesWholeTree(t *testing.T) {
	t.Parallel()
	const pkg = "fixture.example/module/pkg"
	observer := newRepositoryObserver(t.TempDir(), t.TempDir(), map[string]goanalysis.RepositoryReadCandidate{
		pkg: {Unobservable: true},
	}, targetKeySources{})
	arguments := []string{"-test.run=^TestValue$"}
	got, finish := observer.instrumentPackage(pkg, arguments)
	if !slices.Equal(got, arguments) || !observer.wholeTree(goanalysis.Target{Package: pkg}, finish()) {
		t.Fatalf("unobservable candidate was dynamically narrowed: args=%v observation=%+v", got, finish())
	}
}

func TestRepositoryTestLogFailureRecognizesRawAndTest2JSONPaths(t *testing.T) {
	t.Parallel()
	path := `C:\scratch\action.log`
	arguments := []string{"-test.testlogfile=" + path}
	event, err := json.Marshal(map[string]string{"Output": "testing: open " + path + ": access denied\n"})
	if err != nil {
		t.Fatal(err)
	}
	for _, output := range []string{"testing: open " + path + ": access denied", string(event)} {
		if !repositoryTestLogFailure(output, arguments) {
			t.Errorf("observation failure was not recognized in %q", output)
		}
	}
	if repositoryTestLogFailure("testing: an ordinary test failure", arguments) {
		t.Fatal("an unrelated test failure was attributed to the action log")
	}
}

func TestMutationObservationFailureRetriesWithoutChangingTheOutcome(t *testing.T) {
	t.Parallel()
	const pkg = "fixture.example/module/pkg"
	root := t.TempDir()
	sources := targetKeySources{model: goanalysis.Model{Packages: []goanalysis.Package{{ImportPath: pkg, RelativeDir: "."}}}}
	observer := newRepositoryObserver(root, t.TempDir(), map[string]goanalysis.RepositoryReadCandidate{pkg: {}}, sources)
	session := &mutationUnitSession{exec: func(request gomutants.ExecRequest) (gomutants.MutantResult, error) {
		if log, found := repositoryTestLogPath(request.Args); found {
			return gomutants.MutantResult{Outcome: gomutants.OutcomeKilled, OutputTail: "testing: open " + log + ": access denied"}, nil
		}
		return gomutants.MutantResult{Outcome: gomutants.OutcomeSurvived}, nil
	}}
	request := gomutants.ExecRequest{Mutant: "mutant", Package: pkg, Args: []string{"-test.run=^TestValue$"}}
	result, observation, err := executeMutation(t.Context(), session, request, MutationOptions{RepositoryObserver: observer})
	if err != nil || result.Outcome != gomutants.OutcomeSurvived || !observation.unknown {
		t.Fatalf("execution = (%+v, %+v, %v), want the unobserved survivor", result, observation, err)
	}
	if len(session.requests) != 2 || !slices.Equal(session.requests[1].Args, request.Args) {
		t.Fatalf("requests = %+v, want one observed attempt then the original request", session.requests)
	}
}

func TestBaselineObservationFailureRetriesWithoutChangingTheOutcome(t *testing.T) {
	t.Parallel()
	const pkg = "fixture.example/module"
	root := t.TempDir()
	model := goanalysis.Model{ModulePath: pkg, ModuleDir: root, Packages: []goanalysis.Package{{
		ImportPath: pkg, RelativeDir: ".",
	}}}
	sources := newTargetKeySources(evidence.Inputs{Files: map[string]string{"value.go": "value"}}, model,
		"standard-v1", Options{}, map[string]bool{pkg: true})
	observer := newRepositoryObserver(root, t.TempDir(), map[string]goanalysis.RepositoryReadCandidate{pkg: {}}, sources)
	executions := 0
	workspace := &baselineFakeWorkspace{exec: func(command gomutants.Command) (gomutants.CommandResult, error) {
		if len(command.Argv) != 0 && command.Argv[0] == "go" {
			return gomutants.CommandResult{}, nil
		}
		executions++
		if log, found := repositoryTestLogPath(command.Argv); found {
			return gomutants.CommandResult{ExitCode: 2, Output: []byte("testing: open " + log + ": access denied")}, nil
		}
		if err := os.WriteFile(coverageProfileArgument(command), []byte("mode: set\n"+pkg+"/value.go:1.1,1.16 1 1\n"), 0o600); err != nil {
			return gomutants.CommandResult{}, err
		}
		return gomutants.CommandResult{}, nil
	}}
	result, err := CollectBaseline(t.Context(), workspace, model, []BaselineTarget{{Target: baselineTestTarget("TestValue")}},
		BaselineOptions{ArtifactDirectory: t.TempDir(), RepositoryObserver: observer})
	if err != nil || executions != 2 || len(result.Targets) != 1 || !result.Targets[0].WholeTree || !result.Targets[0].RepositoryObserved {
		t.Fatalf("baseline = (%+v, %v), executions=%d; want a conservative passing retry", result, err, executions)
	}
}

func testLogArgument(t *testing.T, arguments []string) string {
	t.Helper()
	for _, argument := range arguments {
		if path, found := strings.CutPrefix(argument, "-test.testlogfile="); found {
			return path
		}
	}
	t.Fatalf("arguments carry no test action log: %v", arguments)
	return ""
}

func writeObservationFile(t *testing.T, root, relative, contents string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}
