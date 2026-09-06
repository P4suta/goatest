// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package app

import (
	"strings"
	"testing"
	"time"

	"github.com/P4suta/goatest/internal/report"
	"github.com/P4suta/goatest/internal/trace"
)

func TestABundleIsNamedForItsRun(t *testing.T) {
	t.Parallel()
	service := Service{
		Now:       func() time.Time { return time.Date(2026, 9, 1, 10, 11, 12, 0, time.UTC) },
		ProcessID: func() int { return appFixtureProcessID },
	}
	const runID = "20260901T101112.000000000Z-a1b2c3d4e5f6"
	if name := service.diagnosticsName(report.Report{RunID: runID}); name != runID {
		t.Fatalf("bundle name = %q, want the run it diagnoses", name)
	}

	for _, id := range []string{"", ".", "..", "../escape", "run/id", "run\\id", "run id"} {
		if name := service.diagnosticsName(report.Report{RunID: id}); name != "20260901T101112Z-4242" {
			t.Fatalf("bundle name of run %q = %q", id, name)
		}
	}
}

const nothingPreserved = "# this run left nothing behind"

func artifactEvent(kind, path string) trace.Event {
	return trace.Event{Type: trace.TypeArtifact, Artifact: &trace.ArtifactRecord{Kind: kind, Path: path}}
}

func TestThePreservedPathsOfABundleNameEveryPathARunLeftBehind(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name      string
		directory string
		events    []trace.Event
		want      []string
		unwanted  []string
	}{
		{

			name: "nothing",
			want: []string{"kind\tpath\n", nothingPreserved + "\n"},
		},
		{

			name:      "recording-and-artifact",
			directory: "/traces/20260901T101112Z-4242",
			events:    []trace.Event{artifactEvent("baseline-scratch", "/tmp/goatest-baseline")},
			want: []string{
				"trace\t/traces/20260901T101112Z-4242\n",
				"baseline-scratch\t/tmp/goatest-baseline\n",
			},
			unwanted: []string{nothingPreserved},
		},
		{

			name: "artifacts-alone",
			events: []trace.Event{
				artifactEvent("baseline-scratch", "/tmp/goatest-baseline"),
				artifactEvent("candidate", "/tmp/goatest-candidate"),
			},
			want: []string{
				"baseline-scratch\t/tmp/goatest-baseline\n",
				"candidate\t/tmp/goatest-candidate\n",
			},
			unwanted: []string{nothingPreserved, "trace\t"},
		},
		{

			name: "nothing-a-reader-could-open",
			events: []trace.Event{
				{
					Type:     trace.TypeProgress,
					Progress: &trace.ProgressRecord{Kind: "snapshot", Detail: "captured"},
					Artifact: &trace.ArtifactRecord{Kind: "progress", Path: "/tmp/goatest-not-an-artifact"},
				},
				{Type: trace.TypeArtifact},
			},
			want:     []string{nothingPreserved + "\n"},
			unwanted: []string{"/tmp/goatest-not-an-artifact"},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			paths := string(diagnosticsPreservedPaths(testCase.directory, testCase.events))
			for _, want := range testCase.want {
				if !strings.Contains(paths, want) {
					t.Fatalf("preserved-paths.txt = %q, want %q in it", paths, want)
				}
			}
			for _, unwanted := range testCase.unwanted {
				if strings.Contains(paths, unwanted) {
					t.Fatalf("preserved-paths.txt = %q, want nothing saying %q in it", paths, unwanted)
				}
			}
		})
	}
}

func TestTheEnvironmentOfABundleNamesTheGoBinaryThatDecidedTheRun(t *testing.T) {
	t.Parallel()

	if text := string(Service{}.diagnosticsEnvironment(report.Report{})); !strings.Contains(text, "go-binary: go\n") {
		t.Fatalf("environment.txt = %q, want the go binary a run given none falls back to", text)
	}
	const chosen = "/opt/toolchains/go1.26/bin/go"
	text := string(Service{GoBinary: chosen}.diagnosticsEnvironment(report.Report{}))
	if !strings.Contains(text, "go-binary: "+chosen+"\n") {
		t.Fatalf("environment.txt = %q, want the go binary %s the run was given", text, chosen)
	}
}

func TestTheEnvironmentOfABundleIsTheOneTheRunsCommandsCouldSee(t *testing.T) {
	t.Setenv("GOATEST_DIAGNOSTICS_PROBE", "probe-value-no-bundle-may-hold")

	text := string(Service{}.diagnosticsEnvironment(report.Report{}))
	if !strings.Contains(text, "\nGOATEST_DIAGNOSTICS_PROBE\n") {
		t.Fatalf("environment.txt = %q, want the variables goatest was started with", text)
	}
	if strings.Contains(text, "probe-value") {
		t.Fatalf("environment.txt holds the value of a variable: %q", text)
	}

	text = string(Service{Environment: []string{"PATH=/usr/bin"}}.diagnosticsEnvironment(report.Report{}))
	if !strings.Contains(text, "\nPATH\n") || strings.Contains(text, "GOATEST_DIAGNOSTICS_PROBE") {
		t.Fatalf("environment.txt = %q, want the environment the caller named", text)
	}
}
