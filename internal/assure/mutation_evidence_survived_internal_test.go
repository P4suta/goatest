// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package assure

import (
	"context"
	"reflect"
	"slices"
	"testing"
	"time"

	gomutants "github.com/P4suta/go-mutants"
	"github.com/P4suta/goatest/internal/evidence"
	goanalysis "github.com/P4suta/goatest/internal/golang"
	"github.com/P4suta/goatest/internal/report"
)

// exhaustedKey names one target of an exhausted set the way a record names it.
func exhaustedKey(identity targetIdentity, key string) evidence.TargetKey {
	return evidence.TargetKey{Package: identity.pkg, Name: identity.name, Kind: identity.kind, Key: key}
}

// survivedEvidenceRecord is an earlier run's record of a mutant every test
// that reached it was run against, and that none of them killed.
func survivedEvidenceRecord(mutant gomutants.Mutant, exhausted ...evidence.TargetKey) evidence.MutationRecord {
	return evidence.MutationRecord{
		MutantID: mutant.ID, Path: mutant.Path, Package: mutant.Package,
		Outcome: evidence.MutationOutcomeSurvived, Provenance: "snapshot=" + digestText("earlier-run"),
		Exhausted: exhausted,
		Finding:   &evidence.FindingSeed{Kind: "surviving-mutant", Summary: mutationSurvivedSummary},
	}
}

// TestEvaluateMutationsReusesASurvivorWhoseReachingSetIsFullyExhausted pins the
// universal half of the claim. A survived verdict says that no test reaching
// the mutant kills it, so it is this run's verdict exactly when every test that
// reaches the mutant now was already run against it under the same behaviour
// key and passed its baseline here.
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

	evaluation, err := EvaluateMutations(t.Context(), session, []TargetEvidence{
		evidenceTarget("TestEarly", goanalysis.KindTest, 3*time.Millisecond),
	}, MutationOptions{Root: t.TempDir(), Contract: "standard-v1", Evidence: index})
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

// TestEvaluateMutationsExecutesASurvivorWhenANewTargetEntersTheReachingSet pins
// the direction that would cost assurance. A test the recording run never ran
// against the mutant is a test that may kill it, so the universal claim is
// simply not about this run's reaching set and the mutant is executed.
func TestEvaluateMutationsExecutesASurvivorWhenANewTargetEntersTheReachingSet(t *testing.T) {
	t.Parallel()
	mutant := evidenceMutant("mutant-a")
	early := evidenceIdentity("TestEarly", goanalysis.KindTest)
	late := evidenceIdentity("TestLate", goanalysis.KindTest)
	keys := map[targetIdentity]string{early: digestText("early-key"), late: digestText("late-key")}
	index := evidenceIndex([]evidence.MutationRecord{survivedEvidenceRecord(mutant, exhaustedKey(early, keys[early]))},
		keys, map[targetIdentity]bool{early: true, late: true})
	session := &mutationUnitSession{catalog: gomutants.Catalog{Mutants: []gomutants.Mutant{mutant}}}

	evaluation, err := EvaluateMutations(t.Context(), session, []TargetEvidence{
		evidenceTarget("TestEarly", goanalysis.KindTest, 3*time.Millisecond),
		evidenceTarget("TestLate", goanalysis.KindTest, 5*time.Millisecond),
	}, MutationOptions{Root: t.TempDir(), Contract: "standard-v1", Evidence: index})
	if err != nil {
		t.Fatal(err)
	}
	if len(session.requests) != 2 || evaluation.Mutants[0].Reused || evaluation.Accounting.ReusedSurvived != 0 {
		t.Fatalf("evaluation = %+v, requests = %+v", evaluation.Mutants, session.requests)
	}
}

