// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package trace_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/P4suta/goatest/internal/trace"
)

// traceOrigin is the wall clock a scripted recording starts from. A fake clock
// makes every field of a recorded event deterministic except the ones the
// recorder derives from the clock itself, which the fake also fixes.
var traceOrigin = time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)

// fakeClock is the injected now function. It is safe for concurrent use so
// that a test may record from several goroutines without racing the clock.
type fakeClock struct {
	mutex sync.Mutex
	now   time.Time
}

func newClock() *fakeClock { return &fakeClock{now: traceOrigin} }

// Now is the func(time.Time) seam passed to trace.New.
func (clock *fakeClock) Now() time.Time {
	clock.mutex.Lock()
	defer clock.mutex.Unlock()
	return clock.now
}

// Advance moves the fake clock forward, standing in for elapsed work.
func (clock *fakeClock) Advance(step time.Duration) {
	clock.mutex.Lock()
	defer clock.mutex.Unlock()
	clock.now = clock.now.Add(step)
}

// recordScript performs one recording that reaches every event type exactly
// once, advancing the clock a whole second before each event. Tests that pin
// the wire format, assert determinism, or validate the schema all record the
// same script, so the recorded events and their pinned bytes cannot drift
// apart.
func recordScript(clock *fakeClock, sink trace.Sink) {
	recorder := trace.New(sink, clock.Now)
	clock.Advance(time.Second)
	endPhase := recorder.PhaseStart("mutation")
	clock.Advance(time.Second)
	recorder.Exec(trace.ExecRecord{
		Argv:       []string{"go", "test", "./..."},
		Dir:        "internal/assure",
		EnvNames:   []string{"GOFLAGS=-mod=mod", "GOCACHE"},
		TimeoutMS:  60000,
		ExitCode:   1,
		DurationMS: 1200,
	})
	clock.Advance(time.Second)
	recorder.MutantExec(trace.MutantRecord{
		ID:         "m-0001",
		DisplayID:  "cond-negate internal/assure/run.go:42",
		Package:    "example.com/app/internal/assure",
		Args:       []string{"-run", "TestRun"},
		TimeoutMS:  30000,
		Outcome:    "killed",
		KilledBy:   "TestRun",
		DurationMS: 900,
	})
	clock.Advance(time.Second)
	recorder.Route(trace.RouteRecord{
		MutantID:        "m-0001",
		Rule:            "cond-negate",
		Path:            "internal/assure/run.go",
		Line:            42,
		ReachingTargets: []string{"TestRun"},
		Plan:            []string{"TestRun"},
		Reason:          trace.ReasonCoverageReaching,
	})
	clock.Advance(time.Second)
	recorder.Progress("mutation-progress", "3/10")
	clock.Advance(time.Second)
	recorder.Artifact("report", "reports/runs/run-1/report.json")
	clock.Advance(time.Second)
	endPhase()
	clock.Advance(time.Second)
	recorder.RunEnd("assured", nil)
}

// scriptedEvents records the script into an unbounded memory sink.
func scriptedEvents(t *testing.T) []trace.Event {
	t.Helper()
	sink := trace.NewMemorySink(0)
	recordScript(newClock(), sink)
	if err := sink.Close(); err != nil {
		t.Fatalf("Close = %v", err)
	}
	return sink.Events()
}

