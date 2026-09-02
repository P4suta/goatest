// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/P4suta/goatest/internal/trace"
)

// runStart is the line every well formed stream opens with, so a case can name
// the one line it is actually about.
const runStart = `{"seq":1,"type":"run-start","schema":"goatest-trace-v1","timestamp":"2026-01-01T00:00:00Z","elapsed_ms":0}`

// stream joins lines into the JSON Lines text a reader is handed.
func stream(lines ...string) string {
	return strings.Join(lines, "\n") + "\n"
}

// readFixture reads one testdata stream.
func readFixture(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read the fixture: %v", err)
	}
	return string(data)
}

func TestReadEventsKeepsTheStreamInOrderWithItsPayloads(t *testing.T) {
	t.Parallel()
	events, err := readEvents(strings.NewReader(readFixture(t, "sample-trace.jsonl")))
	if err != nil {
		t.Fatalf("read the sample trace: %v", err)
	}
	if len(events) != 29 {
		t.Fatalf("read %d events, want 29", len(events))
	}
	if events[0].Type != trace.TypeRunStart || events[0].Schema != trace.SchemaV1 {
		t.Errorf("first event is %+v, want the run-start of %s", events[0], trace.SchemaV1)
	}
	last := events[len(events)-1]
	if last.Type != trace.TypeRunEnd || last.Run == nil || last.Run.Verdict != "INSUFFICIENT" {
		t.Errorf("last event is %+v, want the run-end carrying INSUFFICIENT", last)
	}
	if last.Run != nil && last.Run.EventsDropped != 1 {
		t.Errorf("run-end reports %d dropped events, want 1", last.Run.EventsDropped)
	}
	// The recording numbers every event it attempted, so the event a sink
	// dropped leaves a gap the reader accepts rather than an error.
	if events[17].Seq != 18 || events[18].Seq != 20 {
		t.Errorf("sequence numbers %d and %d around the drop, want 18 and 20", events[17].Seq, events[18].Seq)
	}
	execs := 0
	for _, event := range events {
		if event.Type == trace.TypeExec {
			execs++
			if event.Exec == nil {
				t.Fatalf("exec event %d carries no payload", event.Seq)
			}
		}
	}
	if execs != 6 {
		t.Errorf("read %d exec events, want 6", execs)
	}
}

func TestReadEventsAcceptsATruncatedRecording(t *testing.T) {
	t.Parallel()
	events, err := readEvents(strings.NewReader(readFixture(t, "incomplete-trace.jsonl")))
	if err != nil {
		t.Fatalf("read the incomplete trace: %v", err)
	}
	if len(events) != 5 {
		t.Fatalf("read %d events, want 5", len(events))
	}
	if events[len(events)-1].Type == trace.TypeRunEnd {
		t.Error("the incomplete trace ends with a run-end event; the fixture is meant to be truncated")
	}
}