// TestEvaluateMutationsReusesASurvivorWhenATargetLeftTheReachingSet pins the
// direction that is sound. A test that no longer reaches the mutant cannot
// kill it, so a reaching set that is a subset of the exhausted one is still
// covered by the recorded claim.
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

	evaluation, err := EvaluateMutations(t.Context(), session, []TargetEvidence{
		evidenceTarget("TestEarly", goanalysis.KindTest, 3*time.Millisecond),
	}, MutationOptions{Root: t.TempDir(), Contract: "standard-v1", Evidence: index})
	if err != nil {
		t.Fatal(err)
	}
	if len(session.requests) != 0 || !evaluation.Mutants[0].Reused || evaluation.Accounting.ReusedSurvived != 1 {
		t.Fatalf("evaluation = %+v, requests = %+v", evaluation.Mutants, session.requests)
	}
}

// TestEvaluateMutationsExecutesASurvivorWhoseReachingTargetKeyChanged pins the
// other half of "the same test": a target whose binary reads something that
// changed is, for the purpose of a recorded verdict, a different target, and
// the universal claim was never made about it.
func TestEvaluateMutationsExecutesASurvivorWhoseReachingTargetKeyChanged(t *testing.T) {
	t.Parallel()
	mutant := evidenceMutant("mutant-a")
	early := evidenceIdentity("TestEarly", goanalysis.KindTest)
	index := evidenceIndex(
		[]evidence.MutationRecord{survivedEvidenceRecord(mutant, exhaustedKey(early, digestText("recorded-key")))},
		map[targetIdentity]string{early: digestText("current-key")}, map[targetIdentity]bool{early: true})
	session := &mutationUnitSession{catalog: gomutants.Catalog{Mutants: []gomutants.Mutant{mutant}}}

	evaluation, err := EvaluateMutations(t.Context(), session, []TargetEvidence{
		evidenceTarget("TestEarly", goanalysis.KindTest, 3*time.Millisecond),
	}, MutationOptions{Root: t.TempDir(), Contract: "standard-v1", Evidence: index})
	if err != nil {
		t.Fatal(err)
	}
	if len(session.requests) != 1 || evaluation.Mutants[0].Reused || evaluation.Accounting.ReusedSurvived != 0 {
		t.Fatalf("evaluation = %+v, requests = %+v", evaluation.Mutants, session.requests)
	}
}

// TestEvaluateMutationsExecutesASurvivorWhoseReachingTargetDidNotPass pins the
// control a reused survivor stands on, exactly as a reused kill does: this run
// has to have seen the target pass on the original tree, or there is nothing
// saying the target still runs at all.
func TestEvaluateMutationsExecutesASurvivorWhoseReachingTargetDidNotPass(t *testing.T) {
	t.Parallel()
	mutant := evidenceMutant("mutant-a")
	early := evidenceIdentity("TestEarly", goanalysis.KindTest)
	key := digestText("early-key")
	index := evidenceIndex([]evidence.MutationRecord{survivedEvidenceRecord(mutant, exhaustedKey(early, key))},
		map[targetIdentity]string{early: key}, map[targetIdentity]bool{})
	session := &mutationUnitSession{catalog: gomutants.Catalog{Mutants: []gomutants.Mutant{mutant}}}

	evaluation, err := EvaluateMutations(t.Context(), session, []TargetEvidence{
		evidenceTarget("TestEarly", goanalysis.KindTest, 3*time.Millisecond),
	}, MutationOptions{Root: t.TempDir(), Contract: "standard-v1", Evidence: index})
	if err != nil {
		t.Fatal(err)
	}
	if len(session.requests) != 1 || evaluation.Mutants[0].Reused {
		t.Fatalf("evaluation = %+v, requests = %+v", evaluation.Mutants, session.requests)
	}
}

