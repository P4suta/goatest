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

type verifiedRun struct {
	report report.Report
	events []trace.Event
}

func verifyRecording(t *testing.T, service app.Service) verifiedRun {
	t.Helper()
	return verifyRecordingWithExit(t, service, cli.ExitAssured)
}

func verifyRecordingWithExit(t *testing.T, service app.Service, want int) verifiedRun {
	t.Helper()
	directory := filepath.Join(t.TempDir(), "trace")
	var stdout, stderr bytes.Buffer
	exit := cli.Run(t.Context(), []string{"verify", "--json", "--trace=" + directory}, &stdout, &stderr, service)
	if exit != want {
		t.Fatalf("verify exit = %d, want %d\nstdout: %s\nstderr: %s", exit, want, stdout.String(), stderr.String())
	}
	var result report.Report
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	return verifiedRun{report: result, events: readTrace(t, traceRun(t, directory))}
}

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

func mutantStatuses(result report.Report) map[string]report.MutantStatus {
	statuses := make(map[string]report.MutantStatus, len(result.Mutants))
	for _, mutant := range result.Mutants {
		statuses[mutant.ID] = mutant.Status
	}
	return statuses
}

func executedMutants(events []trace.Event) map[string]bool {
	executed := make(map[string]bool)
	for _, event := range traceOfType(events, trace.TypeMutantExec) {
		executed[event.Mutant.ID] = true
	}
	return executed
}

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

func TestRepositoryReadObservationWidensOnlyTheMutantsEstablishedByTheReader(t *testing.T) {
	t.Parallel()
	repository := testkit.NewRepo(t).BoundaryFixture().
		File("reader/reader.go", repositoryReaderSource).
		File("reader/repository_access.go", actualRepositoryReaderSource).
		File("reader/reader_test.go", repositoryReaderTestSource).
		File("docs/notes.md", "first\n").Git()
	service := app.Service{
		Root: repository.Root(), GoBinary: testkit.GoBinary(t), TempDirectory: t.TempDir(),
		Environment: os.Environ(),
	}
	readerPackage := testkit.BoundaryModule + "/reader"

	first := verifyRecording(t, service)
	if first.report.Verdict != report.VerdictAssured || len(reusedMutants(first.report)) != 0 {
		t.Fatalf("first run = %+v", first.report.Accounting.Mutants)
	}
	if len(mutantsOfPackage(first.report, readerPackage)) == 0 {
		t.Fatal("the reading package contributed no mutant to reuse evidence about")
	}
	var narrowMutants, wholeTreeMutants []string
	for _, mutant := range first.report.Mutants {
		if mutant.Package != readerPackage {
			continue
		}
		switch filepath.ToSlash(mutant.Path) {
		case "reader/reader.go":
			narrowMutants = append(narrowMutants, mutant.ID)
		case "reader/repository_access.go":
			wholeTreeMutants = append(wholeTreeMutants, mutant.ID)
		}
	}
	if len(narrowMutants) == 0 || len(wholeTreeMutants) == 0 {
		t.Fatalf("reader fixture mutants = narrow %v, whole-tree %v", narrowMutants, wholeTreeMutants)
	}

	repository.File("docs/notes.md", "second\n")
	second := verifyRecording(t, service)
	reused := reusedMutants(second.report)
	if len(reused) == 0 {
		t.Fatalf("the second run reused nothing: %+v", second.report.Accounting.Mutants)
	}
	executed := executedMutants(second.events)
	for _, mutant := range narrowMutants {
		if !slices.Contains(reused, mutant) || executed[mutant] {
			t.Errorf("narrow mutant %s = reused %t, executed %t", mutant, slices.Contains(reused, mutant), executed[mutant])
		}
	}
	for _, mutant := range wholeTreeMutants {
		if slices.Contains(reused, mutant) || !executed[mutant] {
			t.Errorf("whole-tree mutant %s = reused %t, executed %t", mutant, slices.Contains(reused, mutant), executed[mutant])
		}
	}
	if !slices.Equal(reused, reusedRoutes(second.events)) {
		t.Fatalf("report reused %v, recording reused %v", reused, reusedRoutes(second.events))
	}
	if second.report.Verdict != first.report.Verdict ||
		!maps.Equal(mutantStatuses(second.report), mutantStatuses(first.report)) {
		t.Fatalf("reuse changed the verdict: %+v against %+v", second.report, first.report)
	}
}

