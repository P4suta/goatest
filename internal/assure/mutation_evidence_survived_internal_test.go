// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package assure

import (
	"context"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	gomutants "github.com/P4suta/go-mutants"
	"github.com/P4suta/goatest/internal/evidence"
	goanalysis "github.com/P4suta/goatest/internal/golang"
	"github.com/P4suta/goatest/internal/report"
)

const (
	initialMutationAttemptCount = 1
)

func exhaustedKey(identity targetIdentity, key string) evidence.TargetKey {
	return evidence.TargetKey{Package: identity.pkg, Name: identity.name, Kind: identity.kind, Key: key}
}

func survivedEvidenceRecord(mutant gomutants.Mutant, exhausted ...evidence.TargetKey) evidence.MutationRecord {
	return evidence.MutationRecord{
		MutantID: mutant.ID, Path: mutant.Path, Package: mutant.Package,
		Outcome: evidence.MutationOutcomeSurvived, Provenance: "snapshot=" + digestText("earlier-run"),
		Exhausted: exhausted,
		Finding:   &evidence.FindingSeed{Kind: "surviving-mutant", Summary: mutationSurvivedSummary},
	}
}

func TestEvaluateMutationsReusesASurvivorWhoseReachingSetIsFullyExhausted(t *testing.T) {
	t.Parallel()
	mutant := evidenceMutant("mutant-a")
	early := evidenceIdentity("TestEarly", goanalysis.KindTest)
	key := digestText("early-key")
	record := survivedEvidenceRecord(mutant, exhaustedKey(early, key))
	index := evidenceIndex([]evidence.MutationRecord{record},
		map[targetIdentity]string{early: key}, map[targetIdentity]bool{early: true})
	catalog := gomutants.Catalog{Mutants: []gomutants.Mutant{mutant}}
	session := refusingSession(t, catalog)

	evaluation, err := evaluateMutationsForTest(t.Context(), session, []TargetEvidence{
		evidenceTarget("TestEarly", goanalysis.KindTest, 3*time.Millisecond),
	}, MutationOptions{Evidence: index})

	if err != nil {
		t.Fatal(err)
	}
	if len(session.requests) != 0 {
		t.Fatalf("executions = %+v, want none", session.requests)
	}
	if len(evaluation.Findings) != 1 || evaluation.Findings[0].Kind != "surviving-mutant" ||
		evaluation.Findings[0].Summary != mutationSurvivedSummary || len(evaluation.Evidence) != 0 {
		t.Fatalf("evaluation = %+v, want the recorded survivor finding", evaluation)
	}
	want := report.MutantDisposition{
		ID: mutant.ID, Status: report.MutantSurvived, Path: mutant.Path, Line: mutant.Line,
		Package: mutant.Package, Rule: mutant.Rule, Detail: mutationSurvivedSummary,
		Reused: true, Provenance: record.Provenance,
	}
	if len(evaluation.Mutants) != 1 || !reflect.DeepEqual(evaluation.Mutants[0], want) {
		t.Fatalf("dispositions = %+v, want [%+v]", evaluation.Mutants, want)
	}
	accounting := report.MutantAccounting{Discovered: 1, Selected: 1, Executed: 1, Survived: 1, ReusedSurvived: 1}
	if !reflect.DeepEqual(evaluation.Accounting, accounting) {
		t.Fatalf("accounting = %+v, want %+v", evaluation.Accounting, accounting)
	}
}

func TestEvaluateMutationsExecutesASurvivorWhenANewTargetEntersTheReachingSet(t *testing.T) {
	t.Parallel()
	mutant := evidenceMutant("mutant-a")
	early := evidenceIdentity("TestEarly", goanalysis.KindTest)
	late := evidenceIdentity("TestLate", goanalysis.KindTest)
	keys := map[targetIdentity]string{early: digestText("early-key"), late: digestText("late-key")}
	index := evidenceIndex([]evidence.MutationRecord{survivedEvidenceRecord(mutant, exhaustedKey(early, keys[early]))},
		keys, map[targetIdentity]bool{early: true, late: true})
	session := &mutationUnitSession{catalog: gomutants.Catalog{Mutants: []gomutants.Mutant{mutant}}}

	evaluation, err := evaluateMutationsForTest(t.Context(), session, []TargetEvidence{
		evidenceTarget("TestEarly", goanalysis.KindTest, 3*time.Millisecond),
		evidenceTarget("TestLate", goanalysis.KindTest, 5*time.Millisecond),
	}, MutationOptions{Evidence: index})

	if err != nil {
		t.Fatal(err)
	}
	if len(session.requests) != 1 || evaluation.Mutants[0].Reused || evaluation.Accounting.ReusedSurvived != 0 {
		t.Fatalf("evaluation = %+v, requests = %+v", evaluation.Mutants, session.requests)
	}
}

