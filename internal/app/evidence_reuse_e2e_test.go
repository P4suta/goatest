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
	return verifyRecordingWithExit(t, service, cli.ExitAssured)
}

// verifyRecordingWithExit is verifyRecording against a fixture whose verdict is
// not ASSURED, which is what a fixture with a surviving mutant has.
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

// TestRepositoryReadObservationWidensOnlyTheMutantsEstablishedByTheReader
// pins the runtime refinement against a real toolchain. One package has both
// ordinary tests and a test that reads its repository directory. A change
// outside every narrow closure preserves evidence established by the ordinary
// targets and invalidates evidence established by the actual reader.
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

	// Only a documentation file changes. Nothing the boundary package's test
	// binary reads has changed. Within the mixed reader package, only the
	// target that actually listed the repository needs the whole-tree key.
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

// mutantsOfPackage names the mutants one package of the fixture contributed.
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

// repositoryReaderSource is a second guarded behaviour, in a package of its
// own, so that the module has mutants on both sides of the reading rule.
const repositoryReaderSource = `package reader

// Threshold clamps value to the largest accepted input, the single guarded
// behaviour this package's tests and mutants argue about.
func Threshold(value int) int {
	if value < 4 {
		return value
	}
	return 3
}
`

// actualRepositoryReaderSource contributes a distinct mutant whose killing
// target really does consult the repository. Keeping it in its own file makes
// the two evidence modes directly observable in the report.
const actualRepositoryReaderSource = `package reader

import "os"

func DirectoryEntryCount() int {
	directory, _ := os.Getwd()
	entries, _ := os.ReadDir(directory)
	return len(entries)
}
`

// repositoryReaderTestSource kills every mutant of that behaviour and lists a
// directory it computes rather than a file it names, which is what makes the
// whole tree an input of this test binary.
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

// TestASecondVerifyReusesEveryMutantAndRunsNoMutantExecution is the whole of
// P4.3 against a real toolchain. A run records what it established about every
// mutant it could name a claim for; the next run over a tree in which nothing
// any test binary reads has changed resolves all of them from those records,
// executes no mutant at all, and reaches byte for byte the same verdict.
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

// TestChangingASourceFileForcesItsMutantsToRunAgain pins the granularity of a
// record. A mutant is named by the content of the file it edits, so changing
// that file leaves every record about it about mutants that no longer exist,
// while a package the change does not reach keeps every verdict it had.
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

// TestChangingATestFileForcesTheSurvivorsThatTestReachesToRunAgain pins the
// universal claim from the side that can cost assurance. A survived verdict is
// a claim about the tests that ran; a test binary that changed is not one of
// them, so every survivor it reaches runs again, while the kills recorded in a
// package the change does not reach stand.
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

	// Nothing any test binary reads changes, so the survivors are resolved from
	// the records the first run left.
	repository.File("docs/notes.md", "second\n")
	second := verifyRecordingWithExit(t, service, cli.ExitInsufficient)
	if second.report.Accounting.Mutants.ReusedSurvived == 0 {
		t.Fatalf("the second run reused no survivor: %+v", second.report.Accounting.Mutants)
	}
	reusedSurvivors := survivorsOfPackage(second.report, unsurePackage)
	if len(reusedSurvivors) == 0 {
		t.Fatalf("no survivor of %s was reused: %+v", unsurePackage, second.report.Mutants)
	}

	// The test that exhausted them changes. What it does now is not what the
	// record is a claim about, so every survivor it reaches runs again; the
	// kills of the package it is not in stand.
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

// survivorsOfPackage names the mutants of one package this run reused a
// survived verdict for.
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

// changedBoundarySource is the fixture's guarded behaviour with a comment
// added: every mutant of the file is named by the file's content, so all of
// them are new mutants while the behaviour and the verdict are unchanged.
const changedBoundarySource = `package assured

// Boundary clamps value to the largest accepted input, the single guarded
// behaviour this fixture's test and mutants argue about. This sentence is here
// to change the file without changing what it does.
func Boundary(value int) int {
	if value < 10 {
		return value
	}
	return 9
}
`

// otherSource is a second guarded behaviour in a package of its own, so that a
// change to one package can be shown not to reach another.
const otherSource = `package other

// Ceiling clamps value to the largest accepted input of this package.
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

// survivingSource computes a value its test does observe and then asks a
// question the mutated value cannot change the answer to for the inputs the
// test gives it. The mutant is therefore infected and reached, and still
// survives, which is the survivor a record can be about.
const survivingSource = `package unsure

// Doubled reports whether twice the value is above zero.
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

// changedSurvivingTestSource is that test with one more case, so that the test
// binary the survivors were exhausted by is not the one running now.
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
