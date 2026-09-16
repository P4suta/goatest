// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/P4suta/goatest/internal/cli"
	"github.com/P4suta/goatest/internal/report"
)

func TestServicePropagatesRepositoryRootResolutionFailure(t *testing.T) {
	sentinel := errors.New("absolute root failed")
	service := Service{
		Root: "relative",
		absolute: func(string) (string, error) {
			return "", sentinel
		},
	}
	_, err := service.Execute(t.Context(), cli.CommandReport, cli.Request{}, "")
	if !errors.Is(err, sentinel) {
		t.Fatalf("root resolution error = %v, want %v", err, sentinel)
	}
}

func TestFinalizeReportMarksUnreadableConfigurationMetadata(t *testing.T) {
	previous := readConfigurationFile
	t.Cleanup(func() { readConfigurationFile = previous })
	readConfigurationFile = func(string) ([]byte, error) {
		return nil, errors.New("configuration read failed")
	}
	now := time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC)
	result := finalizeReportKind(t.Context(), t.TempDir(), cli.Request{}, report.Report{
		Verdict: report.VerdictCompleted,
	}, report.RunOperation, now, now, absentGit)
	if len(result.Configuration.Digest) != len(appTestDigest("a")) {
		t.Fatalf("configuration digest = %q", result.Configuration.Digest)
	}
	found := false
	for _, limitation := range result.Limitations {
		found = found || limitation.Code == "configuration-metadata-unavailable"
	}
	if !found {
		t.Fatalf("configuration limitation missing: %+v", result.Limitations)
	}
}

// absentGit is a repository that is not one, for a test that is about
// something else.
//
// Every test here used to start a real git for this, which always failed
// because a temporary directory is not a repository, and always settled into
// the same limitation. The child changed no answer and cost the test a
// dependency on a tool it needed nothing from.
func absentGit(context.Context, string, ...string) ([]byte, error) {
	return nil, errors.New("not a git repository")
}

// TestGitMetadataComesFromTheServiceGitHook proves the seam is load-bearing:
// the report's repository identity is whatever this hook says, and no child
// process is started to obtain it.
//
// Before the hook existed there was no way to write this test. Every operation
// reached a package-level function that ran the real git, so a test could
// observe the unavailable branch and nothing else - the branch where git
// answers was reachable only by building a repository first, which is why the
// only tests that covered it were the end-to-end ones that take minutes.
func TestGitMetadataComesFromTheServiceGitHook(t *testing.T) {
	t.Parallel()
	const commit = "0123456789abcdef0123456789abcdef01234567"
	var asked [][]string
	scripted := func(_ context.Context, _ string, arguments ...string) ([]byte, error) {
		asked = append(asked, arguments)
		switch arguments[0] {
		case "rev-parse":
			return []byte(commit + "\n"), nil
		case "merge-base":
			return []byte(commit + "\n"), nil
		case "status", "diff", "ls-files":
			return nil, nil
		}
		return nil, errors.New("unexpected git command")
	}
	now := time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC)
	result := finalizeReportKind(t.Context(), t.TempDir(), cli.Request{}, report.Report{
		Verdict: report.VerdictCompleted,
	}, report.RunOperation, now, now, scripted)

	if !result.Repository.Git.Available || result.Repository.Git.Commit != commit {
		t.Fatalf("git metadata = %+v, want the commit the hook reported", result.Repository.Git)
	}
	if result.Repository.Git.Dirty {
		t.Error("an empty status was read as a dirty tree")
	}
	for _, limitation := range result.Limitations {
		if limitation.Code == "git-metadata-unavailable" {
			t.Error("the run reported git as unavailable while the hook was answering")
		}
	}
	if len(asked) == 0 {
		t.Fatal("the hook was never called")
	}
}

// TestGitMetadataIsUnavailableWhenTheHookRefuses covers the other side, which
// is the branch every test in this package used to take by accident.
func TestGitMetadataIsUnavailableWhenTheHookRefuses(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC)
	result := finalizeReportKind(t.Context(), t.TempDir(), cli.Request{}, report.Report{
		Verdict: report.VerdictCompleted,
	}, report.RunOperation, now, now, absentGit)

	if result.Repository.Git.Available || result.Repository.Git.Commit != "unavailable" {
		t.Fatalf("git metadata = %+v, want the unavailable placeholder", result.Repository.Git)
	}
	found := false
	for _, limitation := range result.Limitations {
		found = found || limitation.Code == "git-metadata-unavailable"
	}
	if !found {
		t.Error("no git-metadata-unavailable limitation was recorded")
	}
}
