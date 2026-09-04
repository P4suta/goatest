// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package evidence_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/P4suta/goatest/internal/evidence"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

// mutationModulePath is the module every fixture in this file belongs to. A
// store is only ever trusted for the module it was written for, so the tests
// that exercise identity ask for a different one on purpose.
const mutationModulePath = "example/module"

// mutationDigest builds a sha256-shaped digest out of one hex character, so a
// fixture can name distinct digests without carrying 64-character literals.
func mutationDigest(character string) string {
	return strings.Repeat(character, 64)
}

// mutationProvenance is the recording run's input digest, in the form a record
// must carry.
func mutationProvenance() string {
	return "snapshot=" + mutationDigest("f")
}

func killedMutationRecord() evidence.MutationRecord {
	return evidence.MutationRecord{
		MutantID: mutationDigest("a"), Path: "value.go", Package: mutationModulePath + "/pkg",
		Outcome: evidence.MutationOutcomeKilled, Provenance: mutationProvenance(),
		KilledBy: &evidence.TargetKey{
			Package: mutationModulePath + "/pkg", Name: "TestKills", Kind: "test", Key: mutationDigest("1"),
		},
	}
}

// survivedMutationRecord carries its exhausted targets out of canonical order,
// so a round trip has something to sort.
func survivedMutationRecord() evidence.MutationRecord {
	return evidence.MutationRecord{
		MutantID: mutationDigest("b"), Path: "value.go", Package: mutationModulePath + "/pkg",
		Outcome: evidence.MutationOutcomeSurvived, Provenance: mutationProvenance(),
		Exhausted: []evidence.TargetKey{
			{Package: mutationModulePath + "/pkg", Name: "TestZ", Kind: "test", Key: mutationDigest("2")},
			{Package: mutationModulePath + "/pkg", Name: "TestA", Kind: "test", Key: mutationDigest("3")},
		},
		Finding: &evidence.FindingSeed{Kind: "surviving-mutant", Summary: "no target kills this mutant"},
	}
}

// canonicalSurvivedMutationRecord is survivedMutationRecord with its exhausted
// targets in the order a stored record carries them.
func canonicalSurvivedMutationRecord() evidence.MutationRecord {
	record := survivedMutationRecord()
	record.Exhausted = []evidence.TargetKey{
		{Package: mutationModulePath + "/pkg", Name: "TestA", Kind: "test", Key: mutationDigest("3")},
		{Package: mutationModulePath + "/pkg", Name: "TestZ", Kind: "test", Key: mutationDigest("2")},
	}
	return record
}

func timedOutMutationRecord() evidence.MutationRecord {
	return evidence.MutationRecord{
		MutantID: mutationDigest("c"), Path: "value.go", Package: mutationModulePath + "/pkg",
		Outcome: evidence.MutationOutcomeTimedOut, Provenance: mutationProvenance(),
		Exhausted: []evidence.TargetKey{
			{Package: mutationModulePath + "/pkg", Name: "TestSlow", Kind: "test", Key: mutationDigest("4")},
		},
		Finding: &evidence.FindingSeed{Kind: "timed-out-mutant", Summary: "the mutant did not terminate"},
	}
}

// suiteTimedOutMutationRecord is the other shape a timeout takes: the package
// suite of a mutant no target reached ran out of time, so there is a suite to
// name and no set of executed targets.
func suiteTimedOutMutationRecord() evidence.MutationRecord {
	record := timedOutMutationRecord()
	record.Exhausted = nil
	record.Suite = &evidence.SuiteKey{Package: mutationModulePath + "/pkg", Key: mutationDigest("6")}
	return record
}

func unreachedMutationRecord() evidence.MutationRecord {
	return evidence.MutationRecord{
		MutantID: mutationDigest("d"), Path: "value.go", Package: mutationModulePath + "/pkg",
		Outcome: evidence.MutationOutcomeUnreached, Provenance: mutationProvenance(),
		Suite:   &evidence.SuiteKey{Package: mutationModulePath + "/pkg", Key: mutationDigest("5")},
		Finding: &evidence.FindingSeed{Kind: "unreached-mutant", Summary: "no target reaches this mutant"},
	}
}

// mutationStoreFixture carries one record of every outcome, so a document built
// from it exercises every shape the schema and the loader accept.
func mutationStoreFixture() evidence.MutationStore {
	return evidence.MutationStore{
		Schema: evidence.MutationSchemaV1, ModulePath: mutationModulePath,
		Records: []evidence.MutationRecord{
			killedMutationRecord(), survivedMutationRecord(),
			timedOutMutationRecord(), unreachedMutationRecord(),
		},
	}
}

