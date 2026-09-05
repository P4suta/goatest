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

// probeCatalog is a catalogue whose indices are the ones a probe result names.
// The last mutant has no probe form, which is what makes its absence from a
// measurement say nothing at all.
func probeCatalog() gomutants.Catalog {
	return gomutants.Catalog{Mutants: []gomutants.Mutant{
		{Index: 0, ID: "mutant-a", DisplayID: "a#1", Accepted: true, Probed: true},
		{Index: 1, ID: "mutant-b", DisplayID: "b#1", Accepted: true, Probed: true},
		{Index: 2, ID: "mutant-c", DisplayID: "c#1", Accepted: true, Probed: true},
		{Index: 3, ID: "mutant-d", DisplayID: "d#1", Accepted: true},
	}}
}

// probeEvidence is one measured baseline target of the kind the pass decides by.
func probeEvidence(name string, kind goanalysis.TargetKind, duration time.Duration) TargetEvidence {
	return TargetEvidence{
		Target: goanalysis.Target{
			ID: "target-" + name, Name: name, Kind: kind, Package: "fixture.example/module",
		},
		CoveredFiles: []string{"value.go"}, Environment: []string{"DB=ready"}, Duration: duration,
	}
}

// probeAnswer is what a scripted session says about one target.
type probeAnswer struct {
	result gomutants.ProbeResult
	err    error
}

// measuredAnswer is a pass that ran and named the mutants it made differ.
func measuredAnswer(infected ...uint32) probeAnswer {
	if infected == nil {
		infected = []uint32{}
	}
	return probeAnswer{result: gomutants.ProbeResult{
		Outcome: gomutants.ProbeMeasured, Infected: infected, Duration: 250 * time.Millisecond,
	}}
}

// probeAnswers scripts one answer per target, selected the way the pass selects
// the target itself: by the -test.run argument it sends.
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

// probeRecording keeps a recording as the JSON lines a real trace file holds,
// so a test reads back exactly the bytes a consumer would.
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

// newProbeRecording returns a recorder writing every event into a buffer of
// trace lines.
func newProbeRecording() (*probeRecording, *trace.Recorder) {
	recording := &probeRecording{}
	return recording, trace.New(recording, func() time.Time { return traceSessionOrigin })
}

// validateProbeLines rejects any recorded line the trace schema would reject. A
// trace nothing can validate is a trace nothing can read.
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

// traceSchemaResource is the identity the trace schema is compiled under.
const traceSchemaResource = "https://goatest.invalid/goatest-trace-v1.schema.json"

// probeRecords reads the probe records back out of a recording, keyed by the
// target each one describes.
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

// probeRecordKeys reads the JSON object one probe record was written as, so a
// test can assert that a key is absent rather than merely empty.
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

// TestTargetEvidenceInfectsEveryMutantUnlessProbedAndAbsent pins the one reading
// of an infection fact that is sound: a target the pass measured infects exactly
// the mutants it named, and a target it did not measure infects everything.
// Reading an unmeasured target as infecting nothing would drop the executions
// that find kills.
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
	// A measured target that infected nothing is the strongest fact the pass
	// produces, and it must not read as one that measured nothing.
	if empty := (TargetEvidence{Probed: true}); empty.infects(0) {
		t.Error("a target that measured and infected nothing infected a mutant")
	}
}

