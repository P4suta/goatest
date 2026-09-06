// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package assure

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	gomutants "github.com/P4suta/go-mutants"
	goanalysis "github.com/P4suta/goatest/internal/golang"
	"github.com/P4suta/goatest/internal/trace"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

const probeRoundTripDuration = 9 * time.Millisecond

func probeCatalog() gomutants.Catalog {
	return gomutants.Catalog{Mutants: []gomutants.Mutant{
		{Index: 0, ID: "mutant-a", DisplayID: "a#1", Accepted: true, Probed: true},
		{Index: 1, ID: "mutant-b", DisplayID: "b#1", Accepted: true, Probed: true},
		{Index: 2, ID: "mutant-c", DisplayID: "c#1", Accepted: true, Probed: true},
		{Index: 3, ID: "mutant-d", DisplayID: "d#1", Accepted: true},
	}}
}

func probeEvidence(name string, kind goanalysis.TargetKind, duration time.Duration) TargetEvidence {
	return TargetEvidence{
		Target: goanalysis.Target{
			ID: "target-" + name, Name: name, Kind: kind, Package: "fixture.example/module",
		},
		CoveredFiles: []string{"value.go"}, Environment: []string{"DB=ready"}, Duration: duration,
	}
}

type probeAnswer struct {
	result gomutants.ProbeResult
	err    error
}

func measuredAnswer(infected ...uint32) probeAnswer {
	if infected == nil {
		infected = []uint32{}
	}
	return probeAnswer{result: gomutants.ProbeResult{
		Outcome: gomutants.ProbeMeasured, Infected: infected, Duration: 250 * time.Millisecond,
	}}
}

func probeAnswers(answers map[string]probeAnswer) func(gomutants.ProbeRequest) (gomutants.ProbeResult, error) {
	return func(request gomutants.ProbeRequest) (gomutants.ProbeResult, error) {
		for name, answer := range answers {
			if slices.Contains(request.Args, "-test.run=^"+name+"$") {
				return answer.result, answer.err
			}
		}
		return gomutants.ProbeResult{}, fmt.Errorf("no scripted probe answer for %v", request.Args)
	}
}

type probeRecording struct {
	mutex sync.Mutex
	lines []string
}

func (recording *probeRecording) Emit(event trace.Event) error {
	encoded, err := json.Marshal(event)
	if err != nil {
		return err
	}
	recording.mutex.Lock()
	defer recording.mutex.Unlock()
	recording.lines = append(recording.lines, string(encoded))
	return nil
}

func (recording *probeRecording) Close() error { return nil }

func (recording *probeRecording) Lines() []string {
	recording.mutex.Lock()
	defer recording.mutex.Unlock()
	return slices.Clone(recording.lines)
}

func newProbeRecording() (*probeRecording, *trace.Recorder) {
	recording := &probeRecording{}
	return recording, trace.New(recording, func() time.Time { return traceSessionOrigin })
}

func validateProbeLines(t *testing.T, lines []string) {
	t.Helper()
	document, err := jsonschema.UnmarshalJSON(bytes.NewReader(trace.JSONSchema()))
	if err != nil {
		t.Fatal(err)
	}
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	if err := compiler.AddResource(traceSchemaResource, document); err != nil {
		t.Fatal(err)
	}
	compiled, err := compiler.Compile(traceSchemaResource)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) == 0 {
		t.Fatal("the recording holds no lines to validate")
	}
	for index, line := range lines {
		instance, err := jsonschema.UnmarshalJSON(strings.NewReader(line))
		if err != nil {
			t.Fatalf("trace line %d is not JSON: %v", index+1, err)
		}
		if err := compiled.Validate(instance); err != nil {
			t.Errorf("trace line %d was rejected by the schema: %v", index+1, err)
		}
	}
}

const traceSchemaResource = "https://goatest.invalid/goatest-trace-v1.schema.json"

func probeRecords(t *testing.T, recording *probeRecording) map[string]trace.ProbeRecord {
	t.Helper()
	records := make(map[string]trace.ProbeRecord)
	for _, line := range recording.Lines() {
		var event trace.Event
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("trace line %q is not an event: %v", line, err)
		}
		if event.Type != trace.TypeProbeExec || event.Probe == nil {
			continue
		}
		if _, repeated := records[event.Probe.Target]; repeated {
			t.Fatalf("target %s was recorded twice", event.Probe.Target)
		}
		records[event.Probe.Target] = *event.Probe
	}
	return records
}

func probeRecordKeys(t *testing.T, recording *probeRecording, target string) map[string]any {
	t.Helper()
	for _, line := range recording.Lines() {
		var event struct {
			Type  string         `json:"type"`
			Probe map[string]any `json:"probe"`
		}
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("trace line %q is not an event: %v", line, err)
		}
		if event.Type == trace.TypeProbeExec && event.Probe["target"] == target {
			return event.Probe
		}
	}
	t.Fatalf("the recording holds no probe record for %s", target)
	return nil
}

