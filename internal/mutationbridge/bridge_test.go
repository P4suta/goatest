// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package mutationbridge_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	gomutants "github.com/P4suta/go-mutants"
	"github.com/P4suta/goatest/internal/mutationbridge"
)

func TestProfileMatchesAssuranceContract(t *testing.T) {
	for contract, want := range map[string]string{"standard-v1": "strong", "deep-v1": "all"} {
		got, err := mutationbridge.Profile(contract)
		if err != nil || got != want {
			t.Errorf("Profile(%q) = %q, %v; want %q", contract, got, err, want)
		}
	}
	if _, err := mutationbridge.Profile("unknown"); err == nil {
		t.Fatal("unknown contract was accepted")
	}
}

func TestPromoteCorpusIsAtomicIdempotentAndStandardOnly(t *testing.T) {
	root := t.TempDir()
	artifact := gomutants.Artifact{
		Path: "testdata/fuzz/FuzzBoundary/abc123",
		Data: []byte("go test fuzz v1\n[]byte(\"x\")\n"),
	}
	path, added, err := mutationbridge.PromoteCorpus(root, artifact)
	if err != nil || !added || path != artifact.Path {
		t.Fatalf("first promotion = %q, %v, %v", path, added, err)
	}
	if _, added, err := mutationbridge.PromoteCorpus(root, artifact); err != nil || added {
		t.Fatalf("second promotion = added %v, err %v", added, err)
	}
	got, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
	if err != nil || !slices.Equal(got, artifact.Data) {
		t.Fatalf("promoted bytes = %q, %v", got, err)
	}
	for _, invalid := range []gomutants.Artifact{
		{Path: "production.go", Data: artifact.Data},
		{Path: artifact.Path, Data: []byte("not a standard corpus")},
		{Path: "../testdata/fuzz/FuzzX/x", Data: artifact.Data},
		{Path: "./A:/testdata/fuzz/FuzzX/x", Data: artifact.Data},
		{Path: "testdata/fuzz/FuzzX/\x00", Data: artifact.Data},
	} {
		if _, _, err := mutationbridge.PromoteCorpus(root, invalid); err == nil {
			t.Errorf("invalid artifact was promoted: %+v", invalid)
		}
	}
}

func TestWeakTestToPromotedCorpusToFreshSessionKill(t *testing.T) {
	root := t.TempDir()
	write(t, root, "go.mod", "module fixture.example/bridge\n\ngo 1.26.0\n")
	write(t, root, "boundary.go", `package bridge

func Boundary(value int) int {
	if value < 10 {
		return value
	}
	return 9
}
`)
	write(t, root, "boundary_test.go", `package bridge

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

	bridge, err := mutationbridge.Open(t.Context(), root, mutationbridge.Options{TempDirectory: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	session, err := bridge.Prepare(t.Context(), mutationbridge.PrepareOptions{
		Contract: "standard-v1", Operators: []string{"comparison"}, MutantTimeout: 15 * time.Second,
	})
	if err != nil {
		_ = bridge.Close()
		t.Fatal(err)
	}
	mutant := find(t, session.Catalog(), "lt-to-le")
	weak, err := session.Exec(t.Context(), gomutants.ExecRequest{Mutant: mutant.ID, Args: []string{"-test.run=^TestBoundaryWeak$"}})
	if err != nil || weak.Outcome != gomutants.OutcomeSurvived {
		t.Fatalf("weak result = %+v, %v", weak, err)
	}
	fuzz, err := session.Exec(t.Context(), gomutants.ExecRequest{Mutant: mutant.ID, Args: []string{
		"-test.run=^$", "-test.fuzz=^FuzzBoundary$", "-test.fuzztime=5s",
	}})
	if err != nil || fuzz.Outcome != gomutants.OutcomeKilled || len(fuzz.Artifacts) == 0 {
		t.Fatalf("fuzz result = %+v, %v", fuzz, err)
	}
	if err := bridge.Close(); err != nil {
		t.Fatal(err)
	}
	var promoted string
	for _, artifact := range fuzz.Artifacts {
		if strings.Contains(filepath.ToSlash(artifact.Path), "/FuzzBoundary/") || strings.HasPrefix(filepath.ToSlash(artifact.Path), "testdata/fuzz/FuzzBoundary/") {
			promoted, _, err = mutationbridge.PromoteCorpus(root, artifact)
			if err != nil {
				t.Fatal(err)
			}
			break
		}
	}
	if promoted == "" {
		t.Fatalf("no FuzzBoundary corpus in %+v", fuzz.Artifacts)
	}

	fresh, err := mutationbridge.Open(t.Context(), root, mutationbridge.Options{TempDirectory: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = fresh.Close() }()
	freshSession, err := fresh.Prepare(t.Context(), mutationbridge.PrepareOptions{
		Contract: "standard-v1", Operators: []string{"comparison"}, MutantTimeout: 15 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	freshMutant := find(t, freshSession.Catalog(), "lt-to-le")
	seed, err := freshSession.Exec(t.Context(), gomutants.ExecRequest{
		Mutant: freshMutant.ID, Args: []string{"-test.run=^FuzzBoundary/"},
	})
	if err != nil || seed.Outcome != gomutants.OutcomeKilled {
		t.Fatalf("promoted seed result = %+v, %v", seed, err)
	}
}

func find(t *testing.T, catalog gomutants.Catalog, rule string) gomutants.Mutant {
	t.Helper()
	for _, mutant := range catalog.Mutants {
		if mutant.Rule == rule && mutant.Accepted {
			return mutant
		}
	}
	t.Fatalf("no accepted %s mutant: %+v", rule, catalog.Mutants)
	return gomutants.Mutant{}
}

func write(t *testing.T, root, name, contents string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}