func TestEvaluateMutationsReusesASurvivorWhenATargetLeftTheReachingSet(t *testing.T) {
	t.Parallel()
	mutant := evidenceMutant("mutant-a")
	early := evidenceIdentity("TestEarly", goanalysis.KindTest)
	gone := evidenceIdentity("TestGone", goanalysis.KindTest)
	key := digestText("early-key")
	index := evidenceIndex([]evidence.MutationRecord{survivedEvidenceRecord(mutant,
		exhaustedKey(early, key), exhaustedKey(gone, digestText("gone-key")))},
		map[targetIdentity]string{early: key}, map[targetIdentity]bool{early: true})
	catalog := gomutants.Catalog{Mutants: []gomutants.Mutant{mutant}}
	session := refusingSession(t, catalog)

	evaluation, err := evaluateMutationsForTest(t.Context(), session, []TargetEvidence{
		evidenceTarget("TestEarly", goanalysis.KindTest, 3*time.Millisecond),
	}, MutationOptions{Evidence: index})

	if err != nil {
		t.Fatal(err)
	}
	if len(session.requests) != 0 || !evaluation.Mutants[0].Reused || evaluation.Accounting.ReusedSurvived != 1 {
		t.Fatalf("evaluation = %+v, requests = %+v", evaluation.Mutants, session.requests)
	}
}

func TestEvaluateMutationsExecutesASurvivorWhoseReachingTargetKeyChanged(t *testing.T) {
	t.Parallel()
	mutant := evidenceMutant("mutant-a")
	early := evidenceIdentity("TestEarly", goanalysis.KindTest)
	index := evidenceIndex(
		[]evidence.MutationRecord{survivedEvidenceRecord(mutant, exhaustedKey(early, digestText("recorded-key")))},
		map[targetIdentity]string{early: digestText("current-key")}, map[targetIdentity]bool{early: true})
	session := &mutationUnitSession{catalog: gomutants.Catalog{Mutants: []gomutants.Mutant{mutant}}}

	evaluation, err := evaluateMutationsForTest(t.Context(), session, []TargetEvidence{
		evidenceTarget("TestEarly", goanalysis.KindTest, 3*time.Millisecond),
	}, MutationOptions{Evidence: index})

	if err != nil {
		t.Fatal(err)
	}
	if len(session.requests) != 1 || evaluation.Mutants[0].Reused || evaluation.Accounting.ReusedSurvived != 0 {
		t.Fatalf("evaluation = %+v, requests = %+v", evaluation.Mutants, session.requests)
	}
}

func TestEvaluateMutationsExecutesASurvivorWhoseReachingTargetDidNotPass(t *testing.T) {
	t.Parallel()
	mutant := evidenceMutant("mutant-a")
	early := evidenceIdentity("TestEarly", goanalysis.KindTest)
	key := digestText("early-key")
	index := evidenceIndex([]evidence.MutationRecord{survivedEvidenceRecord(mutant, exhaustedKey(early, key))},
		map[targetIdentity]string{early: key}, map[targetIdentity]bool{})
	session := &mutationUnitSession{catalog: gomutants.Catalog{Mutants: []gomutants.Mutant{mutant}}}

	evaluation, err := evaluateMutationsForTest(t.Context(), session, []TargetEvidence{
		evidenceTarget("TestEarly", goanalysis.KindTest, 3*time.Millisecond),
	}, MutationOptions{Evidence: index})

	if err != nil {
		t.Fatal(err)
	}
	if len(session.requests) != 1 || evaluation.Mutants[0].Reused {
		t.Fatalf("evaluation = %+v, requests = %+v", evaluation.Mutants, session.requests)
	}
}

func TestEvaluateMutationsReusesASurvivorReachedByAnUnchangedFuzzSeedTarget(t *testing.T) {
	t.Parallel()
	mutant := evidenceMutant("mutant-a")
	early := evidenceIdentity("TestEarly", goanalysis.KindTest)
	fuzzer := evidenceIdentity("FuzzValue", goanalysis.KindFuzz)
	keys := map[targetIdentity]string{early: digestText("early-key"), fuzzer: digestText("fuzz-key")}
	loaded := survivedEvidenceRecord(mutant, exhaustedKey(early, keys[early]), exhaustedKey(fuzzer, keys[fuzzer]))
	index := evidenceIndex([]evidence.MutationRecord{loaded}, keys,
		map[targetIdentity]bool{early: true, fuzzer: true})
	catalog := gomutants.Catalog{Mutants: []gomutants.Mutant{mutant}}
	session := &mutationUnitSession{catalog: catalog}

	evaluation, err := evaluateMutationsForTest(t.Context(), session, []TargetEvidence{
		evidenceTarget("TestEarly", goanalysis.KindTest, 3*time.Millisecond),
		evidenceTarget("FuzzValue", goanalysis.KindFuzz, 5*time.Millisecond),
	}, MutationOptions{Evidence: index})

	if err != nil {
		t.Fatal(err)
	}
	if len(session.requests) != 0 || !evaluation.Mutants[0].Reused {
		t.Fatalf("unchanged fuzz seed survivor was not reused: %+v", evaluation.Mutants)
	}

	records := index.store(catalog, evidenceModule).Records
	if len(records) != 1 || !reflect.DeepEqual(records[0], loaded) {
		t.Fatalf("store = %+v, want only the record it was given", records)
	}
}

