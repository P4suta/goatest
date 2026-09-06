// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/P4suta/goatest/internal/trace"
)

func TestRenderSummaryOfAnEmptyStreamSaysSo(t *testing.T) {
	t.Parallel()
	if got := renderSummary("empty.jsonl", nil); got != "trace: empty.jsonl\nthe stream carries no events\n" {
		t.Fatalf("empty summary = %q", got)
	}
}

func TestFormatHelpersRefuseAShareOfNothing(t *testing.T) {
	t.Parallel()
	if got := formatShare(1, 0); got != noValue {
		t.Errorf("share of zero = %q, want %q", got, noValue)
	}
	if got := formatShare(1, -1); got != noValue {
		t.Errorf("share of a negative total = %q, want %q", got, noValue)
	}
	if got := formatShare(1, 3); got != "33.3%" {
		t.Errorf("share = %q, want 33.3%%", got)
	}
	if got := formatMean(5, 0); got != noValue {
		t.Errorf("mean over zero calls = %q, want %q", got, noValue)
	}
	if got := formatMean(6000, 3); got != "2s" {
		t.Errorf("mean = %q, want 2s", got)
	}
	if got := formatDuration(90000); got != "1m30s" {
		t.Errorf("duration = %q, want 1m30s", got)
	}
}

func TestIsAbsolutePathRecognizesBothRecordingPlatforms(t *testing.T) {
	t.Parallel()
	cases := []struct {
		argument string
		want     bool
	}{
		{"/tmp/x", true},
		{`C:\tool.exe`, true},
		{"c:/tool", true},
		{"Z:/x", true},
		{"z:\\x", true},
		{"a:/x", true},
		{"A:\\x", true},
		{"c:/", true},
		{"c:x", false},
		{"c:", false},
		{"1:/x", false},
		{"cd", false},
		{"", false},
		{"relative/path", false},
	}
	for _, testCase := range cases {
		if got := isAbsolutePath(testCase.argument); got != testCase.want {
			t.Errorf("isAbsolutePath(%q) = %t, want %t", testCase.argument, got, testCase.want)
		}
	}
}

func TestExecClassNormalizesTheVaryingHalfOfACommandLine(t *testing.T) {
	t.Parallel()
	if got := execClass(nil); got != noCommand {
		t.Errorf("class of no command = %q, want %q", got, noCommand)
	}
	if got := execClass([]string{"go", "test", "-count=1", "-short", "/abs/pkg.test", `C:\abs\pkg.test`}); got != "go test -count=<value> -short <path> <path>" {
		t.Errorf("class = %q", got)
	}
	long := []string{"go", "test", "-run=X", "a", "b", "c", "d", "e"}
	if got := execClass(long); got != "go test -run=<value> a b c "+ellipsis {
		t.Errorf("long class = %q, want the first %d words and an ellipsis", got, execClassWords)
	}
	exact := []string{"go", "test", "a", "b", "c", "d"}
	if got := execClass(exact); strings.Contains(got, ellipsis) {
		t.Errorf("class of exactly %d words = %q, want no ellipsis", execClassWords, got)
	}
}

func TestRunBlockNamesAnIncompleteRecording(t *testing.T) {
	t.Parallel()
	missing := runBlock([]trace.Event{{Type: trace.TypeRunStart}})
	if len(missing) != 2 || !strings.Contains(missing[1], "incomplete") {
		t.Fatalf("block without a run-end = %q", missing)
	}
	null := runBlock([]trace.Event{{Type: trace.TypeRunEnd, Run: nil}})
	if len(null) != 2 || !strings.Contains(null[1], "incomplete") {
		t.Fatalf("block with a null run payload = %q", null)
	}
	quiet := runBlock([]trace.Event{{Type: trace.TypeRunEnd, Run: &trace.RunRecord{Verdict: "ASSURED", EventsEmitted: 3}}})
	if !slices.Contains(quiet, "verdict: ASSURED") || slices.ContainsFunc(quiet, func(line string) bool { return strings.HasPrefix(line, "error: ") }) {
		t.Fatalf("block of a quiet run = %q", quiet)
	}
	loud := runBlock([]trace.Event{{Type: trace.TypeRunEnd, Run: &trace.RunRecord{Verdict: "ERROR", Error: "boom"}}})
	if !slices.Contains(loud, "error: boom") {
		t.Fatalf("block of a failed run = %q", loud)
	}
}

func mutantExecEvent(id string, duration int64, outcome string) trace.Event {
	return trace.Event{Type: trace.TypeMutantExec, Mutant: &trace.MutantRecord{ID: id, DurationMS: duration, Outcome: outcome}}
}