func TestMutationEvidenceStoreRoundTripsCanonicalRecord(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	path := filepath.Join(root, "nested", "mutation.json")
	unsorted := evidence.MutationStore{
		ModulePath: mutationModulePath,
		Records:    []evidence.MutationRecord{survivedMutationRecord(), killedMutationRecord()},
	}
	if err := evidence.SaveMutation(path, unsorted); err != nil {
		t.Fatal(err)
	}
	want := evidence.MutationStore{
		Schema: evidence.MutationSchemaV1, ModulePath: mutationModulePath,
		Records: []evidence.MutationRecord{killedMutationRecord(), canonicalSurvivedMutationRecord()},
	}
	got, ok, err := evidence.LoadMutation(path, mutationModulePath)
	if err != nil || !ok || !reflect.DeepEqual(got, want) {
		t.Fatalf("LoadMutation = %+v, ok %v, err %v; want %+v", got, ok, err, want)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.MarshalIndent(want, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, append(encoded, '\n')) {
		t.Fatalf("stored bytes are not canonical:\n%s", data)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "mutation.json" {
		t.Fatalf("mutation directory = %v", entries)
	}
}

// mutationRecords reaches the record objects of a decoded document.
func mutationRecords(t *testing.T, document map[string]any) []map[string]any {
	t.Helper()
	raw, ok := document["records"].([]any)
	if !ok {
		t.Fatalf("document records = %T", document["records"])
	}
	records := make([]map[string]any, 0, len(raw))
	for _, entry := range raw {
		record, ok := entry.(map[string]any)
		if !ok {
			t.Fatalf("record = %T", entry)
		}
		records = append(records, record)
	}
	return records
}

// mutationDocument encodes the fixture store as a decoded document the caller
// can damage before it is written, so a test names only the damage it makes.
func mutationDocument(t *testing.T, mutate func(document map[string]any)) []byte {
	t.Helper()
	data, err := json.Marshal(mutationStoreFixture())
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	if mutate != nil {
		mutate(document)
	}
	encoded, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func TestLoadMutationEvidenceMissingReadStrictnessAndIdentity(t *testing.T) {
	t.Parallel()
	missing := filepath.Join(t.TempDir(), "missing.json")
	if got, ok, err := evidence.LoadMutation(missing, mutationModulePath); err != nil || ok || !reflect.DeepEqual(got, evidence.MutationStore{}) {
		t.Fatalf("missing LoadMutation = %+v, ok %v, err %v", got, ok, err)
	}
	for _, testCase := range []struct {
		name      string
		directory bool
		raw       string
		trailing  bool
		mutate    func(document map[string]any)
		want      string
	}{
		{name: "read", directory: true},
		{name: "malformed", raw: "{", want: "decode mutation evidence"},
		{
			name:   "unknown-store-field",
			mutate: func(document map[string]any) { document["unknown"] = true },
			want:   "decode mutation evidence",
		},
		{
			name:   "unknown-record-field",
			mutate: func(document map[string]any) { mutationRecords(t, document)[0]["unknown"] = true },
			want:   "decode mutation evidence",
		},
		{
			name: "unknown-target-key-field",
			mutate: func(document map[string]any) {
				killer, ok := mutationRecords(t, document)[0]["killed_by"].(map[string]any)
				if !ok {
					t.Fatal("the killed fixture carries no killer")
				}
				killer["unknown"] = true
			},
			want: "decode mutation evidence",
		},
		{name: "trailing", trailing: true, want: "trailing data"},
		{
			name:   "schema",
			mutate: func(document map[string]any) { document["schema"] = "mutation-evidence-v2" },
			want:   "identity mismatch",
		},
		{
			name:   "module-path",
			mutate: func(document map[string]any) { document["module_path"] = "other/module" },
			want:   "identity mismatch",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "mutation.json")
			switch {
			case testCase.directory:
				if err := os.Mkdir(path, 0o755); err != nil {
					t.Fatal(err)
				}
			default:
				data := []byte(testCase.raw)
				if testCase.raw == "" {
					data = mutationDocument(t, testCase.mutate)
				}
				if testCase.trailing {
					data = append(data, []byte(" {}")...)
				}
				if err := os.WriteFile(path, data, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			got, ok, err := evidence.LoadMutation(path, mutationModulePath)
			wrongMessage := err != nil && testCase.want != "" && !strings.Contains(err.Error(), testCase.want)
			if err == nil || ok || !reflect.DeepEqual(got, evidence.MutationStore{}) || wrongMessage {
				t.Fatalf("LoadMutation = %+v, ok %v, err %v; want %q", got, ok, err, testCase.want)
			}
		})
	}
}

// mutateMutationStore applies one damage to the fixture store.
func mutateMutationStore(mutate func(store *evidence.MutationStore)) evidence.MutationStore {
	store := mutationStoreFixture()
	mutate(&store)
	return store
}

// TestLoadMutationEvidenceRejectsSelfInconsistentRecords walks every
// self-consistency rule. Each row is checked twice: a store that reached disk
// some other way must not load, and the same store must not be written, so an
// inconsistent record can neither enter nor leave the store.
func TestLoadMutationEvidenceRejectsSelfInconsistentRecords(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name  string
		store evidence.MutationStore
		want  string
	}{
		{
			name: "mutant-id-not-a-digest",
			store: mutateMutationStore(func(store *evidence.MutationStore) {
				store.Records[0].MutantID = "abc"
			}),
			want: "is not a sha256 digest",
		},
		{
			name: "mutant-id-uppercase",
			store: mutateMutationStore(func(store *evidence.MutationStore) {
				store.Records[0].MutantID = strings.ToUpper(mutationDigest("a"))
			}),
			want: "is not a sha256 digest",
		},
		{
			name: "duplicate-mutant-id",
			store: mutateMutationStore(func(store *evidence.MutationStore) {
				store.Records[1].MutantID = store.Records[0].MutantID
			}),
			want: "twice",
		},
		{
			name: "empty-path",
			store: mutateMutationStore(func(store *evidence.MutationStore) {
				store.Records[0].Path = ""
			}),
			want: "requires a path and a package",
		},
		{
			name: "empty-package",
			store: mutateMutationStore(func(store *evidence.MutationStore) {
				store.Records[0].Package = ""
			}),
			want: "requires a path and a package",
		},
		{
			name: "provenance-not-a-snapshot",
			store: mutateMutationStore(func(store *evidence.MutationStore) {
				store.Records[0].Provenance = "snapshot=short"
			}),
			want: "is not a run snapshot",
		},
		{
			name: "unknown-outcome",
			store: mutateMutationStore(func(store *evidence.MutationStore) {
				store.Records[0].Outcome = "flaky"
			}),
			want: "is not a reusable outcome",
		},
		{
			name: "target-key-not-a-digest",
			store: mutateMutationStore(func(store *evidence.MutationStore) {
				store.Records[0].KilledBy.Key = "abc"
			}),
			want: "is not a sha256 digest",
		},
		{
			name: "target-key-without-a-name",
			store: mutateMutationStore(func(store *evidence.MutationStore) {
				store.Records[1].Exhausted[0].Name = ""
			}),
			want: "requires a package, a name, and a kind",
		},
		{
			name: "suite-key-not-a-digest",
			store: mutateMutationStore(func(store *evidence.MutationStore) {
				store.Records[3].Suite.Key = "abc"
			}),
			want: "is not a sha256 digest",
		},
		{
			name: "suite-without-a-package",
			store: mutateMutationStore(func(store *evidence.MutationStore) {
				store.Records[3].Suite.Package = ""
			}),
			want: "requires a package",
		},
		{
			name: "duplicate-exhausted-target",
			store: mutateMutationStore(func(store *evidence.MutationStore) {
				store.Records[1].Exhausted[0] = store.Records[1].Exhausted[1]
			}),
			want: "twice",
		},
		{
			name: "killed-without-a-killer",
			store: mutateMutationStore(func(store *evidence.MutationStore) {
				store.Records[0].KilledBy = nil
			}),
			want: "requires a killer",
		},
		{
			name: "killed-with-exhausted-targets",
			store: mutateMutationStore(func(store *evidence.MutationStore) {
				store.Records[0].Exhausted = survivedMutationRecord().Exhausted
			}),
			want: "requires a killer",
		},
		{
			name: "killed-with-a-finding",
			store: mutateMutationStore(func(store *evidence.MutationStore) {
				store.Records[0].Finding = survivedMutationRecord().Finding
			}),
			want: "requires a killer",
		},
		{
			name: "survived-without-exhausted-targets",
			store: mutateMutationStore(func(store *evidence.MutationStore) {
				store.Records[1].Exhausted = nil
			}),
			want: "requires exhausted targets and a finding",
		},
		{
			name: "survived-with-a-killer",
			store: mutateMutationStore(func(store *evidence.MutationStore) {
				store.Records[1].KilledBy = killedMutationRecord().KilledBy
			}),
			want: "requires exhausted targets and a finding",
		},
		{
			name: "unreached-without-a-suite",
			store: mutateMutationStore(func(store *evidence.MutationStore) {
				store.Records[3].Suite = nil
			}),
			want: "requires a suite and a finding",
		},
		{
			name: "timed-out-naming-neither-targets-nor-a-suite",
			store: mutateMutationStore(func(store *evidence.MutationStore) {
				store.Records[2].Exhausted = nil
			}),
			want: "requires either exhausted targets or a suite",
		},
		{
			name: "timed-out-naming-both-targets-and-a-suite",
			store: mutateMutationStore(func(store *evidence.MutationStore) {
				store.Records[2].Suite = unreachedMutationRecord().Suite
			}),
			want: "requires either exhausted targets or a suite",
		},
		{
			name: "timed-out-with-a-killer",
			store: mutateMutationStore(func(store *evidence.MutationStore) {
				store.Records[2].KilledBy = killedMutationRecord().KilledBy
			}),
			want: "requires either exhausted targets or a suite",
		},
		{
			name: "timed-out-without-a-finding",
			store: mutateMutationStore(func(store *evidence.MutationStore) {
				store.Records[2].Finding = nil
			}),
			want: "requires either exhausted targets or a suite",
		},
		{
			name: "finding-without-a-kind",
			store: mutateMutationStore(func(store *evidence.MutationStore) {
				store.Records[1].Finding.Kind = ""
			}),
			want: "requires a kind and a summary",
		},
		{
			name: "finding-without-a-summary",
			store: mutateMutationStore(func(store *evidence.MutationStore) {
				store.Records[1].Finding.Summary = ""
			}),
			want: "requires a kind and a summary",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			data, err := json.Marshal(testCase.store)
			if err != nil {
				t.Fatal(err)
			}
			stored := filepath.Join(t.TempDir(), "mutation.json")
			if err := os.WriteFile(stored, data, 0o644); err != nil {
				t.Fatal(err)
			}
			got, ok, loadErr := evidence.LoadMutation(stored, mutationModulePath)
			wrongMessage := loadErr != nil && !strings.Contains(loadErr.Error(), testCase.want)
			if loadErr == nil || ok || !reflect.DeepEqual(got, evidence.MutationStore{}) || wrongMessage {
				t.Fatalf("LoadMutation = %+v, ok %v, err %v; want %q", got, ok, loadErr, testCase.want)
			}
			written := filepath.Join(t.TempDir(), "mutation.json")
			saveErr := evidence.SaveMutation(written, testCase.store)
			if saveErr == nil || !strings.Contains(saveErr.Error(), testCase.want) {
				t.Fatalf("SaveMutation error = %v, want %q", saveErr, testCase.want)
			}
			if _, statErr := os.Stat(written); !os.IsNotExist(statErr) {
				t.Fatalf("an inconsistent store created output: %v", statErr)
			}
		})
	}
}

// TestSaveMutationRejectsEmptyModuleBeforeCreatingOutput keeps a store that
// belongs to no module off disk: LoadMutation would refuse it as an identity
// mismatch, so writing one only leaves a file the next run has to reject.
func TestSaveMutationRejectsEmptyModuleBeforeCreatingOutput(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "mutation.json")
	store := mutationStoreFixture()
	store.ModulePath = ""
	err := evidence.SaveMutation(path, store)
	if err == nil || !strings.Contains(err.Error(), "requires a module path") {
		t.Fatalf("SaveMutation error = %v", err)
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatalf("a store without a module path created output: %v", statErr)
	}
}

