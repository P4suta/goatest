// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package assure

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	gomutants "github.com/P4suta/go-mutants"
	"github.com/P4suta/goatest/internal/evidence"
	"github.com/P4suta/goatest/internal/filemode"
	goanalysis "github.com/P4suta/goatest/internal/golang"
	"github.com/P4suta/goatest/internal/report"
	"github.com/P4suta/goatest/internal/trace"
)

const (
	concurrentEvidenceMutants       = 32
	concurrentEvidenceReuseDivisor  = 2
	concurrentReusedEvidenceMutants = concurrentEvidenceMutants / concurrentEvidenceReuseDivisor
	lazyWholeTreeTargetCount        = 2
	changedCommandTimeout           = 11 * time.Minute
	changedTargetTimeout            = 13 * time.Minute
)

func TestWholeTreeEvidenceKeysAreGeneratedOnlyWhenRequired(t *testing.T) {
	t.Parallel()
	sources := repositoryReaderKeyFixture(map[string]bool{evidenceModule: true})
	targets := make([]TargetEvidence, 0, lazyWholeTreeTargetCount)
	inventory := make([]report.TargetDisposition, 0, lazyWholeTreeTargetCount)
	for index := range lazyWholeTreeTargetCount {
		target := evidenceTarget("TestLazy"+strconv.Itoa(index), goanalysis.KindTest, time.Millisecond)
		targets = append(targets, target)
		inventory = append(inventory, report.TargetDisposition{
			Name: target.Target.Name, Kind: string(target.Target.Kind), Package: target.Target.Package, Status: "passed",
		})
	}
	index := newRunMutationEvidence(evidence.MutationStore{}, sources, targets, inventory, nil, digestText("snapshot"))
	if len(index.wholeKeys) != 0 || len(index.wholeSuites) != 0 {
		t.Fatalf("eager whole keys = (%d, %d)", len(index.wholeKeys), len(index.wholeSuites))
	}
	first := targets[0]
	first.WholeTree = true
	if key, whole := index.targetKey(first); key == "" || !whole {
		t.Fatalf("whole target key = (%q, %t)", key, whole)
	}
	if len(index.wholeKeys) != 1 || len(index.wholeSuites) != 0 {
		t.Fatalf("target generation = (%d, %d)", len(index.wholeKeys), len(index.wholeSuites))
	}
	if key, whole := index.suiteKey(evidenceModule, true); key == "" || !whole {
		t.Fatalf("whole suite key = (%q, %t)", key, whole)
	}
	if len(index.wholeKeys) != lazyWholeTreeTargetCount || len(index.wholeSuites) != 1 {
		t.Fatalf("suite generation = (%d, %d)", len(index.wholeKeys), len(index.wholeSuites))
	}
}

func TestMutationExecutionThatReadsTheRepositoryRecordsAWholeTreeKey(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "value.go"), []byte("package fixture\n"), filemode.PrivateFile); err != nil {
		t.Fatal(err)
	}
	target := evidenceTarget("TestEarly", goanalysis.KindTest, 3*time.Millisecond)
	sources := repositoryReaderKeyFixture(map[string]bool{evidenceModule: true})
	sources.model.ModuleDir = root
	observer := newRepositoryObserver(root, t.TempDir(), map[string]goanalysis.RepositoryReadCandidate{
		evidenceModule: {},
	}, sources)
	index := newRunMutationEvidence(evidence.MutationStore{}, sources, []TargetEvidence{target}, []report.TargetDisposition{{
		Name: target.Target.Name, Kind: string(target.Target.Kind), Package: target.Target.Package, Status: "passed",
	}}, nil, digestText("snapshot"))
	mutant := evidenceMutant("repository-reader")
	session := &mutationUnitSession{catalog: gomutants.Catalog{Mutants: []gomutants.Mutant{mutant}}, exec: func(request gomutants.ExecRequest) (gomutants.MutantResult, error) {
		log, _ := repositoryTestLogPath(request.Args)
		if err := os.WriteFile(log, []byte("# test log\nopen "+root+"\n"), filemode.PrivateFile); err != nil {
			return gomutants.MutantResult{}, err
		}
		return gomutants.MutantResult{ID: request.Mutant, Outcome: gomutants.OutcomeSurvived}, nil
	}}

	if _, err := evaluateMutationsForTest(t.Context(), session, []TargetEvidence{target}, MutationOptions{
		Evidence: index, RepositoryObserver: observer,
	}); err != nil {
		t.Fatal(err)
	}
	records := index.store(session.catalog, evidenceModule).Records
	if len(records) != 1 || len(records[0].Exhausted) != 1 || !records[0].Exhausted[0].WholeTree {
		t.Fatalf("recorded evidence = %+v, want a whole-tree exhausted target", records)
	}
	want := sources.targetKey(target.Target, target.Environment, true)
	if records[0].Exhausted[0].Key != want {
		t.Fatalf("whole-tree key = %q, want %q", records[0].Exhausted[0].Key, want)
	}
}

func TestSingleComparativeKillRecordsItsRepositoryObservation(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "value.go"), []byte("package fixture\n"), filemode.PrivateFile); err != nil {
		t.Fatal(err)
	}
	target := evidenceTarget("TestEarly", goanalysis.KindTest, 3*time.Millisecond)
	sources := repositoryReaderKeyFixture(map[string]bool{evidenceModule: true})
	sources.model.ModuleDir = root
	observer := newRepositoryObserver(root, t.TempDir(), map[string]goanalysis.RepositoryReadCandidate{
		evidenceModule: {},
	}, sources)
	index := newRunMutationEvidence(evidence.MutationStore{}, sources, []TargetEvidence{target}, []report.TargetDisposition{{
		Name: target.Target.Name, Kind: string(target.Target.Kind), Package: target.Target.Package, Status: "passed",
	}}, nil, digestText("snapshot"))
	mutant := evidenceMutant("repository-reader-kill")
	calls := 0
	session := &mutationUnitSession{catalog: gomutants.Catalog{Mutants: []gomutants.Mutant{mutant}}, exec: func(request gomutants.ExecRequest) (gomutants.MutantResult, error) {
		calls++
		log, _ := repositoryTestLogPath(request.Args)
		contents := "# test log\nopen " + root + "\n"
		if err := os.WriteFile(log, []byte(contents), filemode.PrivateFile); err != nil {
			return gomutants.MutantResult{}, err
		}
		return gomutants.MutantResult{ID: request.Mutant, Outcome: gomutants.OutcomeKilled}, nil
	}}
	control := func(context.Context, gomutants.ExecRequest) (gomutants.CommandResult, error) {
		return gomutants.CommandResult{}, nil
	}
	if _, err := evaluateMutationsForTest(t.Context(), session, []TargetEvidence{target}, MutationOptions{
		Evidence: index, RepositoryObserver: observer, OriginalControl: control,
	}); err != nil {
		t.Fatal(err)
	}
	records := index.store(session.catalog, evidenceModule).Records
	if calls != 1 || len(records) != 1 || len(records[0].KilledBy) != 1 || !records[0].KilledBy[0].WholeTree {
		t.Fatalf("calls = %d, records = %+v; want one whole-tree kill", calls, records)
	}
}