func TestMutantTotalsSkipWhatCarriesNoPayloadAndAccumulateTheRest(t *testing.T) {
	t.Parallel()
	events := []trace.Event{
		mutantExecEvent("mutant-a", 5, "killed"),
		mutantExecEvent("mutant-a", 7, "killed"),
		{Type: trace.TypeMutantExec, Mutant: nil},
		{Type: trace.TypeExec, Exec: &trace.ExecRecord{}},
	}
	mutants, outcomes, executions, duration := mutantTotals(events)
	if executions != 2 || duration != 12 {
		t.Fatalf("executions = %d, duration = %d, want 2 and 12", executions, duration)
	}
	if len(mutants) != 1 || mutants[0].executions != 2 || mutants[0].duration != 12 {
		t.Fatalf("mutants = %+v", mutants)
	}
	if len(outcomes) != 1 || outcomes[0].outcome != "killed" || outcomes[0].executions != 2 || outcomes[0].duration != 12 {
		t.Fatalf("outcomes = %+v", outcomes)
	}
}

func TestMutantTotalsOrderTiesDeterministically(t *testing.T) {
	t.Parallel()
	events := []trace.Event{
		mutantExecEvent("mutant-b", 3, "killed"),
		mutantExecEvent("mutant-a", 3, "survived"),
	}
	mutants, _, _, _ := mutantTotals(events)
	if len(mutants) != len(events) || mutants[0].id != "mutant-a" || mutants[1].id != "mutant-b" {
		t.Fatalf("tied mutants = %+v, want the identity as the last resort order", mutants)
	}
}

func TestMutantTotalsOrderByExecutionsThenDuration(t *testing.T) {
	t.Parallel()
	const (
		shortDuration      = int64(1)
		longDuration       = int64(100)
		expectedTotalCount = 2
	)
	byExecutions, _, _, _ := mutantTotals([]trace.Event{
		mutantExecEvent("mutant-fewer", longDuration, "killed"),
		mutantExecEvent("mutant-more", shortDuration, "killed"),
		mutantExecEvent("mutant-more", shortDuration, "killed"),
	})
	if len(byExecutions) != expectedTotalCount || byExecutions[0].id != "mutant-more" {
		t.Fatalf("execution order = %+v", byExecutions)
	}
	byDuration, _, _, _ := mutantTotals([]trace.Event{
		mutantExecEvent("mutant-fast", shortDuration, "killed"),
		mutantExecEvent("mutant-slow", longDuration, "killed"),
	})
	if len(byDuration) != expectedTotalCount || byDuration[0].id != "mutant-slow" {
		t.Fatalf("duration order = %+v", byDuration)
	}
}

func TestOutcomeTotalsOrderByExecutionsDurationAndIdentity(t *testing.T) {
	t.Parallel()
	const (
		shortDuration      = int64(1)
		longDuration       = int64(100)
		expectedTotalCount = 2
	)
	for _, test := range []struct {
		name   string
		events []trace.Event
		want   string
	}{
		{
			name: "executions",
			events: []trace.Event{
				mutantExecEvent("mutant-a", longDuration, "few"),
				mutantExecEvent("mutant-b", shortDuration, "many"),
				mutantExecEvent("mutant-c", shortDuration, "many"),
			},
			want: "many",
		},
		{
			name: "duration",
			events: []trace.Event{
				mutantExecEvent("mutant-a", shortDuration, "fast"),
				mutantExecEvent("mutant-b", longDuration, "slow"),
			},
			want: "slow",
		},
		{
			name: "identity",
			events: []trace.Event{
				mutantExecEvent("mutant-a", shortDuration, "z-outcome"),
				mutantExecEvent("mutant-b", shortDuration, "a-outcome"),
			},
			want: "a-outcome",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, outcomes, _, _ := mutantTotals(test.events)
			if len(outcomes) != expectedTotalCount || outcomes[0].outcome != test.want {
				t.Fatalf("outcomes = %+v, want %q first", outcomes, test.want)
			}
		})
	}
}

func TestMutantBlockAccountsForTheRestBeyondTheTop(t *testing.T) {
	t.Parallel()
	events := make([]trace.Event, 0, mutantLimit+summaryOverflowItemCount)
	for index := range cap(events) {
		events = append(events, mutantExecEvent(fmt.Sprintf("mutant-%02d", index), int64(100+index), "survived"))
	}
	lines := mutantBlock(events)
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, fmt.Sprintf("(top %d of %d)", mutantLimit, len(events))) {
		t.Fatalf("block does not name its cap:\n%s", joined)
	}

	if !strings.Contains(joined, "2 more mutants: 2 executions, 201ms") {
		t.Fatalf("block does not account for the rest:\n%s", joined)
	}
	within := make([]trace.Event, 0, mutantLimit)
	for index := range mutantLimit {
		within = append(within, mutantExecEvent(fmt.Sprintf("mutant-%02d", index), int64(100+index), "survived"))
	}
	joined = strings.Join(mutantBlock(within), "\n")
	if strings.Contains(joined, "top") || strings.Contains(joined, "more mutant") {
		t.Fatalf("block within the cap still truncates:\n%s", joined)
	}
}