// compileMutationSchema compiles the published schema the way a consumer of the
// artifact would, so the test fails when the schema itself stops compiling.
func compileMutationSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	const url = "https://goatest.invalid/mutation-evidence-v1.schema.json"
	document, err := jsonschema.UnmarshalJSON(bytes.NewReader(evidence.MutationJSONSchema()))
	if err != nil {
		t.Fatal(err)
	}
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	if err := compiler.AddResource(url, document); err != nil {
		t.Fatal(err)
	}
	compiled, err := compiler.Compile(url)
	if err != nil {
		t.Fatal(err)
	}
	return compiled
}

// savedMutationDocument writes a store and returns the canonical bytes, which
// is what a consumer validates against the schema.
func savedMutationDocument(t *testing.T, store evidence.MutationStore) []byte {
	t.Helper()
	path := filepath.Join(t.TempDir(), "mutation.json")
	if err := evidence.SaveMutation(path, store); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// validateMutationInstance decodes a document into the shape the validator
// walks and reports what the schema says about it.
func validateMutationInstance(t *testing.T, compiled *jsonschema.Schema, data []byte) error {
	t.Helper()
	instance, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	return compiled.Validate(instance)
}

func TestMutationEvidenceSchemaRejectsUnknownFieldsAtEveryLevel(t *testing.T) {
	t.Parallel()
	compiled := compileMutationSchema(t)
	data := savedMutationDocument(t, mutationStoreFixture())
	if err := validateMutationInstance(t, compiled, data); err != nil {
		t.Fatalf("a canonical document failed its own schema: %v\n%s", err, data)
	}
	for _, testCase := range []struct {
		name   string
		damage func(document map[string]any)
	}{
		{name: "store", damage: func(document map[string]any) { document["unknown"] = true }},
		{name: "record", damage: func(document map[string]any) { mutationRecords(t, document)[0]["unknown"] = true }},
		{
			name: "killed_by",
			damage: func(document map[string]any) {
				mutationRecords(t, document)[0]["killed_by"].(map[string]any)["unknown"] = true
			},
		},
		{
			name: "exhausted",
			damage: func(document map[string]any) {
				mutationRecords(t, document)[1]["exhausted"].([]any)[0].(map[string]any)["unknown"] = true
			},
		},
		{
			name: "suite",
			damage: func(document map[string]any) {
				mutationRecords(t, document)[3]["suite"].(map[string]any)["unknown"] = true
			},
		},
		{
			name: "finding",
			damage: func(document map[string]any) {
				mutationRecords(t, document)[1]["finding"].(map[string]any)["unknown"] = true
			},
		},
		{
			name:   "outcome",
			damage: func(document map[string]any) { mutationRecords(t, document)[0]["outcome"] = "flaky" },
		},
		{
			name:   "mutant_id",
			damage: func(document map[string]any) { mutationRecords(t, document)[0]["mutant_id"] = "abc" },
		},
		{
			name:   "provenance",
			damage: func(document map[string]any) { mutationRecords(t, document)[0]["provenance"] = "snapshot=short" },
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			var document map[string]any
			if err := json.Unmarshal(data, &document); err != nil {
				t.Fatal(err)
			}
			testCase.damage(document)
			damaged, err := json.Marshal(document)
			if err != nil {
				t.Fatal(err)
			}
			if err := validateMutationInstance(t, compiled, damaged); err == nil {
				t.Fatalf("the schema accepted a document damaged at %s", testCase.name)
			}
		})
	}
}

