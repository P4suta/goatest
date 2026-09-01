// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"fmt"
	"strings"
	"testing"

	"github.com/P4suta/goatest/internal/testkit"
	"github.com/P4suta/goatest/internal/trace"
)

// summarizeFixture reads a testdata stream and renders it, which is the whole
// path the command runs.
func summarizeFixture(t *testing.T, name string) string {
	t.Helper()
	events, err := readEvents(strings.NewReader(readFixture(t, name)))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return renderSummary("testdata/"+name, events)
}

func TestRenderSummaryBreaksDownACompleteRecording(t *testing.T) {
	t.Parallel()
	testkit.Golden(t, "sample-summary.txt", []byte(summarizeFixture(t, "sample-trace.jsonl")))
}

func TestRenderSummaryReportsWhatATruncatedRecordingLacks(t *testing.T) {
	t.Parallel()
	testkit.Golden(t, "incomplete-summary.txt", []byte(summarizeFixture(t, "incomplete-trace.jsonl")))
}

func TestRenderSummaryDependsOnTheEventsAlone(t *testing.T) {
	t.Parallel()
	first := summarizeFixture(t, "sample-trace.jsonl")
	second := summarizeFixture(t, "sample-trace.jsonl")
	if first != second {
		t.Error("two renderings of one stream differ; the summary is not deterministic")
	}
}

func TestRenderSummaryCapsTheLongTablesAndAccountsForTheRest(t *testing.T) {
	t.Parallel()
	events := []trace.Event{{Seq: 1, Type: trace.TypeRunStart, Schema: trace.SchemaV1}}
	sequence := int64(1)
	add := func(event trace.Event) {
		sequence++
		event.Seq = sequence
		events = append(events, event)
	}
	for index := range 20 {
		add(trace.Event{Type: trace.TypeExec, Exec: &trace.ExecRecord{
			Argv:       []string{"tool" + fmt.Sprintf("%02d", index)},
			DurationMS: int64(index + 1),
		}})
	}
	for index := range 12 {
		add(trace.Event{Type: trace.TypeMutantExec, Mutant: &trace.MutantRecord{
			ID:         fmt.Sprintf("mutant%02d", index),
			Outcome:    "killed",
			DurationMS: int64(index + 1),
		}})
	}
	summary := renderSummary("memory", events)
	for _, want := range []string{
		"exec classes by total duration (top 15 of 20)",
		"5 more classes",
		"mutants by executions (top 10 of 12)",
		"2 more mutants",
	} {
		if !strings.Contains(summary, want) {
			t.Errorf("the summary does not mention %q:\n%s", want, summary)
		}
	}
	if strings.Contains(summary, "tool00") {
		t.Error("the cheapest exec class reached the capped table")
	}
}

func TestRenderSummaryNamesAMutantByItsIdentityWhenItHasNoDisplayIdentity(t *testing.T) {
	t.Parallel()
	identity := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	events := []trace.Event{
		{Seq: 1, Type: trace.TypeRunStart, Schema: trace.SchemaV1},
		{Seq: 2, Type: trace.TypeMutantExec, Mutant: &trace.MutantRecord{ID: identity, DurationMS: 7}},
	}
	summary := renderSummary("memory", events)
	if !strings.Contains(summary, identity) {
		t.Errorf("the summary does not name the mutant by its identity:\n%s", summary)
	}
	if !strings.Contains(summary, "1 mutant") {
		t.Errorf("the summary does not count the one mutant:\n%s", summary)
	}
}

func TestExecClassNormalizesWhatVariesBetweenTwoRunsOfOneCommand(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		argv []string
		want string
	}{
		{
			name: "a command shorter than the class",
			argv: []string{"go", "version"},
			want: "go version",
		},
		{
			name: "a flag without a value is kept",
			argv: []string{"go", "test", "-race", "./..."},
			want: "go test -race ./...",
		},
		{
			name: "the mutant filter is the value of a flag",
			argv: []string{"go", "test", "-count=1", "github.com/P4suta/goatest/internal/assure", "-args", "-test.run=^TestPlan$"},
			want: "go test -count=<value> github.com/P4suta/goatest/internal/assure -args -test.run=<value>",
		},
		{
			name: "a temporary path and the arguments beyond the class",
			argv: []string{"go", "test", "-c", "-coverpkg=github.com/P4suta/goatest/...", "-o", "/tmp/goatest-baseline-460516923/12b3f83f398bb463.test", "github.com/P4suta/goatest"},
			want: "go test -c -coverpkg=<value> -o <path> ...",
		},
		{
			name: "a compiled test binary",
			argv: []string{"/tmp/goatest-baseline-460516923/12b3f83f398bb463.test", "-test.run=^TestPlan$"},
			want: "<path> -test.run=<value>",
		},
		{
			name: "a windows path",
			argv: []string{`C:\Users\dev\go\bin\goatest.exe`, "verify"},
			want: "<path> verify",
		},
		{
			name: "an argument that is no flag keeps its value",
			argv: []string{"go", "run", "a=b"},
			want: "go run a=b",
		},
		{
			name: "no command at all",
			argv: nil,
			want: "(no command)",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			if got := execClass(testCase.argv); got != testCase.want {
				t.Errorf("execClass(%q) = %q, want %q", testCase.argv, got, testCase.want)
			}
		})
	}
}
