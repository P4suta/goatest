// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package app_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
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
	for _, name := range []string{"snapshot", "discover", "baseline", "mutation-prepare", "mutation", "finalize"} {
		if !slices.Contains(phases, name) {
			t.Errorf("phase %q is absent from %v", name, phases)
		}
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
		if event.Route.Reason != trace.ReasonCoverageReaching && event.Route.Reason != trace.ReasonUnreached {
			t.Errorf("route %+v has no reason", event.Route)
		}
		if len(event.Route.Plan) == 0 {
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
}
