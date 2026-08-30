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

	"github.com/P4suta/goatest/internal/evidence"
	goanalysis "github.com/P4suta/goatest/internal/golang"
)

func TestSafeChangedPathCanonicalizesLocalFilesAndRejectsEveryEscapeForm(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		path string
		want string
		ok   bool
	}{
		{path: "pkg/value.go", want: "pkg/value.go", ok: true},
		{path: "./pkg/../pkg/value.go", want: "pkg/value.go", ok: true},
		{path: ""},
		{path: "."},
		{path: ".."},
		{path: "../value.go"},
		{path: "/value.go"},
		{path: "./A:/value.go"},
		{path: `pkg\value.go`},
		{path: "pkg/\x00.go"},
		{path: "pkg/value\n.go"},
		{path: "pkg/value\r.go"},
	} {
		got, ok := safeChangedPath(test.path)
		if got != test.want || ok != test.ok {
			t.Errorf("safeChangedPath(%q) = (%q, %t), want (%q, %t)", test.path, got, ok, test.want, test.ok)
		}
	}
}

func TestGeneratedImpactPathMatchesOnlyOwnedTopLevelDescendants(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		path string
		want bool
	}{
		{path: ".goatest/graph-v1.json", want: true},
		{path: "reports/assurance-report-v1.json", want: true},
		{path: "dist/goatest", want: true},
		{path: ".goatest"},
		{path: ".goatest/"},
		{path: ".goatest.toml"},
		{path: "reports"},
		{path: "reports/"},
		{path: "reports.go"},
		{path: "distribution/value.go"},
		{path: "pkg/.goatest/state.go"},
	} {
		if got := generatedImpactPath(test.path); got != test.want {
			t.Errorf("generatedImpactPath(%q) = %t, want %t", test.path, got, test.want)
		}
	}
}

func TestRunGitNamesUsesNULTerminatedNamesAndFailsClosed(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		preserveImpactHooks(t)
		root := t.TempDir()
		arguments := []string{"diff", "-z"}
		gitNamesOutput = func(ctx context.Context, gotRoot string, gotArguments []string) ([]byte, error) {
			if ctx.Err() != nil || gotRoot != root || !slices.Equal(gotArguments, arguments) {
				t.Fatalf("git output args = (%v, %q, %v)", ctx.Err(), gotRoot, gotArguments)
			}
			return []byte("pkg/a file.go\x00pkg/b.go\x00"), nil
		}
		got, ok := runGitNames(context.Background(), root, arguments)
		want := []string{"pkg/a file.go", "pkg/b.go"}
		if !ok || !slices.Equal(got, want) {
			t.Fatalf("runGitNames = (%v, %t), want (%v, true)", got, ok, want)
		}
	})

	for _, stage := range []string{"command", "context"} {
		t.Run(stage, func(t *testing.T) {
			preserveImpactHooks(t)
			ctx := context.Background()
			if stage == "context" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			sentinel := errors.New("git failed")
			gitNamesOutput = func(context.Context, string, []string) ([]byte, error) {
				if stage == "command" {
					return nil, sentinel
				}
				return []byte("pkg/a.go\x00"), nil
			}
			if names, ok := runGitNames(ctx, t.TempDir(), []string{"diff", "-z"}); ok || names != nil {
				t.Fatalf("runGitNames(%s failure) = (%v, %t)", stage, names, ok)
			}
		})
	}
}