func TestEvaluateMutationsExecutesAMutantReachedByInexactCallerEvidence(t *testing.T) {
	t.Parallel()
	mutant := evidenceMutant("mutant-a")
	early := evidenceIdentity("TestEarly", goanalysis.KindTest)
	inexact := evidenceIdentity("TestInexact", goanalysis.KindTest)
	keys := map[targetIdentity]string{early: digestText("early-key"), inexact: digestText("inexact-key")}
	passed := map[targetIdentity]bool{early: true, inexact: true}
	index := evidenceIndex([]evidence.MutationRecord{survivedEvidenceRecord(mutant,
		exhaustedKey(early, keys[early]), exhaustedKey(inexact, keys[inexact]))}, keys, passed)
	catalog := gomutants.Catalog{Mutants: []gomutants.Mutant{mutant}}
	session := &mutationUnitSession{catalog: catalog}

	evaluation, err := evaluateMutationsForTest(t.Context(), session, []TargetEvidence{
		evidenceTarget("TestEarly", goanalysis.KindTest, 3*time.Millisecond),
		inexactBlockTarget("TestInexact", 5*time.Millisecond),
	}, MutationOptions{
		Evidence:     index,
		Instrumented: blockRoutingInstrumentation(),
	})

	if err != nil {
		t.Fatal(err)
	}
	if len(session.requests) == 0 || evaluation.Mutants[0].Reused {
		t.Fatalf("a mutant reached through inexact input was reused: %+v", evaluation.Mutants)
	}

	if records := index.store(catalog, evidenceModule).Records; len(records) != 1 ||
		records[0].Provenance != "snapshot="+digestText("earlier-run") {
		t.Fatalf("store = %+v, want the record it was given", records)
	}
}

func TestEvaluateMutationsRecordsTheSurvivorsItExhausted(t *testing.T) {
	t.Parallel()
	mutant := evidenceMutant("mutant-a")
	early := evidenceIdentity("TestEarly", goanalysis.KindTest)
	late := evidenceIdentity("TestLate", goanalysis.KindTest)
	keys := map[targetIdentity]string{early: digestText("early-key"), late: digestText("late-key")}
	index := evidenceIndex(nil, keys, map[targetIdentity]bool{early: true, late: true})
	catalog := gomutants.Catalog{Mutants: []gomutants.Mutant{mutant}}
	session := &mutationUnitSession{catalog: catalog}

	if _, err := evaluateMutationsForTest(t.Context(), session, []TargetEvidence{
		evidenceTarget("TestEarly", goanalysis.KindTest, 3*time.Millisecond),
		evidenceTarget("TestLate", goanalysis.KindTest, 5*time.Millisecond),
	}, MutationOptions{Evidence: index}); err != nil {
		t.Fatal(err)
	}
	want := evidence.MutationRecord{
		MutantID: mutant.ID, Path: mutant.Path, Package: mutant.Package,
		Outcome: evidence.MutationOutcomeSurvived, Provenance: "snapshot=" + digestText("this-run"),
		Exhausted: []evidence.TargetKey{exhaustedKey(early, keys[early]), exhaustedKey(late, keys[late])},
		Finding:   &evidence.FindingSeed{Kind: "surviving-mutant", Summary: mutationSurvivedSummary},
	}
	records := index.store(catalog, evidenceModule).Records
	if len(records) != 1 {
		t.Fatalf("records = %+v, want one survived record", records)
	}
	slices.SortFunc(records[0].Exhausted, func(first, second evidence.TargetKey) int {
		return slices.Compare([]string{first.Name}, []string{second.Name})
	})
	if !reflect.DeepEqual(records[0], want) {
		t.Fatalf("record = %+v, want %+v", records[0], want)
	}
}

func TestEvaluateMutationsDoesNotRecordASurvivorNoTestWasRunFor(t *testing.T) {
	t.Parallel()
	mutant := evidenceMutant("mutant-a")
	mutant.Probed, mutant.Index = true, uint32(len(probeCatalog().Mutants))
	early := evidenceIdentity("TestEarly", goanalysis.KindTest)
	index := evidenceIndex(nil, map[targetIdentity]string{early: digestText("early-key")},
		map[targetIdentity]bool{early: true})
	catalog := gomutants.Catalog{Mutants: []gomutants.Mutant{mutant}}
	target := evidenceTarget("TestEarly", goanalysis.KindTest, 3*time.Millisecond)
	target.Probed = true
	session := refusingSession(t, catalog)

	evaluation, err := evaluateMutationsForTest(t.Context(), session, []TargetEvidence{target}, MutationOptions{
		Evidence:     index,
		Instrumented: blockRoutingInstrumentation(),
	})

	if err != nil {
		t.Fatal(err)
	}
	if len(evaluation.Findings) != 1 || evaluation.Findings[0].Kind != "surviving-mutant" {
		t.Fatalf("evaluation = %+v, want a discharged survivor", evaluation.Findings)
	}
	if records := index.store(catalog, evidenceModule).Records; len(records) != 0 {
		t.Fatalf("recorded %+v, want nothing for a survivor no test was run for", records)
	}
}

