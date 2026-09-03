// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package app_test

import (
	"bytes"
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/P4suta/goatest/internal/app"
	"github.com/P4suta/goatest/internal/cli"
	"github.com/P4suta/goatest/internal/report"
	"github.com/P4suta/goatest/internal/testkit"
	"github.com/P4suta/goatest/internal/trace"
)

// verifiedRun is one recorded verify of a real repository: the report it
// printed and the events it recorded.
type verifiedRun struct {
	report report.Report
	events []trace.Event
}

// verifyRecording runs verify against a real toolchain and reads back both the
// report and the recording, which together are the whole claim a run makes.
func verifyRecording(t *testing.T, service app.Service) verifiedRun {
	t.Helper()
	directory := filepath.Join(t.TempDir(), "trace")
	var stdout, stderr bytes.Buffer
	exit := cli.Run(t.Context(), []string{"verify", "--json", "--trace=" + directory}, &stdout, &stderr, service)
	if exit != cli.ExitAssured {
		t.Fatalf("verify exit = %d\nstdout: %s\nstderr: %s", exit, stdout.String(), stderr.String())
	}
	var result report.Report
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	return verifiedRun{report: result, events: readTrace(t, traceRun(t, directory))}
}

// reusedMutants is the set of mutants a run resolved from recorded evidence,
// read off the report.
func reusedMutants(result report.Report) []string {
	var reused []string
	for _, mutant := range result.Mutants {
		if mutant.Reused {
			reused = append(reused, mutant.ID)
		}
	}
	slices.Sort(reused)
	return reused
}

// reusedRoutes is the same set read off the recording, which is where an
// auditor reads it.
func reusedRoutes(events []trace.Event) []string {
	var reused []string
	for _, event := range traceOfType(events, trace.TypeRoute) {
		if event.Route.Reused {
			reused = append(reused, event.Route.MutantID)
		}
	}
	slices.Sort(reused)
	return reused
}

// mutantStatuses is what the two runs must agree on, whatever either of them
// executed.
func mutantStatuses(result report.Report) map[string]report.MutantStatus {
	statuses := make(map[string]report.MutantStatus, len(result.Mutants))
	for _, mutant := range result.Mutants {
		statuses[mutant.ID] = mutant.Status
	}
	return statuses
}

// executedMutants names every mutant a recording shows an execution of.
func executedMutants(events []trace.Event) map[string]bool {
	executed := make(map[string]bool)
	for _, event := range traceOfType(events, trace.TypeMutantExec) {
		executed[event.Mutant.ID] = true
	}
	return executed
}

// TestASecondVerifyReusesTheKillsItRecordedUntilTheKillingTestChanges is the
// whole of P4.2 against a real toolchain: a run records the kills it confirms,
// the next run over an unchanged test binary resolves them without executing
// anything and reaches the same verdict, and a run whose killing test changed
// executes them again, because the test that killed them is no longer the test
// the record is about.
func TestASecondVerifyReusesTheKillsItRecordedUntilTheKillingTestChanges(t *testing.T) {
	t.Parallel()
	repository := testkit.NewRepo(t).BoundaryFixture().File("docs/notes.md", "first\n").Git()
	service := app.Service{
		Root: repository.Root(), GoBinary: testkit.GoBinary(t), TempDirectory: t.TempDir(),
		Environment: os.Environ(),
	}

	first := verifyRecording(t, service)
	if first.report.Verdict != report.VerdictAssured || first.report.Accounting.Mutants.Killed == 0 {
		t.Fatalf("first run = %+v", first.report.Accounting.Mutants)
	}
	if len(reusedMutants(first.report)) != 0 || first.report.Accounting.Mutants.ReusedKilled != 0 {
		t.Fatalf("a cold run reused %v", reusedMutants(first.report))
	}
	store := repository.Path(".goatest/cache/mutation-evidence-v1.json")
	if info, err := os.Stat(store); err != nil || info.Size() == 0 {
		t.Fatalf("mutation evidence store %s = (%v, %v)", store, info, err)
	}

	// Only a documentation file changes: the snapshot the run is keyed on is
	// new, and no input of any test binary is.
	repository.File("docs/notes.md", "second\n")
	second := verifyRecording(t, service)
	reused := reusedMutants(second.report)
	if len(reused) == 0 {
		t.Fatalf("the second run reused nothing: %+v", second.report.Accounting.Mutants)
	}
	if !slices.Equal(reused, reusedRoutes(second.events)) {
		t.Fatalf("report reused %v, recording reused %v", reused, reusedRoutes(second.events))
	}
	if second.report.Accounting.Mutants.ReusedKilled != len(reused) {
		t.Fatalf("accounting = %+v, want %d reused kills", second.report.Accounting.Mutants, len(reused))
	}
	if second.report.Verdict != first.report.Verdict ||
		!maps.Equal(mutantStatuses(second.report), mutantStatuses(first.report)) ||
		len(second.report.Findings) != len(first.report.Findings) {
		t.Fatalf("reuse changed the verdict: %+v against %+v", second.report, first.report)
	}
	executed := executedMutants(second.events)
	for _, mutant := range reused {
		if executed[mutant] {
			t.Errorf("mutant %s was reused and executed", mutant)
		}
	}
	for _, mutant := range second.report.Mutants {
		if mutant.Reused != (mutant.Provenance != "") {
			t.Errorf("mutant %s reuse and provenance disagree: %+v", mutant.ID, mutant)
		}
	}

	// The test that killed them changes. Every mutant keeps its identity,
	// because the file it mutates is untouched, and every record about it is
	// about a test binary that no longer exists.
	repository.File("boundary_test.go", changedBoundaryTestSource)
	third := verifyRecording(t, service)
	if third.report.Verdict != report.VerdictAssured {
		t.Fatalf("third run = %+v", third.report)
	}
	if got := reusedMutants(third.report); len(got) != 0 || third.report.Accounting.Mutants.ReusedKilled != 0 {
		t.Fatalf("a changed killing test still reused %v", got)
	}
	if got := reusedRoutes(third.events); len(got) != 0 {
		t.Fatalf("a changed killing test recorded reused routes %v", got)
	}
	executedAgain := executedMutants(third.events)
	for _, mutant := range reused {
		if !executedAgain[mutant] {
			t.Errorf("mutant %s was neither reused nor executed", mutant)
		}
	}
}

// changedBoundaryTestSource is the fixture's test with one more case, so that
// the test binary is a different one while still killing what it killed.
const changedBoundaryTestSource = `package assured

import "testing"

func TestBoundary(t *testing.T) {
	for _, value := range []int{5, 9, 10, 11} {
		want := value
		if value >= 10 {
			want = 9
		}
		if got := Boundary(value); got != want {
			t.Fatalf("Boundary(%d) = %d, want %d", value, got, want)
		}
	}
}

// TestBoundaryAtZero exercises the guarded return at the value a return-zero
// mutation puts there, so the probe pass measures one target that makes no
// mutated site differ and routing has a target to discharge.
func TestBoundaryAtZero(t *testing.T) {
	if got := Boundary(0); got != 0 {
		t.Fatalf("Boundary(0) = %d, want 0", got)
	}
}
`