func TestChangedFilesCombinesSortsAndValidatesDiffAndUntrackedNames(t *testing.T) {
	t.Run("default reference", func(t *testing.T) {
		preserveImpactHooks(t)
		root := t.TempDir()
		calls := 0
		runImpactGitNames = func(_ context.Context, gotRoot string, arguments []string) ([]string, bool) {
			calls++
			if gotRoot != root {
				t.Fatalf("git root = %q, want %q", gotRoot, root)
			}
			if calls == 1 {
				want := []string{"diff", "--no-ext-diff", "--name-only", "--diff-filter=ACMRD", "-z", "HEAD", "--"}
				if !slices.Equal(arguments, want) {
					t.Fatalf("diff arguments = %v, want %v", arguments, want)
				}
				return []string{
					"pkg/b.go", "./pkg/../pkg/a.go", ".goatest.toml",
					"pkg/.goatest/state.go", "reports.go", "distribution/value.go",
				}, true
			}
			want := []string{"ls-files", "--others", "--exclude-standard", "-z", "--"}
			if !slices.Equal(arguments, want) {
				t.Fatalf("untracked arguments = %v, want %v", arguments, want)
			}
			return []string{
				"pkg/a.go", "pkg/c.go",
				".goatest/graph-v1.json", "reports/assurance-report-v1.json", "dist/goatest",
			}, true
		}
		got, known := changedFiles(context.Background(), root, "")
		want := []string{
			".goatest.toml", "distribution/value.go", "pkg/.goatest/state.go",
			"pkg/a.go", "pkg/b.go", "pkg/c.go", "reports.go",
		}
		if !known || !slices.Equal(got, want) || calls != 2 {
			t.Fatalf("changedFiles = (%v, %t), calls=%d", got, known, calls)
		}
	})

	t.Run("explicit reference", func(t *testing.T) {
		preserveImpactHooks(t)
		calls := 0
		runImpactGitNames = func(_ context.Context, _ string, arguments []string) ([]string, bool) {
			calls++
			if calls == 1 && arguments[5] != "origin/main" {
				t.Fatalf("reference = %q", arguments[5])
			}
			return nil, true
		}
		if got, known := changedFiles(context.Background(), t.TempDir(), "origin/main"); !known || len(got) != 0 {
			t.Fatalf("changedFiles = (%v, %t)", got, known)
		}
	})

	for _, stage := range []string{"diff", "untracked", "invalid path"} {
		t.Run(stage, func(t *testing.T) {
			preserveImpactHooks(t)
			calls := 0
			runImpactGitNames = func(context.Context, string, []string) ([]string, bool) {
				calls++
				switch stage {
				case "diff":
					return nil, false
				case "untracked":
					return nil, calls == 1
				default:
					if calls == 1 {
						return []string{"/escape.go"}, true
					}
					return nil, true
				}
			}
			if got, known := changedFiles(context.Background(), t.TempDir(), "HEAD~1"); known || got != nil {
				t.Fatalf("changedFiles(%s) = (%v, %t)", stage, got, known)
			}
		})
	}
}

