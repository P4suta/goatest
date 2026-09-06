// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package testkit

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/P4suta/goatest/internal/filemode"
)

const (
	BoundaryModule = "fixture.example/assured"

	NarrowedBranchModule = "fixture.example/narrowed"

	GitBranch = "main"

	GitCommitMessage = "testkit fixture"
	GitUserName      = "goatest testkit"
	GitUserEmail     = "testkit@goatest.invalid"

	GitCommitUnixTime = 1767225600
)

const fixtureGoDirective = "go 1.26.0"

const boundarySource = `package assured

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

func TestBoundaryAtZero(t *testing.T) {
	if got := Boundary(0); got != 0 {
		t.Fatalf("Boundary(0) = %d, want 0", got)
	}
}
`

const narrowedBranchSource = `package narrowed

import "strconv"

func Clamp(value, limit int) (int, bool) {
	if value <= limit {
		return value, false
	}
	return limit, true
}

func Load(text string) (int, error) {
	value, err := strconv.Atoi(text)
	if err != nil {
		return 0, err
	}
	return value, nil
}
`

const narrowedBranchTestSource = `package narrowed

import "testing"

func TestClampBelow(t *testing.T) {
	if got, clamped := Clamp(1, 10); got != 1 || clamped {
		t.Fatalf("Clamp(1, 10) = (%d, %t)", got, clamped)
	}
}

func TestClampAtLimit(t *testing.T) {
	if got, clamped := Clamp(10, 10); got != 10 || clamped {
		t.Fatalf("Clamp(10, 10) = (%d, %t)", got, clamped)
	}
}

func TestClampAbove(t *testing.T) {
	if got, clamped := Clamp(20, 10); got != 10 || !clamped {
		t.Fatalf("Clamp(20, 10) = (%d, %t)", got, clamped)
	}
}

func TestLoad(t *testing.T) {
	if got, err := Load("42"); err != nil || got != 42 {
		t.Fatalf("Load(\"42\") = (%d, %v)", got, err)
	}
}
`

type Repo struct {
	t      *testing.T
	root   string
	module string
}

func NewRepo(t *testing.T) *Repo {
	t.Helper()
	return &Repo{t: t, root: t.TempDir()}
}

func (repository *Repo) Module(path string) *Repo {
	repository.t.Helper()
	repository.module = path
	return repository.File("go.mod", "module "+path+"\n\n"+fixtureGoDirective+"\n")
}

func (repository *Repo) File(path, contents string) *Repo {
	repository.t.Helper()
	full := repository.Path(path)
	if err := os.MkdirAll(filepath.Dir(full), filemode.ReadableDirectory); err != nil {
		repository.t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(contents), filemode.ReadableFile); err != nil {
		repository.t.Fatal(err)
	}
	return repository
}

func (repository *Repo) BoundaryFixture() *Repo {
	repository.t.Helper()
	if repository.module == "" {
		repository.Module(BoundaryModule)
	}
	return repository.
		File("boundary.go", boundarySource).
		File("boundary_test.go", boundaryTestSource)
}

func (repository *Repo) NarrowedBranchFixture() *Repo {
	repository.t.Helper()
	if repository.module == "" {
		repository.Module(NarrowedBranchModule)
	}
	return repository.
		File("narrowed.go", narrowedBranchSource).
		File("narrowed_test.go", narrowedBranchTestSource)
}

func (repository *Repo) Git() *Repo {
	repository.t.Helper()
	git := gitBinary(repository.t)
	date := strconv.Itoa(GitCommitUnixTime) + " +0000"

	environment := append(os.Environ(),
		"GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_SYSTEM="+os.DevNull,
		"GIT_AUTHOR_NAME="+GitUserName, "GIT_AUTHOR_EMAIL="+GitUserEmail,
		"GIT_COMMITTER_NAME="+GitUserName, "GIT_COMMITTER_EMAIL="+GitUserEmail,
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

func (repository *Repo) Root() string { return repository.root }

func (repository *Repo) Path(relative string) string {
	return filepath.Join(repository.root, filepath.FromSlash(relative))
}

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