func mutantsOfPackage(result report.Report, path string) []string {
	var mutants []string
	for _, mutant := range result.Mutants {
		if mutant.Package == path {
			mutants = append(mutants, mutant.ID)
		}
	}
	slices.Sort(mutants)
	return mutants
}

const repositoryReaderSource = `package reader

func Threshold(value int) int {
	if value < 4 {
		return value
	}
	return 3
}
`

const actualRepositoryReaderSource = `package reader

import "os"

func DirectoryEntryCount() int {
	directory, _ := os.Getwd()
	entries, _ := os.ReadDir(directory)
	return len(entries)
}
`

const repositoryReaderTestSource = `package reader

import (
	"testing"
)

func TestThreshold(t *testing.T) {
	for _, value := range []int{1, 4} {
		want := value
		if value >= 4 {
			want = 3
		}
		if got := Threshold(value); got != want {
			t.Fatalf("Threshold(%d) = %d, want %d", value, got, want)
		}
	}
}

func TestThresholdAtZero(t *testing.T) {
	if got := Threshold(0); got != 0 {
		t.Fatalf("Threshold(0) = %d, want 0", got)
	}
}

func TestTheDirectoryIsReadable(t *testing.T) {
	count := DirectoryEntryCount()
	if count != 3 {
		t.Fatalf("directory entries = %d, want 3", count)
	}
}
`

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

func TestBoundaryAtZero(t *testing.T) {
	if got := Boundary(0); got != 0 {
		t.Fatalf("Boundary(0) = %d, want 0", got)
	}
}
`

func TestASecondVerifyReusesEveryMutantAndRunsNoMutantExecution(t *testing.T) {
	t.Parallel()
	repository := testkit.NewRepo(t).BoundaryFixture().File("docs/notes.md", "first\n").Git()
	service := app.Service{
		Root: repository.Root(), GoBinary: testkit.GoBinary(t), TempDirectory: t.TempDir(),
		Environment: os.Environ(),
	}

	first := verifyRecording(t, service)
	if len(executedMutants(first.events)) == 0 {
		t.Fatalf("a cold run executed nothing: %+v", first.report.Accounting.Mutants)
	}

	repository.File("docs/notes.md", "second\n")
	second := verifyRecording(t, service)
	if executed := executedMutants(second.events); len(executed) != 0 {
		t.Errorf("the second run executed %d mutants, want none", len(executed))
	}
	for _, event := range traceOfType(second.events, trace.TypeRoute) {
		if !event.Route.Reused || !slices.Equal(event.Route.Plan, []string{"reused"}) {
			t.Errorf("route = %+v, want a reused route with the reuse as its plan", event.Route)
		}
	}
	counted := second.report.Accounting.Mutants
	if counted.ReusedKilled+counted.ReusedSurvived != counted.Executed || counted.Executed == 0 {
		t.Errorf("accounting = %+v, want every execution reused", counted)
	}
	if !slices.Equal(reusedMutants(second.report), reusedRoutes(second.events)) {
		t.Errorf("report reused %v, recording reused %v",
			reusedMutants(second.report), reusedRoutes(second.events))
	}
	if second.report.Verdict != first.report.Verdict ||
		!maps.Equal(mutantStatuses(second.report), mutantStatuses(first.report)) ||
		len(second.report.Findings) != len(first.report.Findings) {
		t.Fatalf("reuse changed the verdict: %+v against %+v", second.report, first.report)
	}
}

func TestChangingASourceFileForcesItsMutantsToRunAgain(t *testing.T) {
	t.Parallel()
	repository := testkit.NewRepo(t).BoundaryFixture().
		File("other/other.go", otherSource).
		File("other/other_test.go", otherTestSource).
		File("docs/notes.md", "first\n").Git()
	service := app.Service{
		Root: repository.Root(), GoBinary: testkit.GoBinary(t), TempDirectory: t.TempDir(),
		Environment: os.Environ(),
	}
	otherPackage := testkit.BoundaryModule + "/other"

	first := verifyRecording(t, service)
	if len(mutantsOfPackage(first.report, otherPackage)) == 0 {
		t.Fatal("the untouched package contributed no mutant")
	}

	repository.File("boundary.go", changedBoundarySource)
	second := verifyRecording(t, service)
	executed := executedMutants(second.events)
	for _, mutant := range second.report.Mutants {
		if mutant.Status == report.MutantOutOfScope || mutant.Status == report.MutantCompileRejected {
			continue
		}
		if mutant.Package == otherPackage {
			if !mutant.Reused {
				t.Errorf("mutant %s of the untouched package was executed again", mutant.ID)
			}
			continue
		}
		if mutant.Reused || !executed[mutant.ID] {
			t.Errorf("mutant %s of the changed file was not executed: %+v", mutant.ID, mutant)
		}
	}
}

func TestChangingATestFileForcesTheSurvivorsThatTestReachesToRunAgain(t *testing.T) {
	t.Parallel()
	repository := testkit.NewRepo(t).BoundaryFixture().
		File("unsure/unsure.go", survivingSource).
		File("unsure/unsure_test.go", survivingTestSource).
		File("docs/notes.md", "first\n").Git()
	service := app.Service{
		Root: repository.Root(), GoBinary: testkit.GoBinary(t), TempDirectory: t.TempDir(),
		Environment: os.Environ(),
	}
	unsurePackage := testkit.BoundaryModule + "/unsure"

	first := verifyRecordingWithExit(t, service, cli.ExitInsufficient)
	if first.report.Accounting.Mutants.Survived == 0 {
		t.Fatalf("the fixture left no survivor: %+v", first.report.Accounting.Mutants)
	}

	repository.File("docs/notes.md", "second\n")
	second := verifyRecordingWithExit(t, service, cli.ExitInsufficient)
	if second.report.Accounting.Mutants.ReusedSurvived == 0 {
		t.Fatalf("the second run reused no survivor: %+v", second.report.Accounting.Mutants)
	}
	reusedSurvivors := survivorsOfPackage(second.report, unsurePackage)
	if len(reusedSurvivors) == 0 {
		t.Fatalf("no survivor of %s was reused: %+v", unsurePackage, second.report.Mutants)
	}

	repository.File("unsure/unsure_test.go", changedSurvivingTestSource)
	third := verifyRecordingWithExit(t, service, cli.ExitInsufficient)
	executed := executedMutants(third.events)
	for _, mutant := range reusedSurvivors {
		if !executed[mutant] {
			t.Errorf("survivor %s was not executed after its test changed", mutant)
		}
	}
	for _, mutant := range third.report.Mutants {
		if mutant.Package == unsurePackage && mutant.Reused {
			t.Errorf("mutant %s kept a verdict its test no longer supports", mutant.ID)
		}
	}
	if third.report.Accounting.Mutants.ReusedKilled == 0 {
		t.Fatalf("a changed test in one package discarded another package's kills: %+v",
			third.report.Accounting.Mutants)
	}
}

func survivorsOfPackage(result report.Report, path string) []string {
	var mutants []string
	for _, mutant := range result.Mutants {
		if mutant.Package == path && mutant.Reused && mutant.Status == report.MutantSurvived {
			mutants = append(mutants, mutant.ID)
		}
	}
	slices.Sort(mutants)
	return mutants
}

const changedBoundarySource = `package assured