func TestSelectImpactFallsBackBroadAndSelectsCoveredPackageDependents(t *testing.T) {
	targets := []goanalysis.Target{
		{ID: "target-a", Package: "example/a", RelativeDir: "a"},
		{ID: "target-b", Package: "example/b", RelativeDir: "b", Dependencies: []string{"example/a"}},
		{ID: "target-c", Package: "example/c", RelativeDir: "c"},
		{ID: "target-new-a", Package: "example/a", RelativeDir: "a"},
		{ID: "target-new-dependent", Package: "example/d", RelativeDir: "d", Dependencies: []string{"example/a"}},
	}
	record := evidence.GraphRecord{
		Schema: evidence.GraphSchemaV1, ModulePath: "example",
		Graph: evidence.Graph{
			FilePackages: map[string]string{
				"a/a.go": "example/a", "a/a_test.go": "example/a",
				"b/b.go": "example/b", "c/c.go": "example/c",
			},
			Targets: []evidence.Target{
				{ID: "target-a", Package: "example/a", CoveredFiles: []string{"a/a.go"}},
				{ID: "target-b", Package: "example/b", Dependencies: []string{"example/a"}, CoveredFiles: []string{"b/b.go"}},
				{ID: "target-c", Package: "example/c", CoveredFiles: []string{"c/c.go"}},
			},
		},
	}

	t.Run("changed disabled", func(t *testing.T) {
		preserveImpactHooks(t)
		loadImpactGraph = func(string) (evidence.GraphRecord, bool, error) {
			t.Fatal("graph loaded when changed mode is disabled")
			return evidence.GraphRecord{}, false, nil
		}
		selection := selectImpact(context.Background(), t.TempDir(), goanalysis.Model{ModulePath: "example"}, targets, Options{})
		if !selection.broad || len(selection.targets) != len(targets) || selection.prior != nil {
			t.Fatalf("selection = %+v", selection)
		}
		selection.targets[0].ID = "mutated"
		if targets[0].ID != "target-a" {
			t.Fatal("selection aliases caller targets")
		}
	})

	for _, stage := range []string{"load error", "not found", "module mismatch"} {
		t.Run(stage, func(t *testing.T) {
			preserveImpactHooks(t)
			loadImpactGraph = func(path string) (evidence.GraphRecord, bool, error) {
				if !strings.HasSuffix(filepath.ToSlash(path), "/.goatest/graph-v1.json") {
					t.Fatalf("graph path = %q", path)
				}
				switch stage {
				case "load error":
					return record, true, errors.New("load failed")
				case "not found":
					return record, false, nil
				default:
					other := record
					other.ModulePath = "other"
					return other, true, nil
				}
			}
			changedImpactFiles = func(context.Context, string, string) ([]string, bool) {
				t.Fatal("changed files inspected without matching graph")
				return nil, false
			}
			selection := selectImpact(context.Background(), t.TempDir(), goanalysis.Model{ModulePath: "example"}, targets, Options{Changed: true})
			if !selection.broad || len(selection.targets) != len(targets) || selection.prior != nil {
				t.Fatalf("selection = %+v", selection)
			}
		})
	}

	for _, test := range []struct {
		name        string
		changed     []string
		known       bool
		wantBroad   bool
		wantTargets []string
	}{
		{name: "unknown changes", known: false, wantBroad: true, wantTargets: []string{"target-a", "target-b", "target-c", "target-new-a", "target-new-dependent"}},
		{name: "empty changes", known: true, wantBroad: true, wantTargets: []string{"target-a", "target-b", "target-c", "target-new-a", "target-new-dependent"}},
		{name: "unknown file", changed: []string{"unknown.go"}, known: true, wantBroad: true, wantTargets: []string{"target-a", "target-b", "target-c", "target-new-a", "target-new-dependent"}},
		{name: "covered production", changed: []string{"a/a.go"}, known: true, wantTargets: []string{"target-a", "target-b"}},
		{name: "test structure", changed: []string{"a/a_test.go"}, known: true, wantTargets: []string{"target-a", "target-b", "target-new-a", "target-new-dependent"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			preserveImpactHooks(t)
			loadImpactGraph = func(string) (evidence.GraphRecord, bool, error) { return record, true, nil }
			changedImpactFiles = func(_ context.Context, _ string, reference string) ([]string, bool) {
				if reference != "origin/main" {
					t.Fatalf("reference = %q", reference)
				}
				return slices.Clone(test.changed), test.known
			}
			selection := selectImpact(context.Background(), t.TempDir(), goanalysis.Model{ModulePath: "example"}, targets, Options{Changed: true, ChangedRef: "origin/main"})
			gotIDs := make([]string, 0, len(selection.targets))
			for _, target := range selection.targets {
				gotIDs = append(gotIDs, target.ID)
			}
			if selection.broad != test.wantBroad || !slices.Equal(gotIDs, test.wantTargets) || !slices.Equal(selection.changed, test.changed) || selection.prior == nil {
				t.Fatalf("selection = {broad:%t targets:%v changed:%v prior:%v}", selection.broad, gotIDs, selection.changed, selection.prior != nil)
			}
		})
	}
}

func TestDependsOnChangedChecksEveryDependency(t *testing.T) {
	t.Parallel()
	changed := map[string]bool{"example/b": true}
	if !dependsOnChanged([]string{"example/a", "example/b"}, changed) {
		t.Fatal("changed second dependency was not detected")
	}
	if dependsOnChanged([]string{"example/a", "example/c"}, changed) || dependsOnChanged(nil, changed) {
		t.Fatal("unchanged dependency was reported changed")
	}
}