func TestReusedFindingsAreRegeneratedThroughThisRunsAcceptance(t *testing.T) {
	t.Parallel()
	mutant := evidenceMutant("mutant-a")
	early := evidenceIdentity("TestEarly", goanalysis.KindTest)
	key := digestText("early-key")
	findingID := report.FindingID("mutation", mutant.ID)
	for _, test := range []struct {
		name     string
		accepted map[string]bool
		findings int
		evidence int
	}{
		{name: "an acceptance this run still holds", accepted: map[string]bool{findingID: true}, evidence: 1},
		{name: "an acceptance that has expired", findings: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			index := evidenceIndex([]evidence.MutationRecord{survivedEvidenceRecord(mutant, exhaustedKey(early, key))},
				map[targetIdentity]string{early: key}, map[targetIdentity]bool{early: true})
			catalog := gomutants.Catalog{Mutants: []gomutants.Mutant{mutant}}
			session := refusingSession(t, catalog)

			evaluation, err := evaluateMutationsForTest(t.Context(), session, []TargetEvidence{
				evidenceTarget("TestEarly", goanalysis.KindTest, 3*time.Millisecond),
			}, MutationOptions{
				Evidence: index, Accepted: test.accepted,
			})

			if err != nil {
				t.Fatal(err)
			}
			if len(evaluation.Findings) != test.findings || len(evaluation.Evidence) != test.evidence {
				t.Fatalf("evaluation = %+v, want %d findings and %d evidence entries",
					evaluation, test.findings, test.evidence)
			}
			if !evaluation.Mutants[0].Reused {
				t.Fatalf("dispositions = %+v, want the reuse either way", evaluation.Mutants)
			}
		})
	}
}

func suiteEvidenceIndex(records []evidence.MutationRecord, keys map[targetIdentity]string,
	passed map[targetIdentity]bool, suites map[string]string,
) *MutationEvidence {
	return newMutationEvidence(
		evidence.MutationStore{Schema: evidence.MutationSchemaV1, ModulePath: evidenceModule, Records: records},
		keys, passed, suites, "snapshot="+digestText("this-run"),
	)
}

func unreachedEvidenceRecord(mutant gomutants.Mutant, key string) evidence.MutationRecord {
	return evidence.MutationRecord{
		MutantID: mutant.ID, Path: mutant.Path, Package: mutant.Package,
		Outcome: evidence.MutationOutcomeUnreached, Provenance: "snapshot=" + digestText("earlier-run"),
		Suite:   &evidence.SuiteKey{Package: mutant.Package, Key: key},
		Finding: &evidence.FindingSeed{Kind: "unreached-mutant", Summary: mutationUnreachedSummary},
	}
}

func TestEvaluateMutationsReusesAnUnreachedMutantOnlyWhenThePackageSuiteKeyMatches(t *testing.T) {
	t.Parallel()
	recorded := digestText("suite-key")
	for _, test := range []struct {
		name     string
		suite    string
		executed int
		reused   bool
	}{
		{name: "the suite this run would run", suite: recorded, reused: true},
		{name: "a suite whose targets changed", suite: digestText("other-suite-key"), executed: 1},
		{name: "a suite this run cannot name", executed: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			mutant := evidenceMutant("mutant-a")
			suites := map[string]string{}
			if test.suite != "" {
				suites[evidenceModule] = test.suite
			}
			index := suiteEvidenceIndex([]evidence.MutationRecord{unreachedEvidenceRecord(mutant, recorded)},
				nil, nil, suites)
			catalog := gomutants.Catalog{Mutants: []gomutants.Mutant{mutant}}
			session := &mutationUnitSession{catalog: catalog}

			evaluation, err := evaluateMutationsForTest(t.Context(), session, nil, MutationOptions{
				Evidence:      index,
				SuiteCoverage: map[string]PackageSuiteCoverage{evidenceModule: {Duration: time.Second}},
			})

			if err != nil {
				t.Fatal(err)
			}
			if len(session.requests) != test.executed || evaluation.Mutants[0].Reused != test.reused {
				t.Fatalf("evaluation = %+v, requests = %+v", evaluation.Mutants, session.requests)
			}
			if !test.reused {
				return
			}
			if len(evaluation.Findings) != 1 || evaluation.Findings[0].Kind != "unreached-mutant" ||
				evaluation.Accounting.ReusedSurvived != 1 || evaluation.Accounting.Survived != 1 {
				t.Fatalf("evaluation = %+v, accounting = %+v", evaluation.Findings, evaluation.Accounting)
			}
		})
	}
}

func TestEvaluateMutationsExecutesAnUnreachedMutantATargetNowReaches(t *testing.T) {
	t.Parallel()
	mutant := evidenceMutant("mutant-a")
	early := evidenceIdentity("TestEarly", goanalysis.KindTest)
	key := digestText("early-key")
	index := suiteEvidenceIndex([]evidence.MutationRecord{unreachedEvidenceRecord(mutant, digestText("suite-key"))},
		map[targetIdentity]string{early: key}, map[targetIdentity]bool{early: true},
		map[string]string{evidenceModule: digestText("suite-key")})
	session := &mutationUnitSession{catalog: gomutants.Catalog{Mutants: []gomutants.Mutant{mutant}}}

	evaluation, err := evaluateMutationsForTest(t.Context(), session, []TargetEvidence{
		evidenceTarget("TestEarly", goanalysis.KindTest, 3*time.Millisecond),
	}, MutationOptions{Evidence: index})

	if err != nil {
		t.Fatal(err)
	}
	if len(session.requests) != 1 || evaluation.Mutants[0].Reused {
		t.Fatalf("evaluation = %+v, requests = %+v", evaluation.Mutants, session.requests)
	}
	if request := session.requests[0]; len(request.Args) == 0 || request.Args[0] != "-test.run=^TestEarly$" {
		t.Fatalf("request = %+v, want the reaching target and not the package suite", request)
	}
}

