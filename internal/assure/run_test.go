// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package assure_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/P4suta/goatest/internal/assure"
	"github.com/P4suta/goatest/internal/provider"
	"github.com/P4suta/goatest/internal/report"
)

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
	}
	first, err := assure.Run(t.Context(), options)
	if err != nil {
		t.Fatal(err)
	}
	if first.Verdict != report.VerdictAssured || first.Snapshot == "" {
		t.Fatalf("first report = %+v", first)
	}

	var events []assure.Event
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

func goBinary(t *testing.T) string {
	t.Helper()
	name := "go"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	path := filepath.Join(runtime.GOROOT(), "bin", name)
	if _, err := os.Stat(path); err != nil {
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
