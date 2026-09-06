// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package assure_test

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/P4suta/goatest/internal/assure"
	"github.com/P4suta/goatest/internal/filemode"
	"github.com/P4suta/goatest/internal/provider"
	"github.com/P4suta/goatest/internal/report"
	"github.com/P4suta/goatest/internal/resource"
	"github.com/P4suta/goatest/internal/testkit"
)

const (
	resourceRequestDecodeExitCode  = 40
	resourceResponseEncodeExitCode = 41
	resourceStopDecodeExitCode     = 42
	fixtureLogOpenExitCode         = 43
	generationRequestExitCode      = 44
	generationContentExitCode      = 45
	generationResponseExitCode     = 46
	endToEndMutationJobs           = 1
)

func TestRunResourceProviderHelper(t *testing.T) {
	if !testkit.HelperEnabled("GOATEST_ASSURE_RESOURCE_HELPER") {
		return
	}
	decoder := json.NewDecoder(os.Stdin)
	encoder := json.NewEncoder(os.Stdout)
	var start resource.Request
	if err := decoder.Decode(&start); err != nil {
		os.Exit(resourceRequestDecodeExitCode)
	}
	appendFixtureLog(os.Getenv("GOATEST_ASSURE_RESOURCE_LOG"), start.Action)
	if err := encoder.Encode(resource.Response{
		Version: resource.ProtocolVersion, Status: "ready", Instance: "postgres-e2e",
		Environment: map[string]string{"DATABASE_URL": "postgres://managed/test"},
	}); err != nil {
		os.Exit(resourceResponseEncodeExitCode)
	}
	var stop resource.Request
	if err := decoder.Decode(&stop); err != nil {
		os.Exit(resourceStopDecodeExitCode)
	}
	appendFixtureLog(os.Getenv("GOATEST_ASSURE_RESOURCE_LOG"), stop.Action)
	_ = encoder.Encode(resource.Response{Version: resource.ProtocolVersion, Status: "stopped", Instance: "postgres-e2e"})
}

func TestRunGenerationProviderHelper(t *testing.T) {
	if !testkit.HelperEnabled("GOATEST_ASSURE_GENERATION_HELPER") {
		return
	}
	var request provider.Request
	if err := json.NewDecoder(os.Stdin).Decode(&request); err != nil {
		os.Exit(generationRequestExitCode)
	}
	content, err := os.ReadFile(os.Getenv("GOATEST_ASSURE_GENERATION_CONTENT"))
	if err != nil {
		os.Exit(generationContentExitCode)
	}
	response := provider.Response{
		Version: provider.ProtocolVersion, FindingID: request.Finding.ID,
		Candidates: []provider.Candidate{{
			Kind: "patch", Path: "boundary_test.go",
			PreimageSHA256: os.Getenv("GOATEST_ASSURE_GENERATION_PREIMAGE"), Content: content,
		}},
	}
	if err := json.NewEncoder(os.Stdout).Encode(response); err != nil {
		os.Exit(generationResponseExitCode)
	}
	os.Exit(0)
}

