//go:build integration

// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package devgates

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"

	"github.com/P4suta/goatest/internal/testkit"
)

// The secret-scanner allowlist ledger.
//
// gitleaks does not ask git what it tracks. `gitleaks dir .` walks the working
// tree, so it reads whatever the last run left behind: this repository's
// tracked files come to about 3.3 MB and the unfiltered scan read 142 MB, all
// but the first of which is the build cache and report artifacts under paths
// git ignores. Two things follow. The gate spends its time on content the
// repository does not contain, and it can go red for that content - a lint
// failure nobody can fix by editing the repository, which is a lint failure
// people learn to scroll past.
//
// .gitleaks.toml narrows the walk. Each entry is not a judgement that something
// is not a secret; it is the claim that a path is outside the repository, and
// that claim is checkable in the two ways it can be false.

const (
	// gitleaksConfiguration holds the allowlist.
	gitleaksConfiguration = ".gitleaks.toml"

	// miseManifest declares the tasks CI runs.
	miseManifest = "mise.toml"

	// allowlistedPath is the shape an entry is allowed to take.
	//
	// gitleaks accepts arbitrary regular expressions here. A line like
	// `.*key.*` would arrive wearing the face of a tidy-up while switching the
	// scanner off, and no amount of checking the entries would notice, because
	// the check would be asking git about a pattern that is not a path. Fixing
	// the shape is what makes the questions below answerable.
	allowlistedPathPattern = `^\(\^\|/\)((?:[A-Za-z0-9_.-]|\\\.)+)/$`
)

// allowlistedPath extracts the directory an entry names.
var allowlistedPath = regexp.MustCompile(allowlistedPathPattern)

// gitleaksAllowlists is the part of the configuration this gate is about.
type gitleaksAllowlists struct {
	Allowlists []struct {
		Description string   `toml:"description"`
		Paths       []string `toml:"paths"`
	} `toml:"allowlists"`
}

// allowlistedDirectories decodes the configuration and returns the directory
// each entry names.
//
// Decoded rather than read as lines. The entries live in an array that taplo
// formats onto one line when it is short enough and across several when it is
// not, and a reader of lines depends on which of those it happens to be - a
// dependency this repository has already paid for once, in the licence gate.
func allowlistedDirectories(t *testing.T, root string) map[string]string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, gitleaksConfiguration))
	if err != nil {
		t.Fatalf("read %s: %v", gitleaksConfiguration, err)
	}
	var configuration gitleaksAllowlists
	if err := toml.Unmarshal(data, &configuration); err != nil {
		t.Fatalf("decode %s: %v", gitleaksConfiguration, err)
	}
	directories := make(map[string]string)
	for _, allowlist := range configuration.Allowlists {
		for _, entry := range allowlist.Paths {
			match := allowlistedPath.FindStringSubmatch(entry)
			if match == nil {
				t.Errorf("%s allows path %q, which is not of the form (^|/)name/ this gate can check", gitleaksConfiguration, entry)
				continue
			}
			directories[entry] = strings.ReplaceAll(match[1], `\.`, ".")
		}
	}
	// Fail closed. A loop over an empty list passes every assertion in it, and
	// that is the shape a gate takes when it has stopped looking: the reader
	// above was told this file holds entries, and if it holds none then either
	// the file moved or the decoding is wrong, and neither is a pass.
	if len(directories) == 0 {
		t.Fatalf("%s yielded no allowlisted path, and the reader of it was told there were some", gitleaksConfiguration)
	}
	return directories
}

// TestEveryScannerAllowlistIsAPathGitIgnores checks the claim against the rules.
func TestEveryScannerAllowlistIsAPathGitIgnores(t *testing.T) {
	t.Parallel()
	root := repositoryRoot(t)
	for entry, directory := range allowlistedDirectories(t, root) {
		command := exec.CommandContext(t.Context(), testkit.GitBinary(t), "-C", root, "check-ignore", "-q", directory+"/")
		if err := command.Run(); err != nil {
			t.Errorf("%s allows %q, and git does not ignore %s/", gitleaksConfiguration, entry, directory)
		}
	}
}