func TestRepositoryObservationWidensAPackageSuiteRecord(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "value.go"), []byte("package fixture\n"), filemode.PrivateFile); err != nil {
		t.Fatal(err)
	}
	sources := repositoryReaderKeyFixture(map[string]bool{evidenceModule: true})
	sources.model.ModuleDir = root
	observer := newRepositoryObserver(root, t.TempDir(), map[string]goanalysis.RepositoryReadCandidate{
		evidenceModule: {},
	}, sources)
	index := newRunMutationEvidence(evidence.MutationStore{}, sources, nil, nil, nil, digestText("snapshot"))
	mutant := evidenceMutant("repository-suite")
	session := &mutationUnitSession{catalog: gomutants.Catalog{Mutants: []gomutants.Mutant{mutant}}, exec: func(request gomutants.ExecRequest) (gomutants.MutantResult, error) {
		log, _ := repositoryTestLogPath(request.Args)
		if err := os.WriteFile(log, []byte("# test log\nopen "+root+"\n"), filemode.PrivateFile); err != nil {
			return gomutants.MutantResult{}, err
		}
		return gomutants.MutantResult{ID: request.Mutant, Outcome: gomutants.OutcomeSurvived}, nil
	}}

	if _, err := evaluateMutationsForTest(t.Context(), session, nil, MutationOptions{
		Evidence: index, RepositoryObserver: observer,
		SuiteCoverage: map[string]PackageSuiteCoverage{evidenceModule: {Duration: time.Second}},
	}); err != nil {
		t.Fatal(err)
	}
	records := index.store(session.catalog, evidenceModule).Records
	if len(records) != 1 || records[0].Suite == nil || !records[0].Suite.WholeTree || records[0].Suite.Key != index.wholeSuites[evidenceModule] {
		t.Fatalf("recorded evidence = %+v, want the whole-tree suite key", records)
	}
}

func TestRepositoryObservationOfABatchWidensEverySelectedTarget(t *testing.T) {
	t.Parallel()
	const repositoryBatchTargets = 10
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "value.go"), []byte("package fixture\n"), filemode.PrivateFile); err != nil {
		t.Fatal(err)
	}
	sources := repositoryReaderKeyFixture(map[string]bool{evidenceModule: true})
	sources.model.ModuleDir = root
	observer := newRepositoryObserver(root, t.TempDir(), map[string]goanalysis.RepositoryReadCandidate{
		evidenceModule: {},
	}, sources)
	var targets []TargetEvidence
	var inventory []report.TargetDisposition
	for index := range repositoryBatchTargets {
		target := evidenceTarget("TestBatch"+strconv.Itoa(index), goanalysis.KindTest, 3*time.Millisecond)
		targets = append(targets, target)
		inventory = append(inventory, report.TargetDisposition{
			Name: target.Target.Name, Kind: string(target.Target.Kind), Package: target.Target.Package, Status: "passed",
		})
	}
	index := newRunMutationEvidence(evidence.MutationStore{}, sources, targets, inventory, nil, digestText("snapshot"))
	mutant := evidenceMutant("repository-batch")
	session := &mutationUnitSession{catalog: gomutants.Catalog{Mutants: []gomutants.Mutant{mutant}}, exec: func(request gomutants.ExecRequest) (gomutants.MutantResult, error) {
		log, _ := repositoryTestLogPath(request.Args)
		contents := "# test log\n"
		if strings.Contains(strings.Join(request.Args, " "), "|") {
			contents += "open " + root + "\n"
		}
		if err := os.WriteFile(log, []byte(contents), filemode.PrivateFile); err != nil {
			return gomutants.MutantResult{}, err
		}
		return gomutants.MutantResult{ID: request.Mutant, Outcome: gomutants.OutcomeSurvived}, nil
	}}

	if _, err := evaluateMutationsForTest(t.Context(), session, targets, MutationOptions{
		Evidence: index, RepositoryObserver: observer,
	}); err != nil {
		t.Fatal(err)
	}
	records := index.store(session.catalog, evidenceModule).Records
	if len(records) != 1 || len(records[0].Exhausted) != len(targets) {
		t.Fatalf("recorded evidence = %+v, want %d exhausted targets", records, len(targets))
	}
	for _, target := range records[0].Exhausted {
		if !target.WholeTree {
			t.Errorf("target %s did not retain the aggregate repository observation", target.Name)
		}
	}
}

