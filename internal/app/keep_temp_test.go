// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package app_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/P4suta/goatest/internal/app"
	"github.com/P4suta/goatest/internal/assure"
	"github.com/P4suta/goatest/internal/cli"
	"github.com/P4suta/goatest/internal/report"
	"github.com/P4suta/goatest/internal/testkit"
	"github.com/P4suta/goatest/internal/trace"
)

func TestKeepTempReachesTheRunAndWhatItKeptReachesTheBundle(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	scratch := filepath.Join(t.TempDir(), "goatest-run-kept", "baseline-kept")
	sentinel := errors.New("mutation workspace failed")
	var kept bool
	service := app.Service{
		Root: root,
		Run: func(_ context.Context, options assure.Options) (report.Report, error) {
			kept = options.KeepTemp
			options.Trace.Artifact("baseline-scratch", scratch)
			return report.Report{}, sentinel
		},
	}
	result, err := service.Execute(t.Context(), cli.CommandVerify, cli.Request{KeepTemp: true}, "")
	if !errors.Is(err, sentinel) || result.Verdict != report.VerdictError {
		t.Fatalf("verify = %+v, %v", result, err)
	}
	if !kept {
		t.Fatal("the run was not asked to keep its temporary directories")
	}
	preserved := bundleFile(t, diagnosticsBundle(t, root), "preserved-paths.txt")
	if !strings.Contains(preserved, scratch) {
		t.Fatalf("preserved-paths.txt = %q, want the scratch directory the run kept", preserved)
	}
}

func TestKeepTempLeavesTheBaselineScratchOfARealRunWhereItSaysItDid(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name string
		args []string
		kept bool
	}{
		{name: "removed by default"},
		{name: "kept on request", args: []string{"--keep-temp"}, kept: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			repository := testkit.NewRepo(t).BoundaryFixture().Git()
			directory := filepath.Join(t.TempDir(), "trace")
			service := app.Service{
				Root: repository.Root(), GoBinary: testkit.GoBinary(t),

				TempDirectory: t.TempDir(), Environment: os.Environ(),
			}
			var stdout, stderr bytes.Buffer
			arguments := append([]string{"verify", "--trace=" + directory}, test.args...)
			if exit := cli.Run(t.Context(), arguments, &stdout, &stderr, service); exit != cli.ExitAssured {
				t.Fatalf("verify exit = %d\nstdout: %s\nstderr: %s", exit, stdout.String(), stderr.String())
			}
			var scratch []string
			for _, event := range traceOfType(readTrace(t, traceRun(t, directory)), trace.TypeArtifact) {
				if event.Artifact.Kind == "baseline-scratch" {
					scratch = append(scratch, event.Artifact.Path)
				}
			}
			if !test.kept {
				if len(scratch) != 0 {
					t.Fatalf("a run that kept nothing recorded %v", scratch)
				}
				return
			}
			if len(scratch) != 1 {
				t.Fatalf("recorded scratch directories = %v, want the one the round made", scratch)
			}
			if info, err := os.Stat(scratch[0]); err != nil || !info.IsDir() {
				t.Fatalf("kept scratch %s = %v", scratch[0], err)
			}
		})
	}
}

func TestARunKeepsNothingUnlessItWasAsked(t *testing.T) {
	t.Parallel()
	var kept bool
	service := app.Service{
		Root: t.TempDir(),
		Run: func(_ context.Context, options assure.Options) (report.Report, error) {
			kept = options.KeepTemp
			return report.Report{Schema: report.SchemaV1, Verdict: report.VerdictAssured, Contract: "standard-v1"}, nil
		},
	}
	if _, err := service.Execute(t.Context(), cli.CommandVerify, cli.Request{}, ""); err != nil {
		t.Fatal(err)
	}
	if kept {
		t.Fatal("an unasked run was told to keep its temporary directories")
	}
}