func TestMutationEvidenceSchemaAcceptsEveryOutcomeShape(t *testing.T) {
	t.Parallel()
	compiled := compileMutationSchema(t)
	for _, testCase := range []struct {
		name   string
		record evidence.MutationRecord
	}{
		{name: evidence.MutationOutcomeKilled, record: killedMutationRecord()},
		{name: evidence.MutationOutcomeSurvived, record: canonicalSurvivedMutationRecord()},
		{name: evidence.MutationOutcomeUnreached, record: unreachedMutationRecord()},
		{name: evidence.MutationOutcomeTimedOut, record: timedOutMutationRecord()},
		{name: evidence.MutationOutcomeTimedOut + "-by-the-package-suite", record: suiteTimedOutMutationRecord()},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			store := evidence.MutationStore{
				ModulePath: mutationModulePath,
				Records:    []evidence.MutationRecord{testCase.record},
			}
			path := filepath.Join(t.TempDir(), "mutation.json")
			if err := evidence.SaveMutation(path, store); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := validateMutationInstance(t, compiled, data); err != nil {
				t.Fatalf("a %s record failed the schema: %v\n%s", testCase.name, err, data)
			}
			got, ok, err := evidence.LoadMutation(path, mutationModulePath)
			if err != nil || !ok || len(got.Records) != 1 || !reflect.DeepEqual(got.Records[0], testCase.record) {
				t.Fatalf("LoadMutation = %+v, ok %v, err %v", got, ok, err)
			}
		})
	}
}