// scriptedJSONL is the pinned wire format of the script, one event per line in
// sequence order. It is the contract every consumer of a trace directory reads,
// so a change to a field name, a field order, or an omitted zero value must
// change this literal deliberately.
var scriptedJSONL = []string{
	`{"seq":1,"type":"run-start","schema":"goatest-trace-v1","timestamp":"2026-01-02T03:04:05Z","elapsed_ms":0}`,
	`{"seq":2,"type":"phase-start","timestamp":"2026-01-02T03:04:06Z","elapsed_ms":1000,"phase":{"name":"mutation"}}`,
	`{"seq":3,"type":"exec","timestamp":"2026-01-02T03:04:07Z","elapsed_ms":2000,"exec":{"argv":["go","test","./..."],"dir":"internal/assure","env_names":["GOCACHE","GOFLAGS"],"timeout_ms":60000,"exit_code":1,"duration_ms":1200}}`,
	`{"seq":4,"type":"mutant-exec","timestamp":"2026-01-02T03:04:08Z","elapsed_ms":3000,"mutant":{"id":"m-0001","display_id":"cond-negate internal/assure/run.go:42","package":"example.com/app/internal/assure","args":["-run","TestRun"],"timeout_ms":30000,"outcome":"killed","killed_by":"TestRun","duration_ms":900}}`,
	`{"seq":5,"type":"route","timestamp":"2026-01-02T03:04:09Z","elapsed_ms":4000,"route":{"mutant_id":"m-0001","rule":"cond-negate","path":"internal/assure/run.go","line":42,"reaching_targets":["TestRun"],"plan":["TestRun"],"reason":"coverage-reaching"}}`,
	`{"seq":6,"type":"progress","timestamp":"2026-01-02T03:04:10Z","elapsed_ms":5000,"progress":{"kind":"mutation-progress","detail":"3/10"}}`,
	`{"seq":7,"type":"artifact","timestamp":"2026-01-02T03:04:11Z","elapsed_ms":6000,"artifact":{"kind":"report","path":"reports/runs/run-1/report.json"}}`,
	`{"seq":8,"type":"phase-end","timestamp":"2026-01-02T03:04:12Z","elapsed_ms":7000,"phase":{"name":"mutation","duration_ms":6000}}`,
	`{"seq":9,"type":"run-end","timestamp":"2026-01-02T03:04:13Z","elapsed_ms":8000,"run":{"verdict":"assured","events_emitted":8,"events_dropped":0}}`,
}

func TestRecordedEventsPinTheirJSONFieldNamesAndOrder(t *testing.T) {
	t.Parallel()
	events := scriptedEvents(t)
	if len(events) != len(scriptedJSONL) {
		t.Fatalf("recorded %d events, want %d", len(events), len(scriptedJSONL))
	}
	for index, event := range events {
		encoded, err := json.Marshal(event)
		if err != nil {
			t.Fatalf("Marshal event %d: %v", index+1, err)
		}
		if string(encoded) != scriptedJSONL[index] {
			t.Errorf("event %d\n got %s\nwant %s", index+1, encoded, scriptedJSONL[index])
		}
	}
}