// TestNoScannerAllowlistCoversAFileThisRepositoryTracks checks the claim against
// the files.
//
// This is the direction that earns its keep. A path can be named in .gitignore
// and tracked at the same time - an ignore rule does not reach a file that was
// committed before it - so asking git whether a path is ignored answers a
// question about the rules and not about the files. An entry in that position
// passes the check above while taking real, tracked, reviewable content out of
// the scan, which is the one outcome this whole file exists to prevent.
func TestNoScannerAllowlistCoversAFileThisRepositoryTracks(t *testing.T) {
	t.Parallel()
	root := repositoryRoot(t)
	tracked := trackedFiles(t, testkit.GitBinary(t), root)
	for entry := range allowlistedDirectories(t, root) {
		pattern, err := regexp.Compile(entry)
		if err != nil {
			t.Errorf("%s allows %q, which is not a regular expression: %v", gitleaksConfiguration, entry, err)
			continue
		}
		for _, relative := range tracked {
			if pattern.MatchString(relative) {
				t.Errorf("%s allows %q, which covers the tracked file %s", gitleaksConfiguration, entry, relative)
			}
		}
	}
}

// TestTheScannerAllowlistGateSeesAnEntryThatCoversTrackedFiles proves the check
// above can fail.
func TestTheScannerAllowlistGateSeesAnEntryThatCoversTrackedFiles(t *testing.T) {
	t.Parallel()
	root := repositoryRoot(t)
	forged := `(^|/)internal/`
	match := allowlistedPath.FindStringSubmatch(forged)
	if match == nil {
		t.Fatalf("%q is not of the shape the gate accepts, so this test proves nothing about it", forged)
	}
	pattern := regexp.MustCompile(forged)
	var covered int
	for _, relative := range trackedFiles(t, testkit.GitBinary(t), root) {
		if pattern.MatchString(relative) {
			covered++
		}
	}
	if covered == 0 {
		t.Fatal("an entry covering internal/ matched no tracked file, so the check it is meant to fail cannot fail")
	}
}

// TestTheLintTaskUsesTheScannerAllowlist keeps the configuration from becoming
// decorative.
//
// gitleaks finds .gitleaks.toml beside the directory it scans without being
// told, which means a file that was renamed, or a task that scans from
// somewhere else, would leave every assertion above passing about a file
// nothing reads.
func TestTheLintTaskUsesTheScannerAllowlist(t *testing.T) {
	t.Parallel()
	root := repositoryRoot(t)
	data, err := os.ReadFile(filepath.Join(root, miseManifest))
	if err != nil {
		t.Fatalf("read %s: %v", miseManifest, err)
	}
	// `run` is a string for a one-step task and an array for the rest, so it
	// is decoded as neither and normalised below. A struct field of one of the
	// two types decodes half the file and fails on the other half.
	var manifest struct {
		Tasks map[string]struct {
			Run any `toml:"run"`
		} `toml:"tasks"`
	}
	if err := toml.Unmarshal(data, &manifest); err != nil {
		t.Fatalf("decode %s: %v", miseManifest, err)
	}
	lint, ok := manifest.Tasks["lint"]
	if !ok {
		t.Fatalf("%s declares no lint task", miseManifest)
	}
	steps := taskSteps(t, lint.Run)
	var found bool
	for _, step := range steps {
		if strings.Contains(step, "gitleaks") && strings.Contains(step, gitleaksConfiguration) {
			found = true
		}
	}
	if !found {
		t.Errorf("the lint task runs %v, none of which hands gitleaks %s", steps, gitleaksConfiguration)
	}
}

// taskSteps reads a mise task's `run`, which is a string or a list of them.
func taskSteps(t *testing.T, value any) []string {
	t.Helper()
	switch typed := value.(type) {
	case string:
		return []string{typed}
	case []any:
		steps := make([]string, 0, len(typed))
		for _, item := range typed {
			step, ok := item.(string)
			if !ok {
				t.Fatalf("a task step is %T rather than a command", item)
			}
			steps = append(steps, step)
		}
		return steps
	default:
		t.Fatalf("a task run is %T rather than a command or a list of them", value)
		return nil
	}
}
