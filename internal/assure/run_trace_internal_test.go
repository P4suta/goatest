// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package assure

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"testing"
	"time"

	gomutants "github.com/P4suta/go-mutants"
	goanalysis "github.com/P4suta/goatest/internal/golang"
	"github.com/P4suta/goatest/internal/mutationbridge"
	"github.com/P4suta/goatest/internal/report"
	"github.com/P4suta/goatest/internal/trace"
)

// everyRunPhase is the phase sequence of a round that runs to its end.
var everyRunPhase = []string{
	"snapshot", "cache-check", "discover", "impact", "resources", "baseline",
	"graph", "race", "mutation-prepare", "probe", "mutation", "repair", "finalize",
}

// recordedPhases returns the phases of a recording in order, failing the test
// unless every phase-start is answered by the phase-end of the same phase. The
// phases of a run are a sequence and never a nesting: one ends where the next
// begins, and the last one ends when the run does.
func recordedPhases(t *testing.T, sink *trace.MemorySink) []string {
	t.Helper()
	var names []string
	open := ""
	for _, event := range sink.Events() {
		switch event.Type {
		case trace.TypePhaseStart:
			if open != "" {
				t.Fatalf("phase %q started while %q was still open", event.Phase.Name, open)
			}
			open = event.Phase.Name
			names = append(names, open)
		case trace.TypePhaseEnd:
			if event.Phase.Name != open {
				t.Fatalf("phase %q ended while %q was open", event.Phase.Name, open)
			}
			open = ""
		}
	}
	if open != "" {
		t.Fatalf("phase %q was never ended", open)
	}
	return names
}

// recordedProgress returns the progress notes of a recording as the run's own
// events, so that a trace can be compared with what the caller was told.
func recordedProgress(sink *trace.MemorySink) []Event {
	var events []Event
	for _, event := range sink.Events() {
		if event.Type == trace.TypeProgress && event.Progress != nil {
			events = append(events, Event{Kind: event.Progress.Kind, Detail: event.Progress.Detail})
		}
	}
	return events
}

func TestRunCoordinatorEndsEveryPhaseItBegins(t *testing.T) {
	t.Parallel()
	cause := errors.New("target discovery failed")
	for _, test := range []struct {
		name    string
		change  func(*runCoordinatorHarness)
		want    []string
		failure bool
	}{
		{name: "assured round", want: everyRunPhase},
		{
			name: "cache hit",
			change: func(harness *runCoordinatorHarness) {
				harness.cache.found = true
				harness.cache.getReport = report.Report{Schema: report.SchemaV1, Verdict: report.VerdictAssured}
			},
			want: []string{"snapshot", "cache-check"},
		},
		{
			name: "discovery failure",
			change: func(harness *runCoordinatorHarness) {
				harness.dependencies.discoverTargets = func(string, []goanalysis.Package) ([]goanalysis.Target, error) {
					return nil, cause
				}
			},
			want:    []string{"snapshot", "cache-check", "discover"},
			failure: true,
		},
		{
			name: "baseline findings",
			change: func(harness *runCoordinatorHarness) {
				harness.baseline.Findings = []report.Finding{{ID: "finding-a", Kind: "baseline-failure"}}
			},
			want: []string{"snapshot", "cache-check", "discover", "impact", "resources", "baseline"},
		},
		{
			name: "race findings",
			change: func(harness *runCoordinatorHarness) {
				harness.race.Findings = []report.Finding{{ID: "finding-b", Kind: "data-race"}}
			},
			want: []string{"snapshot", "cache-check", "discover", "impact", "resources", "baseline", "graph", "race"},
		},
		{
			name:   "repaired rounds",
			change: func(harness *runCoordinatorHarness) { harness.generation.Applied = true },
			want:   slices.Concat(everyRunPhase, everyRunPhase, everyRunPhase),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			harness := newRunCoordinatorHarness(t)
			sink := harness.record()
			if test.change != nil {
				test.change(harness)
			}
			_, err := harness.run(Options{})
			if test.failure != (err != nil) {
				t.Fatalf("run error = %v, want failure %t", err, test.failure)
			}
			if got := recordedPhases(t, sink); !slices.Equal(got, test.want) {
				t.Fatalf("phases = %v, want %v", got, test.want)
			}
		})
	}
}

func TestRunCoordinatorForwardsEveryProgressNoteIntoTheTrace(t *testing.T) {
	t.Parallel()
	harness := newRunCoordinatorHarness(t)
	sink := harness.record()
	if _, err := harness.run(Options{Changed: true}); err != nil {
		t.Fatal(err)
	}
	recorded := recordedProgress(sink)
	if len(recorded) == 0 || !reflect.DeepEqual(recorded, harness.events) {
		t.Fatalf("recorded progress = %+v, want %+v", recorded, harness.events)
	}
}