func TestEveryEventTypeIsRecordedOnceInSequenceOrder(t *testing.T) {
	t.Parallel()
	want := []string{
		trace.TypeRunStart, trace.TypePhaseStart, trace.TypeExec, trace.TypeMutantExec,
		trace.TypeRoute, trace.TypeProgress, trace.TypeArtifact, trace.TypePhaseEnd, trace.TypeRunEnd,
	}
	events := scriptedEvents(t)
	var got []string
	for index, event := range events {
		got = append(got, event.Type)
		if event.Seq != int64(index+1) {
			t.Errorf("event %d has seq %d, want %d", index+1, event.Seq, index+1)
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("event types = %v, want %v", got, want)
	}
	if events[0].Schema != trace.SchemaV1 {
		t.Errorf("run-start schema = %q, want %q", events[0].Schema, trace.SchemaV1)
	}
	for _, event := range events[1:] {
		if event.Schema != "" {
			t.Errorf("%s carries schema %q; only run-start identifies the format", event.Type, event.Schema)
		}
	}
}

func TestPhaseEndReportsTheElapsedPhaseDuration(t *testing.T) {
	t.Parallel()
	clock := newClock()
	sink := trace.NewMemorySink(0)
	recorder := trace.New(sink, clock.Now)
	endPhase := recorder.PhaseStart("discover")
	clock.Advance(1500 * time.Millisecond)
	endPhase()

	events := sink.Events()
	if len(events) != 3 {
		t.Fatalf("recorded %d events, want run-start, phase-start and phase-end", len(events))
	}
	start, end := events[1], events[2]
	if start.Type != trace.TypePhaseStart || start.Phase == nil || start.Phase.Name != "discover" {
		t.Fatalf("phase-start = %+v", start)
	}
	if start.Phase.DurationMS != 0 {
		t.Errorf("phase-start duration = %d, want 0; a phase is only timed when it ends", start.Phase.DurationMS)
	}
	if end.Type != trace.TypePhaseEnd || end.Phase == nil || end.Phase.Name != "discover" {
		t.Fatalf("phase-end = %+v", end)
	}
	if end.Phase.DurationMS != 1500 {
		t.Errorf("phase-end duration = %d, want 1500", end.Phase.DurationMS)
	}
	if end.ElapsedMS != 1500 {
		t.Errorf("phase-end elapsed = %d, want 1500", end.ElapsedMS)
	}
}

func TestPhaseEndIsEmittedOnceHoweverOftenTheCloserRuns(t *testing.T) {
	t.Parallel()
	clock := newClock()
	sink := trace.NewMemorySink(0)
	recorder := trace.New(sink, clock.Now)
	endPhase := recorder.PhaseStart("baseline")
	endPhase()
	endPhase()

	events := sink.Events()
	if len(events) != 3 {
		t.Fatalf("recorded %d events, want one phase-end for a phase that ended twice", len(events))
	}
}

func TestNestedPhasesEndInTheirOwnOrder(t *testing.T) {
	t.Parallel()
	clock := newClock()
	sink := trace.NewMemorySink(0)
	recorder := trace.New(sink, clock.Now)
	endOuter := recorder.PhaseStart("mutation")
	clock.Advance(time.Second)
	endInner := recorder.PhaseStart("mutation-prepare")
	clock.Advance(time.Second)
	endInner()
	clock.Advance(time.Second)
	endOuter()

	events := sink.Events()
	if len(events) != 5 {
		t.Fatalf("recorded %d events, want two phase pairs after run-start", len(events))
	}
	if name := events[3].Phase.Name; name != "mutation-prepare" {
		t.Errorf("first phase-end names %q, want the inner phase", name)
	}
	if events[3].Phase.DurationMS != 1000 {
		t.Errorf("inner phase duration = %d, want 1000", events[3].Phase.DurationMS)
	}
	if name := events[4].Phase.Name; name != "mutation" {
		t.Errorf("second phase-end names %q, want the outer phase", name)
	}
	if events[4].Phase.DurationMS != 3000 {
		t.Errorf("outer phase duration = %d, want 3000", events[4].Phase.DurationMS)
	}
}

func TestExecRecordsEnvironmentNamesSortedAndNeverTheirValues(t *testing.T) {
	t.Parallel()
	clock := newClock()
	sink := trace.NewMemorySink(0)
	recorder := trace.New(sink, clock.Now)
	recorder.Exec(trace.ExecRecord{
		Argv: []string{"go", "test"},
		EnvNames: []string{
			"PATH=/usr/local/bin:/usr/bin",
			"GOATEST_TOKEN=super-secret",
			"GOCACHE",
			"GOATEST_TOKEN=super-secret",
			"",
		},
	})

	events := sink.Events()
	if len(events) != 2 || events[1].Exec == nil {
		t.Fatalf("recorded %+v, want one exec event", events)
	}
	want := []string{"GOATEST_TOKEN", "GOCACHE", "PATH"}
	if !reflect.DeepEqual(events[1].Exec.EnvNames, want) {
		t.Fatalf("env names = %v, want %v", events[1].Exec.EnvNames, want)
	}
	encoded, err := json.Marshal(events[1])
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"super-secret", "/usr/local/bin", "="} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("exec event leaked an environment value: %s", encoded)
		}
	}
}

