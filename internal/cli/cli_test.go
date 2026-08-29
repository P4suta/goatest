// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package cli_test

import (
	"bytes"
	"context"
	"errors"
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
		for _, expected := range []string{"--changed[=REF]", "--contract=standard-v1|deep-v1", "init", "explain ID", "replay ID", "accept ID", "report"} {
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
	exit := cli.Run(t.Context(), []string{"--changed=origin/main", "--contract=deep-v1", "--no-apply", "--json", "--no-tui"}, &stdout, &stderr, fake)
	if exit != cli.ExitAssured || fake.command != cli.CommandVerify {
		t.Fatalf("exit/command = %d/%s", exit, fake.command)
	}
	if !fake.request.Changed || fake.request.ChangedRef != "origin/main" || fake.request.Contract != "deep-v1" || !fake.request.NoApply || !fake.request.JSON || !fake.request.NoTUI {
		t.Errorf("request = %+v", fake.request)
	}
	if stdout.Len() == 0 || stderr.Len() != 0 {
		t.Errorf("stdout/stderr = %q / %q", stdout.String(), stderr.String())
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
		{[]string{"accept", "finding-c"}, cli.CommandAccept, "finding-c"},
		{[]string{"report"}, cli.CommandReport, ""},
	} {
		fake := &service{report: report.Report{Schema: report.SchemaV1, Verdict: report.VerdictAssured}}
		exit := cli.Run(t.Context(), test.args, &bytes.Buffer{}, &bytes.Buffer{}, fake)
		if exit != cli.ExitAssured || fake.command != test.command || fake.id != test.id {
			t.Errorf("%v => %d/%s/%q", test.args, exit, fake.command, fake.id)
		}
	}
	for _, args := range [][]string{{"explain"}, {"accept"}, {"unknown"}, {"--contract=bad"}} {
		var stderr bytes.Buffer
		if exit := cli.Run(t.Context(), args, &bytes.Buffer{}, &stderr, &service{}); exit != cli.ExitError || stderr.Len() == 0 {
			t.Errorf("%v => exit %d stderr %q", args, exit, stderr.String())
		}
	}
}

func TestVerdictsMapToStableExitCodes(t *testing.T) {
	for verdict, want := range map[report.Verdict]int{
		report.VerdictAssured:      cli.ExitAssured,
		report.VerdictDefect:       cli.ExitDefect,
		report.VerdictInsufficient: cli.ExitInsufficient,
		report.VerdictError:        cli.ExitError,
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