func TestRunCoordinatorHandsTheRecorderToEveryTracedComponent(t *testing.T) {
	t.Parallel()
	harness := newRunCoordinatorHarness(t)
	harness.record()
	if _, err := harness.run(Options{}); err != nil {
		t.Fatal(err)
	}
	if harness.workspaceOptions.Trace != harness.recorder {
		t.Fatalf("workspace recorder = %v, want the run's recorder", harness.workspaceOptions.Trace)
	}
	if harness.mutationOptions.Trace != harness.recorder {
		t.Fatalf("mutation recorder = %v, want the run's recorder", harness.mutationOptions.Trace)
	}
	if harness.generationOptions.RepositoryValidator.Trace != harness.recorder {
		t.Fatalf("validation recorder = %v, want the run's recorder", harness.generationOptions.RepositoryValidator.Trace)
	}
}

func TestRunCoordinatorReusesThePreparedProbeForOriginalControls(t *testing.T) {
	t.Parallel()
	harness := newRunCoordinatorHarness(t)
	sink := harness.record()
	session := &mutationUnitSession{catalog: harness.catalog, probe: func(gomutants.ProbeRequest) (gomutants.ProbeResult, error) {
		return gomutants.ProbeResult{Outcome: gomutants.ProbeMeasured, Duration: 25 * time.Millisecond}, nil
	}}
	harness.dependencies.prepareSession = func(_ context.Context, _ *mutationbridge.Workspace, options mutationbridge.PrepareOptions) (MutationSession, error) {
		harness.prepareCalls++
		harness.preparedOptions = options
		return session, nil
	}
	harness.dependencies.evaluateMutations = func(ctx context.Context, got MutationSession, targets []TargetEvidence, options MutationOptions) (MutationEvaluation, error) {
		harness.mutationCalls++
		harness.mutationOptions = options
		harness.mutationTargets = slices.Clone(targets)
		if got != session {
			t.Fatalf("mutation session = %p, want prepared session %p", got, session)
		}
		result, err := options.OriginalControl(ctx, gomutants.ExecRequest{
			Package: "fixture.example/module", Args: []string{"-test.run=^TestValue$"},
			Env: []string{"DB=ready"}, Timeout: 2 * time.Second,
		})
		if err != nil || result.ExitCode != 0 || result.Duration != 25*time.Millisecond {
			t.Fatalf("prepared original control = (%+v, %v)", result, err)
		}
		return harness.mutation, nil
	}
	if _, err := harness.run(Options{}); err != nil {
		t.Fatal(err)
	}
	want := gomutants.ProbeRequest{
		Package: "fixture.example/module", Args: []string{"-test.run=^TestValue$"},
		Env: []string{"DB=ready"}, Timeout: 2 * time.Second,
	}
	if got := session.probeRequests(); len(got) != 1 || !reflect.DeepEqual(got[0], want) {
		t.Fatalf("prepared control requests = %+v, want %+v", got, want)
	}
	if harness.openCalls != 1 {
		t.Fatalf("workspace opens = %d, want only the primary prepared workspace", harness.openCalls)
	}
	var controls []trace.ProbeRecord
	for _, event := range sink.Events() {
		if event.Type == trace.TypeProbeExec && event.Probe != nil && event.Probe.Control {
			controls = append(controls, *event.Probe)
		}
	}
	if len(controls) != 1 || controls[0].Target != "paired-control:fixture.example/module" {
		t.Fatalf("recorded controls = %+v", controls)
	}
}

func TestEmitReachesTheTraceWithoutAProgressCallback(t *testing.T) {
	t.Parallel()
	sink, recorder := newTraceRecording()
	var received []Event
	emit(Options{Trace: recorder}, "cache-hit", "digest-a")
	emit(Options{Trace: recorder, Progress: func(event Event) { received = append(received, event) }}, "race", "1 packages")
	emit(Options{}, "unrecorded", "no recorder and no callback")
	want := []Event{{Kind: "cache-hit", Detail: "digest-a"}, {Kind: "race", Detail: "1 packages"}}
	if recorded := recordedProgress(sink); !reflect.DeepEqual(recorded, want) {
		t.Fatalf("recorded progress = %+v, want %+v", recorded, want)
	}
	if !reflect.DeepEqual(received, []Event{{Kind: "race", Detail: "1 packages"}}) {
		t.Fatalf("callback progress = %+v", received)
	}
}