func TestExecDigestsCapturedOutputWithoutSerialisingIt(t *testing.T) {
	t.Parallel()
	clock := newClock()
	sink := trace.NewMemorySink(0)
	recorder := trace.New(sink, clock.Now)
	output := []byte("--- FAIL: TestBoundary\n")
	recorder.Exec(trace.ExecRecord{Argv: []string{"go", "test"}, Output: output})

	events := sink.Events()
	if len(events) != 2 || events[1].Exec == nil {
		t.Fatalf("recorded %+v, want one exec event", events)
	}
	sum := sha256.Sum256(output)
	if events[1].Exec.OutputBytes != len(output) {
		t.Errorf("output bytes = %d, want %d", events[1].Exec.OutputBytes, len(output))
	}
	if events[1].Exec.OutputSHA256 != hex.EncodeToString(sum[:]) {
		t.Errorf("output digest = %q, want %q", events[1].Exec.OutputSHA256, hex.EncodeToString(sum[:]))
	}
	encoded, err := json.Marshal(events[1])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "FAIL") {
		t.Fatalf("exec event serialised the captured output: %s", encoded)
	}
	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if _, ok := decoded["exec"].(map[string]any)["output"]; ok {
		t.Fatal("exec record has an output field; output is preserved beside the trace, never inside it")
	}
}

func TestRouteRecordsUnreachedMutantsWithoutTargets(t *testing.T) {
	t.Parallel()
	clock := newClock()
	sink := trace.NewMemorySink(0)
	recorder := trace.New(sink, clock.Now)
	recorder.Route(trace.RouteRecord{MutantID: "m-0002", Rule: "arith-swap", Path: "a.go", Line: 7, Reason: trace.ReasonUnreached})

	events := sink.Events()
	if len(events) != 2 || events[1].Route == nil {
		t.Fatalf("recorded %+v, want one route event", events)
	}
	if events[1].Route.Reason != "unreached" {
		t.Errorf("reason = %q, want %q", events[1].Route.Reason, "unreached")
	}
	encoded, err := json.Marshal(events[1])
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"reaching_targets", "plan"} {
		if strings.Contains(string(encoded), field) {
			t.Errorf("unreached route carries %s: %s", field, encoded)
		}
	}
}

func TestRunEndReportsTheVerdictTheErrorAndTheEventAccounting(t *testing.T) {
	t.Parallel()
	clock := newClock()
	sink := trace.NewMemorySink(0)
	recorder := trace.New(sink, clock.Now)
	recorder.Progress("snapshot", "repair round 1")
	recorder.RunEnd("error", errors.New("goatest: workspace closed"))

	events := sink.Events()
	if len(events) != 3 || events[2].Run == nil {
		t.Fatalf("recorded %+v, want a run-end event", events)
	}
	run := events[2].Run
	if run.Verdict != "error" || run.Error != "goatest: workspace closed" {
		t.Fatalf("run-end = %+v", run)
	}
	if run.EventsEmitted != 2 || run.EventsDropped != 0 {
		t.Fatalf("run-end accounting = %d emitted, %d dropped; want 2 and 0", run.EventsEmitted, run.EventsDropped)
	}
}

func TestRunEndIsEmittedOnce(t *testing.T) {
	t.Parallel()
	clock := newClock()
	sink := trace.NewMemorySink(0)
	recorder := trace.New(sink, clock.Now)
	recorder.RunEnd("assured", nil)
	recorder.RunEnd("defect", nil)
	recorder.Progress("late", "after the run ended")

	events := sink.Events()
	if len(events) != 2 {
		t.Fatalf("recorded %+v, want run-start and a single run-end", events)
	}
	if events[1].Run.Verdict != "assured" {
		t.Errorf("run-end verdict = %q, want the first one recorded", events[1].Run.Verdict)
	}
}

