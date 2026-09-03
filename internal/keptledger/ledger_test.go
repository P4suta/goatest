// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package keptledger_test

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/P4suta/goatest/internal/keptledger"
)

// moment is a fixed clock, because what a ledger says has to be readable as a
// literal in a test rather than only as a comparison.
func moment(hour int) time.Time {
	return time.Date(2026, 9, 4, hour, 0, 0, 0, time.UTC)
}

func TestTheLedgerIsTheDocumentTheSchemaPromises(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "kept-temp-v1.json")
	if err := keptledger.Append(path,
		keptledger.Entry{Path: "/tmp/goatest-run-b", RunID: "goatest-run-b", KeptAt: moment(11), Bytes: 2048},
		keptledger.Entry{Path: "/tmp/goatest-run-a", RunID: "goatest-run-a", KeptAt: moment(10), Bytes: 1024},
	); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// A person looking for the gigabytes a keep left behind reads this file,
	// and `cache status` and `cache gc` read the same four fields.
	want := `{"schema":"goatest-kept-temp-v1","entries":[` +
		`{"path":"/tmp/goatest-run-a","run_id":"goatest-run-a","kept_at":"2026-09-04T10:00:00Z","bytes":1024},` +
		`{"path":"/tmp/goatest-run-b","run_id":"goatest-run-b","kept_at":"2026-09-04T11:00:00Z","bytes":2048}]}`
	if got := strings.TrimSpace(string(raw)); got != want {
		t.Fatalf("ledger = %s, want %s", got, want)
	}
}

func TestAppendKeepsWhatEarlierRunsRecorded(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "kept-temp-v1.json")
	first := keptledger.Entry{Path: "/tmp/goatest-run-a", RunID: "a", KeptAt: moment(10), Bytes: 1}
	if err := keptledger.Append(path, first); err != nil {
		t.Fatal(err)
	}
	second := keptledger.Entry{Path: "/tmp/goatest-run-b", RunID: "b", KeptAt: moment(11), Bytes: 2}
	if err := keptledger.Append(path, second); err != nil {
		t.Fatal(err)
	}
	ledger, err := keptledger.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	// The ledger is the record of everything still on the disk, so a second
	// run appends to it rather than replacing what the first one left.
	if !reflect.DeepEqual(ledger.Entries, []keptledger.Entry{first, second}) {
		t.Fatalf("entries = %+v, want both runs in kept-at order", ledger.Entries)
	}
}

func TestAppendReplacesTheEntryForAPathItAlreadyHolds(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "kept-temp-v1.json")
	kept := "/tmp/goatest-run-a"
	if err := keptledger.Append(path, keptledger.Entry{Path: kept, RunID: "a", KeptAt: moment(10), Bytes: 1}); err != nil {
		t.Fatal(err)
	}
	current := keptledger.Entry{Path: kept, RunID: "a", KeptAt: moment(12), Bytes: 4096}
	if err := keptledger.Append(path, current); err != nil {
		t.Fatal(err)
	}
	ledger, err := keptledger.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	// One directory is one entry. A path recorded twice would be counted twice
	// by every reader and removed once by the collector.
	if !reflect.DeepEqual(ledger.Entries, []keptledger.Entry{current}) {
		t.Fatalf("entries = %+v, want the one directory recorded once", ledger.Entries)
	}
}

func TestALedgerNobodyHasWrittenIsAnEmptyOne(t *testing.T) {
	t.Parallel()
	ledger, err := keptledger.Load(filepath.Join(t.TempDir(), "kept-temp-v1.json"))
	if err != nil || len(ledger.Entries) != 0 {
		t.Fatalf("load of a missing ledger = (%+v, %v), want an empty one and no failure", ledger, err)
	}
}

func TestLoadRefusesADocumentItDoesNotUnderstand(t *testing.T) {
	t.Parallel()
	for _, test := range []struct{ name, document string }{
		{name: "another schema", document: `{"schema":"goatest-kept-temp-v2","entries":[]}`},
		{name: "no schema", document: `{"entries":[]}`},
		{name: "a field this version does not know", document: `{"schema":"goatest-kept-temp-v1","entries":[],"removed":true}`},
		{name: "not a document at all", document: `["/tmp/goatest-run-a"]`},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "kept-temp-v1.json")
			if err := os.WriteFile(path, []byte(test.document), 0o600); err != nil {
				t.Fatal(err)
			}
			// Reading a document of another shape as though it were this one
			// is how a reader deletes something it did not understand.
			if ledger, err := keptledger.Load(path); err == nil {
				t.Fatalf("load of %s = %+v, want it refused", test.document, ledger)
			}
		})
	}
}

func TestAWrittenLedgerLeavesNothingBesideIt(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	path := filepath.Join(directory, "kept-temp-v1.json")
	for _, entry := range []keptledger.Entry{
		{Path: "/tmp/goatest-run-a", RunID: "a", KeptAt: moment(10)},
		{Path: "/tmp/goatest-run-b", RunID: "b", KeptAt: moment(11)},
	} {
		if err := keptledger.Append(path, entry); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	// The write goes through a temporary file so that a ledger is never half
	// replaced, and the temporary file is nobody's to find afterwards.
	if len(entries) != 1 || entries[0].Name() != "kept-temp-v1.json" {
		t.Fatalf("directory = %v, want the ledger alone", entries)
	}
}

func TestTheLedgerLivesWhereTheRepositoryKeepsItsOwnFiles(t *testing.T) {
	t.Parallel()
	root := filepath.Join("home", "developer", "project")
	if got, want := keptledger.Path(root), filepath.Join(root, ".goatest", "kept-temp-v1.json"); got != want {
		t.Fatalf("ledger path = %q, want %q", got, want)
	}
}
