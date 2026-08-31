// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package testkit_test

import (
	"errors"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	gomutants "github.com/P4suta/go-mutants"
	"github.com/P4suta/goatest/internal/assure"
	"github.com/P4suta/goatest/internal/testkit"
)

// Like ScriptedWorkspace, the session fake satisfies the assure contract
// structurally; only this external test package imports internal/assure.
var _ assure.MutationSession = (*testkit.ScriptedSession)(nil)

func catalogFixture() gomutants.Catalog {
	return gomutants.Catalog{
		WorkspaceDigest: "workspace-digest",
		Digest:          "catalog-digest",
		ModulePath:      "fixture.example/assured",
		GoVersion:       "go1.26.0",
		Mutants: []gomutants.Mutant{
			{Index: 0, ID: "mutant-one", DisplayID: "m1", Path: "boundary.go", Package: "fixture.example/assured", Line: 4, Rule: "comparison"},
			{Index: 1, ID: "mutant-two", DisplayID: "m2", Path: "boundary.go", Package: "fixture.example/assured", Line: 5, Rule: "comparison"},
		},
		TestPackages: []string{"fixture.example/assured"},
	}
}

func TestScriptedSessionReturnsAnIndependentCatalogCopy(t *testing.T) {
	t.Parallel()
	source := catalogFixture()
	session := testkit.NewSession(source)

	catalog := session.Catalog()
	if catalog.ModulePath != "fixture.example/assured" || len(catalog.Mutants) != 2 {
		t.Fatalf("catalog = %+v", catalog)
	}
	catalog.Mutants[0].ID = "mutated"
	source.Mutants[1].ID = "mutated"
	source.TestPackages[0] = "mutated"

	again := session.Catalog()
	if again.Mutants[0].ID != "mutant-one" || again.Mutants[1].ID != "mutant-two" {
		t.Errorf("Catalog shares mutant storage: %+v", again.Mutants)
	}
	if !slices.Equal(again.TestPackages, []string{"fixture.example/assured"}) {
		t.Errorf("Catalog shares test-package storage: %q", again.TestPackages)
	}
}

func TestScriptedSessionRoutesRequestsByMutantAndArgumentPrefix(t *testing.T) {
	t.Parallel()
	session := testkit.NewSession(catalogFixture())
	session.On("mutant-one").Return(gomutants.MutantResult{ID: "mutant-one", Outcome: gomutants.OutcomeSurvived})
	session.On("mutant-one", "-test.run=^TestBoundary$").
		Return(gomutants.MutantResult{ID: "mutant-one", Outcome: gomutants.OutcomeKilled, KilledBy: "fixture.example/assured"})
	session.On("mutant-two").Do(func(request gomutants.ExecRequest) (gomutants.MutantResult, error) {
		return gomutants.MutantResult{ID: request.Mutant, Outcome: gomutants.OutcomeNotRun, OutputTail: request.Package}, nil
	})

	killed, err := session.Exec(t.Context(), gomutants.ExecRequest{
		Mutant: "mutant-one", Package: "fixture.example/assured", Args: []string{"-test.run=^TestBoundary$"},
	})
	if err != nil || killed.Outcome != gomutants.OutcomeKilled {
		t.Fatalf("specific route = %+v, err = %v", killed, err)
	}
	survived, err := session.Exec(t.Context(), gomutants.ExecRequest{Mutant: "mutant-one", Args: []string{"-test.run=^TestOther$"}})
	if err != nil || survived.Outcome != gomutants.OutcomeSurvived {
		t.Fatalf("general route = %+v, err = %v", survived, err)
	}
	handled, err := session.Exec(t.Context(), gomutants.ExecRequest{Mutant: "mutant-two", Package: "fixture.example/assured"})
	if err != nil || handled.OutputTail != "fixture.example/assured" {
		t.Fatalf("handler route = %+v, err = %v", handled, err)
	}

	requests := session.Requests()
	if len(requests) != 3 || requests[0].Mutant != "mutant-one" || requests[2].Mutant != "mutant-two" {
		t.Fatalf("requests = %+v", requests)
	}
	requests[0].Mutant = "mutated"
	if again := session.Requests(); again[0].Mutant != "mutant-one" {
		t.Error("Requests returned an aliased slice")
	}
}

func TestScriptedSessionPrefersTheFirstRuleOnEqualArgumentPrefixes(t *testing.T) {
	t.Parallel()
	session := testkit.NewSession(catalogFixture())
	session.On("mutant-one").Return(gomutants.MutantResult{DisplayID: "first"})
	session.On("mutant-one").Return(gomutants.MutantResult{DisplayID: "second"})

	result, err := session.Exec(t.Context(), gomutants.ExecRequest{Mutant: "mutant-one"})
	if err != nil || result.DisplayID != "first" {
		t.Fatalf("result = %+v, err = %v", result, err)
	}
}

func TestScriptedSessionReturnsScriptedFailures(t *testing.T) {
	t.Parallel()
	failure := errors.New("mutant timed out unexpectedly")
	session := testkit.NewSession(catalogFixture())
	session.On("mutant-one").Fail(failure)

	result, err := session.Exec(t.Context(), gomutants.ExecRequest{Mutant: "mutant-one"})
	if !errors.Is(err, failure) {
		t.Fatalf("err = %v, want %v", err, failure)
	}
	if result.Outcome != "" || result.ID != "" {
		t.Errorf("failed Exec returned %+v, want the zero result", result)
	}
}

func TestScriptedSessionFailsClosedOnUnscriptedRequests(t *testing.T) {
	t.Parallel()
	session := testkit.NewSession(catalogFixture())
	session.On("mutant-one").Return(gomutants.MutantResult{})

	result, err := session.Exec(t.Context(), gomutants.ExecRequest{Mutant: "mutant-two", Args: []string{"-test.run=^TestBoundary$"}})
	if !errors.Is(err, testkit.ErrNoRule) {
		t.Fatalf("err = %v, want ErrNoRule", err)
	}
	if result.Outcome != "" || result.ID != "" {
		t.Errorf("unscripted Exec returned %+v, want the zero result", result)
	}
	if !strings.Contains(err.Error(), "mutant-two") {
		t.Errorf("error %q does not name the requested mutant", err)
	}
	if requests := session.Requests(); len(requests) != 1 || requests[0].Mutant != "mutant-two" {
		t.Errorf("unscripted request was not recorded: %+v", requests)
	}
}

func TestScriptedSessionSupportsConcurrentExec(t *testing.T) {
	t.Parallel()
	const executions = 16
	session := testkit.NewSession(catalogFixture())
	session.On("mutant-one").Do(func(request gomutants.ExecRequest) (gomutants.MutantResult, error) {
		return gomutants.MutantResult{ID: request.Mutant, OutputTail: request.Package}, nil
	})

	var group sync.WaitGroup
	for index := range executions {
		group.Add(1)
		go func() {
			defer group.Done()
			name := strconv.Itoa(index)
			result, err := session.Exec(t.Context(), gomutants.ExecRequest{Mutant: "mutant-one", Package: name})
			if err != nil || result.OutputTail != name {
				t.Errorf("concurrent Exec %s = %+v, err = %v", name, result, err)
			}
		}()
	}
	group.Wait()

	if requests := session.Requests(); len(requests) != executions {
		t.Fatalf("requests = %d, want %d", len(requests), executions)
	}
}