// TestEvaluateMutationsNeverReusesASurvivorReachedByAFuzzTarget pins both
// directions of the one target a survived verdict is never about. Fuzzing
// explores past the corpus a run measured it on, so "this budget found
// nothing" is not "no input kills it", and a survivor a fuzz target reaches is
// neither recorded nor believed.
func TestEvaluateMutationsNeverReusesASurvivorReachedByAFuzzTarget(t *testing.T) {
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

	evaluation, err := EvaluateMutations(t.Context(), session, []TargetEvidence{
		evidenceTarget("TestEarly", goanalysis.KindTest, 3*time.Millisecond),
		evidenceTarget("FuzzValue", goanalysis.KindFuzz, 5*time.Millisecond),
	}, MutationOptions{Root: t.TempDir(), Contract: "standard-v1", Evidence: index})
	if err != nil {
		t.Fatal(err)
	}
	if len(session.requests) == 0 || evaluation.Mutants[0].Reused {
		t.Fatalf("a survivor a fuzz target reaches was reused: %+v", evaluation.Mutants)
	}
	// The run reached the same verdict by executing everything, and left the
	// store exactly as it found it: nothing about this mutant is a claim a
	// later run could make again.
	records := index.store(catalog, evidenceModule).Records
	if len(records) != 1 || !reflect.DeepEqual(records[0], loaded) {
		t.Fatalf("store = %+v, want only the record it was given", records)
	}
}