func TestEvaluateMutationsRecordsAnUnreachedMutantAgainstItsPackageSuite(t *testing.T) {
	t.Parallel()
	mutant := evidenceMutant("mutant-a")
	key := digestText("suite-key")
	index := suiteEvidenceIndex(nil, nil, nil, map[string]string{evidenceModule: key})
	catalog := gomutants.Catalog{Mutants: []gomutants.Mutant{mutant}}
	session := &mutationUnitSession{catalog: catalog}

	if _, err := evaluateMutationsForTest(t.Context(), session, nil, MutationOptions{
		Evidence:      index,
		SuiteCoverage: map[string]PackageSuiteCoverage{evidenceModule: {Duration: time.Second}},
	}); err != nil {
		t.Fatal(err)
	}
	want := evidence.MutationRecord{
		MutantID: mutant.ID, Path: mutant.Path, Package: mutant.Package,
		Outcome: evidence.MutationOutcomeUnreached, Provenance: "snapshot=" + digestText("this-run"),
		Suite:   &evidence.SuiteKey{Package: mutant.Package, Key: key},
		Finding: &evidence.FindingSeed{Kind: "unreached-mutant", Summary: mutationUnreachedSummary},
	}
	records := index.store(catalog, evidenceModule).Records
	if len(records) != 1 || !reflect.DeepEqual(records[0], want) {
		t.Fatalf("records = %+v, want [%+v]", records, want)
	}
}

func TestEvaluateMutationsRecordsNoSuiteVerdictForAPackageThisRunDidNotMeasure(t *testing.T) {
	t.Parallel()
	mutant := evidenceMutant("mutant-a")
	index := suiteEvidenceIndex(nil, nil, nil, nil)
	catalog := gomutants.Catalog{Mutants: []gomutants.Mutant{mutant}}
	session := &mutationUnitSession{catalog: catalog}

	if _, err := evaluateMutationsForTest(t.Context(), session, nil, MutationOptions{
		Evidence: index,
	}); err != nil {
		t.Fatal(err)
	}
	if records := index.store(catalog, evidenceModule).Records; len(records) != 0 {
		t.Fatalf("recorded %+v, want nothing about a suite this run cannot name", records)
	}
}

func batchedEvidenceTargets(count int) []TargetEvidence {
	targets := make([]TargetEvidence, 0, count)
	for index := range count {
		targets = append(targets, evidenceTarget(
			fmt.Sprintf("TestValue%02d", index), goanalysis.KindTest, time.Duration(index+1)*time.Millisecond))
	}
	return targets
}

func TestEvaluateMutationsStopsAnUnattributableBatchTimeoutWithoutEvidence(t *testing.T) {
	t.Parallel()
	mutant := evidenceMutant("mutant-a")
	targets := batchedEvidenceTargets(10)
	keys := make(map[targetIdentity]string, len(targets))
	passed := make(map[targetIdentity]bool, len(targets))
	for _, target := range targets {
		identity := identify(target.Target)
		keys[identity] = digestText(target.Target.Name)
		passed[identity] = true
	}
	index := evidenceIndex(nil, keys, passed)
	catalog := gomutants.Catalog{Mutants: []gomutants.Mutant{mutant}}

	session := &mutationUnitSession{catalog: catalog, exec: func(request gomutants.ExecRequest) (gomutants.MutantResult, error) {
		if slices.ContainsFunc(request.Args, func(argument string) bool { return strings.Contains(argument, "|") }) {
			return gomutants.MutantResult{ID: request.Mutant, Outcome: gomutants.OutcomeTimedOut}, nil
		}
		return gomutants.MutantResult{ID: request.Mutant, Outcome: gomutants.OutcomeSurvived}, nil
	}}

	evaluation, err := evaluateMutationsForTest(t.Context(), session, targets, MutationOptions{
		Evidence: index,
	})

	if err != nil {
		t.Fatal(err)
	}
	if len(evaluation.Findings) != 1 || evaluation.Findings[0].Kind != "mutation-timeout" {
		t.Fatalf("findings = %+v, want one inconclusive timeout", evaluation.Findings)
	}
	if got, want := len(session.requests), 1; got != want {
		t.Fatalf("requests = %d, want one aggregate attempt", got)
	}
	if records := index.store(catalog, evidenceModule).Records; len(records) != 0 {
		t.Fatalf("recorded %+v, want no durable timeout evidence", records)
	}
}

