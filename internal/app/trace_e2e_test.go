// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package app_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/P4suta/goatest/internal/app"
	"github.com/P4suta/goatest/internal/cli"
	"github.com/P4suta/goatest/internal/report"
	"github.com/P4suta/goatest/internal/testkit"
	"github.com/P4suta/goatest/internal/trace"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

// traceSchemaURL is the resource identity the trace-event schema is compiled
// under while a recorded stream is validated against it.
const traceSchemaURL = "https://goatest.invalid/goatest-trace-v1.schema.json"

// validateTraceStream checks every line of a recorded stream against the
// schema the trace claims to speak. A trace nothing can validate is a trace
// nothing can read.
func validateTraceStream(t *testing.T, directory string) {
	t.Helper()
	document, err := jsonschema.UnmarshalJSON(bytes.NewReader(trace.JSONSchema()))
	if err != nil {
		t.Fatal(err)
	}
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	if err := compiler.AddResource(traceSchemaURL, document); err != nil {
		t.Fatal(err)
	}
	compiled, err := compiler.Compile(traceSchemaURL)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(directory, trace.FileName))
	if err != nil {
		t.Fatal(err)
	}
	for index, line := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
		instance, err := jsonschema.UnmarshalJSON(strings.NewReader(line))
		if err != nil {
			t.Fatalf("trace line %d is not JSON: %v", index+1, err)
		}
		if err := compiled.Validate(instance); err != nil {
			t.Errorf("trace line %d was rejected by the schema: %v", index+1, err)
		}
	}
}