func Boundary(value int) int {
	if value < 10 {
		return value
	}
	return 9
}

`

const otherSource = `package other

func Ceiling(value int) int {
	if value < 4 {
		return value
	}
	return 3
}
`

const otherTestSource = `package other

import "testing"

func TestCeiling(t *testing.T) {
	for _, value := range []int{1, 4} {
		want := value
		if value >= 4 {
			want = 3
		}
		if got := Ceiling(value); got != want {
			t.Fatalf("Ceiling(%d) = %d, want %d", value, got, want)
		}
	}
}

func TestCeilingAtZero(t *testing.T) {
	if got := Ceiling(0); got != 0 {
		t.Fatalf("Ceiling(0) = %d, want 0", got)
	}
}
`

const survivingSource = `package unsure

func Doubled(value int) bool {
	return value*2 > 0
}
`

const survivingTestSource = `package unsure

import "testing"

func TestDoubled(t *testing.T) {
	if !Doubled(4) {
		t.Fatal("Doubled(4) = false, want true")
	}
	if Doubled(-4) {
		t.Fatal("Doubled(-4) = true, want false")
	}
}
`

const changedSurvivingTestSource = `package unsure

import "testing"

func TestDoubled(t *testing.T) {
	if !Doubled(4) {
		t.Fatal("Doubled(4) = false, want true")
	}
	if Doubled(-4) {
		t.Fatal("Doubled(-4) = true, want false")
	}
	if Doubled(-6) {
		t.Fatal("Doubled(-6) = true, want false")
	}
}
`