func TestEvaluateMutationsDoesNotRecordATimeoutAgainstTheTargetsItRan(t *testing.T) {
	t.Parallel()
	mutant := evidenceMutant("mutant-a")
	early := evidenceIdentity("TestEarly", goanalysis.KindTest)
	late := evidenceIdentity("TestLate", goanalysis.KindTest)
	keys := map[targetIdentity]string{early: digestText("early-key"), late: digestText("late-key")}
	index := evidenceIndex(nil, keys, map[targetIdentity]bool{early: true, late: true})
	catalog := gomutants.Catalog{Mutants: []gomutants.Mutant{mutant}}

	session := &mutationUnitSession{catalog: catalog, exec: func(request gomutants.ExecRequest) (gomutants.MutantResult, error) {
		return gomutants.MutantResult{ID: request.Mutant, Outcome: gomutants.OutcomeTimedOut}, nil
	}}
	passingControl := func(context.Context, gomutants.ExecRequest) (gomutants.CommandResult, error) {
		return gomutants.CommandResult{}, nil
	}

	if _, err := evaluateMutationsForTest(t.Context(), session, []TargetEvidence{
		evidenceTarget("TestEarly", goanalysis.KindTest, 3*time.Millisecond),
		evidenceTarget("TestLate", goanalysis.KindTest, 5*time.Millisecond),
	}, MutationOptions{
		Evidence: index, OriginalControl: passingControl,
	}); err != nil {
		t.Fatal(err)
	}
	records := index.store(catalog, evidenceModule).Records
	if len(records) != 0 {
		t.Fatalf("records = %+v, want no reusable timeout", records)
	}
}

func TestEvaluateMutationsDoesNotRecordRejectedOrInconclusiveEvidence(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name    string
		targets []TargetEvidence
		exec    func(gomutants.ExecRequest) (gomutants.MutantResult, error)
		control func(context.Context, gomutants.ExecRequest) (gomutants.CommandResult, error)
	}{
		{
			name: "an inconclusive outcome under a reaching target",
			exec: func(request gomutants.ExecRequest) (gomutants.MutantResult, error) {
				return gomutants.MutantResult{ID: request.Mutant, Outcome: gomutants.OutcomeInconclusive}, nil
			},
		},
		{
			name: "an inconclusive outcome under the package suite", targets: []TargetEvidence{},
			exec: func(request gomutants.ExecRequest) (gomutants.MutantResult, error) {
				return gomutants.MutantResult{ID: request.Mutant, Outcome: gomutants.OutcomeInconclusive}, nil
			},
		},
		{
			name: "an exact original control that failed before mutation",
			control: func(context.Context, gomutants.ExecRequest) (gomutants.CommandResult, error) {
				return gomutants.CommandResult{ExitCode: 1, Output: []byte("the original failed")}, nil
			},
			exec: func(request gomutants.ExecRequest) (gomutants.MutantResult, error) {
				return gomutants.MutantResult{ID: request.Mutant, Outcome: gomutants.OutcomeKilled}, nil
			},
		},
		{
			name: "a timeout whose original timed out",
			control: func(context.Context, gomutants.ExecRequest) (gomutants.CommandResult, error) {
				return gomutants.CommandResult{TimedOut: true}, nil
			},
			exec: func(request gomutants.ExecRequest) (gomutants.MutantResult, error) {
				return gomutants.MutantResult{ID: request.Mutant, Outcome: gomutants.OutcomeTimedOut}, nil
			},
		},
		{
			name: "a timeout whose original failed",
			control: func(context.Context, gomutants.ExecRequest) (gomutants.CommandResult, error) {
				return gomutants.CommandResult{ExitCode: 1}, nil
			},
			exec: func(request gomutants.ExecRequest) (gomutants.MutantResult, error) {
				return gomutants.MutantResult{ID: request.Mutant, Outcome: gomutants.OutcomeTimedOut}, nil
			},
		},
		{
			name: "a timeout without an original control",
			exec: func(request gomutants.ExecRequest) (gomutants.MutantResult, error) {
				return gomutants.MutantResult{ID: request.Mutant, Outcome: gomutants.OutcomeTimedOut}, nil
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			mutant := evidenceMutant("mutant-a")
			early := evidenceIdentity("TestEarly", goanalysis.KindTest)
			targets := test.targets
			if targets == nil {
				targets = []TargetEvidence{evidenceTarget("TestEarly", goanalysis.KindTest, 3*time.Millisecond)}
			}
			index := suiteEvidenceIndex(nil, map[targetIdentity]string{early: digestText("early-key")},
				map[targetIdentity]bool{early: true}, map[string]string{evidenceModule: digestText("suite-key")})
			catalog := gomutants.Catalog{Mutants: []gomutants.Mutant{mutant}}
			session := &mutationUnitSession{catalog: catalog, exec: test.exec}

			if _, err := evaluateMutationsForTest(t.Context(), session, targets, MutationOptions{
				Evidence: index, OriginalControl: test.control,
			}); err != nil {
				t.Fatal(err)
			}
			if records := index.store(catalog, evidenceModule).Records; len(records) != 0 {
				t.Fatalf("recorded %+v, want nothing a later run could reuse", records)
			}
		})
	}
}