func TestTracedVerifyRecordsThePhasesCommandsAndRoutesOfARealRun(t *testing.T) {
	t.Parallel()
	repository := testkit.NewRepo(t).BoundaryFixture().Git()
	directory := filepath.Join(t.TempDir(), "trace")
	service := app.Service{
		Root: repository.Root(), GoBinary: testkit.GoBinary(t), TempDirectory: t.TempDir(),
		Environment: os.Environ(),
	}
	var stdout, stderr bytes.Buffer
	exit := cli.Run(t.Context(), []string{"verify", "--json", "--trace=" + directory}, &stdout, &stderr, service)
	if exit != cli.ExitAssured {
		t.Fatalf("verify exit = %d\nstdout: %s\nstderr: %s", exit, stdout.String(), stderr.String())
	}
	var result report.Report
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Verdict != report.VerdictAssured {
		t.Fatalf("report = %+v", result)
	}

	recording := traceRun(t, directory)
	validateTraceStream(t, recording)
	events := readTrace(t, recording)
	if len(events) < 2 || events[0].Type != trace.TypeRunStart || events[0].Schema != trace.SchemaV1 {
		t.Fatalf("recorded %d events beginning %+v", len(events), events[0])
	}
	last := events[len(events)-1]
	if last.Type != trace.TypeRunEnd || last.Run == nil || last.Run.Verdict != string(result.Verdict) || last.Run.Error != "" {
		t.Fatalf("run-end = %+v", last)
	}
	if last.Run.EventsDropped != 0 || last.Run.EventsEmitted != int64(len(events))-1 {
		t.Fatalf("accounting = %+v of %d events", last.Run, len(events))
	}

	// Every phase a run begins is answered by the end of that same phase
	// before the next one begins: the phases of a run are a sequence.
	var open string
	var phases []string
	for _, event := range events {
		switch event.Type {
		case trace.TypePhaseStart:
			if open != "" {
				t.Fatalf("phase %q began while %q was still open", event.Phase.Name, open)
			}
			open = event.Phase.Name
			phases = append(phases, open)
		case trace.TypePhaseEnd:
			if open != event.Phase.Name {
				t.Fatalf("phase-end %q closed while %q was open", event.Phase.Name, open)
			}
			open = ""
		}
	}
	if open != "" {
		t.Fatalf("phase %q was never ended", open)
	}
	for _, name := range []string{"snapshot", "discover", "baseline", "mutation-prepare", "probe", "mutation", "finalize"} {
		if !slices.Contains(phases, name) {
			t.Errorf("phase %q is absent from %v", name, phases)
		}
	}
	// The round runs one probe pass, between preparing the catalogue and
	// executing it.
	if probes := slices.Index(phases, "probe"); probes < 1 || probes+1 >= len(phases) ||
		phases[probes-1] != "mutation-prepare" || phases[probes+1] != "mutation" {
		t.Errorf("probe phase sits at %d of %v", probes, phases)
	}

	// The fixture's boundary returns a value at every return site, so the probe
	// pass has something to measure and the test that runs both branches
	// measures it.
	var measured int
	measuredProbes := make(map[string][]string)
	for _, event := range traceOfType(events, trace.TypeProbeExec) {
		if event.Probe.Target == "" {
			t.Errorf("probe %+v names no target", event.Probe)
		}
		if (event.Probe.Outcome == "") == (event.Probe.Error == "") {
			t.Errorf("probe %+v reached neither an outcome nor an error, or both", event.Probe)
		}
		if !event.Probe.Control && event.Probe.Outcome == trace.ProbeOutcomeMeasured {
			measured++
			measuredProbes[event.Probe.Target] = event.Probe.Infected
		}
	}
	if measured == 0 {
		t.Error("no target was measured against the probe tree")
	}

	// The commands that establish the toolchain and the package model are the
	// ones a reader looks for first, and each preserved output is beside the
	// stream where the event says it is.
	var executed [][]string
	for _, event := range traceOfType(events, trace.TypeExec) {
		executed = append(executed, event.Exec.Argv)
		if event.Exec.OutputPath == "" {
			continue
		}
		preserved := filepath.Join(recording, filepath.FromSlash(event.Exec.OutputPath))
		if info, err := os.Stat(preserved); err != nil || info.IsDir() {
			t.Errorf("preserved output %s = %v", event.Exec.OutputPath, err)
		}
	}
	for _, argv := range [][]string{{"go", "version"}, {"go", "list"}} {
		if !slices.ContainsFunc(executed, func(candidate []string) bool {
			return len(candidate) >= len(argv) && slices.Equal(candidate[:len(argv)], argv)
		}) {
			t.Errorf("no exec event ran %v: %v", argv, executed)
		}
	}

	// One route explains every mutant that ran, recorded before the executions
	// it explains.
	routed := map[string]int{}
	blockRouted := 0
	for _, event := range traceOfType(events, trace.TypeRoute) {
		if event.Route.Reason != trace.ReasonCoverageReaching &&
			event.Route.Reason != trace.ReasonProbeReaching && event.Route.Reason != trace.ReasonUnreached {
			t.Errorf("route %+v has no reason", event.Route)
		}
		// A route has a plan unless a proof answered for every target that
		// reached it: the package suite behind an empty plan would run the very
		// tests the proof just ruled out.
		if !routeHasPlanOrProof(*event.Route, measuredProbes) {
			t.Errorf("route %+v has no plan", event.Route)
		}
		if event.Route.Granularity != trace.GranularityBlock && event.Route.Granularity != trace.GranularityFile {
			t.Errorf("route %+v has no granularity", event.Route)
		}
		if event.Route.Granularity == trace.GranularityBlock && event.Route.Fallback == "" {
			blockRouted++
		}
		routed[event.Route.MutantID] = int(event.Seq)
	}
	// The fixture's test runs every block of the file it guards, so the
	// mutants inside those blocks are routed by block and not by fallback.
	if blockRouted == 0 {
		t.Error("no mutant was routed by the coverage blocks that contain it")
	}
	if selected := result.Accounting.Mutants.Selected; len(routed) != selected || selected == 0 {
		t.Fatalf("routed %d mutants, report selected %d", len(routed), selected)
	}
	for _, event := range traceOfType(events, trace.TypeMutantExec) {
		sequence, ok := routed[event.Mutant.ID]
		if !ok {
			t.Errorf("mutant %s ran unrouted", event.Mutant.ID)
			continue
		}
		if int64(sequence) > event.Seq {
			t.Errorf("mutant %s was routed at %d, after it ran at %d", event.Mutant.ID, sequence, event.Seq)
		}
	}
	assertInfectionDischargesAreSelfConsistent(t, result, events)
}