func TestTargetEvidenceInfectsEveryMutantUnlessProbedAndAbsent(t *testing.T) {
	t.Parallel()
	unmeasured := TargetEvidence{Infected: []uint32{1}}
	for _, index := range []uint32{0, 1, 7} {
		if !unmeasured.infects(index) {
			t.Errorf("an unmeasured target did not infect mutant %d", index)
		}
	}
	measured := TargetEvidence{Probed: true, Infected: []uint32{1, 4, 9}}
	for _, index := range []uint32{1, 4, 9} {
		if !measured.infects(index) {
			t.Errorf("a measured target did not infect the mutant %d it named", index)
		}
	}
	for _, index := range []uint32{0, 2, 5, 10} {
		if measured.infects(index) {
			t.Errorf("a measured target infected mutant %d, which it never named", index)
		}
	}

	if empty := (TargetEvidence{Probed: true}); empty.infects(0) {
		t.Error("a target that measured and infected nothing infected a mutant")
	}
}

func TestMutationProbeCheckpointRoundTripBindsCompactIndices(t *testing.T) {
	t.Parallel()
	catalog := probeCatalog()
	targets := []TargetEvidence{
		probeEvidence("TestValue", goanalysis.KindTest, 17*time.Millisecond),
		probeEvidence("FuzzValue", goanalysis.KindFuzz, 23*time.Millisecond),
	}
	targets[0].Probed = true
	targets[0].ProbeDuration = probeRoundTripDuration
	targets[0].Infected = []uint32{0, 2}
	evaluation := ProbeEvaluation{
		Targets: targets, Measured: 1, Unmeasured: 1,
		Suites: map[string]PackageProbeEvidence{
			"fixture.example/module": {Measured: true, Duration: 31 * time.Millisecond, Infected: []uint32{1, 2}, WholeTree: true},
			"fixture.example/other":  {},
		},
		SuitesMeasured: 1, SuitesUnmeasured: 1,
	}
	saved := checkpointMutationProbe(catalog, evaluation)
	restored, ok := restoreMutationProbe(
		catalog, []TargetEvidence{targets[0], targets[1]},
		[]string{"fixture.example/other", "fixture.example/module"}, *saved,
	)
	if !ok || !reflect.DeepEqual(restored, evaluation) {
		t.Fatalf("restored probe = (%+v, %t), want %+v", restored, ok, evaluation)
	}

	reindexed := catalog
	reindexed.Mutants = slices.Clone(catalog.Mutants)
	reindexed.Mutants[0].Index, reindexed.Mutants[1].Index = reindexed.Mutants[1].Index, reindexed.Mutants[0].Index
	if MutationCatalogFingerprint(reindexed) != MutationCatalogFingerprint(catalog) {
		t.Fatal("source catalogue fingerprint unexpectedly depends on runtime order")
	}
	if _, ok := restoreMutationProbe(reindexed, targets, []string{"fixture.example/module", "fixture.example/other"}, *saved); ok {
		t.Fatal("probe checkpoint survived a changed index-to-mutant mapping")
	}
}

func TestProbePassMeasuresFuzzSeedTargets(t *testing.T) {
	t.Parallel()
	targets := []TargetEvidence{
		probeEvidence("TestValue", goanalysis.KindTest, time.Second),
		probeEvidence("FuzzValue", goanalysis.KindFuzz, 2*time.Second),
		probeEvidence("ExampleValue", goanalysis.KindExample, 3*time.Second),
	}
	session := &mutationUnitSession{catalog: probeCatalog(), probe: probeAnswers(map[string]probeAnswer{
		"TestValue": measuredAnswer(0), "FuzzValue": measuredAnswer(1), "ExampleValue": measuredAnswer(2),
	})}
	recording, recorder := newProbeRecording()
	evaluation, err := ProbeTargets(t.Context(), session, targets, ProbeOptions{Contract: "standard-v1", Trace: recorder})
	if err != nil {
		t.Fatal(err)
	}
	if evaluation.Measured != 3 || evaluation.Unmeasured != 0 {
		t.Fatalf("evaluation = %+v, want three measured targets and no unmeasured one", evaluation)
	}
	fuzz := evaluation.Targets[1]
	if fuzz.Target.Name != "FuzzValue" || !fuzz.Probed || !slices.Equal(fuzz.Infected, []uint32{1}) {
		t.Fatalf("fuzz target = %+v, want deterministic seed probe facts", fuzz)
	}
	fuzzProbed := false
	for _, request := range session.probeRequests() {
		if slices.Contains(request.Args, "-test.run=^FuzzValue$") {
			fuzzProbed = true
		}
	}
	if !fuzzProbed {
		t.Fatal("the fuzz seed target was not probed")
	}
	if len(session.probeRequests()) != len(targets) {
		t.Fatalf("probe requests = %+v, want one per target", session.probeRequests())
	}
	if records := probeRecords(t, recording); len(records) != len(targets) || records["target-FuzzValue"].Target != "target-FuzzValue" {
		t.Fatalf("records = %+v, want a record for every target", records)
	}
}