func TestReadEventsRejectsDeviationsNamingTheLine(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		stream string
		want   []string
	}{
		{
			name:   "not json",
			stream: stream(runStart, `{"seq":2`),
			want:   []string{"line 2"},
		},
		{
			name:   "not an object",
			stream: stream(runStart, `[1,2,3]`),
			want:   []string{"line 2"},
		},
		{
			name:   "two values on one line",
			stream: stream(runStart, `{"seq":2,"type":"progress","timestamp":"2026-01-01T00:00:01Z","elapsed_ms":1,"progress":{"kind":"note"}} {}`),
			want:   []string{"line 2"},
		},
		{
			name:   "blank line",
			stream: stream(runStart, ``, runStart),
			want:   []string{"line 2", "blank"},
		},
		{
			name:   "unknown field",
			stream: stream(runStart, `{"seq":2,"type":"progress","timestamp":"2026-01-01T00:00:01Z","elapsed_ms":1,"progress":{"kind":"note"},"extra":1}`),
			want:   []string{"line 2", `unknown field "extra"`},
		},
		{
			name:   "unknown field inside a payload",
			stream: stream(runStart, `{"seq":2,"type":"progress","timestamp":"2026-01-01T00:00:01Z","elapsed_ms":1,"progress":{"kind":"note","extra":1}}`),
			want:   []string{"line 2", `unknown field "extra"`},
		},
		{
			name:   "unknown event type",
			stream: stream(runStart, `{"seq":2,"type":"guess","timestamp":"2026-01-01T00:00:01Z","elapsed_ms":1}`),
			want:   []string{"line 2", `unknown event type "guess"`},
		},
		{
			name:   "missing timestamp",
			stream: stream(runStart, `{"seq":2,"type":"progress","elapsed_ms":1,"progress":{"kind":"note"}}`),
			want:   []string{"line 2", `"timestamp"`},
		},
		{
			name:   "missing elapsed_ms",
			stream: stream(runStart, `{"seq":2,"type":"progress","timestamp":"2026-01-01T00:00:01Z","progress":{"kind":"note"}}`),
			want:   []string{"line 2", `"elapsed_ms"`},
		},
		{
			name:   "timestamp that is not rfc 3339",
			stream: stream(runStart, `{"seq":2,"type":"progress","timestamp":"yesterday","elapsed_ms":1,"progress":{"kind":"note"}}`),
			want:   []string{"line 2", "timestamp"},
		},
		{
			name:   "missing payload",
			stream: stream(runStart, `{"seq":2,"type":"exec","timestamp":"2026-01-01T00:00:01Z","elapsed_ms":1}`),
			want:   []string{"line 2", "exec"},
		},
		{
			name:   "payload of another event type",
			stream: stream(runStart, `{"seq":2,"type":"exec","timestamp":"2026-01-01T00:00:01Z","elapsed_ms":1,"exec":{"argv":["go"],"exit_code":0},"phase":{"name":"mutation"}}`),
			want:   []string{"line 2", "phase"},
		},
		{
			name:   "payload on the run-start event",
			stream: stream(`{"seq":1,"type":"run-start","schema":"goatest-trace-v1","timestamp":"2026-01-01T00:00:00Z","elapsed_ms":0,"progress":{"kind":"note"}}`),
			want:   []string{"line 1", "progress"},
		},
		{
			name:   "missing exec field",
			stream: stream(runStart, `{"seq":2,"type":"exec","timestamp":"2026-01-01T00:00:01Z","elapsed_ms":1,"exec":{"argv":["go","version"]}}`),
			want:   []string{"line 2", `"exec.exit_code"`},
		},
		{
			name:   "missing run field",
			stream: stream(runStart, `{"seq":2,"type":"run-end","timestamp":"2026-01-01T00:00:01Z","elapsed_ms":1,"run":{"verdict":"ASSURED","events_emitted":1}}`),
			want:   []string{"line 2", `"run.events_dropped"`},
		},
		{
			name:   "empty mutant identity",
			stream: stream(runStart, `{"seq":2,"type":"mutant-exec","timestamp":"2026-01-01T00:00:01Z","elapsed_ms":1,"mutant":{"id":""}}`),
			want:   []string{"line 2", "mutant.id"},
		},
		{
			name:   "negative duration",
			stream: stream(runStart, `{"seq":2,"type":"phase-end","timestamp":"2026-01-01T00:00:01Z","elapsed_ms":1,"phase":{"name":"mutation","duration_ms":-1}}`),
			want:   []string{"line 2", "phase.duration_ms"},
		},
		{
			name:   "unknown route reason",
			stream: stream(runStart, `{"seq":2,"type":"route","timestamp":"2026-01-01T00:00:01Z","elapsed_ms":1,"route":{"path":"a.go","reason":"guess"}}`),
			want:   []string{"line 2", `route reason "guess"`},
		},
		{
			name:   "environment value in an environment name",
			stream: stream(runStart, `{"seq":2,"type":"exec","timestamp":"2026-01-01T00:00:01Z","elapsed_ms":1,"exec":{"argv":["go"],"exit_code":0,"env_names":["PATH=/usr/bin"]}}`),
			want:   []string{"line 2", "env_names"},
		},
		{
			name:   "repeated environment name",
			stream: stream(runStart, `{"seq":2,"type":"exec","timestamp":"2026-01-01T00:00:01Z","elapsed_ms":1,"exec":{"argv":["go"],"exit_code":0,"env_names":["PATH","PATH"]}}`),
			want:   []string{"line 2", "env_names"},
		},
		{
			name:   "output digest that is not sha-256",
			stream: stream(runStart, `{"seq":2,"type":"exec","timestamp":"2026-01-01T00:00:01Z","elapsed_ms":1,"exec":{"argv":["go"],"exit_code":0,"output_sha256":"abc"}}`),
			want:   []string{"line 2", "output_sha256"},
		},
		{
			name:   "schema on another event type",
			stream: stream(runStart, `{"seq":2,"type":"progress","schema":"goatest-trace-v1","timestamp":"2026-01-01T00:00:01Z","elapsed_ms":1,"progress":{"kind":"note"}}`),
			want:   []string{"line 2", "schema"},
		},
		{
			name:   "unknown schema",
			stream: stream(`{"seq":1,"type":"run-start","schema":"goatest-trace-v2","timestamp":"2026-01-01T00:00:00Z","elapsed_ms":0}`),
			want:   []string{"line 1", "goatest-trace-v2", trace.SchemaV1},
		},
		{
			name:   "run-start without its schema",
			stream: stream(`{"seq":1,"type":"run-start","timestamp":"2026-01-01T00:00:00Z","elapsed_ms":0}`),
			want:   []string{"line 1", `"schema"`},
		},
		{
			name:   "stream that does not open with a run-start",
			stream: stream(`{"seq":1,"type":"progress","timestamp":"2026-01-01T00:00:00Z","elapsed_ms":1,"progress":{"kind":"note"}}`),
			want:   []string{"line 1", "run-start"},
		},
		{
			name:   "second run-start",
			stream: stream(runStart, `{"seq":2,"type":"run-start","schema":"goatest-trace-v1","timestamp":"2026-01-01T00:00:01Z","elapsed_ms":1}`),
			want:   []string{"line 2", "run-start"},
		},
		{
			name: "event after the run-end",
			stream: stream(runStart,
				`{"seq":2,"type":"run-end","timestamp":"2026-01-01T00:00:01Z","elapsed_ms":1,"run":{"verdict":"ASSURED","events_emitted":1,"events_dropped":0}}`,
				`{"seq":3,"type":"progress","timestamp":"2026-01-01T00:00:02Z","elapsed_ms":2,"progress":{"kind":"note"}}`),
			want: []string{"line 3", "run-end"},
		},
		{
			name:   "sequence number that does not advance",
			stream: stream(runStart, `{"seq":1,"type":"progress","timestamp":"2026-01-01T00:00:01Z","elapsed_ms":1,"progress":{"kind":"note"}}`),
			want:   []string{"line 2", "seq"},
		},
		{
			name:   "sequence number below the first",
			stream: stream(`{"seq":0,"type":"run-start","schema":"goatest-trace-v1","timestamp":"2026-01-01T00:00:00Z","elapsed_ms":0}`),
			want:   []string{"line 1", "seq"},
		},
		{
			name:   "recording that opens above the first sequence",
			stream: stream(`{"seq":2,"type":"run-start","schema":"goatest-trace-v1","timestamp":"2026-01-01T00:00:00Z","elapsed_ms":0}`),
			want:   []string{"line 1", "seq 2", "opens"},
		},
		{
			name:   "null payload on the event that requires it",
			stream: stream(runStart, `{"seq":2,"type":"phase-start","timestamp":"2026-01-01T00:00:01Z","elapsed_ms":1,"phase":null}`),
			want:   []string{"line 2", "null", "phase"},
		},
		{
			name: "null run payload on the run-end",
			stream: stream(runStart,
				`{"seq":2,"type":"run-end","timestamp":"2026-01-01T00:00:01Z","elapsed_ms":1,"run":null}`),
			want: []string{"line 2", "null", "run"},
		},
		{
			name:   "null line",
			stream: stream(runStart, `null`),
			want:   []string{"line 2", "seq"},
		},
		{
			name:   "null phase payload on a phase-end",
			stream: stream(runStart, `{"seq":2,"type":"phase-end","timestamp":"2026-01-01T00:00:01Z","elapsed_ms":1,"phase":null}`),
			want:   []string{"line 2", "null", "phase"},
		},
		{
			name:   "null exec payload",
			stream: stream(runStart, `{"seq":2,"type":"exec","timestamp":"2026-01-01T00:00:01Z","elapsed_ms":1,"exec":null}`),
			want:   []string{"line 2", "null", "exec"},
		},
		{
			name:   "null mutant payload",
			stream: stream(runStart, `{"seq":2,"type":"mutant-exec","timestamp":"2026-01-01T00:00:01Z","elapsed_ms":1,"mutant":null}`),
			want:   []string{"line 2", "null", "mutant"},
		},
		{
			name:   "null route payload",
			stream: stream(runStart, `{"seq":2,"type":"route","timestamp":"2026-01-01T00:00:01Z","elapsed_ms":1,"route":null}`),
			want:   []string{"line 2", "null", "route"},
		},
		{
			name:   "null progress payload",
			stream: stream(runStart, `{"seq":2,"type":"progress","timestamp":"2026-01-01T00:00:01Z","elapsed_ms":1,"progress":null}`),
			want:   []string{"line 2", "null", "progress"},
		},
		{
			name:   "null artifact payload",
			stream: stream(runStart, `{"seq":2,"type":"artifact","timestamp":"2026-01-01T00:00:01Z","elapsed_ms":1,"artifact":null}`),
			want:   []string{"line 2", "null", "artifact"},
		},
		{
			name:   "missing seq",
			stream: stream(runStart, `{"type":"progress","timestamp":"2026-01-01T00:00:01Z","elapsed_ms":1,"progress":{"kind":"note"}}`),
			want:   []string{"line 2", "seq"},
		},
		{
			name:   "second value after a valid one",
			stream: stream(runStart, `{"seq":2,"type":"progress","timestamp":"2026-01-01T00:00:01Z","elapsed_ms":1,"progress":{"kind":"note"}} {"seq":3}`),
			want:   []string{"line 2", "more than one value"},
		},
		{
			name:   "negative elapsed_ms",
			stream: stream(runStart, `{"seq":2,"type":"progress","timestamp":"2026-01-01T00:00:01Z","elapsed_ms":-1,"progress":{"kind":"note"}}`),
			want:   []string{"line 2", "elapsed_ms"},
		},
		{
			name:   "empty timestamp",
			stream: stream(runStart, `{"seq":2,"type":"progress","timestamp":"","elapsed_ms":1,"progress":{"kind":"note"}}`),
			want:   []string{"line 2", "timestamp"},
		},
		{
			name:   "phase without its name field",
			stream: stream(runStart, `{"seq":2,"type":"phase-end","timestamp":"2026-01-01T00:00:01Z","elapsed_ms":1,"phase":{"duration_ms":1}}`),
			want:   []string{"line 2", "name"},
		},
		{
			name:   "phase with an empty name",
			stream: stream(runStart, `{"seq":2,"type":"phase-start","timestamp":"2026-01-01T00:00:01Z","elapsed_ms":1,"phase":{"name":""}}`),
			want:   []string{"line 2", "phase.name"},
		},
		{
			name:   "phase with a negative duration",
			stream: stream(runStart, `{"seq":2,"type":"phase-end","timestamp":"2026-01-01T00:00:01Z","elapsed_ms":1,"phase":{"name":"baseline","duration_ms":-1}}`),
			want:   []string{"line 2", "phase.duration_ms"},
		},
		{
			name:   "exec with a negative timeout",
			stream: stream(runStart, `{"seq":2,"type":"exec","timestamp":"2026-01-01T00:00:01Z","elapsed_ms":1,"exec":{"argv":["go"],"exit_code":0,"timeout_ms":-1}}`),
			want:   []string{"line 2", "exec.timeout_ms"},
		},
		{
			name:   "exec with a negative duration",
			stream: stream(runStart, `{"seq":2,"type":"exec","timestamp":"2026-01-01T00:00:01Z","elapsed_ms":1,"exec":{"argv":["go"],"exit_code":0,"duration_ms":-1}}`),
			want:   []string{"line 2", "exec.duration_ms"},
		},
		{
			name:   "exec with negative output bytes",
			stream: stream(runStart, `{"seq":2,"type":"exec","timestamp":"2026-01-01T00:00:01Z","elapsed_ms":1,"exec":{"argv":["go"],"exit_code":0,"output_bytes":-1}}`),
			want:   []string{"line 2", "exec.output_bytes"},
		},
		{
			name:   "output digest of the right length with the wrong alphabet",
			stream: stream(runStart, `{"seq":2,"type":"exec","timestamp":"2026-01-01T00:00:01Z","elapsed_ms":1,"exec":{"argv":["go"],"exit_code":0,"output_sha256":"`+strings.Repeat("Z", 64)+`"}}`),
			want:   []string{"line 2", "output_sha256"},
		},
		{
			name:   "mutant without its id field",
			stream: stream(runStart, `{"seq":2,"type":"mutant-exec","timestamp":"2026-01-01T00:00:01Z","elapsed_ms":1,"mutant":{"outcome":"killed"}}`),
			want:   []string{"line 2", "id"},
		},
		{
			name:   "mutant with a negative timeout",
			stream: stream(runStart, `{"seq":2,"type":"mutant-exec","timestamp":"2026-01-01T00:00:01Z","elapsed_ms":1,"mutant":{"id":"m","timeout_ms":-1}}`),
			want:   []string{"line 2", "mutant.timeout_ms"},
		},
		{
			name:   "mutant with a negative duration",
			stream: stream(runStart, `{"seq":2,"type":"mutant-exec","timestamp":"2026-01-01T00:00:01Z","elapsed_ms":1,"mutant":{"id":"m","duration_ms":-1}}`),
			want:   []string{"line 2", "mutant.duration_ms"},
		},
		{
			name:   "route without its path field",
			stream: stream(runStart, `{"seq":2,"type":"route","timestamp":"2026-01-01T00:00:01Z","elapsed_ms":1,"route":{"reason":"unreached"}}`),
			want:   []string{"line 2", "path"},
		},
		{
			name:   "route with a negative line",
			stream: stream(runStart, `{"seq":2,"type":"route","timestamp":"2026-01-01T00:00:01Z","elapsed_ms":1,"route":{"path":"a.go","reason":"unreached","line":-1}}`),
			want:   []string{"line 2", "route.line"},
		},
		{
			name:   "route with an unknown granularity",
			stream: stream(runStart, `{"seq":2,"type":"route","timestamp":"2026-01-01T00:00:01Z","elapsed_ms":1,"route":{"path":"a.go","reason":"unreached","granularity":"line"}}`),
			want:   []string{"line 2", `route granularity "line"`},
		},
		{
			name:   "route with an unknown fallback",
			stream: stream(runStart, `{"seq":2,"type":"route","timestamp":"2026-01-01T00:00:01Z","elapsed_ms":1,"route":{"path":"a.go","reason":"unreached","fallback":"guess"}}`),
			want:   []string{"line 2", `route fallback "guess"`},
		},
		{
			name:   "route with a fallback on a decision the blocks carried",
			stream: stream(runStart, `{"seq":2,"type":"route","timestamp":"2026-01-01T00:00:01Z","elapsed_ms":1,"route":{"path":"a.go","reason":"unreached","granularity":"block","fallback":"outside-blocks"}}`),
			want:   []string{"line 2", `route fallback "outside-blocks"`, `granularity "block"`},
		},
		{
			name:   "route with a fallback and no granularity",
			stream: stream(runStart, `{"seq":2,"type":"route","timestamp":"2026-01-01T00:00:01Z","elapsed_ms":1,"route":{"path":"a.go","reason":"unreached","fallback":"position-unknown"}}`),
			want:   []string{"line 2", `route fallback "position-unknown"`, "granularity"},
		},
		{
			name:   "route with a column and no granularity",
			stream: stream(runStart, `{"seq":2,"type":"route","timestamp":"2026-01-01T00:00:01Z","elapsed_ms":1,"route":{"path":"a.go","reason":"unreached","column":9}}`),
			want:   []string{"line 2", "route column", "granularity"},
		},
		{
			name:   "route with a file candidate count and no granularity",
			stream: stream(runStart, `{"seq":2,"type":"route","timestamp":"2026-01-01T00:00:01Z","elapsed_ms":1,"route":{"path":"a.go","reason":"unreached","file_candidates":0}}`),
			want:   []string{"line 2", "route file_candidates", "granularity"},
		},
		{
			name:   "route with a negative column",
			stream: stream(runStart, `{"seq":2,"type":"route","timestamp":"2026-01-01T00:00:01Z","elapsed_ms":1,"route":{"path":"a.go","reason":"unreached","column":-1}}`),
			want:   []string{"line 2", "route.column"},
		},
		{
			name:   "route with a negative file candidate count",
			stream: stream(runStart, `{"seq":2,"type":"route","timestamp":"2026-01-01T00:00:01Z","elapsed_ms":1,"route":{"path":"a.go","reason":"unreached","file_candidates":-1}}`),
			want:   []string{"line 2", "route.file_candidates"},
		},
		{
			name:   "progress without its kind field",
			stream: stream(runStart, `{"seq":2,"type":"progress","timestamp":"2026-01-01T00:00:01Z","elapsed_ms":1,"progress":{}}`),
			want:   []string{"line 2", "kind"},
		},
		{
			name:   "progress with an empty kind",
			stream: stream(runStart, `{"seq":2,"type":"progress","timestamp":"2026-01-01T00:00:01Z","elapsed_ms":1,"progress":{"kind":""}}`),
			want:   []string{"line 2", "progress.kind"},
		},
		{
			name:   "artifact without its kind field",
			stream: stream(runStart, `{"seq":2,"type":"artifact","timestamp":"2026-01-01T00:00:01Z","elapsed_ms":1,"artifact":{"path":"kept"}}`),
			want:   []string{"line 2", "kind"},
		},
		{
			name:   "artifact with an empty kind",
			stream: stream(runStart, `{"seq":2,"type":"artifact","timestamp":"2026-01-01T00:00:01Z","elapsed_ms":1,"artifact":{"kind":"","path":"kept"}}`),
			want:   []string{"line 2", "artifact.kind"},
		},
		{
			name:   "artifact with an empty path",
			stream: stream(runStart, `{"seq":2,"type":"artifact","timestamp":"2026-01-01T00:00:01Z","elapsed_ms":1,"artifact":{"kind":"baseline-scratch","path":""}}`),
			want:   []string{"line 2", "artifact.path"},
		},
		{
			name:   "run-end without its accounting",
			stream: stream(runStart, `{"seq":2,"type":"run-end","timestamp":"2026-01-01T00:00:01Z","elapsed_ms":1,"run":{"events_dropped":0}}`),
			want:   []string{"line 2", "events_emitted"},
		},
		{
			name:   "run-end with a negative drop count",
			stream: stream(runStart, `{"seq":2,"type":"run-end","timestamp":"2026-01-01T00:00:01Z","elapsed_ms":1,"run":{"events_emitted":1,"events_dropped":-1}}`),
			want:   []string{"line 2", "events_dropped"},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			events, err := readEvents(strings.NewReader(testCase.stream))
			if err == nil {
				t.Fatalf("read %d events, want an error naming the deviating line", len(events))
			}
			for _, want := range testCase.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not mention %q", err, want)
				}
			}
		})
	}
}

