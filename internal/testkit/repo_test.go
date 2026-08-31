// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package testkit_test

import (
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/P4suta/goatest/internal/testkit"
)

// reexecHelperVariable activates the subprocess half of the os.Args[0]
// re-execution helper, mirroring the provider helpers in internal/assure.
const (
	reexecHelperVariable = "GOATEST_TESTKIT_HELPER"
	reexecHelperMarker   = "testkit-reexec-helper-ok"
)

// TestTestkitReexecHelper stays inert unless a parent test activates it
// through the environment, exactly like TestRunResourceProviderHelper.
func TestTestkitReexecHelper(t *testing.T) {
	t.Parallel()
	if !testkit.HelperEnabled(reexecHelperVariable) {
		return
	}
	fmt.Println(reexecHelperMarker)
}

func TestRepoBuildsFixtureAcceptedByGoList(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name   string
		module string
		build  func(*testing.T) *testkit.Repo
	}{
		{
			name:   "default-module",
			module: testkit.BoundaryModule,
			build:  func(t *testing.T) *testkit.Repo { return testkit.NewRepo(t).BoundaryFixture() },
		},
		{
			name:   "explicit-module",
			module: "fixture.example/custom",
			build: func(t *testing.T) *testkit.Repo {
				return testkit.NewRepo(t).Module("fixture.example/custom").BoundaryFixture()
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			repository := testCase.build(t)
			if output := runGo(t, repository.Root(), "list", "./..."); output != testCase.module {
				t.Fatalf("go list = %q, want %q", output, testCase.module)
			}
			if output := runGo(t, repository.Root(), "vet", "./..."); output != "" {
				t.Fatalf("go vet = %q, want no diagnostics", output)
			}
			for _, name := range []string{"go.mod", "boundary.go", "boundary_test.go"} {
				if _, err := os.Stat(repository.Path(name)); err != nil {
					t.Errorf("fixture is missing %s: %v", name, err)
				}
			}
		})
	}
}

func TestRepoFileWritesContentsVerbatimAndCreatesParents(t *testing.T) {
	t.Parallel()
	const contents = "first\r\nsecond\nthird"
	repository := testkit.NewRepo(t).Module("fixture.example/files").File("nested/dir/data.txt", contents)

	stored, err := os.ReadFile(repository.Path("nested/dir/data.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(stored) != contents {
		t.Fatalf("stored contents = %q, want %q", stored, contents)
	}
	module, err := os.ReadFile(repository.Path("go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(module), "module fixture.example/files\n") || !strings.Contains(string(module), "\ngo 1.") {
		t.Fatalf("go.mod = %q", module)
	}
	if information, err := os.Stat(repository.Root()); err != nil || !information.IsDir() {
		t.Fatalf("Root is not a directory: %v", err)
	}
}

func TestRepoGitCommitsTheFixtureDeterministically(t *testing.T) {
	t.Parallel()
	gitBinary(t)
	repository := testkit.NewRepo(t).BoundaryFixture().Git()

	commits := strings.Fields(runGit(t, repository.Root(), "log", "--format=%H"))
	if len(commits) != 1 {
		t.Fatalf("commits = %v, want exactly one", commits)
	}
	if status := runGit(t, repository.Root(), "status", "--porcelain"); status != "" {
		t.Errorf("worktree is not clean after Git: %q", status)
	}
	if branch := runGit(t, repository.Root(), "rev-parse", "--abbrev-ref", "HEAD"); branch != testkit.GitBranch {
		t.Errorf("branch = %q, want %q", branch, testkit.GitBranch)
	}
	identity := strings.Split(runGit(t, repository.Root(), "log", "-1", "--format=%s%n%an%n%ae%n%at%n%ct"), "\n")
	want := []string{
		testkit.GitCommitMessage, testkit.GitUserName, testkit.GitUserEmail,
		strconv.Itoa(testkit.GitCommitUnixTime), strconv.Itoa(testkit.GitCommitUnixTime),
	}
	if !slices.Equal(identity, want) {
		t.Errorf("commit identity = %q, want %q", identity, want)
	}
}

func TestHelperArgvReexecutesTheTestBinary(t *testing.T) {
	t.Parallel()
	argv := testkit.HelperArgv("TestTestkitReexecHelper")
	if len(argv) != 2 || argv[0] != os.Args[0] || argv[1] != "-test.run=^TestTestkitReexecHelper$" {
		t.Fatalf("HelperArgv = %q", argv)
	}

	command := exec.CommandContext(t.Context(), argv[0], argv[1:]...)
	command.Env = append(os.Environ(), reexecHelperVariable+"=1")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("re-execution failed: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), reexecHelperMarker) {
		t.Fatalf("re-executed helper output = %q, want %q", output, reexecHelperMarker)
	}
	if testkit.HelperEnabled(reexecHelperVariable) {
		t.Error("HelperEnabled reported an inactive helper as active")
	}
}

func runGo(t *testing.T, root string, arguments ...string) string {
	t.Helper()
	command := exec.CommandContext(t.Context(), testkit.GoBinary(t), arguments...)
	command.Dir = root
	command.Env = append(os.Environ(),
		"GOPROXY=off", "GOSUMDB=off", "GOTELEMETRY=off", "GOTOOLCHAIN=local", "GOFLAGS=-mod=mod")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("go %v: %v\n%s", arguments, err, output)
	}
	return strings.TrimSpace(string(output))
}

func gitBinary(t *testing.T) string {
	t.Helper()
	path, err := exec.LookPath("git")
	if err != nil {
		t.Skipf("git binary unavailable: %v", err)
	}
	return path
}

func runGit(t *testing.T, root string, arguments ...string) string {
	t.Helper()
	command := exec.CommandContext(t.Context(), gitBinary(t), arguments...)
	command.Dir = root
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", arguments, err, output)
	}
	return strings.TrimSpace(string(output))
}
