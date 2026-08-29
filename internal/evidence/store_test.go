// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package evidence_test

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/P4suta/goatest/internal/evidence"
)

func TestGraphStoreRoundTripsCanonicalRecord(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "nested", "graph.json")
	want := evidence.GraphRecord{
		ModulePath: "example/module",
		Graph: evidence.Graph{
			FilePackages: map[string]string{"z.go": "z", "a.go": "a"},
			Targets: []evidence.Target{
				{ID: "z", Dependencies: []string{"z", "a"}, CoveredFiles: []string{"z.go", "a.go"}},
				{ID: "a"},
			},
		},
	}
	if err := evidence.SaveGraph(path, want); err != nil {
		t.Fatal(err)
	}
	got, ok, err := evidence.LoadGraph(path)
	if err != nil || !ok || got.Schema != evidence.GraphSchemaV1 || got.ModulePath != want.ModulePath {
		t.Fatalf("LoadGraph = %+v, ok %v, err %v", got, ok, err)
	}
	if got.Graph.Targets[0].ID != "a" || !reflect.DeepEqual(got.Graph.Targets[1].Dependencies, []string{"a", "z"}) {
		t.Fatalf("canonical graph = %+v", got.Graph)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "graph.json" {
		t.Fatalf("graph directory = %v", entries)
	}
}

func TestLoadGraphMissingReadStrictnessAndIdentity(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing.json")
	if got, ok, err := evidence.LoadGraph(missing); err != nil || ok || !reflect.DeepEqual(got, evidence.GraphRecord{}) {
		t.Fatalf("missing LoadGraph = %+v, ok %v, err %v", got, ok, err)
	}
	for _, testCase := range []struct {
		name      string
		data      string
		want      string
		directory bool
	}{
		{name: "read", directory: true},
		{name: "malformed", data: "{", want: "decode evidence graph"},
		{name: "unknown", data: `{"schema":"evidence-graph-v1","module_path":"example/module","graph":{},"extra":true}`, want: "decode evidence graph"},
		{name: "trailing", data: `{"schema":"evidence-graph-v1","module_path":"example/module","graph":{}} {}`, want: "trailing data"},
		{name: "schema", data: `{"schema":"future-v2","module_path":"example/module","graph":{}}`, want: "identity mismatch"},
		{name: "module", data: `{"schema":"evidence-graph-v1","module_path":"","graph":{}}`, want: "identity mismatch"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "graph.json")
			if testCase.directory {
				if err := os.Mkdir(path, 0o755); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(path, []byte(testCase.data), 0o644); err != nil {
				t.Fatal(err)
			}
			got, ok, err := evidence.LoadGraph(path)
			wrongMessage := err != nil && testCase.want != "" && !strings.Contains(err.Error(), testCase.want)
			if err == nil || ok || !reflect.DeepEqual(got, evidence.GraphRecord{}) || wrongMessage {
				t.Fatalf("LoadGraph = %+v, ok %v, err %v; want %q", got, ok, err, testCase.want)
			}
		})
	}
}

func TestSaveGraphRejectsEmptyModuleBeforeCreatingOutput(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "graph.json")
	err := evidence.SaveGraph(path, evidence.GraphRecord{})
	if err == nil || !strings.Contains(err.Error(), "requires a module path") {
		t.Fatalf("SaveGraph error = %v", err)
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatalf("invalid graph created output: %v", statErr)
	}
}

func TestSaveGraphReportsDirectoryFailure(t *testing.T) {
	root := t.TempDir()
	blocking := filepath.Join(root, "parent")
	if err := os.WriteFile(blocking, []byte("file"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := evidence.SaveGraph(filepath.Join(blocking, "graph.json"), evidence.GraphRecord{ModulePath: "example/module"})
	if err == nil {
		t.Fatal("SaveGraph succeeded through a non-directory parent")
	}
}