// TestProbePassSkipsFuzzTargets pins that a fuzz target is never probed: the
// mutation phase fuzzes beyond the seed corpus the probe would measure, and a
// fuzz run on the probe tree would write corpus files into that tree.
func TestProbePassSkipsFuzzTargets(t *testing.T) {
	t.Parallel()
	targets := []TargetEvidence{
		probeEvidence("TestValue", goanalysis.KindTest, time.Second),
		probeEvidence("FuzzValue", goanalysis.KindFuzz, 2*time.Second),
		probeEvidence("ExampleValue", goanalysis.KindExample, 3*time.Second),
	}
	session := &mutationUnitSession{catalog: probeCatalog(), probe: probeAnswers(map[string]probeAnswer{
		"TestValue": measuredAnswer(0), "ExampleValue": measuredAnswer(2),
	})}
	recording, recorder := newProbeRecording()
	evaluation, err := ProbeTargets(t.Context(), session, targets, ProbeOptions{Contract: "standard-v1", Trace: recorder})
	if err != nil {
		t.Fatal(err)
	}
	if evaluation.Measured != 2 || evaluation.Unmeasured != 0 {
		t.Fatalf("evaluation = %+v, want two measured targets and no unmeasured one", evaluation)
	}
	fuzz := evaluation.Targets[1]
	if fuzz.Target.Name != "FuzzValue" || fuzz.Probed || fuzz.Infected != nil {
		t.Fatalf("fuzz target = %+v, want it left unmeasured and without facts", fuzz)
	}
	for _, request := range session.probeRequests() {
		if slices.Contains(request.Args, "-test.run=^FuzzValue$") {
			t.Fatalf("the fuzz target was probed: %+v", request)
		}
	}
	if len(session.probeRequests()) != 2 {
		t.Fatalf("probe requests = %+v, want one per non-fuzz target", session.probeRequests())
	}
	if records := probeRecords(t, recording); len(records) != 2 || records["target-FuzzValue"].Target != "" {
		t.Fatalf("records = %+v, want no record for the fuzz target", records)
	}
}

// TestProbePassSendsTheRequestTheMutationPhaseWouldSend pins the pass to the
// execution it is a measurement of: the same target, selected the same way,
// under the same environment and the same calibrated timeout, minus the mutant
// a probe tree never activates.
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
				calibratedMutationTimeout(options.Contract, target.Duration, options.Timeout))
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
			// The request owns its environment: a later edit of the target's
			// slice cannot rewrite what was executed.
			target.Environment[0] = "DB=mutated"
			if !slices.Equal(session.probeRequests()[0].Env, []string{"DB=ready"}) {
				t.Fatalf("probe environment aliases the target: %+v", session.probeRequests()[0].Env)
			}
		})
	}
}

// TestProbePassKeepsTheFactsOfAMeasuredTarget pins what a measurement leaves
// behind: the catalogue indices the target made differ, ascending and distinct.
func TestProbePassKeepsTheFactsOfAMeasuredTarget(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name     string
		infected []uint32
		want     []uint32
	}{
		{name: "sorted", infected: []uint32{0, 2}, want: []uint32{0, 2}},
		{name: "empty", infected: []uint32{}, want: []uint32{}},
		// The engine contract promises the indices sorted and distinct; a set
		// that arrives otherwise is repaired rather than trusted, because
		// routing will binary-search it.
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
			// The input is never rewritten: the pass answers with evidence of
			// its own so a caller keeps what it handed over.
			if targets[0].Probed || targets[0].Infected != nil {
				t.Fatalf("the input target was rewritten: %+v", targets[0])
			}
		})
	}
}

