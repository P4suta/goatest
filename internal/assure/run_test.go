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
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/P4suta/goatest/internal/assure"
	"github.com/P4suta/goatest/internal/provider"
	"github.com/P4suta/goatest/internal/report"
	"github.com/P4suta/goatest/internal/resource"
)

func TestRunResourceProviderHelper(t *testing.T) {
	if os.Getenv("GOATEST_ASSURE_RESOURCE_HELPER") != "1" {
		return
	}
	decoder := json.NewDecoder(os.Stdin)
	encoder := json.NewEncoder(os.Stdout)
	var start resource.Request
	if err := decoder.Decode(&start); err != nil {
		os.Exit(40)
	}
	appendFixtureLog(os.Getenv("GOATEST_ASSURE_RESOURCE_LOG"), start.Action)
	if err := encoder.Encode(resource.Response{
		Version: 1, Status: "ready", Instance: "postgres-e2e",
		Environment: map[string]string{"DATABASE_URL": "postgres://managed/test"},
	}); err != nil {
		os.Exit(41)
	}
	var stop resource.Request
	if err := decoder.Decode(&stop); err != nil {
		os.Exit(42)
	}
	appendFixtureLog(os.Getenv("GOATEST_ASSURE_RESOURCE_LOG"), stop.Action)
	_ = encoder.Encode(resource.Response{Version: 1, Status: "stopped", Instance: "postgres-e2e"})
}

func TestRunGenerationProviderHelper(t *testing.T) {
	if os.Getenv("GOATEST_ASSURE_GENERATION_HELPER") != "1" {
		return
	}
	var request provider.Request
	if err := json.NewDecoder(os.Stdin).Decode(&request); err != nil {
		os.Exit(44)
	}
	content, err := os.ReadFile(os.Getenv("GOATEST_ASSURE_GENERATION_CONTENT"))
	if err != nil {
		os.Exit(45)
	}
	response := provider.Response{
		Version: provider.ProtocolVersion, FindingID: request.Finding.ID,
		Candidates: []provider.Candidate{{
			Kind: "patch", Path: "boundary_test.go",
			PreimageSHA256: os.Getenv("GOATEST_ASSURE_GENERATION_PREIMAGE"), Content: content,
		}},
	}
	if err := json.NewEncoder(os.Stdout).Encode(response); err != nil {
		os.Exit(46)
	}
	os.Exit(0)
}

func TestRunAssuresRepositoryAndWarmCacheStartsNoTestOrMutant(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "go.mod", "module fixture.example/assured\n\ngo 1.26.0\n")
	writeFixture(t, root, "boundary.go", `package assured

func Boundary(value int) int {
	if value < 10 { return value }
	return 9
}
`)
	writeFixture(t, root, "boundary_test.go", `package assured

import "testing"

func TestBoundary(t *testing.T) {
	for _, value := range []int{5, 10} {
		want := value
		if value >= 10 { want = 9 }
		if got := Boundary(value); got != want { t.Fatalf("Boundary(%d) = %d, want %d", value, got, want) }
	}
}
`)
	options := assure.Options{
		Root: root, Contract: "standard-v1", GoBinary: goBinary(t),
		TempDirectory: t.TempDir(), MutationOperators: []string{"comparison"},
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
	if !hasEvent(events, "cache-hit") {
		t.Fatalf("warm events = %+v", events)
	}
	for _, event := range events {
		if event.Kind == "baseline-target" || event.Kind == "mutation-target" || event.Kind == "mutation-prepare" {
			t.Fatalf("warm cache started child work: %+v", events)
		}
	}
}

