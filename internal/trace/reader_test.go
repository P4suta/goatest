// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package trace

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestReadSummaryMakesMissingIncompleteGapsAndDropsExplicit(t *testing.T) {
	missing, err := ReadSummary(filepath.Join(t.TempDir(), "absent"))
	if err != nil || !missing.Missing || missing.HasRunEnd {
		t.Fatalf("missing summary = (%+v, %v)", missing, err)
	}
	directory := t.TempDir()
	writeTraceEvents(t, directory,
		Event{Seq: 1, Type: TypeRunStart, Schema: SchemaV1, Timestamp: "2026-01-01T00:00:00Z"},
		Event{Seq: 3, Type: TypePhaseEnd, Timestamp: "2026-01-01T00:00:01Z", ElapsedMS: 1000, Phase: &PhaseRecord{Name: "baseline", DurationMS: 900}},
	)
	incomplete, err := ReadSummary(directory)
	if err != nil || incomplete.Missing || incomplete.HasRunEnd || incomplete.MissingSequences != 1 || incomplete.Counts[TypePhaseEnd] != 1 || incomplete.PhaseDurationMS["baseline"] != 900 {
		t.Fatalf("incomplete summary = (%+v, %v)", incomplete, err)
	}
	writeTraceEvents(t, directory,
		Event{Seq: 1, Type: TypeRunStart, Schema: SchemaV1, Timestamp: "2026-01-01T00:00:00Z"},
		Event{Seq: 2, Type: TypeRunEnd, Timestamp: "2026-01-01T00:00:01Z", ElapsedMS: 1000, Run: &RunRecord{Verdict: "ASSURED", EventsEmitted: 1, EventsDropped: 4}},
	)
	complete, err := ReadSummary(directory)
	if err != nil || !complete.HasRunEnd || complete.EventsDropped != 4 || complete.Verdict != "ASSURED" {
		t.Fatalf("complete summary = (%+v, %v)", complete, err)
	}
	difference := Diff(incomplete, complete)
	if difference.EventsDroppedDelta != 4 || difference.MissingSequencesDelta != -1 || difference.AfterVerdict != "ASSURED" || difference.BeforeRunEnd || !difference.AfterRunEnd {
		t.Fatalf("summary diff = %+v", difference)
	}
}

func TestReadSummaryRejectsUnknownFieldsAndOutOfOrderSequences(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, FileName), []byte(`{"seq":1,"type":"run-start","schema":"goatest-trace-v1","timestamp":"2026-01-01T00:00:00Z","elapsed_ms":0,"unknown":true}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadSummary(directory); err == nil {
		t.Fatal("reader accepted an unknown field")
	}
	writeTraceEvents(t, directory,
		Event{Seq: 2, Type: TypeRunStart, Schema: SchemaV1, Timestamp: "2026-01-01T00:00:00Z"},
		Event{Seq: 1, Type: TypeProgress, Timestamp: "2026-01-01T00:00:01Z", Progress: &ProgressRecord{Kind: "late"}},
	)
	if _, err := ReadSummary(directory); err == nil {
		t.Fatal("reader accepted out-of-order events")
	}
}

func TestReadSummaryCountsAProbeExecAndRejectsOneWithoutItsPayload(t *testing.T) {
	directory := t.TempDir()
	writeTraceEvents(t, directory,
		Event{Seq: 1, Type: TypeRunStart, Schema: SchemaV1, Timestamp: "2026-01-01T00:00:00Z"},
		Event{Seq: 2, Type: TypeProbeExec, Timestamp: "2026-01-01T00:00:01Z", ElapsedMS: 1000,
			Probe: &ProbeRecord{Target: "TestRun", Outcome: ProbeOutcomeMeasured, Infected: []string{"m-0001"}}},
	)
	measured, err := ReadSummary(directory)
	if err != nil || measured.Counts[TypeProbeExec] != 1 {
		t.Fatalf("summary of a probe pass = (%+v, %v)", measured, err)
	}
	writeTraceEvents(t, directory,
		Event{Seq: 1, Type: TypeRunStart, Schema: SchemaV1, Timestamp: "2026-01-01T00:00:00Z"},
		Event{Seq: 2, Type: TypeProbeExec, Timestamp: "2026-01-01T00:00:01Z", ElapsedMS: 1000},
	)
	if _, err := ReadSummary(directory); err == nil {
		t.Fatal("reader accepted a probe-exec event without its payload")
	}
}

func writeTraceEvents(t *testing.T, directory string, events ...Event) {
	t.Helper()
	var data []byte
	for _, event := range events {
		line, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		data = append(data, line...)
		data = append(data, '\n')
	}
	if err := os.WriteFile(filepath.Join(directory, FileName), data, 0o600); err != nil {
		t.Fatal(err)
	}
}
