// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package testkit provides the fixtures and scripted fakes goatest's tests are
// built from. It is test-only support: no production package may import it.
// Every helper is deterministic so that a fixture, and any golden file derived
// from one, does not depend on the machine or the operator that built it.
package testkit

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
)

// Fixture identities are constants rather than generated values so that two
// runs of the same test produce byte-identical repositories.
const (
	// BoundaryModule is the module path BoundaryFixture declares unless Module
	// already selected one.
	BoundaryModule = "fixture.example/assured"
	// GitBranch is the branch Git commits on, independent of the operator's
	// init.defaultBranch.
	GitBranch = "main"
	// GitCommitMessage, GitUserName, and GitUserEmail are the whole authorship
	// identity of the single fixture commit.
	GitCommitMessage = "testkit fixture"
	GitUserName      = "goatest testkit"
	GitUserEmail     = "testkit@goatest.invalid"
	// GitCommitUnixTime is 2026-01-01T00:00:00Z, used as both the author and
	// the committer date so that the commit hash is reproducible.
	GitCommitUnixTime = 1767225600
)

// fixtureGoDirective is the language version fixture modules declare. It
// tracks goatest's own go.mod so that a fixture never asks for a newer
// toolchain than the one running the tests.
const fixtureGoDirective = "go 1.26.0"

const boundarySource = `package assured

// Boundary clamps value to the largest accepted input, the single guarded
// behaviour this fixture's test and mutants argue about.
func Boundary(value int) int {
	if value < 10 {
		return value
	}
	return 9
}
`

const boundaryTestSource = `package assured

import "testing"

func TestBoundary(t *testing.T) {
	for _, value := range []int{5, 10} {
		want := value
		if value >= 10 {
			want = 9
		}
		if got := Boundary(value); got != want {
			t.Fatalf("Boundary(%d) = %d, want %d", value, got, want)
		}
	}
}
`

// Repo builds a fixture repository under a test's temporary directory. Every
// method reports failures through the owning test and returns the Repo, so
// that a fixture reads as one chain.
type Repo struct {
	t      *testing.T
	root   string
	module string
}

// NewRepo starts an empty fixture repository that the test framework removes
// when the test finishes.
func NewRepo(t *testing.T) *Repo {
	t.Helper()
	return &Repo{t: t, root: t.TempDir()}
}

// Module writes go.mod for path, replacing any module declared earlier.
func (repository *Repo) Module(path string) *Repo {
	repository.t.Helper()
	repository.module = path
	return repository.File("go.mod", "module "+path+"\n\n"+fixtureGoDirective+"\n")
}

// File writes contents verbatim to a slash-separated path inside the
// repository, creating the parent directories it needs. Line endings are not
// rewritten: a fixture that asserts on CRLF input must receive CRLF bytes.
func (repository *Repo) File(path, contents string) *Repo {
	repository.t.Helper()
	full := repository.Path(path)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		repository.t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(contents), 0o644); err != nil {
		repository.t.Fatal(err)
	}
	return repository
}

// BoundaryFixture writes the smallest module an assurance run has a verdict
// for: one guarded boundary and the test that pins it. The module path stays
// whatever Module already selected, so a caller may name its own fixture.
func (repository *Repo) BoundaryFixture() *Repo {
	repository.t.Helper()
	if repository.module == "" {
		repository.Module(BoundaryModule)
	}
	return repository.
		File("boundary.go", boundarySource).
		File("boundary_test.go", boundaryTestSource)
}

// Git turns the fixture into a repository with exactly one commit of the whole
// worktree. The commit's branch, identity, and both dates are fixed, and the
// operator's global and system configuration is ignored, so that neither the
// commit nor a report derived from it varies between machines. The test is
// skipped when git is unavailable.
func (repository *Repo) Git() *Repo {
	repository.t.Helper()
	git := gitBinary(repository.t)
	date := strconv.Itoa(GitCommitUnixTime) + " +0000"
	environment := append(os.Environ(),
		"GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_SYSTEM="+os.DevNull,
		"GIT_AUTHOR_DATE="+date, "GIT_COMMITTER_DATE="+date)
	for _, arguments := range [][]string{
		{"init", "--quiet", "--initial-branch=" + GitBranch},
		{"config", "user.name", GitUserName},
		{"config", "user.email", GitUserEmail},
		{"add", "-A"},
		{"commit", "--quiet", "-m", GitCommitMessage},
	} {
		command := exec.CommandContext(repository.t.Context(), git, arguments...)
		command.Dir = repository.root
		command.Env = environment
		if output, err := command.CombinedOutput(); err != nil {
			repository.t.Fatalf("git %v: %v\n%s", arguments, err, output)
		}
	}
	return repository
}

// Root is the absolute path of the fixture repository.
func (repository *Repo) Root() string { return repository.root }

// Path resolves a slash-separated fixture path against the repository root.
func (repository *Repo) Path(relative string) string {
	return filepath.Join(repository.root, filepath.FromSlash(relative))
}

// GoBinary resolves the go command a test drives a fixture with, skipping the
// test when no toolchain is installed.
func GoBinary(t *testing.T) string {
	t.Helper()
	path, err := exec.LookPath("go")
	if err != nil {
		t.Skipf("Go binary unavailable: %v", err)
	}
	return path
}

func gitBinary(t *testing.T) string {
	t.Helper()
	path, err := exec.LookPath("git")
	if err != nil {
		t.Skipf("git binary unavailable: %v", err)
	}
	return path
}