// routeHasPlanOrProof answers whether one route either schedules an execution
// or names evidence that settles every execution it removed. Positive suite
// evidence always wins: a suite that reached or infected the mutant still has
// to be in the plan even if another observation was silent.
func routeHasPlanOrProof(record trace.RouteRecord, measuredProbes map[string][]string) bool {
	if len(record.Plan) != 0 {
		return true
	}
	settled := record.SuiteCoverage != "" && !record.SuiteReached
	requiresPlan := record.SuiteCoverage != "" && record.SuiteReached
	if record.SuiteProbe != "" {
		infected, measured := measuredProbes[record.SuiteProbe]
		if measured && slices.Contains(infected, record.MutantID) {
			requiresPlan = true
		} else if measured {
			settled = true
		}
	}
	if requiresPlan {
		return false
	}
	return len(record.Discharged) != 0 || settled
}

func TestRouteHasPlanOrProofRequiresExecutionForPositiveSuiteEvidence(t *testing.T) {
	t.Parallel()
	mutant := "mutant-a"
	probe := "package-suite:example.com/app"
	measured := map[string][]string{probe: {"mutant-b"}}
	tests := []struct {
		name   string
		route  trace.RouteRecord
		probes map[string][]string
		want   bool
	}{
		{name: "an explicit plan", route: trace.RouteRecord{Plan: []string{"package-suite"}}, want: true},
		{name: "an explicit discharge", route: trace.RouteRecord{Discharged: []trace.Discharge{{Target: "target-a"}}}, want: true},
		{name: "no disposition", route: trace.RouteRecord{MutantID: mutant}},
		{name: "unreached suite coverage", route: trace.RouteRecord{MutantID: mutant, SuiteCoverage: "coverage"}, want: true},
		{name: "reached suite coverage", route: trace.RouteRecord{MutantID: mutant, SuiteCoverage: "coverage", SuiteReached: true}},
		{name: "non-infecting suite probe", route: trace.RouteRecord{MutantID: mutant, SuiteProbe: probe}, probes: measured, want: true},
		{name: "missing suite probe", route: trace.RouteRecord{MutantID: mutant, SuiteProbe: probe}},
		{name: "infecting suite probe", route: trace.RouteRecord{MutantID: mutant, SuiteProbe: probe}, probes: map[string][]string{probe: {mutant}}},
		{
			name:   "positive infection overrides silent coverage",
			route:  trace.RouteRecord{MutantID: mutant, SuiteCoverage: "coverage", SuiteProbe: probe},
			probes: map[string][]string{probe: {mutant}},
		},
		{
			name: "positive suite evidence overrides target discharges",
			route: trace.RouteRecord{
				MutantID: mutant, SuiteCoverage: "coverage", SuiteReached: true,
				Discharged: []trace.Discharge{{Target: "target-a"}},
			},
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			if got := routeHasPlanOrProof(testCase.route, testCase.probes); got != testCase.want {
				t.Fatalf("routeHasPlanOrProof(%+v) = %t, want %t", testCase.route, got, testCase.want)
			}
		})
	}
}

