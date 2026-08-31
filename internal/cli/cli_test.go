// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/P4suta/goatest/internal/cli"
	"github.com/P4suta/goatest/internal/report"
)

type service struct {
	request cli.Request
	command cli.Command
	id      string
	report  report.Report
	err     error
}

func TestHelpListsPublicSurfaceWithoutRunningService(t *testing.T) {
	for _, flag := range []string{"--help", "-h"} {
		fake := &service{}
		var stdout, stderr bytes.Buffer
		if exit := cli.Run(t.Context(), []string{flag}, &stdout, &stderr, fake); exit != cli.ExitAssured {
			t.Fatalf("%s exit = %d, stderr = %q", flag, exit, stderr.String())
		}
		for _, expected := range []string{"--changed[=REF]", "--contract=standard-v1|deep-v1", "--trace[=DIR]", "GOATEST_TRACE", "init", "explain ID", "replay ID", "accept ID", "report"} {
			if !strings.Contains(stdout.String(), expected) {
				t.Errorf("%s help omitted %q:\n%s", flag, expected, stdout.String())
			}
		}
		if fake.command != "" {
			t.Errorf("%s ran service command %q", flag, fake.command)
		}
	}
}

func (s *service) Execute(_ context.Context, command cli.Command, request cli.Request, id string) (report.Report, error) {
	s.command, s.request, s.id = command, request, id
	return s.report, s.err
}

func TestDefaultCommandAndGlobalFlags(t *testing.T) {
	fake := &service{report: report.Report{Schema: report.SchemaV1, Verdict: report.VerdictAssured, Contract: "deep-v1"}}
	var stdout, stderr bytes.Buffer
	exit := cli.Run(t.Context(), []string{"--changed=origin/main", "--contract=deep-v1", "--json", "--ui=plain"}, &stdout, &stderr, fake)
	if exit != cli.ExitAssured || fake.command != cli.CommandVerify {
		t.Fatalf("exit/command = %d/%s", exit, fake.command)
	}
	if !fake.request.Changed || fake.request.ChangedRef != "origin/main" || fake.request.Contract != "deep-v1" || !fake.request.JSON || fake.request.UI != cli.UIPlain {
		t.Errorf("request = %+v", fake.request)
	}
	var rendered report.Report
	if err := json.Unmarshal(stdout.Bytes(), &rendered); err != nil || rendered.Verdict != report.VerdictAssured || rendered.Contract != "deep-v1" {
		t.Fatalf("JSON output = %+v, %v\n%s", rendered, err, stdout.Bytes())
	}
	if stderr.Len() != 0 {
		t.Errorf("stdout/stderr = %q / %q", stdout.String(), stderr.String())
	}
}

func TestTestBinaryArgumentsAreCanonicalizedAfterSeparator(t *testing.T) {
	fake := &service{report: report.Report{Schema: report.SchemaV1, Verdict: report.VerdictAssured}}
	exit := cli.Run(t.Context(), []string{"verify", "./...", "--", "-short", "-custom=value"}, &bytes.Buffer{}, &bytes.Buffer{}, fake)
	if exit != cli.ExitAssured {
		t.Fatalf("exit = %d", exit)
	}
	if got, want := fake.request.TestArgs, []string{"-test.short=true", "-custom=value"}; !slices.Equal(got, want) {
		t.Fatalf("test args = %v, want %v", got, want)
	}
}

func TestBareChangedFlagAndCancellationArePreserved(t *testing.T) {
	changed := &service{report: report.Report{Schema: report.SchemaV1, Verdict: report.VerdictAssured}}
	if exit := cli.Run(t.Context(), []string{"--changed"}, &bytes.Buffer{}, &bytes.Buffer{}, changed); exit != cli.ExitAssured || !changed.request.Changed || changed.request.ChangedRef != "" {
		t.Fatalf("bare changed = exit %d request %+v", exit, changed.request)
	}

	cancelled := &service{report: report.Report{Schema: report.SchemaV1, Verdict: report.VerdictError}, err: context.Canceled}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if exit := cli.Run(t.Context(), nil, &stdout, &stderr, cancelled); exit != cli.ExitInterrupted || stderr.String() != "goatest: interrupted\n" || stdout.Len() != 0 {
		t.Fatalf("cancellation = exit %d stdout %q stderr %q", exit, stdout.String(), stderr.String())
	}
}

