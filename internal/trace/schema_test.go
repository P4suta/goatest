// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package trace_test

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/P4suta/goatest/internal/trace"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

// schemaURL is the resource identity the trace-event schema is compiled under.
const schemaURL = "https://goatest.invalid/goatest-trace-v1.schema.json"

// compileSchema compiles the embedded trace-event schema.
func compileSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	document, err := jsonschema.UnmarshalJSON(bytes.NewReader(trace.JSONSchema()))
	if err != nil {
		t.Fatal(err)
	}
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	if err := compiler.AddResource(schemaURL, document); err != nil {
		t.Fatal(err)
	}
	compiled, err := compiler.Compile(schemaURL)
	if err != nil {
		t.Fatal(err)
	}
	return compiled
}

// decodeEvents marshals recorded events and decodes them back into the generic
// documents the schema validates.
func decodeEvents(t *testing.T, events []trace.Event) []map[string]any {
	t.Helper()
	decoded := make([]map[string]any, 0, len(events))
	for _, event := range events {
		encoded, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		var document map[string]any
		if err := json.Unmarshal(encoded, &document); err != nil {
			t.Fatal(err)
		}
		decoded = append(decoded, document)
	}
	return decoded
}

func TestSchemaIdentifiesTheTraceFormat(t *testing.T) {
	t.Parallel()
	var document map[string]any
	if err := json.Unmarshal(trace.JSONSchema(), &document); err != nil {
		t.Fatalf("the embedded schema is not JSON: %v", err)
	}
	if document["$id"] != trace.SchemaV1 {
		t.Fatalf("$id = %v, want %q", document["$id"], trace.SchemaV1)
	}
}

func TestSchemaAcceptsEveryRecordedEvent(t *testing.T) {
	t.Parallel()
	compiled := compileSchema(t)
	for _, document := range decodeEvents(t, scriptedEvents(t)) {
		if err := compiled.Validate(document); err != nil {
			t.Errorf("recorded %v event was rejected: %v", document["type"], err)
		}
	}
}

func TestSchemaRejectsUnknownFields(t *testing.T) {
	t.Parallel()
	compiled := compileSchema(t)
	payloads := []string{"phase", "exec", "mutant", "route", "progress", "artifact", "run"}
	for _, document := range decodeEvents(t, scriptedEvents(t)) {
		eventType, _ := document["type"].(string)
		document["unknown"] = true
		if err := compiled.Validate(document); err == nil {
			t.Errorf("an unknown top-level field passed the schema on a %s event", eventType)
		}
		delete(document, "unknown")
		for _, payload := range payloads {
			record, ok := document[payload].(map[string]any)
			if !ok {
				continue
			}
			record["unknown"] = true
			if err := compiled.Validate(document); err == nil {
				t.Errorf("an unknown %s field passed the schema on a %s event", payload, eventType)
			}
			delete(record, "unknown")
		}
	}
}

func TestSchemaRejectsIncompleteOrUnknownEvents(t *testing.T) {
	t.Parallel()
	compiled := compileSchema(t)
	events := decodeEvents(t, scriptedEvents(t))
	base := events[len(events)-1]

	for _, field := range []string{"seq", "type", "timestamp", "elapsed_ms"} {
		document := cloneDocument(t, base)
		delete(document, field)
		if err := compiled.Validate(document); err == nil {
			t.Errorf("an event without %s passed the schema", field)
		}
	}

	unknownType := cloneDocument(t, base)
	unknownType["type"] = "not-an-event"
	if err := compiled.Validate(unknownType); err == nil {
		t.Error("an unknown event type passed the schema")
	}

	runStart := cloneDocument(t, events[0])
	delete(runStart, "schema")
	if err := compiled.Validate(runStart); err == nil {
		t.Error("a run-start event without a schema identity passed the schema")
	}

	runEnd := cloneDocument(t, base)
	for _, field := range []string{"events_emitted", "events_dropped"} {
		document := cloneDocument(t, runEnd)
		delete(document["run"].(map[string]any), field)
		if err := compiled.Validate(document); err == nil {
			t.Errorf("a run-end event without %s passed the schema; the accounting is never optional", field)
		}
	}
}

func TestSchemaRejectsAPayloadThatIsNotTheEventsOwn(t *testing.T) {
	t.Parallel()
	compiled := compileSchema(t)
	documents := decodeEvents(t, scriptedEvents(t))

	// Every payload of the recording, indexed by the field it arrived under,
	// so each event can be handed a payload belonging to another event.
	payloads := map[string]any{}
	for _, document := range documents {
		for _, name := range []string{"phase", "exec", "mutant", "route", "progress", "artifact", "run"} {
			if record, ok := document[name]; ok {
				payloads[name] = record
			}
		}
	}
	if len(payloads) != 7 {
		t.Fatalf("the recording holds %d payloads, want one of each", len(payloads))
	}

	for _, document := range documents {
		eventType, _ := document["type"].(string)
		for name, record := range payloads {
			if _, own := document[name]; own {
				continue
			}
			amended := cloneDocument(t, document)
			amended[name] = record
			if err := compiled.Validate(amended); err == nil {
				t.Errorf("a %s event carrying a %s payload passed the schema; an event carries its own payload alone", eventType, name)
			}
		}
		if eventType == trace.TypeRunStart {
			continue
		}
		identified := cloneDocument(t, document)
		identified["schema"] = trace.SchemaV1
		if err := compiled.Validate(identified); err == nil {
			t.Errorf("a %s event carrying the format identity passed the schema; run-start carries it alone", eventType)
		}
	}
}

func TestSchemaRejectsEnvironmentValues(t *testing.T) {
	t.Parallel()
	compiled := compileSchema(t)
	var exec map[string]any
	for _, document := range decodeEvents(t, scriptedEvents(t)) {
		if document["type"] == trace.TypeExec {
			exec = document
		}
	}
	if exec == nil {
		t.Fatal("the recording holds no exec event")
	}
	exec["exec"].(map[string]any)["env_names"] = []any{"GOATEST_TOKEN=super-secret"}
	if err := compiled.Validate(exec); err == nil {
		t.Fatal("an environment value passed the schema; a trace records names alone")
	}
}

func TestSchemaRejectsARouteWithoutAKnownReason(t *testing.T) {
	t.Parallel()
	compiled := compileSchema(t)
	var route map[string]any
	for _, document := range decodeEvents(t, scriptedEvents(t)) {
		if document["type"] == trace.TypeRoute {
			route = document
		}
	}
	if route == nil {
		t.Fatal("the recording holds no route event")
	}
	route["route"].(map[string]any)["reason"] = "made-up"
	if err := compiled.Validate(route); err == nil {
		t.Fatal("an unknown routing reason passed the schema")
	}
}

// cloneDocument returns an independent copy of a decoded event.
func cloneDocument(t *testing.T, document map[string]any) map[string]any {
	t.Helper()
	encoded, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	var clone map[string]any
	if err := json.Unmarshal(encoded, &clone); err != nil {
		t.Fatal(err)
	}
	return clone
}