func TestRunEndCountsTheEventsTheSinkDropped(t *testing.T) {
	t.Parallel()
	clock := newClock()
	sink := trace.NewMemorySink(4)
	recorder := trace.New(sink, clock.Now)
	for range 9 {
		recorder.Progress("mutation-progress", "1/9")
	}
	recorder.RunEnd("assured", nil)

	events := sink.Events()
	last := events[len(events)-1]
	if last.Type != trace.TypeRunEnd || last.Run == nil {
		t.Fatalf("last event = %+v, want run-end", last)
	}
	if last.Run.EventsEmitted != 3 || last.Run.EventsDropped != 7 {
		t.Fatalf("run-end accounting = %d emitted, %d dropped; want 3 and 7 for a ring of 4 fed 10 events, one slot held for the run-end",
			last.Run.EventsEmitted, last.Run.EventsDropped)
	}
}

func TestAFullRingAccountsForTheEventTheRunEndDisplaces(t *testing.T) {
	t.Parallel()
	for _, capacity := range []int{1, 2, 3, 4} {
		for _, notes := range []int{0, 1, 5} {
			clock := newClock()
			sink := trace.NewMemorySink(capacity)
			recorder := trace.New(sink, clock.Now)
			for range notes {
				recorder.Progress("mutation-progress", "1/5")
			}
			recorder.RunEnd("assured", nil)

			events := sink.Events()
			last := events[len(events)-1]
			if last.Type != trace.TypeRunEnd || last.Run == nil {
				t.Fatalf("ring of %d fed %d notes ended with %+v, want a run-end", capacity, notes, last)
			}
			// A recording is honest when its own accounting still describes
			// the recording after the run-end was written: as many events
			// beside it as it claims to have kept, and no drop it never
			// counted, however little room the ring had.
			if kept := int64(len(events)) - 1; kept != last.Run.EventsEmitted {
				t.Errorf("ring of %d fed %d notes holds %d events beside its run-end but accounts for %d",
					capacity, notes, kept, last.Run.EventsEmitted)
			}
			if last.Run.EventsDropped != sink.Dropped() {
				t.Errorf("ring of %d fed %d notes reported %d drops, the sink counted %d; a lossy trace must not read as complete",
					capacity, notes, last.Run.EventsDropped, sink.Dropped())
			}
			if attempts := int64(1 + notes); last.Run.EventsEmitted+last.Run.EventsDropped != attempts {
				t.Errorf("ring of %d fed %d notes accounts for %d of %d recorded events",
					capacity, notes, last.Run.EventsEmitted+last.Run.EventsDropped, attempts)
			}
		}
	}
}

func TestNilRecorderIsAnInertNoOp(t *testing.T) {
	t.Parallel()
	var recorder *trace.Recorder
	endPhase := recorder.PhaseStart("discover")
	if endPhase == nil {
		t.Fatal("PhaseStart on a nil recorder returned a nil closer; callers defer it unconditionally")
	}
	endPhase()
	recorder.Exec(trace.ExecRecord{Argv: []string{"go", "test"}})
	recorder.MutantExec(trace.MutantRecord{ID: "m-0001"})
	recorder.Route(trace.RouteRecord{MutantID: "m-0001", Reason: trace.ReasonUnreached})
	recorder.Progress("snapshot", "detail")
	recorder.Artifact("report", "report.json")
	recorder.RunEnd("assured", errors.New("ignored"))
}

func TestNewWithoutASinkReturnsADisabledRecorder(t *testing.T) {
	t.Parallel()
	if recorder := trace.New(nil, newClock().Now); recorder != nil {
		t.Fatalf("New(nil, clock) = %p, want a nil recorder so tracing costs nothing", recorder)
	}
}