func TestTraceFlagAsksForADefaultOrANamedDirectory(t *testing.T) {
	for _, test := range []struct {
		name      string
		args      []string
		command   cli.Command
		id        string
		directory string
	}{
		{name: "default", args: []string{"--trace"}, command: cli.CommandVerify},
		{name: "named", args: []string{"verify", "--trace=/tmp/goatest-trace"}, command: cli.CommandVerify, directory: "/tmp/goatest-trace"},
		{name: "empty-value", args: []string{"verify", "--trace="}, command: cli.CommandVerify},
		{name: "replay", args: []string{"replay", "finding-a", "--trace"}, command: cli.CommandReplay, id: "finding-a"},
	} {
		t.Run(test.name, func(t *testing.T) {
			fake := &service{report: report.Report{Schema: report.SchemaV1, Verdict: report.VerdictAssured}}
			exit := cli.Run(t.Context(), test.args, &bytes.Buffer{}, &bytes.Buffer{}, fake)
			if exit != cli.ExitAssured || fake.command != test.command || fake.id != test.id {
				t.Fatalf("%v => exit %d command %s id %q", test.args, exit, fake.command, fake.id)
			}
			if !fake.request.Trace || fake.request.TraceDirectory != test.directory {
				t.Fatalf("%v => trace %t directory %q", test.args, fake.request.Trace, fake.request.TraceDirectory)
			}
		})
	}
	unrequested := &service{report: report.Report{Schema: report.SchemaV1, Verdict: report.VerdictAssured}}
	if exit := cli.Run(t.Context(), []string{"verify"}, &bytes.Buffer{}, &bytes.Buffer{}, unrequested); exit != cli.ExitAssured || unrequested.request.Trace {
		t.Fatalf("unrequested verify = exit %d request %+v", exit, unrequested.request)
	}
	separated := &service{report: report.Report{Schema: report.SchemaV1, Verdict: report.VerdictAssured}}
	if exit := cli.Run(t.Context(), []string{"verify", "--", "--trace"}, &bytes.Buffer{}, &bytes.Buffer{}, separated); exit != cli.ExitAssured || separated.request.Trace {
		t.Fatalf("test-binary --trace = exit %d request %+v", exit, separated.request)
	}
}

func TestSubcommandsRequireTheirDocumentedArguments(t *testing.T) {
	for _, test := range []struct {
		args    []string
		command cli.Command
		id      string
	}{
		{[]string{"init"}, cli.CommandInit, ""},
		{[]string{"explain", "finding-a"}, cli.CommandExplain, "finding-a"},
		{[]string{"replay", "finding-b"}, cli.CommandReplay, "finding-b"},
		{[]string{"accept", "finding-c", "--reason=reviewed", "--expires=2026-12-01T00:00:00Z"}, cli.CommandAccept, "finding-c"},
		{[]string{"report"}, cli.CommandReport, ""},
	} {
		fake := &service{report: report.Report{Schema: report.SchemaV1, Verdict: report.VerdictAssured}}
		exit := cli.Run(t.Context(), test.args, &bytes.Buffer{}, &bytes.Buffer{}, fake)
		if exit != cli.ExitAssured || fake.command != test.command || fake.id != test.id {
			t.Errorf("%v => %d/%s/%q", test.args, exit, fake.command, fake.id)
		}
	}
	for _, args := range [][]string{
		{"explain"}, {"accept"}, {"unknown"}, {"--contract=bad"}, {"--unknown"},
		{"--no-tui"}, {"--no-apply"},
		{"verify", "--apply"}, {"doctor", "--changed"}, {"init", "--contract=deep-v1"},
		{"verify", "--latest-full"}, {"fix", "--reason=reviewed"}, {"report", "--apply"},
		{"doctor", "--trace"}, {"plan", "--trace=out"}, {"report", "--trace"}, {"fix", "--trace"},
		{"plan", "--", "-short"}, {"doctor", "--", "-short"}, {"report", "--", "-short"},
		{"init", "extra"}, {"report", "extra"}, {"replay", ""}, {"accept", "finding-c"},
	} {
		var stderr bytes.Buffer
		if exit := cli.Run(t.Context(), args, &bytes.Buffer{}, &stderr, &service{}); exit != cli.ExitError || stderr.Len() == 0 {
			t.Errorf("%v => exit %d stderr %q", args, exit, stderr.String())
		}
	}
}