func TestProbePassMeasuresOneWholeSuitePerMutantPackage(t *testing.T) {
	t.Parallel()
	catalog := probeCatalog()
	for index := range catalog.Mutants {
		catalog.Mutants[index].Package = "fixture.example/module"
	}
	targets := []TargetEvidence{
		probeEvidence("TestFirst", goanalysis.KindTest, 2*time.Second),
		probeEvidence("TestSecond", goanalysis.KindTest, 3*time.Second),
	}
	session := &mutationUnitSession{catalog: catalog, probe: func(request gomutants.ProbeRequest) (gomutants.ProbeResult, error) {
		if slices.ContainsFunc(request.Args, func(argument string) bool {
			return strings.HasPrefix(argument, "-test.run=")
		}) {
			return measuredAnswer().result, nil
		}
		return gomutants.ProbeResult{
			Outcome: gomutants.ProbeMeasured, Infected: []uint32{2, 0, 2}, Duration: 7 * time.Second,
		}, nil
	}}
	recording, recorder := newProbeRecording()
	var progress []int
	evaluation, err := ProbeTargets(t.Context(), session, targets, ProbeOptions{
		Contract: "standard-v1", TestArgs: []string{"-test.short=true"}, Jobs: 2,
		PackageSuites: true, SuiteEnvironment: []string{"DB=ready"}, Trace: recorder,
		Progress: func(completed, total int) {
			if total != len(targets)+1 {
				t.Errorf("progress total = %d, want two targets and one suite", total)
			}
			progress = append(progress, completed)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if evaluation.Measured != 2 || evaluation.Unmeasured != 0 ||
		evaluation.SuitesMeasured != 1 || evaluation.SuitesUnmeasured != 0 {
		t.Fatalf("evaluation counts = %+v", evaluation)
	}
	suite, present := evaluation.Suites["fixture.example/module"]
	if !present || !suite.Measured || !slices.Equal(suite.Infected, []uint32{0, 2}) ||
		suite.Duration != 7*time.Second || suite.WholeTree {
		t.Fatalf("suite evidence = %+v", suite)
	}
	for _, target := range evaluation.Targets {
		if !target.Probed || target.ProbeDuration != 250*time.Millisecond {
			t.Fatalf("target evidence = %+v, want the probe control duration", target)
		}
	}
	requests := session.probeRequests()
	var suiteRequest *gomutants.ProbeRequest
	for index := range requests {
		if !slices.ContainsFunc(requests[index].Args, func(argument string) bool {
			return strings.HasPrefix(argument, "-test.run=")
		}) {
			suiteRequest = &requests[index]
		}
	}
	if suiteRequest == nil || suiteRequest.Package != "fixture.example/module" ||
		!slices.Equal(suiteRequest.Args, []string{"-test.short=true"}) ||
		!slices.Equal(suiteRequest.Env, []string{"DB=ready"}) ||
		suiteRequest.Timeout != 5*time.Second {
		t.Fatalf("suite request = %+v, want the measured five-second package control", suiteRequest)
	}
	validateProbeLines(t, recording.Lines())
	record := probeRecords(t, recording)[packageSuiteProbeTarget("fixture.example/module")]
	if !record.Suite || record.Package != "fixture.example/module" ||
		!slices.Equal(record.Args, []string{"-test.short=true"}) || record.TimeoutMS != 5_000 ||
		record.Outcome != trace.ProbeOutcomeMeasured || record.DurationMS != 7_000 ||
		!slices.Equal(record.Infected, []string{"mutant-a", "mutant-c"}) {
		t.Fatalf("suite record = %+v", record)
	}
	slices.Sort(progress)
	if !slices.Equal(progress, []int{1, 2, 3}) {
		t.Fatalf("progress = %v", progress)
	}
}

func TestProbePassLeavesNoPackageFactsWhenTheSuiteIsNotMeasured(t *testing.T) {
	t.Parallel()
	catalog := probeCatalog()
	for index := range catalog.Mutants {
		catalog.Mutants[index].Package = "fixture.example/module"
	}
	session := &mutationUnitSession{catalog: catalog, probe: func(gomutants.ProbeRequest) (gomutants.ProbeResult, error) {
		return gomutants.ProbeResult{
			Outcome: gomutants.ProbeTimedOut, ExitCode: -1,
		}, nil
	}}
	evaluation, err := ProbeTargets(t.Context(), session, nil, ProbeOptions{
		Contract: "standard-v1", PackageSuites: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	suite, present := evaluation.Suites["fixture.example/module"]
	if !present || suite.Measured || suite.Infected != nil ||
		evaluation.SuitesMeasured != 0 || evaluation.SuitesUnmeasured != 1 {
		t.Fatalf("evaluation = %+v, want one suite without facts", evaluation)
	}
}

func TestProbePassSendsTheRequestTheMutationPhaseWouldSend(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name     string
		limit    time.Duration
		testArgs []string
	}{
		{name: "calibrated"},
		{name: "configured ceiling", limit: 90 * time.Second},
		{name: "extra test flags", testArgs: []string{"-test.short=true", "-test.parallel=4"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			target := probeEvidence("TestValue/sub", goanalysis.KindTest, 2*time.Second)
			session := &mutationUnitSession{catalog: probeCatalog(), probe: func(gomutants.ProbeRequest) (gomutants.ProbeResult, error) {
				return gomutants.ProbeResult{Outcome: gomutants.ProbeMeasured, Infected: []uint32{}}, nil
			}}
			options := ProbeOptions{
				Contract: "standard-v1", Timeout: test.limit, TestArgs: slices.Clone(test.testArgs),
			}
			if _, err := ProbeTargets(t.Context(), session, []TargetEvidence{target}, options); err != nil {
				t.Fatal(err)
			}
			seed := seedRequest(gomutants.Mutant{ID: "mutant-a"}, target,
				mutationExecutionTimeout(options.Timeout, target.Duration))
			seed.Args = append(seed.Args, test.testArgs...)
			requests := session.probeRequests()
			if len(requests) != 1 {
				t.Fatalf("probe requests = %+v, want one", requests)
			}
			got := requests[0]
			if got.Package != seed.Package || !slices.Equal(got.Args, seed.Args) ||
				!slices.Equal(got.Env, seed.Env) || got.Timeout != seed.Timeout {
				t.Fatalf("probe request = %+v, want the mutation request %+v without its mutant", got, seed)
			}

			target.Environment[0] = "DB=mutated"
			if !slices.Equal(session.probeRequests()[0].Env, []string{"DB=ready"}) {
				t.Fatalf("probe environment aliases the target: %+v", session.probeRequests()[0].Env)
			}
		})
	}
}

func TestProbePassKeepsTheFactsOfAMeasuredTarget(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name     string
		infected []uint32
		want     []uint32
	}{
		{name: "sorted", infected: []uint32{0, 2}, want: []uint32{0, 2}},
		{name: "empty", infected: []uint32{}, want: []uint32{}},

		{name: "unsorted and repeated", infected: []uint32{2, 0, 2, 1}, want: []uint32{0, 1, 2}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			session := &mutationUnitSession{catalog: probeCatalog(), probe: func(gomutants.ProbeRequest) (gomutants.ProbeResult, error) {
				return gomutants.ProbeResult{Outcome: gomutants.ProbeMeasured, Infected: slices.Clone(test.infected)}, nil
			}}
			targets := []TargetEvidence{probeEvidence("TestValue", goanalysis.KindTest, time.Second)}
			evaluation, err := ProbeTargets(t.Context(), session, targets, ProbeOptions{Contract: "standard-v1"})
			if err != nil {
				t.Fatal(err)
			}
			measured := evaluation.Targets[0]
			if !measured.Probed || !slices.Equal(measured.Infected, test.want) {
				t.Fatalf("measured target = %+v, want the infections %v", measured, test.want)
			}
			if evaluation.Measured != 1 || evaluation.Unmeasured != 0 {
				t.Fatalf("evaluation = %+v, want one measured target", evaluation)
			}

			if targets[0].Probed || targets[0].Infected != nil {
				t.Fatalf("the input target was rewritten: %+v", targets[0])
			}
		})
	}
}

func TestProbePassLeavesNoFactsOnATargetThatFailed(t *testing.T) {
	t.Parallel()
	for _, outcome := range []gomutants.ProbeOutcome{
		gomutants.ProbeTestFailed, gomutants.ProbeTimedOut, gomutants.ProbeUnavailable,
	} {
		t.Run(string(outcome), func(t *testing.T) {
			t.Parallel()
			session := &mutationUnitSession{catalog: probeCatalog(), probe: func(gomutants.ProbeRequest) (gomutants.ProbeResult, error) {
				return gomutants.ProbeResult{Outcome: outcome, ExitCode: 1, Output: []byte("FAIL")}, nil
			}}
			recording, recorder := newProbeRecording()
			evaluation, err := ProbeTargets(t.Context(), session,
				[]TargetEvidence{probeEvidence("TestValue", goanalysis.KindTest, time.Second)},
				ProbeOptions{Contract: "standard-v1", Trace: recorder})
			if err != nil {
				t.Fatal(err)
			}
			target := evaluation.Targets[0]
			if target.Probed || target.Infected != nil {
				t.Fatalf("target = %+v, want no facts", target)
			}
			if evaluation.Measured != 0 || evaluation.Unmeasured != 1 {
				t.Fatalf("evaluation = %+v, want one unmeasured target", evaluation)
			}
			record := probeRecords(t, recording)["target-TestValue"]
			if record.Outcome != string(outcome) || record.ExitCode != 1 || record.Infected != nil || record.Error != "" {
				t.Fatalf("record = %+v, want the outcome alone", record)
			}
		})
	}
}

func TestProbePassKeepsGoingWhenOneTargetErrors(t *testing.T) {
	t.Parallel()
	cause := errors.New("probe scratch could not be made")
	session := &mutationUnitSession{catalog: probeCatalog(), probe: probeAnswers(map[string]probeAnswer{
		"TestFirst":  measuredAnswer(0),
		"TestSecond": {err: cause},
		"TestThird":  measuredAnswer(1, 2),
	})}
	recording, recorder := newProbeRecording()
	evaluation, err := ProbeTargets(t.Context(), session, []TargetEvidence{
		probeEvidence("TestFirst", goanalysis.KindTest, time.Second),
		probeEvidence("TestSecond", goanalysis.KindTest, time.Second),
		probeEvidence("TestThird", goanalysis.KindTest, time.Second),
	}, ProbeOptions{Contract: "standard-v1", Trace: recorder})
	if err != nil {
		t.Fatalf("ProbeTargets error = %v, want a pass that kept going", err)
	}
	if evaluation.Measured != 2 || evaluation.Unmeasured != 1 {
		t.Fatalf("evaluation = %+v, want two measured targets and one without facts", evaluation)
	}
	if !slices.Equal(evaluation.Targets[0].Infected, []uint32{0}) || !slices.Equal(evaluation.Targets[2].Infected, []uint32{1, 2}) {
		t.Fatalf("measured targets = %+v", evaluation.Targets)
	}
	failed := evaluation.Targets[1]
	if failed.Probed || failed.Infected != nil {
		t.Fatalf("errored target = %+v, want no facts", failed)
	}
	record := probeRecords(t, recording)["target-TestSecond"]
	if record.Error != cause.Error() || record.Outcome != "" || record.Infected != nil {
		t.Fatalf("record = %+v, want the error that stopped it and no outcome", record)
	}
}

func TestProbePassStopsOnCancellation(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	session := &mutationUnitSession{catalog: probeCatalog(), probe: func(gomutants.ProbeRequest) (gomutants.ProbeResult, error) {
		cancel()
		return gomutants.ProbeResult{}, context.Canceled
	}}
	evaluation, err := ProbeTargets(ctx, session, []TargetEvidence{
		probeEvidence("TestFirst", goanalysis.KindTest, time.Second),
		probeEvidence("TestSecond", goanalysis.KindTest, time.Second),
		probeEvidence("TestThird", goanalysis.KindTest, time.Second),
	}, ProbeOptions{Contract: "standard-v1", Jobs: 1})
	if !errors.Is(err, context.Canceled) || !reflect.DeepEqual(evaluation, ProbeEvaluation{}) {
		t.Fatalf("ProbeTargets = (%+v, %v), want the cancellation", evaluation, err)
	}
	if requests := session.probeRequests(); len(requests) != 1 {
		t.Fatalf("probe requests = %+v, want the pass to stop after the cancellation", requests)
	}
}

func TestProbePassRefusesAnUnpreparedSession(t *testing.T) {
	t.Parallel()
	session := &mutationUnitSession{catalog: probeCatalog(), probe: func(gomutants.ProbeRequest) (gomutants.ProbeResult, error) {
		return gomutants.ProbeResult{}, fmt.Errorf("gomutants: session probe: %w", gomutants.ErrProbeNotPrepared)
	}}
	evaluation, err := ProbeTargets(t.Context(), session, []TargetEvidence{
		probeEvidence("TestFirst", goanalysis.KindTest, time.Second),
	}, ProbeOptions{Contract: "standard-v1"})
	if !errors.Is(err, gomutants.ErrProbeNotPrepared) || !reflect.DeepEqual(evaluation, ProbeEvaluation{}) {
		t.Fatalf("ProbeTargets = (%+v, %v), want the unprepared session refused", evaluation, err)
	}
}

func TestProbePassRefusesANilSession(t *testing.T) {
	t.Parallel()
	evaluation, err := ProbeTargets(t.Context(), nil, nil, ProbeOptions{})
	if err == nil || err.Error() != "goatest: nil mutation session" || !reflect.DeepEqual(evaluation, ProbeEvaluation{}) {
		t.Fatalf("ProbeTargets = (%+v, %v)", evaluation, err)
	}
}

func TestProbePassIsDeterministicAcrossJobCounts(t *testing.T) {
	t.Parallel()
	answers := map[string]probeAnswer{
		"TestFirst":  measuredAnswer(0, 2),
		"TestSecond": {result: gomutants.ProbeResult{Outcome: gomutants.ProbeTestFailed, ExitCode: 1}},
		"TestThird":  measuredAnswer(),
		"TestFourth": {err: errors.New("probe failed to start")},
		"TestFifth":  measuredAnswer(1),
		"TestSixth":  {result: gomutants.ProbeResult{Outcome: gomutants.ProbeTimedOut, ExitCode: -1}},
		"FuzzValue":  measuredAnswer(2),
	}
	targets := []TargetEvidence{
		probeEvidence("TestFirst", goanalysis.KindTest, time.Second),
		probeEvidence("TestSecond", goanalysis.KindTest, 2*time.Second),
		probeEvidence("FuzzValue", goanalysis.KindFuzz, 3*time.Second),
		probeEvidence("TestThird", goanalysis.KindTest, 4*time.Second),
		probeEvidence("TestFourth", goanalysis.KindTest, 5*time.Second),
		probeEvidence("TestFifth", goanalysis.KindTest, 6*time.Second),
		probeEvidence("TestSixth", goanalysis.KindTest, 7*time.Second),
	}
	pass := func(jobs int) (ProbeEvaluation, []trace.ProbeRecord, []int) {
		t.Helper()
		session := &mutationUnitSession{catalog: probeCatalog(), probe: probeAnswers(answers)}
		recording, recorder := newProbeRecording()
		var completions []int
		evaluation, err := ProbeTargets(t.Context(), session, targets, ProbeOptions{
			Contract: "standard-v1", Jobs: jobs, Trace: recorder,
			Progress: func(completed, total int) {
				if total != len(targets) {
					t.Errorf("progress total = %d, want all targets", total)
				}
				completions = append(completions, completed)
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		records := make([]trace.ProbeRecord, 0, len(targets))
		for _, record := range probeRecords(t, recording) {
			records = append(records, record)
		}
		slices.SortFunc(records, func(first, second trace.ProbeRecord) int {
			return strings.Compare(first.Target, second.Target)
		})
		slices.Sort(completions)
		return evaluation, records, completions
	}
	serial, serialRecords, serialProgress := pass(1)
	parallel, parallelRecords, parallelProgress := pass(4)
	if !reflect.DeepEqual(serial, parallel) {
		t.Fatalf("evidence of one job = %+v, of four = %+v", serial, parallel)
	}
	if !reflect.DeepEqual(serialRecords, parallelRecords) {
		t.Fatalf("records of one job = %+v, of four = %+v", serialRecords, parallelRecords)
	}
	if want := []int{1, 2, 3, 4, 5, 6, 7}; !slices.Equal(serialProgress, want) || !slices.Equal(parallelProgress, want) {
		t.Fatalf("progress = %v and %v, want %v", serialProgress, parallelProgress, want)
	}
	if serial.Measured != 4 || serial.Unmeasured != 3 {
		t.Fatalf("evaluation = %+v, want four measured targets and three without facts", serial)
	}
}

func TestProbePassRecordsWhatEachTargetMeasured(t *testing.T) {
	t.Parallel()
	cause := errors.New("probe log could not be read")
	session := &mutationUnitSession{catalog: probeCatalog(), probe: probeAnswers(map[string]probeAnswer{
		"TestInfecting": {result: gomutants.ProbeResult{
			Outcome: gomutants.ProbeMeasured, Infected: []uint32{0, 2}, Duration: 1500 * time.Millisecond,
		}},
		"TestClean":   measuredAnswer(),
		"TestFailing": {result: gomutants.ProbeResult{Outcome: gomutants.ProbeTestFailed, ExitCode: 2}},
		"TestErrored": {err: cause},
		"TestUnknown": {result: gomutants.ProbeResult{Outcome: gomutants.ProbeMeasured, Infected: []uint32{9}}},
	})}
	recording, recorder := newProbeRecording()
	targets := []TargetEvidence{
		probeEvidence("TestInfecting", goanalysis.KindTest, time.Second),
		probeEvidence("TestClean", goanalysis.KindTest, time.Second),
		probeEvidence("TestFailing", goanalysis.KindTest, time.Second),
		probeEvidence("TestErrored", goanalysis.KindTest, time.Second),
		probeEvidence("TestUnknown", goanalysis.KindTest, time.Second),
	}
	evaluation, err := ProbeTargets(t.Context(), session, targets, ProbeOptions{Contract: "standard-v1", TestArgs: []string{"-test.short=true"}, Trace: recorder})
	if err != nil {
		t.Fatal(err)
	}
	validateProbeLines(t, recording.Lines())
	records := probeRecords(t, recording)
	if len(records) != len(targets) {
		t.Fatalf("records = %+v, want one per probed target", records)
	}
	timeout := traceMilliseconds(mutationExecutionTimeout(0, time.Second))
	want := trace.ProbeRecord{
		Target: "target-TestInfecting", Package: "fixture.example/module",
		Args:      []string{"-test.run=^TestInfecting$", "-test.short=true"},
		TimeoutMS: timeout, Outcome: trace.ProbeOutcomeMeasured, DurationMS: 1500,
		Infected: []string{"mutant-a", "mutant-c"},
	}
	if got := records["target-TestInfecting"]; !reflect.DeepEqual(got, want) {
		t.Fatalf("infecting record = %+v, want %+v", got, want)
	}

	clean := records["target-TestClean"]
	if clean.Outcome != trace.ProbeOutcomeMeasured || clean.Infected != nil || clean.Error != "" {
		t.Fatalf("clean record = %+v", clean)
	}
	if _, present := probeRecordKeys(t, recording, "target-TestClean")["infected"]; present {
		t.Fatalf("a measured execution with no infections wrote an infected key: %v",
			probeRecordKeys(t, recording, "target-TestClean"))
	}
	failing := records["target-TestFailing"]
	if failing.Outcome != trace.ProbeOutcomeTestFailed || failing.ExitCode != 2 || failing.Error != "" {
		t.Fatalf("failing record = %+v", failing)
	}
	errored := records["target-TestErrored"]
	if errored.Error != cause.Error() || errored.Outcome != "" || errored.Infected != nil {
		t.Fatalf("errored record = %+v", errored)
	}

	unknown := records["target-TestUnknown"]
	if unknown.Error != "probe reported an unknown mutant index 9" || unknown.Outcome != "" || unknown.Infected != nil {
		t.Fatalf("unknown-index record = %+v", unknown)
	}
	if target := evaluation.Targets[4]; target.Probed || target.Infected != nil {
		t.Fatalf("unknown-index target = %+v, want no facts", target)
	}
	if evaluation.Measured != 2 || evaluation.Unmeasured != 3 {
		t.Fatalf("evaluation = %+v", evaluation)
	}
}

func TestPreparedProbeMutationControlRunsTheExactSemanticOriginal(t *testing.T) {
	t.Parallel()
	session := &mutationUnitSession{probe: func(gomutants.ProbeRequest) (gomutants.ProbeResult, error) {
		return gomutants.ProbeResult{
			Outcome: gomutants.ProbeMeasured, ExitCode: 0,
			Duration: 1250 * time.Millisecond, Output: []byte("passing output"),
		}, nil
	}}
	recording, recorder := newProbeRecording()
	args := []string{"-test.run=^TestBoundary$", "-test.short=true"}
	environment := []string{"DB=ready", "TOKEN=fixture"}
	request := gomutants.ExecRequest{
		Mutant: "mutant-a", Package: "fixture.example/module/pkg",
		Args: args, Env: environment, Timeout: 7 * time.Second,
	}
	result, err := preparedProbeMutationControl(session, recorder)(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	wantRequest := gomutants.ProbeRequest{
		Package: request.Package, Args: slices.Clone(args), Env: slices.Clone(environment), Timeout: request.Timeout,
	}
	args[0], environment[0] = "mutated", "mutated"
	if got := session.probeRequests(); len(got) != 1 || !reflect.DeepEqual(got[0], wantRequest) {
		t.Fatalf("probe requests = %+v, want %+v", got, wantRequest)
	}
	wantResult := gomutants.CommandResult{
		ExitCode: 0, Duration: 1250 * time.Millisecond, Output: []byte("passing output"),
	}
	if !reflect.DeepEqual(result, wantResult) {
		t.Fatalf("control result = %+v, want %+v", result, wantResult)
	}
	validateProbeLines(t, recording.Lines())
	records := probeRecords(t, recording)
	wantRecord := trace.ProbeRecord{
		Target: trace.MutationControlProbePrefix + "fixture.example/module/pkg", Package: "fixture.example/module/pkg",
		Control: true, Args: wantRequest.Args, TimeoutMS: 7000,
		Outcome: trace.ProbeOutcomeMeasured, DurationMS: 1250,
	}
	if got := records[wantRecord.Target]; !reflect.DeepEqual(got, wantRecord) {
		t.Fatalf("control record = %+v, want %+v", got, wantRecord)
	}
}

func TestPreparedProbeMutationControlFailsClosed(t *testing.T) {
	t.Parallel()
	cause := errors.New("probe process did not start")
	tests := []struct {
		name       string
		result     gomutants.ProbeResult
		failure    error
		wantError  string
		wantResult gomutants.CommandResult
	}{
		{
			name: "a timed-out original", result: gomutants.ProbeResult{
				Outcome: gomutants.ProbeTimedOut, ExitCode: -1, Duration: 3 * time.Second, Output: []byte("stalled"),
			},
			wantResult: gomutants.CommandResult{
				ExitCode: -1, TimedOut: true, Duration: 3 * time.Second, Output: []byte("stalled"),
			},
		},
		{name: "an execution error", failure: cause, wantError: "goatest: prepared original control: probe process did not start"},
		{
			name: "an unknown outcome", result: gomutants.ProbeResult{Outcome: gomutants.ProbeOutcome("future")},
			wantError: `goatest: prepared original control returned unknown outcome "future"`,
		},
		{
			name: "a contradictory measured outcome", result: gomutants.ProbeResult{Outcome: gomutants.ProbeMeasured, ExitCode: 2},
			wantError: "goatest: prepared original control returned measured with exit code 2",
		},
		{
			name: "a contradictory failed outcome", result: gomutants.ProbeResult{Outcome: gomutants.ProbeTestFailed},
			wantError: "goatest: prepared original control returned test-failed with exit code 0",
		},
		{
			name: "a negative duration", result: gomutants.ProbeResult{Outcome: gomutants.ProbeMeasured, Duration: -time.Nanosecond},
			wantError: "goatest: prepared original control returned negative duration -1ns",
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			session := &mutationUnitSession{probe: func(gomutants.ProbeRequest) (gomutants.ProbeResult, error) {
				return testCase.result, testCase.failure
			}}
			recording, recorder := newProbeRecording()
			result, err := preparedProbeMutationControl(session, recorder)(t.Context(), gomutants.ExecRequest{Timeout: time.Second})
			if testCase.wantError == "" {
				if err != nil || !reflect.DeepEqual(result, testCase.wantResult) {
					t.Fatalf("control = (%+v, %v), want (%+v, nil)", result, err, testCase.wantResult)
				}
			} else if err == nil || err.Error() != testCase.wantError || !reflect.DeepEqual(result, gomutants.CommandResult{}) {
				t.Fatalf("control = (%+v, %v), want zero result and %q", result, err, testCase.wantError)
			}
			validateProbeLines(t, recording.Lines())
			record := probeRecords(t, recording)[trace.MutationControlProbePrefix+"all"]
			if !record.Control || (testCase.wantError != "" && record.Error == "") {
				t.Fatalf("control record = %+v", record)
			}
		})
	}
}

const (
	probeTargetControl = 3 * time.Second
	probeSuiteControl  = 5 * time.Second
)

func TestProbeDeadlinesSumEveryPositiveControl(t *testing.T) {
	t.Parallel()
	target := probeEvidence("TestValue", goanalysis.KindTest, probeTargetControl)
	measured := map[string]PackageSuiteCoverage{target.Target.Package: {Duration: probeSuiteControl}}
	unmeasured := map[string]PackageSuiteCoverage{target.Target.Package: {}}
	for _, test := range []struct {
		name   string
		limit  time.Duration
		suites map[string]PackageSuiteCoverage
		want   time.Duration
	}{
		{name: "the target control alone", want: probeTargetControl},
		{name: "every measured control", suites: measured, want: probeTargetControl + probeSuiteControl},
		{name: "an unmeasured suite adds nothing", suites: unmeasured, want: probeTargetControl},
		{
			name: "capped by the containment ceiling", limit: probeTargetControl,
			suites: measured, want: probeTargetControl,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			session := &mutationUnitSession{catalog: probeCatalog(), probe: func(gomutants.ProbeRequest) (gomutants.ProbeResult, error) {
				return gomutants.ProbeResult{Outcome: gomutants.ProbeMeasured, Infected: []uint32{}}, nil
			}}
			options := ProbeOptions{Timeout: test.limit, SuiteCoverage: test.suites}
			if _, err := ProbeTargets(t.Context(), session, []TargetEvidence{target}, options); err != nil {
				t.Fatal(err)
			}
			requests := session.probeRequests()
			if len(requests) != 1 {
				t.Fatalf("probe requests = %+v, want one", requests)
			}
			if requests[0].Timeout != test.want {
				t.Fatalf("probe deadline = %v; want %v", requests[0].Timeout, test.want)
			}
		})
	}
}

func TestProbeSuiteDeadlineSumsItsTargetsAndItsOwnControl(t *testing.T) {
	t.Parallel()
	target := probeEvidence("TestValue", goanalysis.KindTest, probeTargetControl)
	pkg := target.Target.Package
	session := &mutationUnitSession{catalog: probeCatalog(), probe: func(gomutants.ProbeRequest) (gomutants.ProbeResult, error) {
		return gomutants.ProbeResult{Outcome: gomutants.ProbeMeasured, Infected: []uint32{}}, nil
	}}
	options := ProbeOptions{
		SuitePackages: []string{pkg},
		SuiteCoverage: map[string]PackageSuiteCoverage{pkg: {Duration: probeSuiteControl}},
	}
	if _, err := ProbeTargets(t.Context(), session, []TargetEvidence{target}, options); err != nil {
		t.Fatal(err)
	}
	var suiteRequests []gomutants.ProbeRequest
	for _, request := range session.probeRequests() {
		if !slices.ContainsFunc(request.Args, func(argument string) bool {
			return strings.HasPrefix(argument, "-test.run=")
		}) {
			suiteRequests = append(suiteRequests, request)
		}
	}
	if len(suiteRequests) != 1 {
		t.Fatalf("package suite requests = %+v, want one", suiteRequests)
	}
	if want := probeTargetControl + probeSuiteControl; suiteRequests[0].Timeout != want {
		t.Fatalf("package suite deadline = %v; want %v", suiteRequests[0].Timeout, want)
	}
}
