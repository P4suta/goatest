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
		ProcessID: func() int { return 4242 },
	}
	const runID = "20260901T101112.000000000Z-a1b2c3d4e5f6"
	if name := service.diagnosticsName(report.Report{RunID: runID}); name != runID {
		t.Fatalf("bundle name = %q, want the run it diagnoses", name)
	}
	// A run may die before it has an identity, and a failure that early is the
	// one a developer most needs the bundle for. The moment and the process
	// name it instead, which is the name a recording of the same run takes.
	for _, id := range []string{"", ".", "..", "../escape", "run/id", "run\\id", "run id"} {
		if name := service.diagnosticsName(report.Report{RunID: id}); name != "20260901T101112Z-4242" {
			t.Fatalf("bundle name of run %q = %q", id, name)
		}
	}
}

// nothingPreserved is the note a bundle leaves where a run left no path behind.
const nothingPreserved = "# this run left nothing behind"

// artifactEvent is one artifact a run recorded.
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
			// A run that recorded into memory and was not asked to keep its
			// temporary directories left nothing on the disk. It says so,
			// because a list with no entries reads as a list nobody filled in
			// rather than as a run that left nothing.
			name: "nothing",
			want: []string{"kind\tpath\n", nothingPreserved + "\n"},
		},
		{
			// The recording is a path a developer opens like any other, and a
			// run that also kept an artifact left both. Counting one of them
			// and not the other is what would let a bundle list two paths and
			// call the run empty in the same file.
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
			// A run that recorded into memory reports the temporaries it was
			// asked to keep, and nothing about them is a recording.
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
			// Only an artifact event carrying a record names a path. Every
			// other event is an account of something the run did, and an
			// artifact event without its record is a truncated one: neither is
			// somewhere a developer could go and look.
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
	// A run handed no go binary ran the one its PATH names. The bundle says so
	// rather than leaving the field out, because a machine with several
	// toolchains installed is exactly the machine a bundle is read on.
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
	// The process environment is what this test reads back, so it cannot be
	// parallel.
	t.Setenv("GOATEST_DIAGNOSTICS_PROBE", "probe-value-no-bundle-may-hold")
	// A run nobody handed an environment ran in the one goatest itself was
	// started in, and that is the environment its commands could see.
	text := string(Service{}.diagnosticsEnvironment(report.Report{}))
	if !strings.Contains(text, "\nGOATEST_DIAGNOSTICS_PROBE\n") {
		t.Fatalf("environment.txt = %q, want the variables goatest was started with", text)
	}
	if strings.Contains(text, "probe-value") {
		t.Fatalf("environment.txt holds the value of a variable: %q", text)
	}
	// A caller that named an environment is answered with that one alone: what
	// its commands could see is what it passed them, and nothing else.
	text = string(Service{Environment: []string{"PATH=/usr/bin"}}.diagnosticsEnvironment(report.Report{}))
	if !strings.Contains(text, "\nPATH\n") || strings.Contains(text, "GOATEST_DIAGNOSTICS_PROBE") {
		t.Fatalf("environment.txt = %q, want the environment the caller named", text)
	}
}