func TestEvaluateMutationsDoesNotRetryATimeoutIntoASurvival(t *testing.T) {
	t.Parallel()
	mutant := evidenceMutant("mutant-a")
	early := evidenceIdentity("TestEarly", goanalysis.KindTest)
	key := digestText("early-key")
	index := suiteEvidenceIndex(nil, map[targetIdentity]string{early: key},
		map[targetIdentity]bool{early: true}, map[string]string{evidenceModule: digestText("suite-key")})
	attempts := 0
	session := &mutationUnitSession{
		catalog: gomutants.Catalog{Mutants: []gomutants.Mutant{mutant}},
		exec: func(request gomutants.ExecRequest) (gomutants.MutantResult, error) {
			attempts++
			outcome := gomutants.OutcomeTimedOut
			if attempts > initialMutationAttemptCount {
				outcome = gomutants.OutcomeSurvived
			}
			return gomutants.MutantResult{ID: request.Mutant, Outcome: outcome}, nil
		},
	}
	passingControl := func(context.Context, gomutants.ExecRequest) (gomutants.CommandResult, error) {
		return gomutants.CommandResult{}, nil
	}

	evaluation, err := evaluateMutationsForTest(t.Context(), session,
		[]TargetEvidence{evidenceTarget("TestEarly", goanalysis.KindTest, 3*time.Millisecond)},
		MutationOptions{Evidence: index, OriginalControl: passingControl})

	if err != nil {
		t.Fatal(err)
	}
	records := index.store(session.catalog, evidenceModule).Records
	if attempts != initialMutationAttemptCount || len(records) != 0 || len(evaluation.Findings) != 1 ||
		evaluation.Findings[0].Kind != "mutation-timeout" {
		t.Fatalf("records = %+v, evaluation = %+v", records, evaluation)
	}
}

func TestASurvivedRecordReplacesAContradictedKill(t *testing.T) {
	t.Parallel()
	early := evidenceIdentity("TestEarly", goanalysis.KindTest)
	key := digestText("early-key")
	for _, test := range []struct {
		name     string
		loaded   func(gomutants.Mutant) evidence.MutationRecord
		outcome  gomutants.Outcome
		want     string
		disputed string
	}{
		{
			name: "a kill the tests no longer make",
			loaded: func(mutant gomutants.Mutant) evidence.MutationRecord {
				return killedEvidenceRecord(mutant, early, digestText("stale-key"))
			},
			outcome: gomutants.OutcomeSurvived, want: evidence.MutationOutcomeSurvived,
		},
		{
			name: "a survival the tests now contradict",
			loaded: func(mutant gomutants.Mutant) evidence.MutationRecord {
				return survivedEvidenceRecord(mutant, exhaustedKey(early, digestText("stale-key")))
			},
			outcome: gomutants.OutcomeKilled, want: evidence.MutationOutcomeKilled,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			mutant := evidenceMutant("mutant-a")
			loaded := test.loaded(mutant)
			index := evidenceIndex([]evidence.MutationRecord{loaded},
				map[targetIdentity]string{early: key}, map[targetIdentity]bool{early: true})
			catalog := gomutants.Catalog{Mutants: []gomutants.Mutant{mutant}}
			session := &mutationUnitSession{catalog: catalog, exec: func(request gomutants.ExecRequest) (gomutants.MutantResult, error) {
				return gomutants.MutantResult{ID: request.Mutant, Outcome: test.outcome}, nil
			}}

			if _, err := evaluateMutationsForTest(t.Context(), session, []TargetEvidence{
				evidenceTarget("TestEarly", goanalysis.KindTest, 3*time.Millisecond),
			}, MutationOptions{Evidence: index}); err != nil {
				t.Fatal(err)
			}
			records := index.store(catalog, evidenceModule).Records
			if len(records) != 1 || records[0].Outcome != test.want ||
				records[0].Provenance != "snapshot="+digestText("this-run") {
				t.Fatalf("store = %+v, want one %s record of this run", records, test.want)
			}
		})
	}
}

func TestAResumedMutantKeepsTheReuseItWasCheckpointedWith(t *testing.T) {
	t.Parallel()
	mutant := evidenceMutant("mutant-a")
	provenance := "snapshot=" + digestText("earlier-run")
	catalog := gomutants.Catalog{Mutants: []gomutants.Mutant{mutant}}
	session := refusingSession(t, catalog)

	evaluation, err := evaluateMutationsForTest(t.Context(), session, nil, MutationOptions{
		Resume: map[string]MutationEvaluation{mutant.ID: {
			Evidence:   []report.Evidence{{Kind: "mutation", ID: mutant.ID, Status: "killed", Detail: "TestEarly"}},
			Provenance: provenance,
		}},
	})

	if err != nil {
		t.Fatal(err)
	}
	if len(evaluation.Mutants) != 1 || !evaluation.Mutants[0].Reused ||
		evaluation.Mutants[0].Provenance != provenance || evaluation.Accounting.ReusedKilled != 1 {
		t.Fatalf("dispositions = %+v, accounting = %+v", evaluation.Mutants, evaluation.Accounting)
	}
}

func TestAReusedMutantIsCheckpointedWithItsProvenance(t *testing.T) {
	t.Parallel()
	mutant := evidenceMutant("mutant-a")
	early := evidenceIdentity("TestEarly", goanalysis.KindTest)
	key := digestText("early-key")
	record := survivedEvidenceRecord(mutant, exhaustedKey(early, key))
	index := evidenceIndex([]evidence.MutationRecord{record},
		map[targetIdentity]string{early: key}, map[targetIdentity]bool{early: true})
	catalog := gomutants.Catalog{Mutants: []gomutants.Mutant{mutant}}
	session := refusingSession(t, catalog)
	saved := make(map[string]MutationEvaluation)

	if _, err := evaluateMutationsForTest(t.Context(), session, []TargetEvidence{
		evidenceTarget("TestEarly", goanalysis.KindTest, 3*time.Millisecond),
	}, MutationOptions{
		Evidence:   index,
		Checkpoint: func(id string, evaluation MutationEvaluation) { saved[id] = evaluation },
	}); err != nil {
		t.Fatal(err)
	}
	if unit, checkpointed := saved[mutant.ID]; !checkpointed || unit.Provenance != record.Provenance {
		t.Fatalf("checkpointed %+v, want the provenance of the run that established the verdict", saved)
	}
}