// assertInfectionDischargesAreSelfConsistent holds every never-infected
// discharge of a real recording to the measurement it was taken on: the route
// names a probed mutant, the pass measured the target it removed, that
// measurement left the mutant out, and the run never executed the pair.
//
// The fixture returns the value a return-zero mutation puts there for one of
// its tests, so that test infects nothing and the recording carries at least
// one such discharge; a recording without one would prove nothing here.
func assertInfectionDischargesAreSelfConsistent(t *testing.T, result report.Report, events []trace.Event) {
	t.Helper()
	measured := make(map[string][]string)
	for _, event := range traceOfType(events, trace.TypeProbeExec) {
		if event.Probe.Outcome == trace.ProbeOutcomeMeasured {
			measured[event.Probe.Target] = event.Probe.Infected
		}
	}
	named := make(map[string]string, len(result.Targets))
	for _, target := range result.Targets {
		named[target.ID] = target.Name
	}
	discharges := 0
	for _, event := range traceOfType(events, trace.TypeRoute) {
		for _, discharge := range event.Route.Discharged {
			if discharge.Reason != trace.DischargeNeverInfected {
				continue
			}
			discharges++
			if !event.Route.Probed {
				t.Errorf("route %+v discharged %s without a probe form of the mutant", event.Route, discharge.Target)
			}
			infected, ran := measured[discharge.Target]
			if !ran {
				t.Errorf("route %+v discharged %s, which the pass never measured", event.Route, discharge.Target)
				continue
			}
			if slices.Contains(infected, event.Route.MutantID) {
				t.Errorf("route %+v discharged %s, which the pass saw make its site differ", event.Route, discharge.Target)
			}
			for _, arguments := range mutantArguments(events, event.Route.MutantID) {
				if slices.Contains(selectedTests(arguments), named[discharge.Target]) {
					t.Errorf("the discharged target ran anyway: %s", arguments)
				}
			}
		}
	}
	if discharges == 0 {
		t.Error("no reaching target was discharged as never-infected")
	}
}

// selectedTests are the tests one recorded execution selected. A run groups the
// targets it executes as it pleases, so a discharged target is looked for among
// the names the selection carries rather than in the string it was rendered as.
func selectedTests(arguments string) []string {
	for _, argument := range strings.Fields(arguments) {
		pattern, selective := strings.CutPrefix(argument, "-test.run=")
		if !selective {
			continue
		}
		pattern = strings.TrimSuffix(strings.TrimPrefix(pattern, "^"), "$")
		pattern = strings.TrimSuffix(strings.TrimPrefix(pattern, "("), ")")
		return strings.Split(pattern, "|")
	}
	return nil
}

// oneRoute returns the single route a rule was recorded for, failing the test
// when the recording holds any other number: an assertion about "the" mutant of
// a rule is only an assertion when exactly one mutant carries it.
func oneRoute(t *testing.T, events []trace.Event, rule string) trace.RouteRecord {
	t.Helper()
	var found []trace.RouteRecord
	for _, event := range traceOfType(events, trace.TypeRoute) {
		if event.Route.Rule == rule {
			found = append(found, *event.Route)
		}
	}
	if len(found) != 1 {
		t.Fatalf("the recording holds %d routes for %s, want one: %+v", len(found), rule, found)
	}
	return found[0]
}

// mutantArguments returns the arguments every recorded execution of one mutant
// ran with, one entry per execution.
func mutantArguments(events []trace.Event, id string) []string {
	var arguments []string
	for _, event := range traceOfType(events, trace.TypeMutantExec) {
		if event.Mutant.ID == id {
			arguments = append(arguments, strings.Join(event.Mutant.Args, " "))
		}
	}
	return arguments
}