func TestWholeTreeMarkerSelectsTheMatchingRepositoryBoundary(t *testing.T) {
	t.Parallel()
	mutant := evidenceMutant("reader-migration")
	target := evidenceTarget("TestEarly", goanalysis.KindTest, 3*time.Millisecond)
	identity := identify(target.Target)
	sources := repositoryReaderKeyFixture(map[string]bool{evidenceModule: true})
	narrow := sources.targetKey(target.Target, target.Environment, false)
	whole := sources.targetKey(target.Target, target.Environment, true)
	passed := []report.TargetDisposition{{
		Name: target.Target.Name, Kind: string(target.Target.Kind), Package: target.Target.Package, Status: "passed",
	}}
	route := mutationRoute{reaching: []TargetEvidence{target}}

	record := func(key string, marked bool) evidence.MutationRecord {
		return evidence.MutationRecord{
			MutantID: mutant.ID, Path: mutant.Path, Package: mutant.Package,
			Outcome: evidence.MutationOutcomeKilled, Provenance: "snapshot=" + digestText("earlier"),
			KilledBy: []evidence.TargetKey{{Package: identity.pkg, Name: identity.name, Kind: identity.kind, Key: key, WholeTree: marked}},
		}
	}
	for _, test := range []struct {
		name          string
		baselineWhole bool
		record        evidence.MutationRecord
		wantReuse     bool
	}{
		{name: "narrow record matches narrow baseline", record: record(narrow, false), wantReuse: true},
		{name: "whole key without marker is not narrow", record: record(whole, false)},
		{name: "whole record matches the available whole key", record: record(whole, true), wantReuse: true},
		{name: "whole baseline refuses a narrow record", baselineWhole: true, record: record(narrow, false)},
	} {
		t.Run(test.name, func(t *testing.T) {
			current := target
			current.WholeTree = test.baselineWhole
			index := newRunMutationEvidence(evidence.MutationStore{Records: []evidence.MutationRecord{test.record}},
				sources, []TargetEvidence{current}, passed, nil, digestText("current"))
			_, _, reused := index.reuseKill(mutant, route)
			if reused != test.wantReuse {
				t.Fatalf("reuse = %t, want %t", reused, test.wantReuse)
			}
		})
	}
}

const evidenceModule = "fixture.example/module"

func evidenceTarget(name string, kind goanalysis.TargetKind, duration time.Duration) TargetEvidence {
	target := blockTarget(name, duration, goanalysis.CoverageBlock{StartLine: 7, StartColumn: 2, EndLine: 9, EndColumn: 3})
	target.Target.Kind = kind
	return target
}

func evidenceMutant(name string) gomutants.Mutant {
	return gomutants.Mutant{
		ID: digestText(name), DisplayID: "arithmetic#1", Accepted: true, Rule: "arithmetic",
		Path: "value.go", Line: 8, Column: 5, Package: evidenceModule,
	}
}

func evidenceIdentity(name string, kind goanalysis.TargetKind) targetIdentity {
	return targetIdentity{pkg: evidenceModule, name: name, kind: string(kind)}
}

func killedEvidenceRecord(mutant gomutants.Mutant, killer targetIdentity, key string) evidence.MutationRecord {
	return evidence.MutationRecord{
		MutantID: mutant.ID, Path: mutant.Path, Package: mutant.Package,
		Outcome: evidence.MutationOutcomeKilled, Provenance: "snapshot=" + digestText("earlier-run"),
		KilledBy: []evidence.TargetKey{{Package: killer.pkg, Name: killer.name, Kind: killer.kind, Key: key}},
	}
}

func evidenceIndex(records []evidence.MutationRecord, keys map[targetIdentity]string, passed map[targetIdentity]bool) *MutationEvidence {
	return newMutationEvidence(
		evidence.MutationStore{Schema: evidence.MutationSchemaV1, ModulePath: evidenceModule, Records: records},
		keys, passed, nil, "snapshot="+digestText("this-run"),
	)
}

func refusingSession(t *testing.T, catalog gomutants.Catalog) *mutationUnitSession {
	t.Helper()
	return &mutationUnitSession{catalog: catalog, exec: func(gomutants.ExecRequest) (gomutants.MutantResult, error) {
		t.Error("a reused mutant was executed")
		return gomutants.MutantResult{Outcome: gomutants.OutcomeSurvived}, nil
	}}
}

func TestEvaluateMutationsReusesAKilledMutantWithoutExecutingIt(t *testing.T) {
	t.Parallel()
	mutant := evidenceMutant("mutant-a")
	killer := evidenceIdentity("TestEarly", goanalysis.KindTest)
	key := digestText("early-key")
	record := killedEvidenceRecord(mutant, killer, key)
	index := evidenceIndex([]evidence.MutationRecord{record},
		map[targetIdentity]string{killer: key}, map[targetIdentity]bool{killer: true})
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
	wantEvidence := []report.Evidence{{Kind: "mutation", ID: mutant.ID, Status: "killed", Detail: "TestEarly"}}
	if !reflect.DeepEqual(evaluation.Evidence, wantEvidence) || len(evaluation.Findings) != 0 {
		t.Fatalf("evaluation = %+v, want %+v", evaluation, wantEvidence)
	}
	want := report.MutantDisposition{
		ID: mutant.ID, Status: report.MutantKilled, Path: mutant.Path, Line: mutant.Line,
		Package: mutant.Package, Rule: mutant.Rule, Detail: "TestEarly",
		Reused: true, Provenance: record.Provenance,
	}
	if len(evaluation.Mutants) != 1 || !reflect.DeepEqual(evaluation.Mutants[0], want) {
		t.Fatalf("dispositions = %+v, want [%+v]", evaluation.Mutants, want)
	}
	accounting := report.MutantAccounting{Discovered: 1, Selected: 1, Executed: 1, Killed: 1, ReusedKilled: 1}
	if !reflect.DeepEqual(evaluation.Accounting, accounting) {
		t.Fatalf("accounting = %+v, want %+v", evaluation.Accounting, accounting)
	}
}

func TestAggregateKillReuseRequiresOneCompatibleExecutionGroup(t *testing.T) {
	t.Parallel()
	mutant := evidenceMutant("mutant-a")
	early := evidenceIdentity("TestEarly", goanalysis.KindTest)
	late := evidenceIdentity("TestLate", goanalysis.KindTest)
	earlyKey := digestText("early-key")
	lateKey := digestText("late-key")
	record := evidence.MutationRecord{
		MutantID: mutant.ID, Path: mutant.Path, Package: mutant.Package,
		Outcome: evidence.MutationOutcomeKilled, Provenance: "snapshot=" + digestText("earlier-run"),
		KilledBy: []evidence.TargetKey{
			exhaustedKey(early, earlyKey), exhaustedKey(late, lateKey),
		},
	}
	index := evidenceIndex([]evidence.MutationRecord{record},
		map[targetIdentity]string{early: earlyKey, late: lateKey},
		map[targetIdentity]bool{early: true, late: true})
	targets := []TargetEvidence{
		evidenceTarget("TestEarly", goanalysis.KindTest, time.Millisecond),
		evidenceTarget("TestLate", goanalysis.KindTest, time.Millisecond),
	}
	targets[1].Environment = []string{"RESOURCE=other"}
	if _, _, reused := index.reuseKill(mutant, mutationRoute{reaching: targets}); reused {
		t.Fatal("a witness spanning incompatible execution groups was reused")
	}
}

