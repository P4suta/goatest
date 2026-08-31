// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package assure

import (
	"fmt"
	"reflect"
	"slices"
	"testing"
	"time"

	gomutants "github.com/P4suta/go-mutants"
	goanalysis "github.com/P4suta/goatest/internal/golang"
	"github.com/P4suta/goatest/internal/trace"
)

// recordedRoutes returns the route records of a recording in emission order.
func recordedRoutes(sink *trace.MemorySink) []trace.RouteRecord {
	var records []trace.RouteRecord
	for _, event := range sink.Events() {
		if event.Type == trace.TypeRoute && event.Route != nil {
			records = append(records, *event.Route)
		}
	}
	return records
}

// reachedMutationTargets returns nine measured tests and one fuzz target, each
// reaching value.go and each slower than the one before it, which is more
// targets than routing runs individually and enough to leave a batch behind.
func reachedMutationTargets() []TargetEvidence {
	targets := make([]TargetEvidence, 0, 10)
	for index := range 9 {
		name := fmt.Sprintf("TestValue%d", index)
		targets = append(targets, TargetEvidence{
			Target: goanalysis.Target{
				ID: "target-" + name, Name: name, Kind: goanalysis.KindTest, Package: "fixture.example/module",
			},
			CoveredFiles: []string{"value.go"}, Duration: time.Duration(index+1) * time.Millisecond,
		})
	}
	return append(targets, TargetEvidence{
		Target: goanalysis.Target{
			ID: "target-FuzzValue", Name: "FuzzValue", Kind: goanalysis.KindFuzz, Package: "fixture.example/module",
		},
		CoveredFiles: []string{"value.go"}, Duration: 10 * time.Millisecond,
	})
}

func TestEvaluateMutationsRecordsHowCoverageRoutedAMutant(t *testing.T) {
	t.Parallel()
	mutant := gomutants.Mutant{
		ID: "mutant-a", DisplayID: "arithmetic#1", Accepted: true, Rule: "arithmetic",
		Path: "value.go", Line: 42, Package: "fixture.example/module",
	}
	sink, recorder := newTraceRecording()
	session := newTracedSession(&mutationUnitSession{
		catalog: gomutants.Catalog{Mutants: []gomutants.Mutant{mutant}},
	}, recorder)
	if _, err := EvaluateMutations(t.Context(), session, reachedMutationTargets(), MutationOptions{
		Root: t.TempDir(), Contract: "standard-v1", Trace: recorder,
	}); err != nil {
		t.Fatal(err)
	}
	routes := recordedRoutes(sink)
	if len(routes) != 1 {
		t.Fatalf("routes = %+v", routes)
	}
	route := routes[0]
	if route.MutantID != "mutant-a" || route.Rule != "arithmetic" || route.Path != "value.go" ||
		route.Line != 42 || route.Reason != trace.ReasonCoverageReaching {
		t.Fatalf("recorded route = %+v", route)
	}
	wantTargets := []string{
		"target-TestValue0", "target-TestValue1", "target-TestValue2", "target-TestValue3", "target-TestValue4",
		"target-TestValue5", "target-TestValue6", "target-TestValue7", "target-TestValue8", "target-FuzzValue",
	}
	if !slices.Equal(route.ReachingTargets, wantTargets) {
		t.Fatalf("reaching targets = %v, want %v", route.ReachingTargets, wantTargets)
	}
	wantPlan := []string{
		"individual:TestValue0", "individual:TestValue1", "individual:TestValue2", "individual:TestValue3",
		"individual:TestValue4", "individual:TestValue5", "individual:TestValue6", "individual:TestValue7",
		"batch:fixture.example/module(2)", "fuzz:FuzzValue",
	}
	if !slices.Equal(route.Plan, wantPlan) {
		t.Fatalf("plan = %v, want %v", route.Plan, wantPlan)
	}
	if !recordedBefore(t, sink, trace.TypeRoute, trace.TypeMutantExec) {
		t.Fatalf("the plan was recorded after it was carried out: %+v", sink.Events())
	}
}

// recordedBefore reports whether the first event of one type precedes the
// first event of another. A plan is only diagnostic if it is recorded before
// the executions it explains.
func recordedBefore(t *testing.T, sink *trace.MemorySink, first, second string) bool {
	t.Helper()
	firstSeen, secondSeen := -1, -1
	for index, event := range sink.Events() {
		if event.Type == first && firstSeen < 0 {
			firstSeen = index
		}
		if event.Type == second && secondSeen < 0 {
			secondSeen = index
		}
	}
	if firstSeen < 0 || secondSeen < 0 {
		t.Fatalf("recording holds no %q or no %q event", first, second)
	}
	return firstSeen < secondSeen
}

func TestEvaluateMutationsRecordsTheRouteOfAnUnreachedMutant(t *testing.T) {
	t.Parallel()
	mutant := gomutants.Mutant{
		ID: "mutant-b", DisplayID: "conditional#2", Accepted: true, Rule: "conditional",
		Path: "other/value.go", Line: 7, Package: "fixture.example/module/other",
	}
	sink, recorder := newTraceRecording()
	session := &mutationUnitSession{catalog: gomutants.Catalog{Mutants: []gomutants.Mutant{mutant}}}
	if _, err := EvaluateMutations(t.Context(), session, nil, MutationOptions{
		Root: t.TempDir(), Contract: "standard-v1", Trace: recorder,
	}); err != nil {
		t.Fatal(err)
	}
	want := trace.RouteRecord{
		MutantID: "mutant-b", Rule: "conditional", Path: "other/value.go", Line: 7,
		Plan: []string{"package-suite"}, Reason: trace.ReasonUnreached,
	}
	if routes := recordedRoutes(sink); len(routes) != 1 || !reflect.DeepEqual(routes[0], want) {
		t.Fatalf("routes = %+v, want [%+v]", routes, want)
	}
}

func TestMutationRoutingRecordsOneRoutePerSelectedMutantAndChangesNoEvaluation(t *testing.T) {
	t.Parallel()
	catalog := gomutants.Catalog{Mutants: []gomutants.Mutant{
		{ID: "mutant-a", DisplayID: "a#1", Accepted: true, Path: "value.go", Line: 1},
		{ID: "mutant-b", DisplayID: "b#1", Accepted: true, Path: "other/value.go", Line: 2},
		{ID: "mutant-c", DisplayID: "c#1", Accepted: false, Path: "value.go", Line: 3},
	}}
	catalog.Rejections = []gomutants.Rejection{{ID: "mutant-c", Diagnostic: "does not compile"}}
	targets := reachedMutationTargets()
	options := MutationOptions{Root: t.TempDir(), Contract: "standard-v1", Jobs: 2}
	untraced, err := EvaluateMutations(t.Context(), &mutationUnitSession{catalog: catalog}, targets, options)
	if err != nil {
		t.Fatal(err)
	}
	sink, recorder := newTraceRecording()
	options.Trace = recorder
	traced, err := EvaluateMutations(t.Context(), &mutationUnitSession{catalog: catalog}, targets, options)
	if err != nil || !reflect.DeepEqual(traced, untraced) {
		t.Fatalf("traced evaluation = (%+v, %v), want %+v", traced, err, untraced)
	}
	routed := make(map[string]string)
	for _, route := range recordedRoutes(sink) {
		routed[route.MutantID] = route.Reason
	}
	want := map[string]string{"mutant-a": trace.ReasonCoverageReaching, "mutant-b": trace.ReasonUnreached}
	if !reflect.DeepEqual(routed, want) {
		t.Fatalf("routed mutants = %v, want %v", routed, want)
	}
}