// TestTracedVerifyDischargesTheTestsThatNeverTakeANarrowedBranch pins the proof
// layer against a real run: the tests a branch proof shows cannot observe a
// mutation are named in the route and never executed, the test that can observe
// it still is and still kills it, and a mutation every reaching test was
// discharged for is reported as a survivor without a single execution.
func TestTracedVerifyDischargesTheTestsThatNeverTakeANarrowedBranch(t *testing.T) {
	t.Parallel()
	repository := testkit.NewRepo(t).NarrowedBranchFixture().Git()
	directory := filepath.Join(t.TempDir(), "trace")
	service := app.Service{
		Root: repository.Root(), GoBinary: testkit.GoBinary(t), TempDirectory: t.TempDir(),
		Environment: os.Environ(),
	}
	var stdout, stderr bytes.Buffer
	// The fixture leaves a mutation no test can observe alive on purpose, so the
	// run is insufficient rather than assured.
	exit := cli.Run(t.Context(), []string{"verify", "--json", "--trace=" + directory}, &stdout, &stderr, service)
	if exit != cli.ExitInsufficient {
		t.Fatalf("verify exit = %d\nstdout: %s\nstderr: %s", exit, stdout.String(), stderr.String())
	}
	var result report.Report
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	recording := traceRun(t, directory)
	validateTraceStream(t, recording)
	events := readTrace(t, recording)

	identified := make(map[string]string, len(result.Targets))
	for _, target := range result.Targets {
		identified[target.Name] = target.ID
	}

	// The clamp's equal case enters the guarded body and the case above the
	// limit does not, so the proof discharges that one alone.
	clamp := oneRoute(t, events, "le-to-lt")
	if clamp.Reason != trace.ReasonCoverageReaching || clamp.Granularity != trace.GranularityBlock {
		t.Fatalf("clamp route = %+v, want a route decided by coverage blocks", clamp)
	}
	wantDischarged := []trace.Discharge{{Target: identified["TestClampAbove"], Reason: trace.DischargeBranchNeverTaken}}
	if !reflect.DeepEqual(clamp.Discharged, wantDischarged) {
		t.Fatalf("clamp route discharged %+v, want %+v", clamp.Discharged, wantDischarged)
	}
	wantReaching := []string{identified["TestClampAtLimit"], identified["TestClampBelow"]}
	slices.Sort(wantReaching)
	if reaching := slices.Sorted(slices.Values(clamp.ReachingTargets)); !slices.Equal(reaching, wantReaching) {
		t.Fatalf("clamp route reaches %v, want %v", reaching, wantReaching)
	}
	for _, arguments := range mutantArguments(events, clamp.MutantID) {
		if strings.Contains(arguments, "TestClampAbove") {
			t.Errorf("the discharged target ran anyway: %s", arguments)
		}
	}
	if status := mutantStatus(t, result, clamp.MutantID); status != report.MutantKilled {
		t.Fatalf("clamp mutant %s = %s, want it killed by the test the proof kept", clamp.MutantID, status)
	}

	// The loader's only test never produces an error, so every test the blocks
	// route to the guarded body is discharged and nothing is left to run.
	load := oneRoute(t, events, "nil-error-branch")
	if load.Reason != trace.ReasonCoverageReaching || load.Granularity != trace.GranularityBlock ||
		len(load.ReachingTargets) != 0 || len(load.Plan) != 0 {
		t.Fatalf("load route = %+v, want a coverage-reaching route with nothing left to run", load)
	}
	wantDischarged = []trace.Discharge{{Target: identified["TestLoad"], Reason: trace.DischargeBranchNeverTaken}}
	if !reflect.DeepEqual(load.Discharged, wantDischarged) {
		t.Fatalf("load route discharged %+v, want %+v", load.Discharged, wantDischarged)
	}
	if arguments := mutantArguments(events, load.MutantID); len(arguments) != 0 {
		t.Fatalf("the fully discharged mutant ran %d times: %v", len(arguments), arguments)
	}
	wantSummary := "no reaching test was run: every one was discharged because none takes the branch this mutation narrows"
	if summary := mutantFinding(t, result, load.MutantID); summary.Kind != "surviving-mutant" || summary.Summary != wantSummary {
		t.Fatalf("load finding = %+v, want a surviving-mutant summarised %q", summary, wantSummary)
	}
}

// mutantStatus is the inventory status one mutant was given.
func mutantStatus(t *testing.T, result report.Report, id string) report.MutantStatus {
	t.Helper()
	for _, mutant := range result.Mutants {
		if mutant.ID == id {
			return mutant.Status
		}
	}
	t.Fatalf("mutant %s is absent from the inventory", id)
	return ""
}

// mutantFinding is the single finding one mutant was reported through.
func mutantFinding(t *testing.T, result report.Report, id string) report.Finding {
	t.Helper()
	var found []report.Finding
	for _, finding := range result.Findings {
		if finding.MutantID == id {
			found = append(found, finding)
		}
	}
	if len(found) != 1 {
		t.Fatalf("mutant %s has %d findings, want one: %+v", id, len(found), found)
	}
	return found[0]
}
