// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package assure_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gomutants "github.com/P4suta/go-mutants"
	"github.com/P4suta/goatest/internal/assure"
	goanalysis "github.com/P4suta/goatest/internal/golang"
)

type fakeSession struct {
	catalog  gomutants.Catalog
	requests []gomutants.ExecRequest
	exec     func(gomutants.ExecRequest) (gomutants.MutantResult, error)
}

func (session *fakeSession) Catalog() gomutants.Catalog { return session.catalog }

func (session *fakeSession) Exec(_ context.Context, request gomutants.ExecRequest) (gomutants.MutantResult, error) {
	session.requests = append(session.requests, request)
	return session.exec(request)
}

func TestEvaluateRunsOnlyCoveringTargetsAndRecordsKill(t *testing.T) {
	mutant := acceptedMutant()
	session := &fakeSession{catalog: gomutants.Catalog{Mutants: []gomutants.Mutant{mutant}}}
	session.exec = func(request gomutants.ExecRequest) (gomutants.MutantResult, error) {
		return gomutants.MutantResult{ID: mutant.ID, Outcome: gomutants.OutcomeKilled}, nil
	}
	targets := []assure.TargetEvidence{
		{Target: target("TestUnrelated", goanalysis.KindTest), CoveredFiles: []string{"other.go"}},
		{Target: target("TestBoundary", goanalysis.KindTest), CoveredFiles: []string{"boundary.go"}, Environment: []string{"DB_URL=fixture"}},
	}

	result, err := assure.EvaluateMutations(t.Context(), session, targets, assure.MutationOptions{
		Root: t.TempDir(), Contract: "standard-v1", FuzzExecutions: 10_000, Timeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Findings) != 0 || len(result.Evidence) != 1 || result.Applied {
		t.Fatalf("result = %+v", result)
	}
	if len(session.requests) != 1 {
		t.Fatalf("requests = %+v", session.requests)
	}
	request := session.requests[0]
	if request.Package != "fixture.example/module" || strings.Join(request.Args, " ") != "-test.run=^TestBoundary$" {
		t.Fatalf("request = %+v", request)
	}
	if strings.Join(request.Env, " ") != "DB_URL=fixture" {
		t.Fatalf("request env = %v", request.Env)
	}
}

func TestEvaluateFailsClosedWhenNoTargetReachesMutant(t *testing.T) {
	mutant := acceptedMutant()
	session := &fakeSession{
		catalog: gomutants.Catalog{Mutants: []gomutants.Mutant{mutant}},
		exec: func(gomutants.ExecRequest) (gomutants.MutantResult, error) {
			t.Fatal("unreachable mutant must not run a target")
			return gomutants.MutantResult{}, nil
		},
	}
	result, err := assure.EvaluateMutations(t.Context(), session, nil, assure.MutationOptions{Root: t.TempDir(), Contract: "standard-v1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Findings) != 1 || result.Findings[0].Kind != "unreached-mutant" {
		t.Fatalf("findings = %+v", result.Findings)
	}
}

func TestEvaluatePromotesTargetedFuzzArtifactAndRequestsFreshRound(t *testing.T) {
	root := t.TempDir()
	mutant := acceptedMutant()
	session := &fakeSession{catalog: gomutants.Catalog{Mutants: []gomutants.Mutant{mutant}}}
	session.exec = func(request gomutants.ExecRequest) (gomutants.MutantResult, error) {
		if hasArgPrefix(request.Args, "-test.fuzz=") {
			return gomutants.MutantResult{
				ID: mutant.ID, Outcome: gomutants.OutcomeKilled,
				Artifacts: []gomutants.Artifact{{
					Path: "testdata/fuzz/FuzzBoundary/a1b2c3",
					Data: []byte("go test fuzz v1\n[]byte(\"boundary\")\n"),
				}},
			}, nil
		}
		return gomutants.MutantResult{ID: mutant.ID, Outcome: gomutants.OutcomeSurvived}, nil
	}
	targets := []assure.TargetEvidence{{
		Target: target("FuzzBoundary", goanalysis.KindFuzz), CoveredFiles: []string{"boundary.go"},
	}}

	result, err := assure.EvaluateMutations(t.Context(), session, targets, assure.MutationOptions{
		Root: root, Contract: "standard-v1", FuzzExecutions: 10_000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Applied || len(result.Repairs) != 1 || result.Repairs[0].Status != "applied" {
		t.Fatalf("result = %+v", result)
	}
	if len(session.requests) != 2 || !hasArg(session.requests[1].Args, "-test.fuzztime=10000x") {
		t.Fatalf("requests = %+v", session.requests)
	}
	data, err := os.ReadFile(filepath.Join(root, "testdata", "fuzz", "FuzzBoundary", "a1b2c3"))
	if err != nil || !strings.HasPrefix(string(data), "go test fuzz v1\n") {
		t.Fatalf("promoted corpus = %q, %v", data, err)
	}
}

func TestEvaluateNoApplyLeavesFuzzKillInsufficient(t *testing.T) {
	mutant := acceptedMutant()
	session := &fakeSession{catalog: gomutants.Catalog{Mutants: []gomutants.Mutant{mutant}}}
	session.exec = func(request gomutants.ExecRequest) (gomutants.MutantResult, error) {
		if hasArgPrefix(request.Args, "-test.fuzz=") {
			return gomutants.MutantResult{ID: mutant.ID, Outcome: gomutants.OutcomeKilled, Artifacts: []gomutants.Artifact{{
				Path: "testdata/fuzz/FuzzBoundary/candidate", Data: []byte("go test fuzz v1\n[]byte(\"x\")\n"),
			}}}, nil
		}
		return gomutants.MutantResult{ID: mutant.ID, Outcome: gomutants.OutcomeSurvived}, nil
	}
	result, err := assure.EvaluateMutations(t.Context(), session, []assure.TargetEvidence{{
		Target: target("FuzzBoundary", goanalysis.KindFuzz), CoveredFiles: []string{"boundary.go"},
	}}, assure.MutationOptions{Root: t.TempDir(), Contract: "standard-v1", NoApply: true})
	if err != nil {
		t.Fatal(err)
	}
	if result.Applied || len(result.Findings) != 1 || result.Findings[0].Kind != "unpersisted-fuzz-kill" {
		t.Fatalf("result = %+v", result)
	}
}

func acceptedMutant() gomutants.Mutant {
	return gomutants.Mutant{
		ID: "mutant-full-id", DisplayID: "mutant-1", Accepted: true,
		Path: "boundary.go", Package: "fixture.example/module", Line: 4,
		Rule: "lt-to-le", Original: "<", Replacement: "<=",
	}
}

func target(name string, kind goanalysis.TargetKind) goanalysis.Target {
	return goanalysis.Target{ID: "target-" + name, Name: name, Kind: kind, Package: "fixture.example/module", RelativeDir: "."}
}

func hasArg(arguments []string, want string) bool {
	for _, argument := range arguments {
		if argument == want {
			return true
		}
	}
	return false
}

func hasArgPrefix(arguments []string, prefix string) bool {
	for _, argument := range arguments {
		if strings.HasPrefix(argument, prefix) {
			return true
		}
	}
	return false
}
