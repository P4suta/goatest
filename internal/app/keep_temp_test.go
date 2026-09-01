// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package app_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/P4suta/goatest/internal/app"
	"github.com/P4suta/goatest/internal/assure"
	"github.com/P4suta/goatest/internal/cli"
	"github.com/P4suta/goatest/internal/report"
)

// A request to keep the temporary directories of a run reaches the run itself,
// and the paths it then keeps reach the developer: the recording names each of
// them, and the bundle of a run that failed lists them among the paths it left
// behind, which is the file a developer reads before going looking on the disk.
func TestKeepTempReachesTheRunAndWhatItKeptReachesTheBundle(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	scratch := filepath.Join(t.TempDir(), "goatest-baseline-kept")
	sentinel := errors.New("mutation workspace failed")
	var kept bool
	service := app.Service{
		Root: root,
		Run: func(_ context.Context, options assure.Options) (report.Report, error) {
			kept = options.KeepTemp
			options.Trace.Artifact("baseline-scratch", scratch)
			return report.Report{}, sentinel
		},
	}
	result, err := service.Execute(t.Context(), cli.CommandVerify, cli.Request{KeepTemp: true}, "")
	if !errors.Is(err, sentinel) || result.Verdict != report.VerdictError {
		t.Fatalf("verify = %+v, %v", result, err)
	}
	if !kept {
		t.Fatal("the run was not asked to keep its temporary directories")
	}
	preserved := bundleFile(t, diagnosticsBundle(t, root), "preserved-paths.txt")
	if !strings.Contains(preserved, scratch) {
		t.Fatalf("preserved-paths.txt = %q, want the scratch directory the run kept", preserved)
	}
}

// A run that was not asked to keep anything is not told to.
func TestARunKeepsNothingUnlessItWasAsked(t *testing.T) {
	t.Parallel()
	var kept bool
	service := app.Service{
		Root: t.TempDir(),
		Run: func(_ context.Context, options assure.Options) (report.Report, error) {
			kept = options.KeepTemp
			return report.Report{Schema: report.SchemaV1, Verdict: report.VerdictAssured, Contract: "standard-v1"}, nil
		},
	}
	if _, err := service.Execute(t.Context(), cli.CommandVerify, cli.Request{}, ""); err != nil {
		t.Fatal(err)
	}
	if kept {
		t.Fatal("an unasked run was told to keep its temporary directories")
	}
}