func TestRunAssuresRepositoryAndWarmCacheStartsNoTestOrMutant(t *testing.T) {
	t.Parallel()
	testkit.SerializeHeavy(t)
	repository := testkit.NewRepo(t).
		File("go.mod", crlfFixture("module fixture.example/assured\n\ngo 1.26.0\n")).
		File("boundary.go", crlfFixture(`package assured

func Boundary(value int) int {
	if value < 10 { return value }
	return 9
}
`)).
		File("boundary_test.go", crlfFixture(`package assured

import "testing"

func checkBoundary(t *testing.T, value, want int) {
	t.Helper()
	if got := Boundary(value); got != want { t.Fatalf("Boundary(%d) = %d, want %d", value, got, want) }
}

func TestBelowBoundary(t *testing.T) { checkBoundary(t, 5, 5) }
func TestAtBoundary(t *testing.T) { checkBoundary(t, 10, 9) }
func TestAboveBoundary(t *testing.T) { checkBoundary(t, 11, 9) }
`))
	options := assure.Options{
		Root: repository.Root(), Contract: "standard-v1", GoBinary: testkit.GoBinary(t),
		TempDirectory: t.TempDir(), MutationOperators: []string{"comparison"},
		MutationJobs: endToEndMutationJobs,
		Environment: append(os.Environ(),
			"STARSHIP_SESSION_KEY=first-shell", "__MISE_SESSION=first-shell"),
	}
	first, err := assure.Run(t.Context(), options)
	if err != nil {
		t.Fatal(err)
	}
	if first.Verdict != report.VerdictAssured || first.Snapshot == "" {
		t.Fatalf("first report = %+v", first)
	}

	var events []assure.Event
	options.Environment = append(os.Environ(),
		"STARSHIP_SESSION_KEY=second-shell", "__MISE_SESSION=second-shell")
	options.Progress = func(event assure.Event) { events = append(events, event) }
	second, err := assure.Run(t.Context(), options)
	if err != nil {
		t.Fatal(err)
	}
	if second.Verdict != report.VerdictAssured || second.Snapshot != first.Snapshot {
		t.Fatalf("second report = %+v", second)
	}
	if !testkit.HasEvent(events, "cache-hit") {
		t.Fatalf("warm events = %+v", events)
	}
	for _, event := range events {
		if event.Kind == "baseline-progress" || event.Kind == "mutation-target" {
			t.Fatalf("warm cache started child work: %+v", events)
		}
	}
}

func TestPlanEnumeratesTargetsAndMutantsWithoutRunningTestTargets(t *testing.T) {
	t.Parallel()
	testkit.SerializeHeavy(t)
	marker := filepath.Join(t.TempDir(), "test-binary-ran")
	repository := testkit.NewRepo(t).
		File("go.mod", crlfFixture("module fixture.example/plan\n\ngo 1.26.0\n")).
		File("value.go", crlfFixture("package plan\n\nfunc Value(v int) bool { return v < 10 }\n")).
		File("value_test.go", crlfFixture(fmt.Sprintf(`package plan

import (
	"os"
	"testing"
)

func init() {
	file, err := os.Create(%q)
	if err != nil { panic(err) }
	if err := file.Close(); err != nil { panic(err) }
}

func TestValue(t *testing.T) {
	t.Fatal("plan ran a test target")
}
`, marker)))
	planned, err := assure.Plan(t.Context(), assure.Options{
		Root: repository.Root(), Contract: "standard-v1", GoBinary: testkit.GoBinary(t), TempDirectory: t.TempDir(),
		MutationOperators: []string{"comparison"}, MutationJobs: endToEndMutationJobs,
	})
	if err != nil {
		t.Fatal(err)
	}
	if planned.Verdict != report.VerdictCompleted || planned.RunKind != report.RunOperation || planned.Scope.Resolved.Kind != "full" {
		t.Fatalf("plan = %+v", planned)
	}
	var targetCount, mutantCount, summaryCount int
	for _, item := range planned.Evidence {
		switch item.Kind {
		case "plan-target":
			targetCount++
		case "plan-mutant":
			mutantCount++
		case "plan":
			summaryCount++
		}
	}
	if targetCount != 1 || mutantCount == 0 || summaryCount != 1 {
		t.Fatalf("plan evidence = %+v", planned.Evidence)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("plan ran a test binary: %v", err)
	}
}

