// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"testing"

	"github.com/P4suta/goatest/internal/report"
	"github.com/P4suta/goatest/internal/testkit"
)

// sampleReports are the two reports the golden comparison is rendered from.
// They are built from the report types rather than written as JSON, so a
// change to the report contract reaches this fixture through the compiler.
//
// The pair is the shape of the change this tool exists to audit: the same
// verdict, a batch of surviving mutants that became unreached ones, an
// inventory the two runs do not fully share, and the one mutant that stopped
// being killed — which is the regression a routing change must never cause.
func sampleReports() (report.Report, report.Report) {
	before := report.Report{
		Schema:  report.SchemaV1,
		RunID:   "20260101T000000Z-1000",
		Verdict: report.VerdictInsufficient,
		Timing:  report.Timing{DurationMS: 600000},
		Accounting: report.Accounting{
			Targets: report.CountAccounting{Discovered: 12, Selected: 10, Executed: 10, Excluded: 2},
			Mutants: report.MutantAccounting{
				Discovered: 8, Selected: 8, Executed: 7, Killed: 4, Survived: 3, Inconclusive: 0,
				CompileRejected: 1,
			},
			Race: report.CountAccounting{Discovered: 3, Selected: 3, Executed: 3},
		},
		Mutants: []report.MutantDisposition{
			mutantAt("m-01", report.MutantKilled, "internal/assure/plan.go", 12),
			mutantAt("m-02", report.MutantKilled, "internal/assure/plan.go", 40),
			mutantAt("m-03", report.MutantSurvived, "internal/assure/run.go", 7),
			mutantAt("m-04", report.MutantSurvived, "internal/assure/run.go", 91),
			mutantAt("m-05", report.MutantSurvived, "internal/report/report.go", 5),
			mutantAt("m-06", report.MutantCompileRejected, "internal/report/report.go", 33),
			mutantAt("m-07", report.MutantKilled, "internal/trace/sink.go", 18),
		},
		Findings: []report.Finding{
			findingOf("surviving-mutant", "m-03"),
			findingOf("surviving-mutant", "m-04"),
			findingOf("surviving-mutant", "m-05"),
		},
	}
	after := report.Report{
		Schema:  report.SchemaV1,
		RunID:   "20260102T000000Z-2000",
		Verdict: report.VerdictInsufficient,
		Timing:  report.Timing{DurationMS: 420000},
		Accounting: report.Accounting{
			Targets: report.CountAccounting{Discovered: 12, Selected: 10, Executed: 10, Excluded: 2},
			Mutants: report.MutantAccounting{
				Discovered: 8, Selected: 8, Executed: 7, Killed: 3, Survived: 3, Inconclusive: 1,
				CompileRejected: 1,
			},
			Race: report.CountAccounting{Discovered: 3, Selected: 3, Executed: 3},
		},
		Mutants: []report.MutantDisposition{
			mutantAt("m-01", report.MutantKilled, "internal/assure/plan.go", 12),
			mutantAt("m-02", report.MutantInconclusive, "internal/assure/plan.go", 40),
			mutantAt("m-03", report.MutantSurvived, "internal/assure/run.go", 7),
			mutantAt("m-04", report.MutantSurvived, "internal/assure/run.go", 91),
			mutantAt("m-05", report.MutantSurvived, "internal/report/report.go", 5),
			mutantAt("m-06", report.MutantCompileRejected, "internal/report/report.go", 33),
			mutantAt("m-08", report.MutantKilled, "internal/trace/sink.go", 24),
		},
		Findings: []report.Finding{
			findingOf("surviving-mutant", "m-03"),
			findingOf("unreached-mutant", "m-04"),
			findingOf("unreached-mutant", "m-05"),
		},
	}
	return before, after
}

// renderSample renders the sample comparison under fixed paths, so the golden
// records the comparison rather than the temporary directory it was read from.
func renderSample() string {
	before, after := sampleReports()
	return renderComparison("testdata/before.json", "testdata/after.json", compare(before, after))
}

func TestRenderComparisonBreaksDownTwoReports(t *testing.T) {
	t.Parallel()
	testkit.Golden(t, "sample-diff.txt", []byte(renderSample()))
}

func TestRenderComparisonDependsOnTheReportsAlone(t *testing.T) {
	t.Parallel()
	first, second := renderSample(), renderSample()
	if first != second {
		t.Error("two renderings of one pair of reports differ; the comparison is not deterministic")
	}
}