// TestEvaluateMutationsExecutesAMutantWhoseReachingTargetCameFromACheckpoint
// pins the boundary between the two layers. A target restored from a
// checkpoint carries no coverage blocks, so routing keeps it for the whole
// file: the reaching set it belongs to is wider than the one this run measured,
// and a claim about the measured set is not a claim about that.
func TestEvaluateMutationsExecutesAMutantWhoseReachingTargetCameFromACheckpoint(t *testing.T) {
	t.Parallel()
	mutant := evidenceMutant("mutant-a")
	early := evidenceIdentity("TestEarly", goanalysis.KindTest)
	resumed := evidenceIdentity("TestResumed", goanalysis.KindTest)
	keys := map[targetIdentity]string{early: digestText("early-key"), resumed: digestText("resumed-key")}
	passed := map[targetIdentity]bool{early: true, resumed: true}
	index := evidenceIndex([]evidence.MutationRecord{survivedEvidenceRecord(mutant,
		exhaustedKey(early, keys[early]), exhaustedKey(resumed, keys[resumed]))}, keys, passed)
	catalog := gomutants.Catalog{Mutants: []gomutants.Mutant{mutant}}
	session := &mutationUnitSession{catalog: catalog}

	evaluation, err := EvaluateMutations(t.Context(), session, []TargetEvidence{
		evidenceTarget("TestEarly", goanalysis.KindTest, 3*time.Millisecond),
		resumedBlockTarget("TestResumed", 5*time.Millisecond),
	}, MutationOptions{
		Root: t.TempDir(), Contract: "standard-v1", Evidence: index,
		Instrumented: blockRoutingInstrumentation(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(session.requests) == 0 || evaluation.Mutants[0].Reused {
		t.Fatalf("a mutant a resumed target reaches was reused: %+v", evaluation.Mutants)
	}
	// Nor is anything recorded about it: what this run exhausted is not the set
	// its own measurement names.
	if records := index.store(catalog, evidenceModule).Records; len(records) != 1 ||
		records[0].Provenance != "snapshot="+digestText("earlier-run") {
		t.Fatalf("store = %+v, want the record it was given", records)
	}
}

// TestEvaluateMutationsRecordsTheSurvivorsItExhausted pins what a run leaves
// for the next one. A survivor every reaching target ran against and none
// killed is a universal claim over exactly those targets, so each of them is
// named with the behaviour key it had, beside the finding the verdict raised.
func TestEvaluateMutationsRecordsTheSurvivorsItExhausted(t *testing.T) {
	t.Parallel()
	mutant := evidenceMutant("mutant-a")
	early := evidenceIdentity("TestEarly", goanalysis.KindTest)
	late := evidenceIdentity("TestLate", goanalysis.KindTest)
	keys := map[targetIdentity]string{early: digestText("early-key"), late: digestText("late-key")}
	index := evidenceIndex(nil, keys, map[targetIdentity]bool{early: true, late: true})
	catalog := gomutants.Catalog{Mutants: []gomutants.Mutant{mutant}}
	session := &mutationUnitSession{catalog: catalog}

	if _, err := EvaluateMutations(t.Context(), session, []TargetEvidence{
		evidenceTarget("TestEarly", goanalysis.KindTest, 3*time.Millisecond),
		evidenceTarget("TestLate", goanalysis.KindTest, 5*time.Millisecond),
	}, MutationOptions{Root: t.TempDir(), Contract: "standard-v1", Evidence: index}); err != nil {
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

// TestEvaluateMutationsDoesNotRecordASurvivorNoTestWasRunFor pins the one
// survivor that is left alone. A mutant whose whole reaching set a proof
// discharged was settled without executing anything, so re-deriving it costs
// nothing and the proofs are their own evidence; recording it would store a
// claim about tests that never ran.
func TestEvaluateMutationsDoesNotRecordASurvivorNoTestWasRunFor(t *testing.T) {
	t.Parallel()
	mutant := evidenceMutant("mutant-a")
	mutant.Probed, mutant.Index = true, 4
	early := evidenceIdentity("TestEarly", goanalysis.KindTest)
	index := evidenceIndex(nil, map[targetIdentity]string{early: digestText("early-key")},
		map[targetIdentity]bool{early: true})
	catalog := gomutants.Catalog{Mutants: []gomutants.Mutant{mutant}}
	target := evidenceTarget("TestEarly", goanalysis.KindTest, 3*time.Millisecond)
	target.Probed = true
	session := refusingSession(t, catalog)

	evaluation, err := EvaluateMutations(t.Context(), session, []TargetEvidence{target}, MutationOptions{
		Root: t.TempDir(), Contract: "standard-v1", Evidence: index,
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

// TestReusedFindingsAreRegeneratedThroughThisRunsAcceptance pins that a reused
// verdict never carries an old acceptance with it. A record holds the kind and
// the summary of the finding and nothing else, so the finding is raised again
// here and this run's acceptances decide it: an acceptance that has since
// expired resurrects the finding, and a live one still silences it.
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

			evaluation, err := EvaluateMutations(t.Context(), session, []TargetEvidence{
				evidenceTarget("TestEarly", goanalysis.KindTest, 3*time.Millisecond),
			}, MutationOptions{
				Root: t.TempDir(), Contract: "standard-v1", Evidence: index, Accepted: test.accepted,
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

// suiteEvidenceIndex indexes the given records for a run that knows what each
// named package's suite does, beside what each named target does.
func suiteEvidenceIndex(records []evidence.MutationRecord, keys map[targetIdentity]string,
	passed map[targetIdentity]bool, suites map[string]string,
) *MutationEvidence {
	return newMutationEvidence(
		evidence.MutationStore{Schema: evidence.MutationSchemaV1, ModulePath: evidenceModule, Records: records},
		keys, passed, suites, "snapshot="+digestText("this-run"),
	)
}

// unreachedEvidenceRecord is an earlier run's record of a mutant no measured
// target reached, and whose package suite did not kill it.
func unreachedEvidenceRecord(mutant gomutants.Mutant, key string) evidence.MutationRecord {
	return evidence.MutationRecord{
		MutantID: mutant.ID, Path: mutant.Path, Package: mutant.Package,
		Outcome: evidence.MutationOutcomeUnreached, Provenance: "snapshot=" + digestText("earlier-run"),
		Suite:   &evidence.SuiteKey{Package: mutant.Package, Key: key},
		Finding: &evidence.FindingSeed{Kind: "unreached-mutant", Summary: mutationUnreachedSummary},
	}
}

// TestEvaluateMutationsReusesAnUnreachedMutantOnlyWhenThePackageSuiteKeyMatches
// pins what an unreached verdict is a statement about. No target reached the
// mutation, so the claim is the package suite's and not any one target's: it is
// this run's claim exactly when the suite still does what it did, which is the
// conjunction of every target of the package and of what the package-level run
// itself reads.
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

			evaluation, err := EvaluateMutations(t.Context(), session, nil, MutationOptions{
				Root: t.TempDir(), Contract: "standard-v1", Evidence: index,
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

// TestEvaluateMutationsExecutesAnUnreachedMutantATargetNowReaches pins the
// direction the widened claim falls in. A mutant a target now reaches is no
// longer a statement about the package suite at all, so the recorded one says
// nothing about it and the reaching targets run.
func TestEvaluateMutationsExecutesAnUnreachedMutantATargetNowReaches(t *testing.T) {
	t.Parallel()
	mutant := evidenceMutant("mutant-a")
	early := evidenceIdentity("TestEarly", goanalysis.KindTest)
	key := digestText("early-key")
	index := suiteEvidenceIndex([]evidence.MutationRecord{unreachedEvidenceRecord(mutant, digestText("suite-key"))},
		map[targetIdentity]string{early: key}, map[targetIdentity]bool{early: true},
		map[string]string{evidenceModule: digestText("suite-key")})
	session := &mutationUnitSession{catalog: gomutants.Catalog{Mutants: []gomutants.Mutant{mutant}}}

	evaluation, err := EvaluateMutations(t.Context(), session, []TargetEvidence{
		evidenceTarget("TestEarly", goanalysis.KindTest, 3*time.Millisecond),
	}, MutationOptions{Root: t.TempDir(), Contract: "standard-v1", Evidence: index})
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

// TestEvaluateMutationsRecordsAnUnreachedMutantAgainstItsPackageSuite pins what
// a run leaves for the next one about a mutant nothing reached: the suite that
// ran it and the key that suite had, so that a later run can tell the same
// suite from one whose targets changed.
func TestEvaluateMutationsRecordsAnUnreachedMutantAgainstItsPackageSuite(t *testing.T) {
	t.Parallel()
	mutant := evidenceMutant("mutant-a")
	key := digestText("suite-key")
	index := suiteEvidenceIndex(nil, nil, nil, map[string]string{evidenceModule: key})
	catalog := gomutants.Catalog{Mutants: []gomutants.Mutant{mutant}}
	session := &mutationUnitSession{catalog: catalog}

	if _, err := EvaluateMutations(t.Context(), session, nil, MutationOptions{
		Root: t.TempDir(), Contract: "standard-v1", Evidence: index,
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

// TestEvaluateMutationsRecordsNoSuiteVerdictForAPackageThisRunDidNotMeasure
// pins the fail-closed side of the suite key. A package with a target this run
// restored from a checkpoint or never saw pass is a package whose suite this
// run cannot describe, so it names no key and nothing about it is written down.
func TestEvaluateMutationsRecordsNoSuiteVerdictForAPackageThisRunDidNotMeasure(t *testing.T) {
	t.Parallel()
	mutant := evidenceMutant("mutant-a")
	index := suiteEvidenceIndex(nil, nil, nil, nil)
	catalog := gomutants.Catalog{Mutants: []gomutants.Mutant{mutant}}
	session := &mutationUnitSession{catalog: catalog}

	if _, err := EvaluateMutations(t.Context(), session, nil, MutationOptions{
		Root: t.TempDir(), Contract: "standard-v1", Evidence: index,
	}); err != nil {
		t.Fatal(err)
	}
	if records := index.store(catalog, evidenceModule).Records; len(records) != 0 {
		t.Fatalf("recorded %+v, want nothing about a suite this run cannot name", records)
	}
}

// timedOutEvidenceRecord is an earlier run's record of a mutant that ran out of
// time under one of the targets that reach it.
func timedOutEvidenceRecord(mutant gomutants.Mutant, exhausted ...evidence.TargetKey) evidence.MutationRecord {
	return evidence.MutationRecord{
		MutantID: mutant.ID, Path: mutant.Path, Package: mutant.Package,
		Outcome: evidence.MutationOutcomeTimedOut, Provenance: "snapshot=" + digestText("earlier-run"),
		Exhausted: exhausted,
		Finding:   &evidence.FindingSeed{Kind: "mutation-timeout", Summary: mutationTargetTimeoutSummary},
	}
}

// timingOutSession answers every execution with a timeout, which is how a
// mutant that does not terminate under a target reaches its verdict.
func timingOutSession(catalog gomutants.Catalog) *mutationUnitSession {
	return &mutationUnitSession{catalog: catalog, exec: func(request gomutants.ExecRequest) (gomutants.MutantResult, error) {
		return gomutants.MutantResult{ID: request.Mutant, Outcome: gomutants.OutcomeTimedOut}, nil
	}}
}

// TestEvaluateMutationsReusesATimedOutMutantUnderTheUniversalCondition pins the
// one reuse that is not a proof. A timeout says the run could not settle the
// mutant, so reusing it keeps a finding rather than removing one, which is the
// direction that cannot cost assurance; the condition is still the universal
// one, so a mutant a new test now reaches runs again.
func TestEvaluateMutationsReusesATimedOutMutantUnderTheUniversalCondition(t *testing.T) {
	t.Parallel()
	mutant := evidenceMutant("mutant-a")
	early := evidenceIdentity("TestEarly", goanalysis.KindTest)
	key := digestText("early-key")
	record := timedOutEvidenceRecord(mutant, exhaustedKey(early, key))
	index := evidenceIndex([]evidence.MutationRecord{record},
		map[targetIdentity]string{early: key}, map[targetIdentity]bool{early: true})
	catalog := gomutants.Catalog{Mutants: []gomutants.Mutant{mutant}}
	session := refusingSession(t, catalog)

	evaluation, err := EvaluateMutations(t.Context(), session, []TargetEvidence{
		evidenceTarget("TestEarly", goanalysis.KindTest, 3*time.Millisecond),
	}, MutationOptions{Root: t.TempDir(), Contract: "standard-v1", Evidence: index})
	if err != nil {
		t.Fatal(err)
	}
	if len(session.requests) != 0 {
		t.Fatalf("executions = %+v, want none", session.requests)
	}
	if len(evaluation.Findings) != 1 || evaluation.Findings[0].Kind != "mutation-timeout" ||
		evaluation.Findings[0].Summary != mutationTargetTimeoutSummary {
		t.Fatalf("findings = %+v, want the recorded timeout", evaluation.Findings)
	}
	// A timeout is not a verdict about the mutant, so the disposition is
	// inconclusive: the reuse is visible in the flag and its provenance, and in
	// no counter, because a reused timeout was never a kill or a survivor.
	if !evaluation.Mutants[0].Reused || evaluation.Mutants[0].Status != report.MutantInconclusive {
		t.Fatalf("dispositions = %+v", evaluation.Mutants)
	}
	accounting := report.MutantAccounting{Discovered: 1, Selected: 1, Executed: 1, Inconclusive: 1}
	if !reflect.DeepEqual(evaluation.Accounting, accounting) {
		t.Fatalf("accounting = %+v, want %+v", evaluation.Accounting, accounting)
	}
}

// TestEvaluateMutationsRecordsATimeoutAgainstTheTargetsItRan pins what a run
// writes down about a mutant it could not settle: the targets it did run,
// including the one time ran out under, each with the behaviour key it had.
func TestEvaluateMutationsRecordsATimeoutAgainstTheTargetsItRan(t *testing.T) {
	t.Parallel()
	mutant := evidenceMutant("mutant-a")
	early := evidenceIdentity("TestEarly", goanalysis.KindTest)
	late := evidenceIdentity("TestLate", goanalysis.KindTest)
	keys := map[targetIdentity]string{early: digestText("early-key"), late: digestText("late-key")}
	index := evidenceIndex(nil, keys, map[targetIdentity]bool{early: true, late: true})
	catalog := gomutants.Catalog{Mutants: []gomutants.Mutant{mutant}}
	// The cheapest target runs first and times out, so the second target never
	// runs and is not part of what this run exhausted.
	session := timingOutSession(catalog)

	if _, err := EvaluateMutations(t.Context(), session, []TargetEvidence{
		evidenceTarget("TestEarly", goanalysis.KindTest, 3*time.Millisecond),
		evidenceTarget("TestLate", goanalysis.KindTest, 5*time.Millisecond),
	}, MutationOptions{Root: t.TempDir(), Contract: "standard-v1", Evidence: index}); err != nil {
		t.Fatal(err)
	}
	want := evidence.MutationRecord{
		MutantID: mutant.ID, Path: mutant.Path, Package: mutant.Package,
		Outcome: evidence.MutationOutcomeTimedOut, Provenance: "snapshot=" + digestText("this-run"),
		Exhausted: []evidence.TargetKey{exhaustedKey(early, keys[early])},
		Finding:   &evidence.FindingSeed{Kind: "mutation-timeout", Summary: mutationTargetTimeoutSummary},
	}
	records := index.store(catalog, evidenceModule).Records
	if len(records) != 1 || !reflect.DeepEqual(records[0], want) {
		t.Fatalf("records = %+v, want [%+v]", records, want)
	}
}

// TestEvaluateMutationsReusesAPackageSuiteTimeoutAgainstTheSuiteKey pins the
// other shape a timeout takes. A mutant no target reached is left to the
// package suite, so a suite that ran out of time is a statement about the
// suite and names no targets at all.
func TestEvaluateMutationsReusesAPackageSuiteTimeoutAgainstTheSuiteKey(t *testing.T) {
	t.Parallel()
	mutant := evidenceMutant("mutant-a")
	key := digestText("suite-key")
	recorded := evidence.MutationRecord{
		MutantID: mutant.ID, Path: mutant.Path, Package: mutant.Package,
		Outcome: evidence.MutationOutcomeTimedOut, Provenance: "snapshot=" + digestText("earlier-run"),
		Suite:   &evidence.SuiteKey{Package: mutant.Package, Key: key},
		Finding: &evidence.FindingSeed{Kind: "mutation-timeout", Summary: mutationSuiteTimeoutSummary},
	}
	index := suiteEvidenceIndex([]evidence.MutationRecord{recorded}, nil, nil,
		map[string]string{evidenceModule: key})
	catalog := gomutants.Catalog{Mutants: []gomutants.Mutant{mutant}}
	session := refusingSession(t, catalog)

	evaluation, err := EvaluateMutations(t.Context(), session, nil, MutationOptions{
		Root: t.TempDir(), Contract: "standard-v1", Evidence: index,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(session.requests) != 0 || !evaluation.Mutants[0].Reused ||
		evaluation.Mutants[0].Status != report.MutantInconclusive {
		t.Fatalf("evaluation = %+v, requests = %+v", evaluation.Mutants, session.requests)
	}
	if len(evaluation.Findings) != 1 || evaluation.Findings[0].Summary != mutationSuiteTimeoutSummary {
		t.Fatalf("findings = %+v, want the recorded suite timeout", evaluation.Findings)
	}
}

// TestEvaluateMutationsRecordsAPackageSuiteTimeoutAgainstTheSuiteKey is the
// recording half of the same shape.
func TestEvaluateMutationsRecordsAPackageSuiteTimeoutAgainstTheSuiteKey(t *testing.T) {
	t.Parallel()
	mutant := evidenceMutant("mutant-a")
	key := digestText("suite-key")
	index := suiteEvidenceIndex(nil, nil, nil, map[string]string{evidenceModule: key})
	catalog := gomutants.Catalog{Mutants: []gomutants.Mutant{mutant}}
	session := timingOutSession(catalog)

	if _, err := EvaluateMutations(t.Context(), session, nil, MutationOptions{
		Root: t.TempDir(), Contract: "standard-v1", Evidence: index,
	}); err != nil {
		t.Fatal(err)
	}
	want := evidence.MutationRecord{
		MutantID: mutant.ID, Path: mutant.Path, Package: mutant.Package,
		Outcome: evidence.MutationOutcomeTimedOut, Provenance: "snapshot=" + digestText("this-run"),
		Suite:   &evidence.SuiteKey{Package: mutant.Package, Key: key},
		Finding: &evidence.FindingSeed{Kind: "mutation-timeout", Summary: mutationSuiteTimeoutSummary},
	}
	records := index.store(catalog, evidenceModule).Records
	if len(records) != 1 || !reflect.DeepEqual(records[0], want) {
		t.Fatalf("records = %+v, want [%+v]", records, want)
	}
}

// TestEvaluateMutationsNeverReusesFlakyOrInconclusiveEvidence pins the outcomes
// a run refuses to write down at all. A control that failed, a kill that did
// not reproduce, and an outcome the engine could not determine each say that
// this run could not observe the mutant reliably; a later run that reused one
// would be reusing an answer nobody ever had.
func TestEvaluateMutationsNeverReusesFlakyOrInconclusiveEvidence(t *testing.T) {
	t.Parallel()
	passingControl := func(context.Context, gomutants.ExecRequest) (gomutants.CommandResult, error) {
		return gomutants.CommandResult{}, nil
	}
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
			name: "a control that failed before the kill was confirmed",
			control: func(context.Context, gomutants.ExecRequest) (gomutants.CommandResult, error) {
				return gomutants.CommandResult{ExitCode: 1, Output: []byte("the original failed")}, nil
			},
			exec: func(request gomutants.ExecRequest) (gomutants.MutantResult, error) {
				return gomutants.MutantResult{ID: request.Mutant, Outcome: gomutants.OutcomeKilled}, nil
			},
		},
		{
			name: "a kill that did not reproduce", control: passingControl,
			exec: func() func(gomutants.ExecRequest) (gomutants.MutantResult, error) {
				attempts := 0
				return func(request gomutants.ExecRequest) (gomutants.MutantResult, error) {
					attempts++
					if attempts == 1 {
						return gomutants.MutantResult{ID: request.Mutant, Outcome: gomutants.OutcomeKilled}, nil
					}
					return gomutants.MutantResult{ID: request.Mutant, Outcome: gomutants.OutcomeSurvived}, nil
				}
			}(),
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

			if _, err := EvaluateMutations(t.Context(), session, targets, MutationOptions{
				Root: t.TempDir(), Contract: "standard-v1", Evidence: index, OriginalControl: test.control,
			}); err != nil {
				t.Fatal(err)
			}
			if records := index.store(catalog, evidenceModule).Records; len(records) != 0 {
				t.Fatalf("recorded %+v, want nothing a later run could reuse", records)
			}
		})
	}
}

// TestASurvivedRecordReplacesAContradictedKill pins how a stale record is
// removed: by being contradicted. Every mutant a run executes produces a fresh
// record, and this run's record wins over the one it was read from, so a kill
// the tests no longer make and a survival they now make each replace the other
// without anything having to expire a record or decide it is old.
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

			if _, err := EvaluateMutations(t.Context(), session, []TargetEvidence{
				evidenceTarget("TestEarly", goanalysis.KindTest, 3*time.Millisecond),
			}, MutationOptions{Root: t.TempDir(), Contract: "standard-v1", Evidence: index}); err != nil {
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

// TestAResumedMutantKeepsTheReuseItWasCheckpointedWith pins the seam between
// the two layers of saved work. A checkpoint carries a mutant's verdict across
// an interrupted run inside one input digest; when that verdict was itself
// resolved from an earlier run's evidence, the run that resumes it did not
// observe it either, so the provenance travels with it and the report says the
// same thing it would have said had the run not been interrupted.
func TestAResumedMutantKeepsTheReuseItWasCheckpointedWith(t *testing.T) {
	t.Parallel()
	mutant := evidenceMutant("mutant-a")
	provenance := "snapshot=" + digestText("earlier-run")
	catalog := gomutants.Catalog{Mutants: []gomutants.Mutant{mutant}}
	session := refusingSession(t, catalog)

	evaluation, err := EvaluateMutations(t.Context(), session, nil, MutationOptions{
		Root: t.TempDir(), Contract: "standard-v1",
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

// TestAReusedMutantIsCheckpointedWithItsProvenance is the other half of the
// same seam: what the interrupted run wrote down is what the resumed run reads.
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

	if _, err := EvaluateMutations(t.Context(), session, []TargetEvidence{
		evidenceTarget("TestEarly", goanalysis.KindTest, 3*time.Millisecond),
	}, MutationOptions{
		Root: t.TempDir(), Contract: "standard-v1", Evidence: index,
		Checkpoint: func(id string, evaluation MutationEvaluation) { saved[id] = evaluation },
	}); err != nil {
		t.Fatal(err)
	}
	if unit, checkpointed := saved[mutant.ID]; !checkpointed || unit.Provenance != record.Provenance {
		t.Fatalf("checkpointed %+v, want the provenance of the run that established the verdict", saved)
	}
}