func TestMutationEvidenceBoundaryCases(t *testing.T) {
	t.Parallel()
	early := evidenceIdentity("TestEarly", goanalysis.KindTest)
	late := evidenceIdentity("TestLate", goanalysis.KindTest)
	fuzzer := evidenceIdentity("FuzzValue", goanalysis.KindFuzz)
	keys := map[targetIdentity]string{
		early: digestText("early-key"), late: digestText("late-key"), fuzzer: digestText("fuzz-key"),
	}
	passed := map[targetIdentity]bool{early: true, late: true, fuzzer: true}
	suites := map[string]string{evidenceModule: digestText("suite-key")}
	reaches := func(names ...string) []TargetEvidence {
		targets := make([]TargetEvidence, 0, len(names))
		for index, name := range names {
			kind := goanalysis.KindTest
			if strings.HasPrefix(name, "Fuzz") {
				kind = goanalysis.KindFuzz
			}
			targets = append(targets, evidenceTarget(name, kind, time.Duration(index+1)*time.Millisecond))
		}
		return targets
	}

	for _, test := range []struct {
		name    string
		record  func(gomutants.Mutant) *evidence.MutationRecord
		targets []TargetEvidence
		reuse   bool
	}{
		{name: "no record at all", targets: reaches("TestEarly")},
		{
			name: "a killed record whose killer still reaches unchanged", reuse: true,
			targets: reaches("TestEarly"),
			record: func(mutant gomutants.Mutant) *evidence.MutationRecord {
				record := killedEvidenceRecord(mutant, early, keys[early])
				return &record
			},
		},
		{
			name: "a killed record whose killer is a fuzz target", reuse: true, targets: reaches("FuzzValue"),
			record: func(mutant gomutants.Mutant) *evidence.MutationRecord {
				record := killedEvidenceRecord(mutant, fuzzer, keys[fuzzer])
				return &record
			},
		},
		{
			name: "a survived record covering the whole reaching set", reuse: true,
			targets: reaches("TestEarly"),
			record: func(mutant gomutants.Mutant) *evidence.MutationRecord {
				record := survivedEvidenceRecord(mutant, exhaustedKey(early, keys[early]), exhaustedKey(late, keys[late]))
				return &record
			},
		},
		{
			name:    "a survived record a target has entered the reaching set of",
			targets: reaches("TestEarly", "TestLate"),
			record: func(mutant gomutants.Mutant) *evidence.MutationRecord {
				record := survivedEvidenceRecord(mutant, exhaustedKey(early, keys[early]))
				return &record
			},
		},
		{
			name: "a survived record a fuzz target reaches", reuse: true, targets: reaches("TestEarly", "FuzzValue"),
			record: func(mutant gomutants.Mutant) *evidence.MutationRecord {
				record := survivedEvidenceRecord(mutant, exhaustedKey(early, keys[early]), exhaustedKey(fuzzer, keys[fuzzer]))
				return &record
			},
		},
		{
			name: "an unreached record whose package suite is unchanged", reuse: true,
			record: func(mutant gomutants.Mutant) *evidence.MutationRecord {
				record := unreachedEvidenceRecord(mutant, suites[evidenceModule])
				return &record
			},
		},
		{
			name: "an unreached record a target now reaches", targets: reaches("TestEarly"),
			record: func(mutant gomutants.Mutant) *evidence.MutationRecord {
				record := unreachedEvidenceRecord(mutant, suites[evidenceModule])
				return &record
			},
		},
		{
			name: "a record about another mutant", targets: reaches("TestEarly"),
			record: func(gomutants.Mutant) *evidence.MutationRecord {
				other := evidenceMutant("mutant-b")
				record := survivedEvidenceRecord(other, exhaustedKey(early, keys[early]))
				return &record
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			mutant := evidenceMutant("mutant-a")
			var records []evidence.MutationRecord
			if test.record != nil {
				records = append(records, *test.record(mutant))
			}
			index := suiteEvidenceIndex(records, keys, passed, suites)
			catalog := gomutants.Catalog{Mutants: []gomutants.Mutant{mutant}}
			session := &mutationUnitSession{catalog: catalog}

			evaluation, err := evaluateMutationsForTest(t.Context(), session, test.targets, MutationOptions{
				Evidence: index,
			})

			if err != nil {
				t.Fatal(err)
			}
			if evaluation.Mutants[0].Reused != test.reuse {
				t.Fatalf("reused = %t, want %t (%d executions)",
					evaluation.Mutants[0].Reused, test.reuse, len(session.requests))
			}
			if test.reuse && len(session.requests) != 0 {
				t.Fatalf("a reused mutant was executed: %+v", session.requests)
			}
			if !test.reuse && len(session.requests) == 0 {
				t.Fatal("a mutant no record answers for was not executed")
			}
		})
	}
}