func TestNewWithoutAClockUsesTheWallClock(t *testing.T) {
	t.Parallel()
	sink := trace.NewMemorySink(0)
	recorder := trace.New(sink, nil)
	recorder.RunEnd("assured", nil)

	events := sink.Events()
	if len(events) != 2 {
		t.Fatalf("recorded %+v, want run-start and run-end", events)
	}
	for _, event := range events {
		stamp, err := time.Parse(time.RFC3339Nano, event.Timestamp)
		if err != nil {
			t.Fatalf("timestamp %q is not RFC3339: %v", event.Timestamp, err)
		}
		if stamp.Location() != time.UTC {
			t.Errorf("timestamp %q is not UTC", event.Timestamp)
		}
		if event.ElapsedMS < 0 {
			t.Errorf("elapsed = %d, want a monotonic non-negative duration", event.ElapsedMS)
		}
	}
}

func TestConcurrentRecordingKeepsEveryEventAndItsSequenceOrder(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	dirSink, err := trace.NewDirSink(directory, trace.Filesystem{})
	if err != nil {
		t.Fatalf("NewDirSink = %v", err)
	}
	memory := trace.NewMemorySink(0)
	recorder := trace.New(trace.NewTeeSink(dirSink, memory), newClock().Now)

	const writers, perWriter = 8, 20
	var waiting sync.WaitGroup
	for writer := range writers {
		waiting.Add(1)
		go func() {
			defer waiting.Done()
			for range perWriter {
				if writer%2 == 0 {
					recorder.Progress("mutation-progress", "concurrent")
					continue
				}
				recorder.Exec(trace.ExecRecord{Argv: []string{"go", "test"}, ExitCode: 0})
			}
		}()
	}
	waiting.Wait()
	recorder.RunEnd("assured", nil)
	if err := dirSink.Close(); err != nil {
		t.Fatalf("Close = %v", err)
	}

	events := memory.Events()
	want := 1 + writers*perWriter + 1
	if len(events) != want {
		t.Fatalf("recorded %d events, want %d", len(events), want)
	}
	for index, event := range events {
		if event.Seq != int64(index+1) {
			t.Fatalf("event %d has seq %d; events must reach the sink in sequence order", index+1, event.Seq)
		}
	}
	if run := events[len(events)-1].Run; run == nil || run.EventsEmitted != int64(want-1) || run.EventsDropped != 0 {
		t.Fatalf("run-end accounting = %+v, want %d emitted and 0 dropped", run, want-1)
	}
	data, err := os.ReadFile(filepath.Join(directory, trace.FileName))
	if err != nil {
		t.Fatal(err)
	}
	if lines := len(jsonLines(t, data)); lines != want {
		t.Fatalf("trace file holds %d lines, want %d", lines, want)
	}
}

func TestIdenticalRecordingsProduceIdenticalBytes(t *testing.T) {
	t.Parallel()
	first := scriptedEvents(t)
	second := scriptedEvents(t)
	if len(first) != len(second) {
		t.Fatalf("recorded %d and %d events", len(first), len(second))
	}
	for index := range first {
		left, err := json.Marshal(first[index])
		if err != nil {
			t.Fatal(err)
		}
		right, err := json.Marshal(second[index])
		if err != nil {
			t.Fatal(err)
		}
		if string(left) != string(right) {
			t.Fatalf("event %d differs between recordings\n%s\n%s", index+1, left, right)
		}
	}
}

// jsonLines splits a JSONL file into its lines and fails the test when any
// line is not a JSON object.
func jsonLines(t *testing.T, data []byte) []string {
	t.Helper()
	if len(data) == 0 {
		return nil
	}
	if data[len(data)-1] != '\n' {
		t.Fatalf("JSONL data does not end with a newline: %q", data)
	}
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	for index, line := range lines {
		var decoded map[string]any
		if err := json.Unmarshal([]byte(line), &decoded); err != nil {
			t.Fatalf("line %d is not a JSON object: %v\n%s", index+1, err, line)
		}
	}
	return lines
}