func TestPlanEnumeratesTargetsAndMutantsWithoutRunningTestTargets(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "go.mod", "module fixture.example/plan\n\ngo 1.26.0\n")
	writeFixture(t, root, "value.go", "package plan\n\nfunc Value(v int) bool { return v < 10 }\n")
	writeFixture(t, root, "value_test.go", `package plan

import (
	"os"
	"testing"
)

func TestValue(t *testing.T) {
	if err := os.WriteFile("test-target-ran", []byte("ran"), 0o600); err != nil { t.Fatal(err) }
}
`)
	planned, err := assure.Plan(t.Context(), assure.Options{
		Root: root, Contract: "standard-v1", GoBinary: goBinary(t), TempDirectory: t.TempDir(),
		MutationOperators: []string{"comparison"},
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
	if _, err := os.Stat(filepath.Join(root, "test-target-ran")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("plan ran a test target: %v", err)
	}
}

func TestRunReturnsDefectForRepeatableBaselineFailureWithoutPreparingMutants(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "go.mod", "module fixture.example/defect\n\ngo 1.26.0\n")
	writeFixture(t, root, "value.go", "package defect\n\nfunc Value() int { return 1 }\n")
	writeFixture(t, root, "value_test.go", `package defect

import "testing"

func TestValue(t *testing.T) {
	if Value() != 2 { t.Fatal("reproduced defect") }
}
`)
	var events []assure.Event
	result, err := assure.Run(t.Context(), assure.Options{
		Root: root, Contract: "standard-v1", GoBinary: goBinary(t), TempDirectory: t.TempDir(),
		MutationOperators: []string{"comparison"}, Progress: func(event assure.Event) { events = append(events, event) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Verdict != report.VerdictDefect || len(result.Findings) != 1 || result.Findings[0].Kind != "baseline-failure" {
		t.Fatalf("report = %+v", result)
	}
	if hasEvent(events, "mutation-prepare") {
		t.Fatalf("mutation was prepared after a baseline defect: %+v", events)
	}
}

func TestRunChangedInvalidatesOnlyImpactedTargetsAndBroadensForUnknownFiles(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "go.mod", "module fixture.example/incremental\n\ngo 1.26.0\n")
	writeFixture(t, root, ".gitignore", ".goatest/\n")
	writeFixture(t, root, "a.go", "package incremental\n\nfunc A(v int) bool { return v < 10 }\n")
	writeFixture(t, root, "b.go", "package incremental\n\nfunc B(v int) bool { return v > 0 }\n")
	writeFixture(t, root, "values_test.go", `package incremental

import "testing"

func TestA(t *testing.T) {
	for _, v := range []int{9, 10} { if got := A(v); got != (v < 10) { t.Fatalf("A(%d) = %t", v, got) } }
}
func TestB(t *testing.T) {
	for _, v := range []int{0, 1} { if got := B(v); got != (v > 0) { t.Fatalf("B(%d) = %t", v, got) } }
}
`)
	runGit(t, root, "init")
	runGit(t, root, "config", "user.email", "goatest@example.invalid")
	runGit(t, root, "config", "user.name", "goatest fixture")
	runGit(t, root, "add", ".")
	runGit(t, root, "commit", "-m", "fixture")
	base := assure.Options{
		Root: root, Contract: "standard-v1", GoBinary: goBinary(t), TempDirectory: t.TempDir(),
		MutationOperators: []string{"comparison"},
	}
	if result, err := assure.Run(t.Context(), base); err != nil || result.Verdict != report.VerdictAssured {
		t.Fatalf("initial run = %+v, %v", result, err)
	}
	writeFixture(t, root, "a.go", "package incremental\n\n// changed comment\nfunc A(v int) bool { return v < 10 }\n")
	var targeted []assure.Event
	base.Changed = true
	base.Progress = func(event assure.Event) { targeted = append(targeted, event) }
	if result, err := assure.Run(t.Context(), base); err != nil || result.Verdict != report.VerdictAssured {
		t.Fatalf("targeted run = %+v, %v", result, err)
	}
	if got := baselineTargetDetails(targeted); len(got) != 1 || !strings.Contains(got[0], "TestA") {
		t.Fatalf("targeted baseline events = %v; all events=%+v", got, targeted)
	}

	writeFixture(t, root, "unknown.txt", "force safe fallback\n")
	var broad []assure.Event
	base.Progress = func(event assure.Event) { broad = append(broad, event) }
	if _, err := assure.Run(t.Context(), base); err != nil {
		t.Fatal(err)
	}
	if got := baselineTargetDetails(broad); len(got) != 2 || !hasEvent(broad, "impact-broad") {
		t.Fatalf("broad baseline events = %v; all events=%+v", got, broad)
	}
}

func TestRunPromotesTargetedFuzzCorpusAndReverifiesFromFreshSnapshot(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "go.mod", "module fixture.example/repair\n\ngo 1.26.0\n")
	writeFixture(t, root, "boundary.go", `package repair

func Boundary(value int) int {
	if value < 10 { return value }
	return 9
}
`)
	writeFixture(t, root, "boundary_test.go", `package repair

import "testing"

func TestBoundaryWeak(t *testing.T) {
	if got := Boundary(5); got != 5 { t.Fatalf("got %d", got) }
}

func FuzzBoundary(f *testing.F) {
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, input []byte) {
		value := 5
		if len(input) > 0 { value = 10 }
		want := value
		if value >= 10 { want = 9 }
		if got := Boundary(value); got != want { t.Fatalf("Boundary(%d) = %d, want %d", value, got, want) }
	})
}
`)
	var events []assure.Event
	result, err := assure.Run(t.Context(), assure.Options{
		Root: root, Contract: "standard-v1", GoBinary: goBinary(t), TempDirectory: t.TempDir(),
		MutationOperators: []string{"comparison"}, FuzzExecutions: 10_000,
		Progress: func(event assure.Event) { events = append(events, event) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Verdict != report.VerdictAssured || len(result.Repairs) != 1 || result.Repairs[0].Status != "applied" {
		t.Fatalf("report = %+v", result)
	}
	if countEvent(events, "snapshot") < 2 || !hasEvent(events, "repair-applied") {
		t.Fatalf("events = %+v", events)
	}
	entries, err := os.ReadDir(filepath.Join(root, "testdata", "fuzz", "FuzzBoundary"))
	if err != nil || len(entries) == 0 {
		t.Fatalf("promoted corpus = %v, %v", entries, err)
	}
}

func TestRunValidatesAppliesGeneratedTestAndReverifiesFreshSnapshot(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "go.mod", "module fixture.example/generated\n\ngo 1.26.0\n")
	writeFixture(t, root, "boundary.go", `package generated

func Boundary(value int) int {
	if value < 10 { return value }
	return 9
}
`)
	writeFixture(t, root, "boundary_test.go", `package generated

import "testing"

func TestBoundaryWeak(t *testing.T) {
	if got := Boundary(5); got != 5 { t.Fatalf("got %d", got) }
}
`)
	preimage, err := os.ReadFile(filepath.Join(root, "boundary_test.go"))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(preimage)
	generated := strings.ReplaceAll(`package generated

import "testing"

func TestBoundaryWeak(t *testing.T) {
	for _, value := range []int{5, 10} {
		want := value
		if value >= 10 { want = 9 }
		if got := Boundary(value); got != want { t.Fatalf("got %d, want %d", got, want) }
	}
}
`, "\n", "\r\n")
	generatedCalls := 0
	result, err := assure.Run(t.Context(), assure.Options{
		Root: root, Contract: "standard-v1", GoBinary: goBinary(t), TempDirectory: t.TempDir(),
		MutationOperators: []string{"comparison"}, Validator: stableValidator{},
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

func (stableValidator) OriginalStable(context.Context, provider.Candidate) error        { return nil }
func (stableValidator) Kills(context.Context, report.Finding, provider.Candidate) error { return nil }
func (stableValidator) Suite(context.Context, provider.Candidate) error                 { return nil }

func TestRunCacheInvalidatesWhenLocalReplacementDependencyChanges(t *testing.T) {
	parent := t.TempDir()
	dependency := filepath.Join(parent, "dependency")
	root := filepath.Join(parent, "subject")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFixture(t, dependency, "go.mod", "module fixture.example/dependency\n\ngo 1.26.0\n")
	writeFixture(t, dependency, "value.go", "package dependency\n\nfunc Value() int { return 1 }\n")
	goMod := "module fixture.example/subject\n\ngo 1.26.0\n\nrequire fixture.example/dependency v0.0.0\nreplace fixture.example/dependency => " + filepath.ToSlash(dependency) + "\n"
	writeFixture(t, root, "go.mod", goMod)
	writeFixture(t, root, "value.go", "package subject\n\nimport dependency \"fixture.example/dependency\"\n\nfunc Value() int { return dependency.Value() }\n")
	writeFixture(t, root, "value_test.go", "package subject\n\nimport \"testing\"\n\nfunc TestValue(t *testing.T) { if Value() != 1 { t.Fatal(Value()) } }\n")
	options := assure.Options{
		Root: root, Contract: "standard-v1", GoBinary: goBinary(t), TempDirectory: t.TempDir(),
		MutationOperators: []string{"comparison"},
	}
	first, err := assure.Run(t.Context(), options)
	if err != nil || first.Verdict != report.VerdictAssured {
		t.Fatalf("first = %+v, %v", first, err)
	}
	writeFixture(t, dependency, "value.go", "package dependency\n\nfunc Value() int { return 2 }\n")
	var events []assure.Event
	options.Progress = func(event assure.Event) { events = append(events, event) }
	second, err := assure.Run(t.Context(), options)
	if err != nil {
		t.Fatal(err)
	}
	if second.Verdict != report.VerdictDefect || hasEvent(events, "cache-hit") {
		t.Fatalf("dependency change reused stale evidence: report=%+v events=%+v", second, events)
	}
}

func TestRunManagesIntegrationResourceAcrossBaselineAndMutants(t *testing.T) {
	root := t.TempDir()
	log := filepath.Join(t.TempDir(), "resource.log")
	t.Setenv("GOATEST_ASSURE_RESOURCE_HELPER", "1")
	t.Setenv("GOATEST_ASSURE_RESOURCE_LOG", log)
	configuration := fmt.Sprintf(`version = 1
contract = "standard-v1"

[resources.postgres]
command = [%s, "-test.run=^TestRunResourceProviderHelper$"]
timeout = "10s"
shared = true
environment = ["GOATEST_ASSURE_RESOURCE_HELPER", "GOATEST_ASSURE_RESOURCE_LOG"]
`, strconv.Quote(os.Args[0]))
	writeFixture(t, root, ".goatest.toml", configuration)
	writeFixture(t, root, "go.mod", "module github.com/P4suta/goatest\n\ngo 1.26.0\n")
	writeFixture(t, root, "api.go", `package goatest

import "testing"

type TestScope struct{ Capability string }
func Integration(capability string) TestScope { return TestScope{Capability: capability} }
type T struct{ *testing.T }
func Run(t *testing.T, _ TestScope, body func(*T)) { body(&T{T: t}) }
`)
	writeFixture(t, root, "subject/boundary.go", `package subject

func Boundary(value int) int {
	if value < 10 { return value }
	return 9
}
`)
	writeFixture(t, root, "subject/boundary_test.go", `package subject

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
`)
	result, err := assure.Run(t.Context(), assure.Options{
		Root: root, Contract: "standard-v1", GoBinary: goBinary(t), TempDirectory: t.TempDir(),
		MutationOperators: []string{"comparison"},
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
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		os.Exit(43)
	}
	_, _ = fmt.Fprintln(file, action)
	_ = file.Close()
}

func TestRunUsesExternalGenerationProtocolAndProductionValidator(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "go.mod", "module fixture.example/provider-e2e\n\ngo 1.26.0\n")
	writeFixture(t, root, "boundary.go", `package providere2e

func Boundary(value int) int {
	if value < 10 { return value }
	return 9
}
`)
	writeFixture(t, root, "boundary_test.go", `package providere2e

import "testing"

func TestBoundaryWeak(t *testing.T) {
	if got := Boundary(5); got != 5 { t.Fatalf("got %d", got) }
}
`)
	preimage, err := os.ReadFile(filepath.Join(root, "boundary_test.go"))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(preimage)
	contentPath := filepath.Join(t.TempDir(), "candidate.go")
	writeFixture(t, filepath.Dir(contentPath), filepath.Base(contentPath), `package providere2e

import "testing"

func TestBoundaryWeak(t *testing.T) {
	for _, value := range []int{5, 10} {
		want := value
		if value >= 10 { want = 9 }
		if got := Boundary(value); got != want { t.Fatalf("got %d, want %d", got, want) }
	}
}
`)
	t.Setenv("GOATEST_ASSURE_GENERATION_HELPER", "1")
	t.Setenv("GOATEST_ASSURE_GENERATION_CONTENT", contentPath)
	t.Setenv("GOATEST_ASSURE_GENERATION_PREIMAGE", hex.EncodeToString(sum[:]))
	configuration := fmt.Sprintf(`version = 1
contract = "standard-v1"

[generation]
command = [%s, "-test.run=^TestRunGenerationProviderHelper$"]
allowed_paths = ["boundary_test.go"]
environment = ["GOATEST_ASSURE_GENERATION_HELPER", "GOATEST_ASSURE_GENERATION_CONTENT", "GOATEST_ASSURE_GENERATION_PREIMAGE"]
`, strconv.Quote(os.Args[0]))
	writeFixture(t, root, ".goatest.toml", configuration)
	result, err := assure.Run(t.Context(), assure.Options{
		Root: root, Contract: "standard-v1", GoBinary: goBinary(t), TempDirectory: t.TempDir(),
		MutationOperators: []string{"comparison"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Verdict != report.VerdictAssured || len(result.Repairs) != 1 || result.Repairs[0].Status != "applied" {
		t.Fatalf("report = %+v", result)
	}
}

func goBinary(t *testing.T) string {
	t.Helper()
	path, err := exec.LookPath("go")
	if err != nil {
		t.Skipf("Go binary unavailable: %v", err)
	}
	return path
}

func writeFixture(t *testing.T, root, path, contents string) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(strings.ReplaceAll(contents, "\n", "\r\n")), 0o644); err != nil {
		t.Fatal(err)
	}
}

func hasEvent(events []assure.Event, kind string) bool {
	for _, event := range events {
		if event.Kind == kind {
			return true
		}
	}
	return false
}

func baselineTargetDetails(events []assure.Event) []string {
	var result []string
	for _, event := range events {
		if event.Kind == "baseline-target" {
			result = append(result, event.Detail)
		}
	}
	return result
}

func countEvent(events []assure.Event, kind string) int {
	count := 0
	for _, event := range events {
		if event.Kind == kind {
			count++
		}
	}
	return count
}

func runGit(t *testing.T, root string, arguments ...string) {
	t.Helper()
	command := exec.Command("git", arguments...)
	command.Dir = root
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", arguments, err, output)
	}
}
