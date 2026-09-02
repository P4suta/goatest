// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeCatalog writes one catalog document into a temporary directory and
// returns its path, so a fixture proves the decoding as well as the rule.
func writeCatalog(t *testing.T, document string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "catalog.json")
	if err := os.WriteFile(path, []byte(document), 0o644); err != nil {
		t.Fatalf("write the catalog: %v", err)
	}
	return path
}

func TestReadCatalogReadsWhatTheLayerNeedsAndIgnoresTheRest(t *testing.T) {
	t.Parallel()
	// A catalog is written by another tool on its own release schedule, so the
	// audit reads documents older and newer than itself. Everything the layer
	// does not decide by — the workspace, the operator, the replacement text,
	// the diagnostic direction of a proof — is carried past rather than
	// refused, and only the position and the gated body are read.
	path := writeCatalog(t, `{
	  "document_type": "go-mutants/catalog",
	  "schema_version": 1,
	  "tool_version": "0.0.0-fixture",
	  "workspace": {"module_path": "example.com/audited"},
	  "mutants": [
	    {"id": "`+firstMutant+`", "display_id": "`+firstDisplay+`", "path": "`+subjectPath+`",
	     "rule": "or-to-and", "line": 20, "column": 4, "original": "||", "replacement": "&&",
	     "branch": {"direction": "decreasing", "body_start_line": 20, "body_start_column": 15,
	                "body_end_line": 22, "body_end_column": 3, "note": "a field this audit does not read"}},
	    {"id": "`+secondMutant+`", "path": "`+subjectPath+`", "line": 30, "column": 4}
	  ],
	  "skips": [{"path": "vendor/other.go", "reason": "excluded"}]
	}`)

	catalog, err := readCatalog(path)
	if err != nil {
		t.Fatalf("read the catalog: %v", err)
	}
	proved, listed := catalog.lookup(firstMutant)
	if !listed {
		t.Fatalf("the catalog does not list %s", firstMutant)
	}
	if proved.Path != subjectPath || proved.Line != 20 || proved.Column != 4 {
		t.Errorf("the catalog placed the mutant at %s:%d:%d, want %s:20:4",
			proved.Path, proved.Line, proved.Column, subjectPath)
	}
	if proved.Branch == nil {
		t.Fatal("the catalog carried no proof for a mutant that has one")
	}
	if *proved.Branch != *gatedBody(20, 15, 22, 3) {
		t.Errorf("the proof names the body %+v, want the span the document carried", *proved.Branch)
	}
	unproved, listed := catalog.lookup(secondMutant)
	if !listed {
		t.Fatalf("the catalog does not list %s", secondMutant)
	}
	if unproved.Branch != nil {
		t.Errorf("a mutant the document gave no proof for carries %+v", *unproved.Branch)
	}
	if _, listed := catalog.lookup(thirdMutant); listed {
		t.Error("the catalog lists a mutant the document never named")
	}
}

func TestReadCatalogRefusesADocumentItCannotBeSureOf(t *testing.T) {
	t.Parallel()
	// A document of another kind or another version may name the same fields
	// and mean something else by them. An audit that read it anyway would
	// report a soundness result it has no evidence for, so it refuses and says
	// what it was given.
	cases := []struct {
		name     string
		document string
		want     []string
	}{
		{
			name:     "another kind of document",
			document: `{"document_type": "go-mutants/report", "schema_version": 1, "mutants": []}`,
			want:     []string{"go-mutants/report", "go-mutants/catalog"},
		},
		{
			name:     "a schema this audit does not read",
			document: `{"document_type": "go-mutants/catalog", "schema_version": 2, "mutants": []}`,
			want:     []string{"2", "1"},
		},
		{
			name:     "a document that names neither",
			document: `{"mutants": []}`,
			want:     []string{"go-mutants/catalog"},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			path := writeCatalog(t, testCase.document)

			_, err := readCatalog(path)
			if err == nil {
				t.Fatalf("readCatalog accepted %s", testCase.document)
			}
			for _, want := range append(testCase.want, path) {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("the error is %q, want it to name %q", err, want)
				}
			}
		})
	}
}

func TestReadCatalogReportsADocumentItCannotRead(t *testing.T) {
	t.Parallel()
	missing := filepath.Join(t.TempDir(), "absent.json")
	if _, err := readCatalog(missing); err == nil {
		t.Fatal("a missing catalog was accepted")
	} else if !strings.Contains(err.Error(), missing) {
		t.Errorf("the error is %q, want it to name the file it could not read", err)
	}

	broken := writeCatalog(t, `{"document_type": "go-mutants/catalog",`)
	if _, err := readCatalog(broken); err == nil {
		t.Fatal("a truncated catalog was accepted")
	} else if !strings.Contains(err.Error(), broken) {
		t.Errorf("the error is %q, want it to name the file it could not read", err)
	}
}

func TestALookupWithoutACatalogListsNothing(t *testing.T) {
	t.Parallel()
	// A run audited without -catalog has no catalog at all, and the layer that
	// reads one is not in the audit. Every other caller still asks, so the
	// absent catalog answers rather than panicking.
	var absent *mutantCatalog
	if _, listed := absent.lookup(firstMutant); listed {
		t.Error("a run audited without a catalog listed a mutant")
	}
}