func TestEvaluateMutationsExecutesAKilledMutantWhoseKillerLeftTheReachingSet(t *testing.T) {
	t.Parallel()
	mutant := evidenceMutant("mutant-a")
	absent := evidenceIdentity("TestGone", goanalysis.KindTest)
	key := digestText("gone-key")
	index := evidenceIndex([]evidence.MutationRecord{killedEvidenceRecord(mutant, absent, key)},
		map[targetIdentity]string{absent: key}, map[targetIdentity]bool{absent: true})
	session := &mutationUnitSession{catalog: gomutants.Catalog{Mutants: []gomutants.Mutant{mutant}}}

	evaluation, err := evaluateMutationsForTest(t.Context(), session, []TargetEvidence{
		evidenceTarget("TestEarly", goanalysis.KindTest, 3*time.Millisecond),
	}, MutationOptions{Evidence: index})

	if err != nil {
		t.Fatal(err)
	}
	if len(session.requests) != 1 || len(evaluation.Findings) != 1 || evaluation.Findings[0].Kind != "surviving-mutant" {
		t.Fatalf("evaluation = %+v, requests = %+v", evaluation, session.requests)
	}
	if evaluation.Mutants[0].Reused || evaluation.Mutants[0].Provenance != "" || evaluation.Accounting.ReusedKilled != 0 {
		t.Fatalf("dispositions = %+v, accounting = %+v", evaluation.Mutants, evaluation.Accounting)
	}
}

func TestEvaluateMutationsExecutesAKilledMutantWhoseKillerKeyChanged(t *testing.T) {
	t.Parallel()
	mutant := evidenceMutant("mutant-a")
	killer := evidenceIdentity("TestEarly", goanalysis.KindTest)
	index := evidenceIndex([]evidence.MutationRecord{killedEvidenceRecord(mutant, killer, digestText("recorded-key"))},
		map[targetIdentity]string{killer: digestText("current-key")}, map[targetIdentity]bool{killer: true})
	session := &mutationUnitSession{catalog: gomutants.Catalog{Mutants: []gomutants.Mutant{mutant}}}

	evaluation, err := evaluateMutationsForTest(t.Context(), session, []TargetEvidence{
		evidenceTarget("TestEarly", goanalysis.KindTest, 3*time.Millisecond),
	}, MutationOptions{Evidence: index})

	if err != nil {
		t.Fatal(err)
	}
	if len(session.requests) != 1 || evaluation.Mutants[0].Reused || evaluation.Accounting.ReusedKilled != 0 {
		t.Fatalf("evaluation = %+v, requests = %+v", evaluation, session.requests)
	}
}

func TestEvaluateMutationsExecutesAKilledMutantWhoseBaselineTargetDidNotPass(t *testing.T) {
	t.Parallel()
	mutant := evidenceMutant("mutant-a")
	killer := evidenceIdentity("TestEarly", goanalysis.KindTest)
	key := digestText("early-key")
	index := evidenceIndex([]evidence.MutationRecord{killedEvidenceRecord(mutant, killer, key)},
		map[targetIdentity]string{killer: key}, map[targetIdentity]bool{})
	session := &mutationUnitSession{catalog: gomutants.Catalog{Mutants: []gomutants.Mutant{mutant}}}

	evaluation, err := evaluateMutationsForTest(t.Context(), session, []TargetEvidence{
		evidenceTarget("TestEarly", goanalysis.KindTest, 3*time.Millisecond),
	}, MutationOptions{Evidence: index})

	if err != nil {
		t.Fatal(err)
	}
	if len(session.requests) != 1 || evaluation.Mutants[0].Reused || evaluation.Accounting.ReusedKilled != 0 {
		t.Fatalf("evaluation = %+v, requests = %+v", evaluation, session.requests)
	}
}

func TestEvaluateMutationsReusesAnExactKillerSetWhenTheReachingSetGrows(t *testing.T) {
	t.Parallel()
	mutant := evidenceMutant("mutant-a")
	fuzzer := evidenceIdentity("FuzzValue", goanalysis.KindFuzz)
	key := digestText("fuzz-key")
	targets := []TargetEvidence{
		evidenceTarget("TestEarly", goanalysis.KindTest, 3*time.Millisecond),
		evidenceTarget("FuzzValue", goanalysis.KindFuzz, 5*time.Millisecond),
	}
	keys := map[targetIdentity]string{fuzzer: key, evidenceIdentity("TestEarly", goanalysis.KindTest): digestText("early-key")}
	passed := map[targetIdentity]bool{fuzzer: true, evidenceIdentity("TestEarly", goanalysis.KindTest): true}
	loaded := killedEvidenceRecord(mutant, fuzzer, key)
	index := evidenceIndex([]evidence.MutationRecord{loaded}, keys, passed)
	catalog := gomutants.Catalog{Mutants: []gomutants.Mutant{mutant}}
	session := &mutationUnitSession{catalog: catalog}

	evaluation, err := evaluateMutationsForTest(t.Context(), session, targets, MutationOptions{
		Evidence: index,
	})

	if err != nil {
		t.Fatal(err)
	}
	if len(session.requests) != 0 || !evaluation.Mutants[0].Reused {
		t.Fatalf("an exact historical witness was not reused: %+v", evaluation.Mutants)
	}

	records := index.store(catalog, evidenceModule).Records
	if len(records) != 1 || !reflect.DeepEqual(records[0], loaded) {
		t.Fatalf("store = %+v, want the reused witness", records)
	}
}

