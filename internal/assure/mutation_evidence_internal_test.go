// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package assure

import (
	"context"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	gomutants "github.com/P4suta/go-mutants"
	"github.com/P4suta/goatest/internal/evidence"
	goanalysis "github.com/P4suta/goatest/internal/golang"
	"github.com/P4suta/goatest/internal/report"
	"github.com/P4suta/goatest/internal/trace"
)

// evidenceModule is the module every fixture in this file belongs to, and the
// identity a store is only trusted under.
const evidenceModule = "fixture.example/module"

// evidenceTarget is one measured target that ran value.go:7-9, the block every
// mutant in this file lives in.
func evidenceTarget(name string, kind goanalysis.TargetKind, duration time.Duration) TargetEvidence {
	target := blockTarget(name, duration, goanalysis.CoverageBlock{StartLine: 7, StartColumn: 2, EndLine: 9, EndColumn: 3})
	target.Target.Kind = kind
	return target
}

// evidenceMutant is one accepted mutant inside that block.
func evidenceMutant(name string) gomutants.Mutant {
	return gomutants.Mutant{
		ID: digestText(name), DisplayID: "arithmetic#1", Accepted: true, Rule: "arithmetic",
		Path: "value.go", Line: 8, Column: 5, Package: evidenceModule,
	}
}

// evidenceIdentity names a target the way a record names it: by what
// -test.run selects, never by a target ID, which carries a line number.
func evidenceIdentity(name string, kind goanalysis.TargetKind) targetIdentity {
	return targetIdentity{pkg: evidenceModule, name: name, kind: string(kind)}
}

// killedEvidenceRecord is an earlier run's record of one confirmed kill.
func killedEvidenceRecord(mutant gomutants.Mutant, killer targetIdentity, key string) evidence.MutationRecord {
	return evidence.MutationRecord{
		MutantID: mutant.ID, Path: mutant.Path, Package: mutant.Package,
		Outcome: evidence.MutationOutcomeKilled, Provenance: "snapshot=" + digestText("earlier-run"),
		KilledBy: &evidence.TargetKey{Package: killer.pkg, Name: killer.name, Kind: killer.kind, Key: key},
	}
}

// evidenceIndex indexes the given records for a run in which every named
// target carries the given behaviour key and passed the baseline.
func evidenceIndex(records []evidence.MutationRecord, keys map[targetIdentity]string, passed map[targetIdentity]bool) *MutationEvidence {
	return newMutationEvidence(
		evidence.MutationStore{Schema: evidence.MutationSchemaV1, ModulePath: evidenceModule, Records: records},
		keys, passed, "snapshot="+digestText("this-run"),
	)
}

// refusingSession fails the test if the evaluation executes anything.
func refusingSession(t *testing.T, catalog gomutants.Catalog) *mutationUnitSession {
	t.Helper()
	return &mutationUnitSession{catalog: catalog, exec: func(gomutants.ExecRequest) (gomutants.MutantResult, error) {
		t.Error("a reused mutant was executed")
		return gomutants.MutantResult{Outcome: gomutants.OutcomeSurvived}, nil
	}}
}

// TestEvaluateMutationsReusesAKilledMutantWithoutExecutingIt pins the whole
// claim of a reused kill: nothing runs, and what the run reports is what a
// fresh confirmed kill by the same target reports, beside the provenance of
// the run that established it.
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

	evaluation, err := EvaluateMutations(t.Context(), session, []TargetEvidence{
		evidenceTarget("TestEarly", goanalysis.KindTest, 3*time.Millisecond),
	}, MutationOptions{Root: t.TempDir(), Contract: "standard-v1", Evidence: index})
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