func TestVerdictsMapToStableExitCodes(t *testing.T) {
	for verdict, want := range map[report.Verdict]int{
		report.VerdictAssured:       cli.ExitAssured,
		report.VerdictChangeAssured: cli.ExitAssured,
		report.VerdictScopeAssured:  cli.ExitAssured,
		report.VerdictResolved:      cli.ExitAssured,
		report.VerdictCompleted:     cli.ExitAssured,
		report.VerdictDefect:        cli.ExitDefect,
		report.VerdictReproduced:    cli.ExitDefect,
		report.VerdictInsufficient:  cli.ExitInsufficient,
		report.VerdictError:         cli.ExitError,
	} {
		fake := &service{report: report.Report{Schema: report.SchemaV1, Verdict: verdict}}
		if got := cli.Run(t.Context(), nil, &bytes.Buffer{}, &bytes.Buffer{}, fake); got != want {
			t.Errorf("%s => %d, want %d", verdict, got, want)
		}
	}
}

func TestErrorsEscapeTerminalControlCharactersOntoOneLine(t *testing.T) {
	fake := &service{err: errors.New("failed\nFINDING forged\x1b[31m")}
	var stderr bytes.Buffer
	if exit := cli.Run(t.Context(), nil, &bytes.Buffer{}, &stderr, fake); exit != cli.ExitError {
		t.Fatalf("exit = %d", exit)
	}
	if got, want := stderr.String(), "goatest: failed\\nFINDING forged\\u001b[31m\n"; got != want {
		t.Fatalf("stderr = %q, want %q", got, want)
	}
}

func TestInfrastructureErrorsRenderTheirErrorReportBeforeTheDiagnostic(t *testing.T) {
	for _, jsonOutput := range []bool{false, true} {
		t.Run(map[bool]string{false: "lines", true: "json"}[jsonOutput], func(t *testing.T) {
			result := report.Report{
				Schema: report.SchemaV1, Verdict: report.VerdictError, Contract: "standard-v1",
				Findings: []report.Finding{{ID: "infrastructure-error", Kind: "infrastructure", Summary: "workspace failed"}},
			}
			fake := &service{report: result, err: errors.New("workspace failed")}
			var stdout, stderr bytes.Buffer
			var arguments []string
			if jsonOutput {
				arguments = []string{"--json"}
			}
			if exit := cli.Run(t.Context(), arguments, &stdout, &stderr, fake); exit != cli.ExitError {
				t.Fatalf("exit = %d", exit)
			}
			if jsonOutput {
				var rendered report.Report
				if err := json.Unmarshal(stdout.Bytes(), &rendered); err != nil || rendered.Verdict != report.VerdictError {
					t.Fatalf("JSON ERROR report = %+v, %v\n%s", rendered, err, stdout.Bytes())
				}
			} else if !strings.HasPrefix(stdout.String(), "ERROR standard-v1") {
				t.Fatalf("line ERROR report = %q", stdout.String())
			}
			if got, want := stderr.String(), "goatest: workspace failed\n"; got != want {
				t.Fatalf("stderr = %q, want %q", got, want)
			}
		})
	}
}