func TestRunReturnsDefectForRepeatableBaselineFailureBeforeExecutingMutants(t *testing.T) {
	t.Parallel()
	testkit.SerializeHeavy(t)
	repository := testkit.NewRepo(t).
		File("go.mod", crlfFixture("module fixture.example/defect\n\ngo 1.26.0\n")).
		File("value.go", crlfFixture("package defect\n\nfunc Value() int { return 1 }\n")).
		File("value_test.go", crlfFixture(`package defect

import "testing"

func TestValue(t *testing.T) {
	if Value() != 2 { t.Fatal("reproduced defect") }
}
`))
	var events []assure.Event
	result, err := assure.Run(t.Context(), assure.Options{
		Root: repository.Root(), Contract: "standard-v1", GoBinary: testkit.GoBinary(t), TempDirectory: t.TempDir(),
		MutationOperators: []string{"comparison"}, MutationJobs: endToEndMutationJobs,
		Progress: func(event assure.Event) { events = append(events, event) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Verdict != report.VerdictDefect || len(result.Findings) != 1 || result.Findings[0].Kind != "baseline-failure" {
		t.Fatalf("report = %+v", result)
	}
	if testkit.HasEvent(events, "mutation-target") {
		t.Fatalf("baseline defect execution boundary = %+v", events)
	}
}

func TestRunChangedInvalidatesOnlyImpactedTargetsAndBroadensForUnknownFiles(t *testing.T) {
	t.Parallel()
	testkit.SerializeHeavy(t)
	repository := testkit.NewRepo(t).
		File("go.mod", crlfFixture("module fixture.example/incremental\n\ngo 1.26.0\n")).
		File(".gitignore", crlfFixture(".goatest/\n")).
		File("a.go", crlfFixture("package incremental\n\nfunc A(v int) bool { return v < 10 }\n")).
		File("b.go", crlfFixture("package incremental\n\nfunc B(v int) bool { return v > 0 }\n")).
		File("values_test.go", crlfFixture(`package incremental

import "testing"

func TestA(t *testing.T) {
	for _, v := range []int{9, 10} { if got := A(v); got != (v < 10) { t.Fatalf("A(%d) = %t", v, got) } }
}
func TestB(t *testing.T) {
	for _, v := range []int{0, 1} { if got := B(v); got != (v > 0) { t.Fatalf("B(%d) = %t", v, got) } }
}
func TestShared(t *testing.T) {
	if !A(9) || !B(1) { t.Fatal("shared behavior changed") }
}
`)).
		Git()
	base := assure.Options{
		Root: repository.Root(), Contract: "standard-v1", GoBinary: testkit.GoBinary(t), TempDirectory: t.TempDir(),
		MutationOperators: []string{"comparison"}, MutationJobs: endToEndMutationJobs,
	}
	if result, err := assure.Run(t.Context(), base); err != nil || result.Verdict != report.VerdictAssured {
		t.Fatalf("initial run = %+v, %v", result, err)
	}
	repository.File("a.go", crlfFixture("package incremental\n\n\nfunc A(v int) bool { return v < 10 }\n"))
	var targeted []assure.Event
	base.Changed = true
	base.Progress = func(event assure.Event) { targeted = append(targeted, event) }
	if result, err := assure.Run(t.Context(), base); err != nil || result.Verdict != report.VerdictAssured {
		t.Fatalf("targeted run = %+v, %v", result, err)
	}
	if got, want := testkit.EventDetails(targeted, "baseline-progress"), []string{"0/2", "1/2", "2/2"}; !slices.Equal(got, want) {
		t.Fatalf("targeted baseline events = %v; all events=%+v", got, targeted)
	}

	repository.File("unknown.txt", crlfFixture("force safe fallback\n"))
	var broad []assure.Event
	base.Progress = func(event assure.Event) { broad = append(broad, event) }
	if _, err := assure.Run(t.Context(), base); err != nil {
		t.Fatal(err)
	}
	if got, want := testkit.EventDetails(broad, "baseline-progress"), []string{"0/3", "1/3", "2/3", "3/3"}; !slices.Equal(got, want) || !testkit.HasEvent(broad, "impact-broad") {
		t.Fatalf("broad baseline events = %v; all events=%+v", got, broad)
	}
}

func TestRunUsesFuzzSeedCorpusAsDeterministicMutationEvidence(t *testing.T) {
	t.Parallel()
	testkit.SerializeHeavy(t)
	repository := testkit.NewRepo(t).
		File("go.mod", crlfFixture("module fixture.example/repair\n\ngo 1.26.0\n")).
		File("boundary.go", crlfFixture(`package repair

func Boundary(value int) int {
	if value < 10 { return value }
	return 9
}
`)).
		File("boundary_test.go", crlfFixture(`package repair

import "testing"

func TestBoundaryWeak(t *testing.T) {
	if got := Boundary(5); got != 5 { t.Fatalf("got %d", got) }
}

func FuzzBoundary(f *testing.F) {
	f.Add([]byte{1})
	f.Fuzz(func(t *testing.T, input []byte) {
		value := 5
		if len(input) > 0 { value = 10 }
		want := value
		if value >= 10 { want = 9 }
		if got := Boundary(value); got != want { t.Fatalf("Boundary(%d) = %d, want %d", value, got, want) }
	})
}
`))
	result, err := assure.Run(t.Context(), assure.Options{
		Root: repository.Root(), Contract: "standard-v1", GoBinary: testkit.GoBinary(t), TempDirectory: t.TempDir(),
		MutationOperators: []string{"comparison"}, MutationJobs: endToEndMutationJobs,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Verdict != report.VerdictAssured || len(result.Repairs) != 0 {
		t.Fatalf("report = %+v", result)
	}
}

func TestRunValidatesAppliesGeneratedTestAndReverifiesFreshSnapshot(t *testing.T) {
	t.Parallel()
	testkit.SerializeHeavy(t)
	repository := testkit.NewRepo(t).
		File("go.mod", crlfFixture("module fixture.example/generated\n\ngo 1.26.0\n")).
		File("boundary.go", crlfFixture(`package generated

func Boundary(value int) int {
	if value < 10 { return value }
	return 9
}
`)).
		File("boundary_test.go", crlfFixture(`package generated

import "testing"

func TestBoundaryWeak(t *testing.T) {
	if got := Boundary(5); got != 5 { t.Fatalf("got %d", got) }
}
`))
	preimage, err := os.ReadFile(repository.Path("boundary_test.go"))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(preimage)
	generated := crlfFixture(`package generated

import "testing"

func TestBoundaryWeak(t *testing.T) {
	for _, value := range []int{5, 10} {
		want := value
		if value >= 10 { want = 9 }
		if got := Boundary(value); got != want { t.Fatalf("got %d, want %d", got, want) }
	}
}
`)
	generatedCalls := 0
	result, err := assure.Run(t.Context(), assure.Options{
		Root: repository.Root(), Contract: "standard-v1", GoBinary: testkit.GoBinary(t), TempDirectory: t.TempDir(),
		MutationOperators: []string{"comparison"}, MutationJobs: endToEndMutationJobs, Validator: stableValidator{},
		AllowedGenerationPaths: []string{"boundary_test.go"},
		Generate: func(_ context.Context, request provider.Request) (provider.Response, error) {
			generatedCalls++
			return provider.Response{
				Version: provider.ProtocolVersion, FindingID: request.Finding.ID,
				Candidates: []provider.Candidate{{
					Kind: "patch", Path: "boundary_test.go", PreimageSHA256: hex.EncodeToString(sum[:]), Content: []byte(generated),
				}},
			}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Verdict != report.VerdictAssured || generatedCalls != 1 || len(result.Repairs) != 1 || result.Repairs[0].Path != "boundary_test.go" {
		t.Fatalf("report=%+v calls=%d", result, generatedCalls)
	}
}

type stableValidator struct{}

func (stableValidator) OriginalPasses(context.Context, provider.Candidate) error        { return nil }
func (stableValidator) Kills(context.Context, report.Finding, provider.Candidate) error { return nil }
func (stableValidator) Suite(context.Context, provider.Candidate) error                 { return nil }

func TestRunCacheInvalidatesWhenLocalReplacementDependencyChanges(t *testing.T) {
	t.Parallel()
	testkit.SerializeHeavy(t)
	dependency := testkit.NewRepo(t).
		File("go.mod", crlfFixture("module fixture.example/dependency\n\ngo 1.26.0\n")).
		File("value.go", crlfFixture("package dependency\n\nfunc Value() int { return 1 }\n"))
	goMod := "module fixture.example/subject\n\ngo 1.26.0\n\nrequire fixture.example/dependency v0.0.0\nreplace fixture.example/dependency => " + filepath.ToSlash(dependency.Root()) + "\n"
	subject := testkit.NewRepo(t).
		File("go.mod", crlfFixture(goMod)).
		File("value.go", crlfFixture("package subject\n\nimport dependency \"fixture.example/dependency\"\n\nfunc Value() int { return dependency.Value() }\n")).
		File("value_test.go", crlfFixture("package subject\n\nimport \"testing\"\n\nfunc TestValue(t *testing.T) { if Value() != 1 { t.Fatal(Value()) } }\n"))
	options := assure.Options{
		Root: subject.Root(), Contract: "standard-v1", GoBinary: testkit.GoBinary(t), TempDirectory: t.TempDir(),
		MutationOperators: []string{"comparison"}, MutationJobs: endToEndMutationJobs,
	}
	first, err := assure.Run(t.Context(), options)
	if err != nil || first.Verdict != report.VerdictAssured {
		t.Fatalf("first = %+v, %v", first, err)
	}
	dependency.File("value.go", crlfFixture("package dependency\n\nfunc Value() int { return 2 }\n"))
	var events []assure.Event
	options.Progress = func(event assure.Event) { events = append(events, event) }
	second, err := assure.Run(t.Context(), options)
	if err != nil {
		t.Fatal(err)
	}
	if second.Verdict != report.VerdictDefect || testkit.HasEvent(events, "cache-hit") {
		t.Fatalf("dependency change reused stale evidence: report=%+v events=%+v", second, events)
	}
}

func TestRunManagesIntegrationResourceAcrossBaselineAndMutants(t *testing.T) {
	t.Parallel()
	testkit.SerializeHeavy(t)
	log := filepath.Join(t.TempDir(), "resource.log")
	environment := append(os.Environ(),
		"GOATEST_ASSURE_RESOURCE_HELPER=1",
		"GOATEST_ASSURE_RESOURCE_LOG="+log,
	)
	configuration := fmt.Sprintf(`version = 1
contract = "standard-v1"

[resources.postgres]
command = [%s]
timeout = "10s"
shared = true
environment = ["GOATEST_ASSURE_RESOURCE_HELPER", "GOATEST_ASSURE_RESOURCE_LOG"]
`, tomlArgv(testkit.HelperArgv("TestRunResourceProviderHelper")))
	repository := testkit.NewRepo(t).
		File(".goatest.toml", crlfFixture(configuration)).
		File("go.mod", crlfFixture("module github.com/P4suta/goatest\n\ngo 1.26.0\n")).
		File("api.go", crlfFixture(`package goatest

import "testing"

type TestScope struct{ capabilities []string }
func Integration(capabilities ...string) TestScope { return TestScope{capabilities: capabilities} }
type T struct{ *testing.T }
func Run(t *testing.T, _ TestScope, body func(*T)) { body(&T{T: t}) }
`)).
		File("subject/boundary.go", crlfFixture(`package subject

func Boundary(value int) int {
	if value < 10 { return value }
	return 9
}
`)).
		File("subject/boundary_test.go", crlfFixture(`package subject

import (
	"os"
	"testing"
	goatest "github.com/P4suta/goatest"
)

func TestManagedPostgres(t *testing.T) {
	goatest.Run(t, goatest.Integration("postgres"), func(gt *goatest.T) {
		if os.Getenv("DATABASE_URL") != "postgres://managed/test" { gt.Fatal("managed environment missing") }
		for _, value := range []int{5, 10} {
			want := value
			if value >= 10 { want = 9 }
			if got := Boundary(value); got != want { gt.Fatalf("got %d, want %d", got, want) }
		}
	})
}
`))
	result, err := assure.Run(t.Context(), assure.Options{
		Root: repository.Root(), Contract: "standard-v1", GoBinary: testkit.GoBinary(t), TempDirectory: t.TempDir(),
		MutationOperators: []string{"comparison"}, MutationJobs: endToEndMutationJobs,
		Environment: environment,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Verdict != report.VerdictAssured {
		t.Fatalf("report = %+v", result)
	}
	file, err := os.Open(log)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	var actions []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		actions = append(actions, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if strings.Join(actions, ",") != "start,stop" {
		t.Fatalf("resource lifecycle = %v", actions)
	}
}

func appendFixtureLog(path, action string) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, filemode.PrivateFile)
	if err != nil {
		os.Exit(fixtureLogOpenExitCode)
	}
	_, _ = fmt.Fprintln(file, action)
	_ = file.Close()
}

func TestRunUsesExternalGenerationProtocolAndProductionValidator(t *testing.T) {
	t.Parallel()
	testkit.SerializeHeavy(t)
	repository := testkit.NewRepo(t).
		File("go.mod", crlfFixture("module fixture.example/provider-e2e\n\ngo 1.26.0\n")).
		File("boundary.go", crlfFixture(`package providere2e

func Boundary(value int) int {
	if value < 10 { return value }
	return 9
}
`)).
		File("boundary_test.go", crlfFixture(`package providere2e

import "testing"

func TestBoundaryWeak(t *testing.T) {
	if got := Boundary(5); got != 5 { t.Fatalf("got %d", got) }
}
`))
	preimage, err := os.ReadFile(repository.Path("boundary_test.go"))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(preimage)
	candidate := testkit.NewRepo(t).File("candidate.go", crlfFixture(`package providere2e

import "testing"

func TestBoundaryWeak(t *testing.T) {
	for _, value := range []int{5, 10} {
		want := value
		if value >= 10 { want = 9 }
		if got := Boundary(value); got != want { t.Fatalf("got %d, want %d", got, want) }
	}
}
`))
	environment := append(os.Environ(),
		"GOATEST_ASSURE_GENERATION_HELPER=1",
		"GOATEST_ASSURE_GENERATION_CONTENT="+candidate.Path("candidate.go"),
		"GOATEST_ASSURE_GENERATION_PREIMAGE="+hex.EncodeToString(sum[:]),
	)
	configuration := fmt.Sprintf(`version = 1
contract = "standard-v1"

[generation]
command = [%s]
allowed_paths = ["boundary_test.go"]
environment = ["GOATEST_ASSURE_GENERATION_HELPER", "GOATEST_ASSURE_GENERATION_CONTENT", "GOATEST_ASSURE_GENERATION_PREIMAGE"]
`, tomlArgv(testkit.HelperArgv("TestRunGenerationProviderHelper")))
	repository.File(".goatest.toml", crlfFixture(configuration))
	result, err := assure.Run(t.Context(), assure.Options{
		Root: repository.Root(), Contract: "standard-v1", GoBinary: testkit.GoBinary(t), TempDirectory: t.TempDir(),
		MutationOperators: []string{"comparison"}, MutationJobs: endToEndMutationJobs,
		Environment: environment,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Verdict != report.VerdictAssured || len(result.Repairs) != 1 || result.Repairs[0].Status != "applied" {
		t.Fatalf("report = %+v", result)
	}
}

func crlfFixture(contents string) string {
	return strings.ReplaceAll(contents, "\n", "\r\n")
}

func tomlArgv(argv []string) string {
	quoted := make([]string, len(argv))
	for index, argument := range argv {
		quoted[index] = strconv.Quote(argument)
	}
	return strings.Join(quoted, ", ")
}

func TestRunPackageScopeBoundsTheMutationCatalogToTheResolvedPackages(t *testing.T) {
	t.Parallel()
	testkit.SerializeHeavy(t)
	repository := testkit.NewRepo(t).
		File("go.mod", crlfFixture("module fixture.example/scoped\n\ngo 1.26.0\n")).
		File("naked.go", crlfFixture("package scoped\n\nfunc Naked(value int) bool { return value < 10 }\n")).
		File("covered/covered.go", crlfFixture(`package covered

func Boundary(value int) int {
	if value < 10 { return value }
	return 9
}
`)).
		File("covered/covered_test.go", crlfFixture(`package covered

import "testing"

func TestBoundary(t *testing.T) {
	for _, value := range []int{5, 10} {
		want := value
		if value >= 10 { want = 9 }
		if got := Boundary(value); got != want { t.Fatalf("Boundary(%d) = %d, want %d", value, got, want) }
	}
}
`))
	result, err := assure.Run(t.Context(), assure.Options{
		Root: repository.Root(), Contract: "standard-v1", GoBinary: testkit.GoBinary(t),
		TempDirectory: t.TempDir(), MutationOperators: []string{"comparison"}, MutationJobs: endToEndMutationJobs,
		Packages: []string{"./covered"}, PackageScope: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Verdict != report.VerdictAssured || result.Accounting.Mutants.Executed == 0 {
		t.Fatalf("scoped report = verdict %s accounting %+v", result.Verdict, result.Accounting.Mutants)
	}
	for _, mutant := range result.Mutants {
		if !strings.HasPrefix(filepath.ToSlash(mutant.Path), "covered/") {
			t.Fatalf("catalog reached beyond the scope: %+v", mutant)
		}
	}
}