func TestBuildGraphMapsGoFilesAndClonesTargetEvidence(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "pkg", "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	for path, contents := range map[string]string{
		"root.go": "package example\n", "root_test.go": "package example\n", "README.md": "ignored\n",
		"pkg/value.go": "package pkg\n", "pkg/value_test.go": "package pkg\n", "pkg/nested/ignored.go": "package nested\n",
	} {
		full := filepath.Join(root, filepath.FromSlash(path))
		if err := os.WriteFile(full, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	model := goanalysis.Model{ModulePath: "example", Packages: []goanalysis.Package{
		{ImportPath: "example", RelativeDir: "."},
		{ImportPath: "example/pkg", RelativeDir: "pkg"},
	}}
	dependencies := []string{"example/pkg"}
	covered := []string{"pkg/value.go"}
	targetEvidence := []TargetEvidence{{Target: goanalysis.Target{
		ID: "target-a", Package: "example", Kind: goanalysis.KindTest, Dependencies: dependencies,
	}, CoveredFiles: covered}}
	graph, err := buildGraph(root, model, targetEvidence)
	if err != nil {
		t.Fatal(err)
	}
	wantFiles := map[string]string{
		"root.go": "example", "root_test.go": "example",
		"pkg/value.go": "example/pkg", "pkg/value_test.go": "example/pkg",
	}
	if !reflect.DeepEqual(graph.FilePackages, wantFiles) || len(graph.Targets) != 1 {
		t.Fatalf("graph = %+v", graph)
	}
	wantTarget := evidence.Target{ID: "target-a", Package: "example", Kind: "test", Dependencies: dependencies, CoveredFiles: covered}
	if !reflect.DeepEqual(graph.Targets[0], wantTarget) {
		t.Fatalf("target = %+v, want %+v", graph.Targets[0], wantTarget)
	}
	dependencies[0], covered[0] = "mutated", "mutated.go"
	if graph.Targets[0].Dependencies[0] != "example/pkg" || graph.Targets[0].CoveredFiles[0] != "pkg/value.go" {
		t.Fatal("graph aliases target evidence")
	}

	missing := goanalysis.Model{ModulePath: "example", Packages: []goanalysis.Package{{ImportPath: "example/missing", RelativeDir: "missing"}}}
	if got, err := buildGraph(root, missing, nil); err == nil || got.FilePackages != nil || got.Targets != nil || !strings.Contains(err.Error(), "build graph for example/missing") {
		t.Fatalf("missing buildGraph = (%+v, %v)", got, err)
	}
}

func TestMergeGraphPreservesUnselectedPriorTargetsOnlyForNarrowRuns(t *testing.T) {
	t.Parallel()
	current := evidence.Graph{FilePackages: map[string]string{"new.go": "example"}, Targets: []evidence.Target{{ID: "updated"}}}
	prior := &evidence.GraphRecord{Graph: evidence.Graph{Targets: []evidence.Target{{ID: "updated"}, {ID: "preserved"}}}}
	selection := impactSelection{targets: []goanalysis.Target{{ID: "updated"}}}
	merged := mergeGraph(current, prior, selection)
	if !reflect.DeepEqual(merged.FilePackages, current.FilePackages) || len(merged.Targets) != 2 || merged.Targets[0].ID != "updated" || merged.Targets[1].ID != "preserved" {
		t.Fatalf("merged graph = %+v", merged)
	}
	if got := mergeGraph(current, nil, selection); !reflect.DeepEqual(got, current) {
		t.Fatalf("nil-prior merge = %+v", got)
	}
	selection.broad = true
	if got := mergeGraph(current, prior, selection); !reflect.DeepEqual(got, current) {
		t.Fatalf("broad merge = %+v", got)
	}
}

func TestMutationScopeIsDeterministicNarrowAndPackageRelative(t *testing.T) {
	t.Parallel()
	if include, packages := mutationScope(impactSelection{broad: true}); include != nil || packages != nil {
		t.Fatalf("broad scope = (%v, %v)", include, packages)
	}
	selection := impactSelection{
		changed: []string{"z.go", "a_test.go", "README.md", "a.go", "z.go"},
		targets: []goanalysis.Target{
			{RelativeDir: "pkg"}, {RelativeDir: "./pkg"}, {RelativeDir: "."}, {RelativeDir: ""},
		},
	}
	include, packages := mutationScope(selection)
	if !slices.Equal(include, []string{"a.go", "z.go"}) || !slices.Equal(packages, []string{".", "./pkg"}) {
		t.Fatalf("narrow scope = (%v, %v)", include, packages)
	}
}

func preserveImpactHooks(t *testing.T) {
	t.Helper()
	loadHook, changedHook := loadImpactGraph, changedImpactFiles
	runHook, outputHook := runImpactGitNames, gitNamesOutput
	t.Cleanup(func() {
		loadImpactGraph, changedImpactFiles = loadHook, changedHook
		runImpactGitNames, gitNamesOutput = runHook, outputHook
	})
}