// TestEvaluateMutationsExecutesAKilledMutantWhoseKillerLeftTheReachingSet
// pins that a record is only ever reused about a target this run would have
// run: a killer coverage no longer routes to proves nothing about the mutant
// now.
func TestEvaluateMutationsExecutesAKilledMutantWhoseKillerLeftTheReachingSet(t *testing.T) {
	t.Parallel()
	mutant := evidenceMutant("mutant-a")
	absent := evidenceIdentity("TestGone", goanalysis.KindTest)
	key := digestText("gone-key")
	index := evidenceIndex([]evidence.MutationRecord{killedEvidenceRecord(mutant, absent, key)},
		map[targetIdentity]string{absent: key}, map[targetIdentity]bool{absent: true})
	session := &mutationUnitSession{catalog: gomutants.Catalog{Mutants: []gomutants.Mutant{mutant}}}

	evaluation, err := EvaluateMutations(t.Context(), session, []TargetEvidence{
		evidenceTarget("TestEarly", goanalysis.KindTest, 3*time.Millisecond),
	}, MutationOptions{Root: t.TempDir(), Contract: "standard-v1", Evidence: index})
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

// TestEvaluateMutationsExecutesAKilledMutantWhoseKillerKeyChanged pins the
// other half: the target is still routed to, but it is not the target the
// record is about, because something the test binary reads has changed.
func TestEvaluateMutationsExecutesAKilledMutantWhoseKillerKeyChanged(t *testing.T) {
	t.Parallel()
	mutant := evidenceMutant("mutant-a")
	killer := evidenceIdentity("TestEarly", goanalysis.KindTest)
	index := evidenceIndex([]evidence.MutationRecord{killedEvidenceRecord(mutant, killer, digestText("recorded-key"))},
		map[targetIdentity]string{killer: digestText("current-key")}, map[targetIdentity]bool{killer: true})
	session := &mutationUnitSession{catalog: gomutants.Catalog{Mutants: []gomutants.Mutant{mutant}}}

	evaluation, err := EvaluateMutations(t.Context(), session, []TargetEvidence{
		evidenceTarget("TestEarly", goanalysis.KindTest, 3*time.Millisecond),
	}, MutationOptions{Root: t.TempDir(), Contract: "standard-v1", Evidence: index})
	if err != nil {
		t.Fatal(err)
	}
	if len(session.requests) != 1 || evaluation.Mutants[0].Reused || evaluation.Accounting.ReusedKilled != 0 {
		t.Fatalf("evaluation = %+v, requests = %+v", evaluation, session.requests)
	}
}

// TestEvaluateMutationsExecutesAKilledMutantWhoseBaselineTargetDidNotPass pins
// the control a reused kill stands on. The recording run confirmed the kill
// against an original that passed; this run's fresh evidence that the original
// still passes is its own baseline running that target. Without it there is no
// control, and the mutant is executed.
func TestEvaluateMutationsExecutesAKilledMutantWhoseBaselineTargetDidNotPass(t *testing.T) {
	t.Parallel()
	mutant := evidenceMutant("mutant-a")
	killer := evidenceIdentity("TestEarly", goanalysis.KindTest)
	key := digestText("early-key")
	index := evidenceIndex([]evidence.MutationRecord{killedEvidenceRecord(mutant, killer, key)},
		map[targetIdentity]string{killer: key}, map[targetIdentity]bool{})
	session := &mutationUnitSession{catalog: gomutants.Catalog{Mutants: []gomutants.Mutant{mutant}}}

	evaluation, err := EvaluateMutations(t.Context(), session, []TargetEvidence{
		evidenceTarget("TestEarly", goanalysis.KindTest, 3*time.Millisecond),
	}, MutationOptions{Root: t.TempDir(), Contract: "standard-v1", Evidence: index})
	if err != nil {
		t.Fatal(err)
	}
	if len(session.requests) != 1 || evaluation.Mutants[0].Reused || evaluation.Accounting.ReusedKilled != 0 {
		t.Fatalf("evaluation = %+v, requests = %+v", evaluation, session.requests)
	}
}

// TestEvaluateMutationsNeverReusesAFuzzLoopKill pins both directions of the
// one kill that is not a repeatable claim: fuzzing found an input this time,
// which says nothing about the next budget, so a fuzz kill is neither recorded
// nor believed.
func TestEvaluateMutationsNeverReusesAFuzzLoopKill(t *testing.T) {
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
	session := &mutationUnitSession{catalog: catalog, exec: func(request gomutants.ExecRequest) (gomutants.MutantResult, error) {
		if slices.ContainsFunc(request.Args, func(argument string) bool { return strings.HasPrefix(argument, "-test.fuzz=") }) {
			return gomutants.MutantResult{ID: request.Mutant, Outcome: gomutants.OutcomeKilled}, nil
		}
		return gomutants.MutantResult{ID: request.Mutant, Outcome: gomutants.OutcomeSurvived}, nil
	}}

	evaluation, err := EvaluateMutations(t.Context(), session, targets, MutationOptions{
		Root: t.TempDir(), Contract: "standard-v1", Evidence: index,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(session.requests) == 0 || evaluation.Mutants[0].Reused {
		t.Fatalf("a fuzz kill was reused: %+v", evaluation.Mutants)
	}
	// Fuzzing killed the mutant in this run too, and the store is exactly what
	// it was: the kill added nothing, so nothing about it can be believed
	// later. The record that was already there is kept because the mutant is
	// still in the catalogue, and it stays as unusable as it was.
	records := index.store(catalog, evidenceModule).Records
	if len(records) != 1 || !reflect.DeepEqual(records[0], loaded) {
		t.Fatalf("store = %+v, want only the record it was given", records)
	}
}

// TestEvaluateMutationsRecordsAReusedRouteInTheTrace pins what an audit reads:
// the route says the verdict was reused, its plan is the reuse itself, and no
// execution is recorded for the mutant, because none happened.
func TestEvaluateMutationsRecordsAReusedRouteInTheTrace(t *testing.T) {
	t.Parallel()
	mutant := evidenceMutant("mutant-a")
	killer := evidenceIdentity("TestEarly", goanalysis.KindTest)
	key := digestText("early-key")
	index := evidenceIndex([]evidence.MutationRecord{killedEvidenceRecord(mutant, killer, key)},
		map[targetIdentity]string{killer: key}, map[targetIdentity]bool{killer: true})
	sink, recorder := newTraceRecording()
	catalog := gomutants.Catalog{Mutants: []gomutants.Mutant{mutant}}
	session := newTracedSession(refusingSession(t, catalog), recorder)

	if _, err := EvaluateMutations(t.Context(), session, []TargetEvidence{
		evidenceTarget("TestEarly", goanalysis.KindTest, 3*time.Millisecond),
	}, MutationOptions{
		Root: t.TempDir(), Contract: "standard-v1", Evidence: index,
		Trace: recorder, Instrumented: blockRoutingInstrumentation(),
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

// TestEvaluateMutationsRecordsAConfirmedKillAndNothingElse pins what this run
// is willing to write down. Only a kill one named target confirmed is a claim
// a later run can check: a survivor, an inconclusive outcome, a kill that did
// not reproduce, a control that failed, and a kill by a batch of targets each
// leave the store as they found it.
func TestEvaluateMutationsRecordsAConfirmedKillAndNothingElse(t *testing.T) {
	t.Parallel()
	passingControl := func(context.Context, gomutants.ExecRequest) (gomutants.CommandResult, error) {
		return gomutants.CommandResult{}, nil
	}
	for _, test := range []struct {
		name    string
		targets []TargetEvidence
		exec    func(gomutants.ExecRequest) (gomutants.MutantResult, error)
		control func(context.Context, gomutants.ExecRequest) (gomutants.CommandResult, error)
		killer  string
	}{
		{
			name: "a kill one target confirmed", killer: "TestEarly",
			exec: func(request gomutants.ExecRequest) (gomutants.MutantResult, error) {
				return gomutants.MutantResult{ID: request.Mutant, Outcome: gomutants.OutcomeKilled}, nil
			},
		},
		{name: "a survivor"},
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
		{
			name: "a kill by a batch of targets", targets: reachedMutationTargets(),
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
			if _, err := EvaluateMutations(t.Context(), session, targets, MutationOptions{
				Root: t.TempDir(), Contract: "standard-v1", Evidence: index, OriginalControl: test.control,
			}); err != nil {
				t.Fatal(err)
			}
			records := index.store(catalog, evidenceModule).Records
			if test.killer == "" {
				if len(records) != 0 {
					t.Fatalf("recorded %+v, want nothing", records)
				}
				return
			}
			want := evidence.MutationRecord{
				MutantID: mutant.ID, Path: mutant.Path, Package: mutant.Package,
				Outcome: evidence.MutationOutcomeKilled, Provenance: "snapshot=" + digestText("this-run"),
				KilledBy: &evidence.TargetKey{
					Package: evidenceModule, Name: test.killer, Kind: string(goanalysis.KindTest), Key: digestText(test.killer),
				},
			}
			if len(records) != 1 || !reflect.DeepEqual(records[0], want) {
				t.Fatalf("records = %+v, want [%+v]", records, want)
			}
		})
	}
}

// TestEvaluateMutationsDoesNotRecordEvidenceForResumedMutants pins the
// boundary between the two layers. A checkpoint keeps a mutant's verdict
// inside one input digest and keeps no reaching set with it, so a resumed
// mutant carries nothing a later digest could check a reuse against.
func TestEvaluateMutationsDoesNotRecordEvidenceForResumedMutants(t *testing.T) {
	t.Parallel()
	mutant := evidenceMutant("mutant-a")
	killer := evidenceIdentity("TestEarly", goanalysis.KindTest)
	index := evidenceIndex(nil, map[targetIdentity]string{killer: digestText("early-key")}, map[targetIdentity]bool{killer: true})
	catalog := gomutants.Catalog{Mutants: []gomutants.Mutant{mutant}}
	session := refusingSession(t, catalog)

	evaluation, err := EvaluateMutations(t.Context(), session, []TargetEvidence{
		evidenceTarget("TestEarly", goanalysis.KindTest, 3*time.Millisecond),
	}, MutationOptions{
		Root: t.TempDir(), Contract: "standard-v1", Evidence: index,
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

// TestMutationEvidenceCollectsConcurrentlyWithoutRacing runs the workers the
// mutation phase runs, because collection happens on all of them at once.
func TestMutationEvidenceCollectsConcurrentlyWithoutRacing(t *testing.T) {
	t.Parallel()
	killer := evidenceIdentity("TestEarly", goanalysis.KindTest)
	key := digestText("early-key")
	catalog := gomutants.Catalog{}
	records := make([]evidence.MutationRecord, 0, 32)
	for index := range 32 {
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

	evaluation, err := EvaluateMutations(t.Context(), session, []TargetEvidence{
		evidenceTarget("TestEarly", goanalysis.KindTest, 3*time.Millisecond),
	}, MutationOptions{Root: t.TempDir(), Contract: "standard-v1", Jobs: 8, Evidence: index})
	if err != nil || evaluation.Accounting.Killed != 32 || evaluation.Accounting.ReusedKilled != 16 {
		t.Fatalf("evaluation = (%+v, %v)", evaluation.Accounting, err)
	}
	// Every mutant is killed, half of them without running: the sixteen that
	// ran are recorded, and the sixteen that were reused keep the record they
	// were reused from.
	if stored := index.store(catalog, evidenceModule).Records; len(stored) != 32 {
		t.Fatalf("stored %d records, want 32", len(stored))
	}
}

// TestTargetBehaviorKeyReadsEveryInputOfTheTestBinaryAndNothingElse pins the
// builder against the allowlist from the other side: every input the key is
// built from invalidates it, and everything a run may differ in without
// changing what a target does leaves it alone.
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
		return evidence.TargetBehaviorKey(sources.inputsFor(of))
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
			sources.commandTimeout = 11 * time.Minute
		}},
		{name: "the target timeout", want: true, change: func(sources *targetKeySources) {
			sources.targetTimeout = 13 * time.Minute
		}},
		{name: "the goatest version", want: true, change: func(sources *targetKeySources) {
			sources.inputs.GoatestVersion = "v9.9.9"
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

// TestTargetBehaviorKeyIgnoresTheDiagnosticsAndTheParallelism holds the key to
// the same rule the cache identity lives under: asking for a trace, keeping the
// temporary directories, or running more mutants at once changes how a run is
// observed and how long it takes, never what a target does.
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
	if evidence.TargetBehaviorKey(quiet.inputsFor(target)) != evidence.TargetBehaviorKey(noisy.inputsFor(target)) {
		t.Fatal("a diagnostic or the parallelism entered a behaviour key")
	}
}

// TestTargetBehaviorKeyOfARepositoryReaderCoversTheWholeTree pins the one
// target whose closure is not the answer. A test that reads the repository as
// data can change its verdict when a file no closure of its own names changes,
// and no key built from a closure would notice; the key of such a target is
// therefore built from the whole snapshot, so that it survives only an
// identical tree. Every other target keys its closure exactly as before.
func TestTargetBehaviorKeyOfARepositoryReaderCoversTheWholeTree(t *testing.T) {
	t.Parallel()
	target := goanalysis.Target{
		ID: "target-TestValue", Name: "TestValue", Kind: goanalysis.KindTest, Package: evidenceModule,
		RelativeDir: ".", Path: "value_test.go", Line: 5,
		Dependencies: []string{evidenceModule + "/internal/helper", "fmt"},
	}
	key := func(sources targetKeySources) string { return evidence.TargetBehaviorKey(sources.inputsFor(target)) }
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
	if key(repositoryReaderKeyFixture(readers)) == key(targetKeyFixture()) {
		t.Fatal("a repository reader keys the same inputs as a package that reads no directory")
	}
	if key(repositoryReaderKeyFixture(readers)) != key(repositoryReaderKeyFixture(readers)) {
		t.Fatal("the builder is not deterministic for a repository reader")
	}
}

// targetKeyFixture is a module with one package that depends on another, a
// testdata file, an embedded template, a fuzz corpus, and files outside every
// closure, so that a key can be asked about each of them.
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

// repositoryReaderKeyFixture is that same module read by a run that found the
// named packages reading directories they compute rather than files they name.
func repositoryReaderKeyFixture(readers map[string]bool) targetKeySources {
	sources := targetKeyFixture()
	return newTargetKeySources(sources.inputs, sources.model, sources.contract,
		Options{CommandTimeout: 7 * time.Minute, TargetTimeout: 3 * time.Minute}, readers)
}