func TestReadEventsAcceptsAFallbackOnTheRouteItDroppedToTheFile(t *testing.T) {
	t.Parallel()
	// A fallback is what dropped a route to the file, so it belongs on a
	// route decided by file and nowhere else. Every other combination of the
	// two labels is a route the routing could have taken.
	cases := []struct {
		name  string
		route string
	}{
		{
			name:  "a file route that fell back",
			route: `{"path":"a.go","reason":"coverage-reaching","granularity":"file","fallback":"outside-blocks"}`,
		},
		{
			name:  "a file route that recorded no fallback",
			route: `{"path":"a.go","reason":"coverage-reaching","granularity":"file"}`,
		},
		{
			name:  "a block route",
			route: `{"path":"a.go","reason":"coverage-reaching","granularity":"block"}`,
		},
		{
			name:  "a block route carrying its column and candidate count",
			route: `{"path":"a.go","reason":"coverage-reaching","granularity":"block","column":9,"file_candidates":3}`,
		},
		{
			name:  "a file route that found no candidate",
			route: `{"path":"a.go","reason":"coverage-reaching","granularity":"file","fallback":"outside-blocks","column":9}`,
		},
		{
			name:  "a route from a recording made before the labels existed",
			route: `{"path":"a.go","reason":"coverage-reaching"}`,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			line := `{"seq":2,"type":"route","timestamp":"2026-01-01T00:00:01Z","elapsed_ms":1,"route":` + testCase.route + `}`
			events, err := readEvents(strings.NewReader(stream(runStart, line)))
			if err != nil || len(events) != 2 {
				t.Fatalf("read (%d events, %v), want the run-start and the route", len(events), err)
			}
		})
	}
}