func TestExecBlockCapsItsTableAndAccountsForTheRest(t *testing.T) {
	t.Parallel()
	events := make([]trace.Event, 0, execClassLimit+summaryOverflowItemCount)
	for index := range cap(events) {
		events = append(events, execEvent([]string{"tool", fmt.Sprintf("verb-%02d", index)}, int64(100+index)))
	}
	joined := strings.Join(execBlock(events), "\n")
	if !strings.Contains(joined, fmt.Sprintf("(top %d of %d)", execClassLimit, len(events))) {
		t.Fatalf("block does not name its cap:\n%s", joined)
	}

	if !strings.Contains(joined, "2 more classes: 2 calls, 201ms") {
		t.Fatalf("block does not account for the rest:\n%s", joined)
	}
	within := events[:execClassLimit]
	joined = strings.Join(execBlock(within), "\n")
	if strings.Contains(joined, "top") || strings.Contains(joined, "more class") {
		t.Fatalf("block within the cap still truncates:\n%s", joined)
	}
}

func TestExecTotalsSkipWhatCarriesNoPayload(t *testing.T) {
	t.Parallel()
	totals := execTotals([]trace.Event{
		execEvent([]string{"go", "vet"}, 4),
		{Type: trace.TypeExec, Exec: nil},
		{Type: trace.TypeProgress, Progress: &trace.ProgressRecord{Kind: "note"}},
	})
	if len(totals) != 1 || totals[0].calls != 1 {
		t.Fatalf("totals = %+v, want the one exec that carried a payload", totals)
	}
}

func TestPhaseBlockNamesARecordingWhereNoPhaseEnded(t *testing.T) {
	t.Parallel()
	lines := phaseBlock([]trace.Event{{Type: trace.TypeRunStart}})
	if len(lines) != 2 || !strings.Contains(lines[1], "no phase ended") {
		t.Fatalf("block without phases = %q", lines)
	}
}

func phaseEndEvent(name string, duration int64) trace.Event {
	return trace.Event{Type: trace.TypePhaseEnd, Phase: &trace.PhaseRecord{Name: name, DurationMS: duration}}
}

func TestPhaseTotalsAccumulatePassesAndOrderTies(t *testing.T) {
	t.Parallel()
	totals := phaseTotals([]trace.Event{
		phaseEndEvent("baseline", 12),
		phaseEndEvent("mutation", 5),
		phaseEndEvent("mutation", 7),
	})
	if len(totals) != phaseTotalCount {
		t.Fatalf("totals = %+v", totals)
	}
	if totals[0].name != "mutation" || totals[0].passes != phaseTotalCount || totals[0].duration != 12 {
		t.Fatalf("repeated phase = %+v, want two passes summing 12", totals[0])
	}

	tied := phaseTotals([]trace.Event{phaseEndEvent("race", 3), phaseEndEvent("impact", 3)})
	if tied[0].name != "impact" || tied[1].name != "race" {
		t.Fatalf("tied phases = %+v", tied)
	}
}

func execEvent(argv []string, duration int64) trace.Event {
	return trace.Event{Type: trace.TypeExec, Exec: &trace.ExecRecord{Argv: argv, DurationMS: duration}}
}

func TestExecTotalsOrderTiesByCallsThenClass(t *testing.T) {
	t.Parallel()
	totals := execTotals([]trace.Event{
		execEvent([]string{"go", "vet"}, 4),
		execEvent([]string{"go", "build"}, 2),
		execEvent([]string{"go", "build"}, 2),
	})
	if len(totals) != 2 || totals[0].class != "go build" || totals[0].calls != 2 {
		t.Fatalf("totals = %+v, want the class with more calls first on a duration tie", totals)
	}
	tied := execTotals([]trace.Event{execEvent([]string{"go", "vet"}, 3), execEvent([]string{"go", "build"}, 3)})
	if tied[0].class != "go build" || tied[1].class != "go vet" {
		t.Fatalf("tied classes = %+v, want the class name as the last resort order", tied)
	}
}

func TestRenderTableAlignsNumbersRightAndTrimsTrailingSpace(t *testing.T) {
	t.Parallel()
	lines := renderTable(
		[]column{{"n", true}, {"name", false}},
		[][]string{{"1", "a"}, {"22", "bb"}},
	)
	want := []string{" n  name", " 1  a", "22  bb"}
	if !slices.Equal(lines, want) {
		t.Fatalf("table = %q, want %q", lines, want)
	}
}