func TestEvaluateMutationsRecordsAReusedRouteInTheTrace(t *testing.T) {
	t.Parallel()
	mutant := evidenceMutant("mutant-a")
	killer := evidenceIdentity("TestEarly", goanalysis.KindTest)
	key := digestText("early-key")
	index := evidenceIndex([]evidence.MutationRecord{killedEvidenceRecord(mutant, killer, key)},
		map[targetIdentity]string{killer: key}, map[targetIdentity]bool{killer: true})
	sink, recorder := newTraceRecording()
	catalog := gomutants.Catalog{Mutants: []gomutants.Mutant{mutant}}
	session := refusingSession(t, catalog)

	if _, err := evaluateMutationsForTest(t.Context(), session, []TargetEvidence{
		evidenceTarget("TestEarly", goanalysis.KindTest, 3*time.Millisecond),
	}, MutationOptions{
		Evidence: index,
		Trace:    recorder, Instrumented: blockRoutingInstrumentation(),
	}); err != nil {
		t.Fatal(err)
	}
	want := trace.RouteRecord{
		MutantID: mutant.ID, Rule: "arithmetic", Path: "value.go", Line: 8, Column: 5,
		ReachingTargets: []string{"target-TestEarly"}, Plan: []string{"reused"},
		Reason: trace.ReasonCoverageReaching, Granularity: trace.GranularityBlock, FileCandidates: 1,
		Reused: true,
	}
	if routes := recordedRoutes(sink); len(routes) != 1 || !reflect.DeepEqual(routes[0], want) {
		t.Fatalf("routes = %+v, want [%+v]", routes, want)
	}
	for _, event := range sink.Events() {
		if event.Type == trace.TypeMutantExec {
			t.Fatalf("a reused mutant recorded an execution: %+v", event.Mutant)
		}
	}
}