// failingReader hands out its content and then fails, the way a disk that
// died mid-read does.
type failingReader struct {
	content string
	served  bool
}

func (reader *failingReader) Read(buffer []byte) (int, error) {
	if reader.served {
		return 0, errors.New("the disk fell over")
	}
	reader.served = true
	return copy(buffer, reader.content), nil
}

func TestReadEventsReportsAReaderThatFailedMidStream(t *testing.T) {
	t.Parallel()
	events, err := readEvents(&failingReader{content: stream(runStart)})
	if err == nil || !strings.Contains(err.Error(), "the disk fell over") {
		t.Fatalf("read %d events with error %v, want the reader's failure", len(events), err)
	}
}

func TestReadEventsAcceptsAStreamWithoutAFinalNewline(t *testing.T) {
	t.Parallel()
	events, err := readEvents(strings.NewReader(runStart))
	if err != nil || len(events) != 1 {
		t.Fatalf("read (%d events, %v), want the one event the unfinished line carries", len(events), err)
	}
}

func TestReadEventsRejectsAStreamWithoutEvents(t *testing.T) {
	t.Parallel()
	if _, err := readEvents(strings.NewReader("")); err == nil {
		t.Fatal("read an empty stream without an error")
	}
}

func TestReadEventsAcceptsALineLongerThanAScannerBuffer(t *testing.T) {
	t.Parallel()
	targets := make([]string, 0, 4096)
	for index := range 4096 {
		targets = append(targets, `"github.com/P4suta/goatest/internal/package`+strings.Repeat("x", index%17)+`"`)
	}
	long := `{"seq":2,"type":"route","timestamp":"2026-01-01T00:00:01Z","elapsed_ms":1,"route":{"path":"a.go","reason":"unreached","reaching_targets":[` +
		strings.Join(targets, ",") + `]}}`
	if len(long) < 64*1024 {
		t.Fatalf("the fixture line is %d bytes, which does not exceed a default scanner buffer", len(long))
	}
	events, err := readEvents(strings.NewReader(stream(runStart, long)))
	if err != nil {
		t.Fatalf("read a long line: %v", err)
	}
	if len(events) != 2 || events[1].Route == nil || len(events[1].Route.ReachingTargets) != 4096 {
		t.Fatalf("read %d events, want the run-start and a route carrying 4096 targets", len(events))
	}
}
