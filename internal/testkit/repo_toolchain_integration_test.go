//go:build integration

// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package testkit_test

import (
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/P4suta/goatest/internal/testkit"
)

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

func TestRepoGitCommitsTheFixtureDeterministically(t *testing.T) {
	t.Parallel()
	testkit.GitBinary(t)
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

func TestRepoGitIgnoresTheOperatorsGitEnvironment(t *testing.T) {
	testkit.GitBinary(t)
	timestamp := strconv.Itoa(testkit.GitCommitUnixTime)
	operators := []struct {
		name  string
		email string
		date  string
	}{
		{name: "Operator One", email: "one@operator.invalid", date: "1500000000 +0900"},
		{name: "Operator Two", email: "two@operator.invalid", date: "1600000000 -0500"},
	}
	commits := make([]string, 0, len(operators))
	for _, operator := range operators {
		t.Setenv("GIT_AUTHOR_NAME", operator.name)
		t.Setenv("GIT_COMMITTER_NAME", operator.name)
		t.Setenv("GIT_AUTHOR_EMAIL", operator.email)
		t.Setenv("GIT_COMMITTER_EMAIL", operator.email)
		t.Setenv("GIT_AUTHOR_DATE", operator.date)
		t.Setenv("GIT_COMMITTER_DATE", operator.date)

		repository := testkit.NewRepo(t).BoundaryFixture().Git()

		format := "--format=%an%n%ae%n%cn%n%ce%n%at%n%ct"
		identity := strings.Split(runGit(t, repository.Root(), "log", "-1", format), "\n")
		want := []string{
			testkit.GitUserName, testkit.GitUserEmail,
			testkit.GitUserName, testkit.GitUserEmail,
			timestamp, timestamp,
		}
		if !slices.Equal(identity, want) {
			t.Errorf("commit identity under %s = %q, want %q", operator.name, identity, want)
		}
		commits = append(commits, runGit(t, repository.Root(), "rev-parse", "HEAD"))
	}
	if commits[0] != commits[1] {
		t.Errorf("commits = %q, want one hash whatever the environment sets", commits)
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

func runGit(t *testing.T, root string, arguments ...string) string {
	t.Helper()
	command := exec.CommandContext(t.Context(), testkit.GitBinary(t), arguments...)
	command.Dir = root
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", arguments, err, output)
	}
	return strings.TrimSpace(string(output))
}