func TestEvaluateMutationsRecordsAKillOrASurvivorAndNothingElse(t *testing.T) {
	t.Parallel()
	passingControl := func(context.Context, gomutants.ExecRequest) (gomutants.CommandResult, error) {
		return gomutants.CommandResult{}, nil
	}
	for _, test := range []struct {
		name     string
		targets  []TargetEvidence
		exec     func(gomutants.ExecRequest) (gomutants.MutantResult, error)
		control  func(context.Context, gomutants.ExecRequest) (gomutants.CommandResult, error)
		killer   bool
		survivor bool
	}{
		{
			name: "a kill by one target", killer: true,
			exec: func(request gomutants.ExecRequest) (gomutants.MutantResult, error) {
				return gomutants.MutantResult{ID: request.Mutant, Outcome: gomutants.OutcomeKilled}, nil
			},
		},
		{name: "a survivor every reaching target ran", survivor: true},
		{
			name: "an inconclusive outcome",
			exec: func(request gomutants.ExecRequest) (gomutants.MutantResult, error) {
				return gomutants.MutantResult{ID: request.Mutant, Outcome: gomutants.OutcomeInconclusive}, nil
			},
		},
		{
			name: "a kill the original control refused", control: func(context.Context, gomutants.ExecRequest) (gomutants.CommandResult, error) {
				return gomutants.CommandResult{ExitCode: 1, Output: []byte("the original failed")}, nil
			},
			exec: func(request gomutants.ExecRequest) (gomutants.MutantResult, error) {
				return gomutants.MutantResult{ID: request.Mutant, Outcome: gomutants.OutcomeKilled}, nil
			},
		},
		{
			name: "a comparative kill is not retried", control: passingControl, killer: true,
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
		{
			name: "a kill by a batch of targets", targets: reachedMutationTargets(),
			killer: true,
			exec: func(request gomutants.ExecRequest) (gomutants.MutantResult, error) {
				if slices.ContainsFunc(request.Args, func(argument string) bool { return strings.Contains(argument, "|") }) {
					return gomutants.MutantResult{ID: request.Mutant, Outcome: gomutants.OutcomeKilled}, nil
				}
				return gomutants.MutantResult{ID: request.Mutant, Outcome: gomutants.OutcomeSurvived}, nil
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			mutant := evidenceMutant("mutant-a")
			targets := test.targets
			if targets == nil {
				targets = []TargetEvidence{evidenceTarget("TestEarly", goanalysis.KindTest, 3*time.Millisecond)}
			}
			keys := make(map[targetIdentity]string, len(targets))
			passed := make(map[targetIdentity]bool, len(targets))
			for _, target := range targets {
				identity := targetIdentity{pkg: target.Target.Package, name: target.Target.Name, kind: string(target.Target.Kind)}
				keys[identity] = digestText(target.Target.Name)
				passed[identity] = true
			}
			index := evidenceIndex(nil, keys, passed)
			catalog := gomutants.Catalog{Mutants: []gomutants.Mutant{mutant}}
			session := &mutationUnitSession{catalog: catalog, exec: test.exec}
			if _, err := evaluateMutationsForTest(t.Context(), session, targets, MutationOptions{
				Evidence: index, OriginalControl: test.control,
			}); err != nil {
				t.Fatal(err)
			}
			records := index.store(catalog, evidenceModule).Records
			if test.survivor {
				if len(records) != 1 || records[0].Outcome != evidence.MutationOutcomeSurvived {
					t.Fatalf("recorded %+v, want one survived record", records)
				}
				return
			}
			if !test.killer {
				if len(records) != 0 {
					t.Fatalf("recorded %+v, want nothing", records)
				}
				return
			}
			wantKillers := make([]evidence.TargetKey, 0, len(targets))
			for _, target := range targets {
				identity := identify(target.Target)
				wantKillers = append(wantKillers, evidence.TargetKey{
					Package: identity.pkg, Name: identity.name, Kind: identity.kind, Key: digestText(identity.name),
				})
			}
			slices.SortFunc(wantKillers, func(first, second evidence.TargetKey) int {
				if order := strings.Compare(first.Package, second.Package); order != 0 {
					return order
				}
				if order := strings.Compare(first.Name, second.Name); order != 0 {
					return order
				}
				return strings.Compare(first.Kind, second.Kind)
			})
			want := evidence.MutationRecord{
				MutantID: mutant.ID, Path: mutant.Path, Package: mutant.Package,
				Outcome: evidence.MutationOutcomeKilled, Provenance: "snapshot=" + digestText("this-run"),
				KilledBy: wantKillers,
			}
			if len(records) != 1 || !reflect.DeepEqual(records[0], want) {
				t.Fatalf("records = %+v, want [%+v]", records, want)
			}
		})
	}
}

func TestEvaluateMutationsDoesNotRecordEvidenceForResumedMutants(t *testing.T) {
	t.Parallel()
	mutant := evidenceMutant("mutant-a")
	killer := evidenceIdentity("TestEarly", goanalysis.KindTest)
	index := evidenceIndex(nil, map[targetIdentity]string{killer: digestText("early-key")}, map[targetIdentity]bool{killer: true})
	catalog := gomutants.Catalog{Mutants: []gomutants.Mutant{mutant}}
	session := refusingSession(t, catalog)

	evaluation, err := evaluateMutationsForTest(t.Context(), session, []TargetEvidence{
		evidenceTarget("TestEarly", goanalysis.KindTest, 3*time.Millisecond),
	}, MutationOptions{
		Evidence: index,
		Resume: map[string]MutationEvaluation{mutant.ID: {Evidence: []report.Evidence{{
			Kind: "mutation", ID: mutant.ID, Status: "killed", Detail: "TestEarly",
		}}}},
	})

	if err != nil || len(evaluation.Evidence) != 1 || evaluation.Accounting.Killed != 1 {
		t.Fatalf("evaluation = (%+v, %v)", evaluation, err)
	}
	if records := index.store(catalog, evidenceModule).Records; len(records) != 0 {
		t.Fatalf("recorded %+v, want nothing for a resumed mutant", records)
	}
	if evaluation.Mutants[0].Reused || evaluation.Accounting.ReusedKilled != 0 {
		t.Fatalf("dispositions = %+v", evaluation.Mutants)
	}
}

func TestMutationEvidenceCollectsConcurrentlyWithoutRacing(t *testing.T) {
	t.Parallel()
	killer := evidenceIdentity("TestEarly", goanalysis.KindTest)
	key := digestText("early-key")
	catalog := gomutants.Catalog{}
	records := make([]evidence.MutationRecord, 0, concurrentEvidenceMutants)
	for index := range concurrentEvidenceMutants {
		mutant := evidenceMutant("mutant-" + strconv.Itoa(index))
		catalog.Mutants = append(catalog.Mutants, mutant)
		if index%2 == 0 {
			records = append(records, killedEvidenceRecord(mutant, killer, key))
		}
	}
	index := evidenceIndex(records, map[targetIdentity]string{killer: key}, map[targetIdentity]bool{killer: true})
	session := &mutationUnitSession{catalog: catalog, exec: func(request gomutants.ExecRequest) (gomutants.MutantResult, error) {
		return gomutants.MutantResult{ID: request.Mutant, Outcome: gomutants.OutcomeKilled}, nil
	}}

	evaluation, err := evaluateMutationsForTest(t.Context(), session, []TargetEvidence{
		evidenceTarget("TestEarly", goanalysis.KindTest, 3*time.Millisecond),
	}, MutationOptions{Jobs: 8, Evidence: index})

	if err != nil || evaluation.Accounting.Killed != concurrentEvidenceMutants || evaluation.Accounting.ReusedKilled != concurrentReusedEvidenceMutants {
		t.Fatalf("evaluation = (%+v, %v)", evaluation.Accounting, err)
	}

	if stored := index.store(catalog, evidenceModule).Records; len(stored) != concurrentEvidenceMutants {
		t.Fatalf("stored %d records, want 32", len(stored))
	}
}

func TestTargetBehaviorKeyReadsEveryInputOfTheTestBinaryAndNothingElse(t *testing.T) {
	t.Parallel()
	base := targetKeyFixture()
	target := goanalysis.Target{
		ID: "target-TestValue", Name: "TestValue", Kind: goanalysis.KindTest, Package: evidenceModule,
		RelativeDir: ".", Path: "value_test.go", Line: 5,
		Dependencies: []string{evidenceModule + "/internal/helper", "fmt"},
	}
	fuzzer := target
	fuzzer.ID, fuzzer.Name, fuzzer.Kind = "target-FuzzValue", "FuzzValue", goanalysis.KindFuzz
	key := func(sources targetKeySources, of goanalysis.Target) string {
		return evidence.TargetBehaviorKey(sources.narrowInputsFor(of))
	}
	unchanged := key(base, target)

	for _, test := range []struct {
		name   string
		change func(*targetKeySources)
		target goanalysis.Target
		want   bool
	}{
		{name: "a closure file of the target's own package", want: true, change: func(sources *targetKeySources) {
			sources.inputs.Files["value.go"] = digestText("edited")
		}},
		{name: "the target's own test file", want: true, change: func(sources *targetKeySources) {
			sources.inputs.Files["value_test.go"] = digestText("edited")
		}},
		{name: "a closure file of a dependency", want: true, change: func(sources *targetKeySources) {
			sources.inputs.Files["internal/helper/helper.go"] = digestText("edited")
		}},
		{name: "a testdata file of a closure package", want: true, change: func(sources *targetKeySources) {
			sources.inputs.Files["internal/helper/testdata/golden.txt"] = digestText("edited")
		}},
		{name: "a file a closure package embeds", want: true, change: func(sources *targetKeySources) {
			sources.inputs.Files["internal/helper/templates/page.tmpl"] = digestText("edited")
		}},
		{name: "the module manifest", want: true, change: func(sources *targetKeySources) {
			sources.inputs.Files["go.mod"] = digestText("edited")
		}},
		{name: "an external dependency", want: true, change: func(sources *targetKeySources) {
			sources.inputs.Dependencies["example.com/dependency"] = digestText("edited")
		}},
		{name: "the toolchain", want: true, change: func(sources *targetKeySources) {
			sources.inputs.Toolchain = "go version go1.27.0"
		}},
		{name: "the platform", want: true, change: func(sources *targetKeySources) {
			sources.inputs.Platform = "plan9/arm"
		}},
		{name: "the selected environment", want: true, change: func(sources *targetKeySources) {
			sources.inputs.Environment = append(slices.Clone(sources.inputs.Environment), "GOFLAGS=-trimpath")
		}},
		{name: "the contract", want: true, change: func(sources *targetKeySources) { sources.contract = "deep-v1" }},
		{name: "the test arguments", want: true, change: func(sources *targetKeySources) {
			sources.testArgs = []string{"-test.short=true"}
		}},
		{name: "the build tags", want: true, change: func(sources *targetKeySources) {
			sources.buildTags = []string{"integration"}
		}},
		{name: "the command timeout", want: true, change: func(sources *targetKeySources) {
			sources.commandTimeout = changedCommandTimeout
		}},
		{name: "the target timeout", want: true, change: func(sources *targetKeySources) {
			sources.targetTimeout = changedTargetTimeout
		}},
		{name: "the goatest version", want: true, change: func(sources *targetKeySources) {
			sources.inputs.GoatestVersion = "v9.9.9"
		}},
		{name: "the goatest build", want: true, change: func(sources *targetKeySources) {
			sources.inputs.GoatestBuild = digestText("changed build")
		}},
		{name: "the go-mutants version", want: true, change: func(sources *targetKeySources) {
			sources.inputs.GoMutantsVersion = "v9.9.9"
		}},
		{name: "the fuzz corpus of the fuzz target", want: true, target: fuzzer, change: func(sources *targetKeySources) {
			sources.inputs.Corpus["testdata/fuzz/FuzzValue/seed-a"] = digestText("edited")
		}},
		{name: "the fuzz corpus, for a test target", change: func(sources *targetKeySources) {
			sources.inputs.Corpus["testdata/fuzz/FuzzValue/seed-a"] = digestText("edited")
		}},
		{name: "a file outside the closure", change: func(sources *targetKeySources) {
			sources.inputs.Files["other/other.go"] = digestText("edited")
		}},
		{name: "a documentation file", change: func(sources *targetKeySources) {
			sources.inputs.Files["docs/notes.md"] = digestText("edited")
		}},
		{name: "the test file of another package", change: func(sources *targetKeySources) {
			sources.inputs.Files["internal/helper/helper_test.go"] = digestText("edited")
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			subject := test.target
			if subject.Name == "" {
				subject = target
			}
			before := key(targetKeyFixture(), subject)
			changed := targetKeyFixture()
			test.change(&changed)
			if got := key(changed, subject) != before; got != test.want {
				t.Fatalf("changing %s invalidated the key = %t, want %t", test.name, got, test.want)
			}
		})
	}
	if key(targetKeyFixture(), target) != unchanged {
		t.Fatal("the builder is not deterministic")
	}
}

func TestTargetBehaviorKeyIncludesTheExactExecutionEnvironment(t *testing.T) {
	t.Parallel()
	sources := targetKeyFixture()
	target := evidenceTarget("TestValue", goanalysis.KindTest, time.Millisecond)
	base := sources.targetKey(target.Target, []string{"RESOURCE=first"}, false)
	changedBase := targetKeyFixture()
	changedBase.inputs.Environment = append(changedBase.inputs.Environment, "BASE=changed")
	changedResource := sources.targetKey(target.Target, []string{"RESOURCE=second"}, false)
	if base == changedBase.targetKey(target.Target, []string{"RESOURCE=first"}, false) || base == changedResource {
		t.Fatal("a base or resource environment change retained the behaviour key")
	}

	ordered := targetKeyFixture()
	ordered.inputs.Environment = []string{"ALPHA=one", "BETA=two"}
	reordered := targetKeyFixture()
	reordered.inputs.Environment = []string{"BETA=two", "ALPHA=one"}
	if ordered.targetKey(target.Target, nil, false) != reordered.targetKey(target.Target, nil, false) {
		t.Fatal("equivalent base environment order changed the behaviour key")
	}

	overridden := targetKeyFixture()
	overridden.inputs.Environment = []string{"MODE=base", "OTHER=value", "MODE=last-base"}
	canonical := targetKeyFixture()
	canonical.inputs.Environment = []string{"MODE=resource", "OTHER=value"}
	if overridden.targetKey(target.Target, []string{"MODE=resource"}, false) != canonical.targetKey(target.Target, nil, false) {
		t.Fatal("last-wins resource overlay was not canonical")
	}
	caseVariant := targetKeyFixture()
	caseVariant.inputs.Environment = []string{"MODE=base"}
	windowsResult := targetKeyFixture()
	windowsResult.inputs.Environment = []string{"MODE=resource"}
	unixResult := targetKeyFixture()
	unixResult.inputs.Environment = []string{"MODE=base", "mode=resource"}
	wantCaseKey := unixResult.targetKey(target.Target, nil, false)
	if runtime.GOOS == "windows" {
		wantCaseKey = windowsResult.targetKey(target.Target, nil, false)
	}
	if caseVariant.targetKey(target.Target, []string{"mode=resource"}, false) != wantCaseKey {
		t.Fatal("environment key case handling differs from the process environment")
	}
	if overridden.suiteKey(evidenceModule, nil, nil, false) == canonical.suiteKey(evidenceModule, nil, nil, false) {
		t.Fatal("suite behaviour key ignored its base environment")
	}
	if ordered.suiteKey(evidenceModule, nil, []string{"RESOURCE=first"}, false) == ordered.suiteKey(evidenceModule, nil, []string{"RESOURCE=second"}, false) {
		t.Fatal("suite behaviour key ignored its resource environment")
	}
	reorderedSuite := targetKeyFixture()
	reorderedSuite.inputs.Environment = slices.Clone(ordered.inputs.Environment)
	slices.Reverse(reorderedSuite.inputs.Environment)
	if ordered.suiteKey(evidenceModule, nil, nil, false) != reorderedSuite.suiteKey(evidenceModule, nil, nil, false) {
		t.Fatal("suite behaviour key was not environment-order independent")
	}
}

func TestRunMutationEvidenceIncludesTheSuiteResourceEnvironment(t *testing.T) {
	t.Parallel()
	sources := targetKeyFixture()
	target := evidenceTarget("TestValue", goanalysis.KindTest, time.Millisecond)
	inventory := []report.TargetDisposition{{
		Name: target.Target.Name, Kind: string(target.Target.Kind), Package: target.Target.Package, Status: "passed",
	}}
	key := func(resource string) string {
		collected := newRunMutationEvidence(evidence.MutationStore{}, sources,
			[]TargetEvidence{target}, inventory, []string{"RESOURCE=" + resource}, digestText("snapshot"))
		return collected.suites[evidenceModule]
	}
	if key("first") == key("second") {
		t.Fatal("suite resource environment change retained the evidence key")
	}
}

func TestTargetBehaviorKeyIgnoresTheDiagnosticsAndTheParallelism(t *testing.T) {
	t.Parallel()
	target := goanalysis.Target{
		ID: "target-TestValue", Name: "TestValue", Kind: goanalysis.KindTest, Package: evidenceModule,
		RelativeDir: ".", Path: "value_test.go", Line: 5,
	}
	inputs := targetKeyFixture().inputs
	model := targetKeyFixture().model
	_, recorder := newTraceRecording()
	plain := Options{CommandTimeout: 7 * time.Minute}
	loud := Options{CommandTimeout: 7 * time.Minute, MutationJobs: 9, Trace: recorder, KeepTemp: true}
	quiet := newTargetKeySources(inputs, model, "standard-v1", plain, nil)
	noisy := newTargetKeySources(inputs, model, "standard-v1", loud, nil)
	if evidence.TargetBehaviorKey(quiet.narrowInputsFor(target)) != evidence.TargetBehaviorKey(noisy.narrowInputsFor(target)) {
		t.Fatal("a diagnostic or the parallelism entered a behaviour key")
	}
}

func TestTargetBehaviorKeyOfARepositoryReaderCoversTheWholeTree(t *testing.T) {
	t.Parallel()
	target := goanalysis.Target{
		ID: "target-TestValue", Name: "TestValue", Kind: goanalysis.KindTest, Package: evidenceModule,
		RelativeDir: ".", Path: "value_test.go", Line: 5,
		Dependencies: []string{evidenceModule + "/internal/helper", "fmt"},
	}
	key := func(sources targetKeySources) string {
		return evidence.TargetBehaviorKey(sources.wholeTreeInputsFor(target))
	}
	readers := map[string]bool{evidenceModule: true}

	for _, test := range []struct {
		name   string
		change func(*targetKeySources)
	}{
		{name: "a documentation file outside every closure", change: func(sources *targetKeySources) {
			sources.inputs.Files["docs/notes.md"] = digestText("edited")
		}},
		{name: "a Go file of a package the target never links", change: func(sources *targetKeySources) {
			sources.inputs.Files["other/other.go"] = digestText("edited")
		}},
		{name: "the test file of another package", change: func(sources *targetKeySources) {
			sources.inputs.Files["internal/helper/helper_test.go"] = digestText("edited")
		}},
		{name: "a fuzz corpus entry the target never reads", change: func(sources *targetKeySources) {
			sources.inputs.Corpus["testdata/fuzz/FuzzValue/seed-a"] = digestText("edited")
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			reader := repositoryReaderKeyFixture(readers)
			changed := repositoryReaderKeyFixture(readers)
			test.change(&changed)
			if key(changed) == key(reader) {
				t.Errorf("changing %s left a repository reader's key alone", test.name)
			}
			closed := targetKeyFixture()
			narrowed := targetKeyFixture()
			test.change(&narrowed)
			if key(narrowed) != key(closed) {
				t.Errorf("changing %s invalidated the key of a package that reads no directory", test.name)
			}
		})
	}
	reader := key(repositoryReaderKeyFixture(readers))
	if reader == key(targetKeyFixture()) {
		t.Fatal("a repository reader keys the same inputs as a package that reads no directory")
	}
	if reader != key(repositoryReaderKeyFixture(readers)) {
		t.Fatal("the builder is not deterministic for a repository reader")
	}
}

func targetKeyFixture() targetKeySources {
	inputs := evidence.Inputs{
		Files: map[string]string{
			"go.mod":                              digestText("go.mod"),
			"go.sum":                              digestText("go.sum"),
			"value.go":                            digestText("value.go"),
			"value_test.go":                       digestText("value_test.go"),
			"internal/helper/helper.go":           digestText("helper.go"),
			"internal/helper/helper_test.go":      digestText("helper_test.go"),
			"internal/helper/testdata/golden.txt": digestText("golden.txt"),
			"internal/helper/templates/page.tmpl": digestText("page.tmpl"),
			"other/other.go":                      digestText("other.go"),
			"docs/notes.md":                       digestText("notes.md"),
		},
		Corpus:           map[string]string{"testdata/fuzz/FuzzValue/seed-a": digestText("seed-a")},
		Dependencies:     map[string]string{"example.com/dependency": digestText("dependency")},
		Toolchain:        "go version go1.26.6",
		Platform:         "linux/amd64",
		Environment:      []string{"GOTOOLCHAIN=local"},
		GoatestVersion:   "v0.1.0-dev",
		GoatestBuild:     digestText("goatest build"),
		GoMutantsVersion: "v0.0.1",
	}
	model := goanalysis.Model{ModulePath: evidenceModule, Packages: []goanalysis.Package{
		{ImportPath: evidenceModule, RelativeDir: ".", Dependencies: []string{evidenceModule + "/internal/helper", "fmt"}},
		{
			ImportPath: evidenceModule + "/internal/helper", RelativeDir: "internal/helper",
			EmbedFiles: []string{"internal/helper/templates/page.tmpl"},
		},
		{ImportPath: evidenceModule + "/other", RelativeDir: "other"},
	}}
	return newTargetKeySources(inputs, model, "standard-v1", Options{CommandTimeout: 7 * time.Minute, TargetTimeout: 3 * time.Minute}, nil)
}

func repositoryReaderKeyFixture(readers map[string]bool) targetKeySources {
	sources := targetKeyFixture()
	return newTargetKeySources(sources.inputs, sources.model, sources.contract,
		Options{CommandTimeout: 7 * time.Minute, TargetTimeout: 3 * time.Minute}, readers)
}