// TestProbePassLeavesNoFactsOnATargetThatFailed pins the fail-closed half: a
// pass that could not be vouched for carries no facts, never "infected
// nothing", because reading it the other way drops the executions that kill.
func TestProbePassLeavesNoFactsOnATargetThatFailed(t *testing.T) {
	t.Parallel()
	for _, outcome := range []gomutants.ProbeOutcome{
		gomutants.ProbeTestFailed, gomutants.ProbeTimedOut, gomutants.ProbeUnavailable,
	} {
		t.Run(string(outcome), func(t *testing.T) {
			t.Parallel()
			session := &mutationUnitSession{catalog: probeCatalog(), probe: func(gomutants.ProbeRequest) (gomutants.ProbeResult, error) {
				return gomutants.ProbeResult{Outcome: outcome, ExitCode: 1, OutputTail: "FAIL"}, nil
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

// TestProbePassKeepsGoingWhenOneTargetErrors pins that a measurement that could
// not be taken costs its own facts and nothing else: the pass is an
// optimisation, so one failed measurement must not fail the run.
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

// TestProbePassStopsOnCancellation pins that a cancelled run stops asking for
// measurements it will never use, and reports the cancellation rather than a
// pass that measured nothing.
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

// TestProbePassRefusesAnUnpreparedSession pins the one probe failure that is a
// programming error rather than a measurement: a session prepared without a
// probe tree can never answer, so the run stops instead of recording an error
// per target.
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

// TestProbePassRefusesANilSession keeps the entry point fail-closed: a pass
// with nothing to measure against reports that rather than panicking inside a
// worker.
func TestProbePassRefusesANilSession(t *testing.T) {
	t.Parallel()
	evaluation, err := ProbeTargets(t.Context(), nil, nil, ProbeOptions{})
	if err == nil || err.Error() != "goatest: nil mutation session" || !reflect.DeepEqual(evaluation, ProbeEvaluation{}) {
		t.Fatalf("ProbeTargets = (%+v, %v)", evaluation, err)
	}
}

// TestProbePassIsDeterministicAcrossJobCounts pins that concurrency changes only
// how long the pass takes: the evidence and the recording of a pass run one
// target at a time are the evidence and the recording of a pass run four at a
// time.
func TestProbePassIsDeterministicAcrossJobCounts(t *testing.T) {
	t.Parallel()
	answers := map[string]probeAnswer{
		"TestFirst":  measuredAnswer(0, 2),
		"TestSecond": {result: gomutants.ProbeResult{Outcome: gomutants.ProbeTestFailed, ExitCode: 1}},
		"TestThird":  measuredAnswer(),
		"TestFourth": {err: errors.New("probe failed to start")},
		"TestFifth":  measuredAnswer(1),
		"TestSixth":  {result: gomutants.ProbeResult{Outcome: gomutants.ProbeTimedOut, ExitCode: -1}},
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
				if total != 6 {
					t.Errorf("progress total = %d, want the six non-fuzz targets", total)
				}
				completions = append(completions, completed)
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		records := make([]trace.ProbeRecord, 0, 6)
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
	if want := []int{1, 2, 3, 4, 5, 6}; !slices.Equal(serialProgress, want) || !slices.Equal(parallelProgress, want) {
		t.Fatalf("progress = %v and %v, want %v", serialProgress, parallelProgress, want)
	}
	if serial.Measured != 3 || serial.Unmeasured != 3 {
		t.Fatalf("evaluation = %+v, want three measured targets and three without facts", serial)
	}
}

// TestProbePassRecordsWhatEachTargetMeasured pins the record a developer reads
// the pass through: what ran, how it ended, and the mutants it named — and
// nothing where there is nothing to say.
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
	evaluation, err := ProbeTargets(t.Context(), session, []TargetEvidence{
		probeEvidence("TestInfecting", goanalysis.KindTest, time.Second),
		probeEvidence("TestClean", goanalysis.KindTest, time.Second),
		probeEvidence("TestFailing", goanalysis.KindTest, time.Second),
		probeEvidence("TestErrored", goanalysis.KindTest, time.Second),
		probeEvidence("TestUnknown", goanalysis.KindTest, time.Second),
	}, ProbeOptions{Contract: "standard-v1", TestArgs: []string{"-test.short=true"}, Trace: recorder})
	if err != nil {
		t.Fatal(err)
	}
	validateProbeLines(t, recording.Lines())
	records := probeRecords(t, recording)
	if len(records) != 5 {
		t.Fatalf("records = %+v, want one per probed target", records)
	}
	timeout := traceMilliseconds(calibratedMutationTimeout("standard-v1", time.Second, 0))
	want := trace.ProbeRecord{
		Target: "target-TestInfecting", Package: "fixture.example/module",
		Args:      []string{"-test.run=^TestInfecting$", "-test.short=true"},
		TimeoutMS: timeout, Outcome: trace.ProbeOutcomeMeasured, DurationMS: 1500,
		Infected: []string{"mutant-a", "mutant-c"},
	}
	if got := records["target-TestInfecting"]; !reflect.DeepEqual(got, want) {
		t.Fatalf("infecting record = %+v, want %+v", got, want)
	}
	// A measured target that infected nothing says so by the outcome alone: the
	// key is absent rather than an empty list nobody can tell from a missing
	// measurement.
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
	// An index the catalogue does not know is a contract violation, and a
	// measurement naming a mutant nobody can identify is no measurement.
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
