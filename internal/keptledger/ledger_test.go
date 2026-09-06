// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package keptledger_test

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/P4suta/goatest/internal/filemode"
	"github.com/P4suta/goatest/internal/keptledger"
)

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
			if err := os.WriteFile(path, []byte(test.document), filemode.PrivateFile); err != nil {
				t.Fatal(err)
			}

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

	var left []string
	for _, entry := range entries {
		if entry.Name() != "kept-temp-v1.json" && entry.Name() != "kept-temp-v1.json.lock" {
			left = append(left, entry.Name())
		}
	}
	if len(left) != 0 {
		t.Fatalf("directory holds %v, want the ledger and its lock alone", left)
	}
}

func TestTheLedgerLivesWhereTheRepositoryKeepsItsOwnFiles(t *testing.T) {
	t.Parallel()
	root := filepath.Join("home", "developer", "project")
	if got, want := keptledger.Path(root), filepath.Join(root, ".goatest", "kept-temp-v1.json"); got != want {
		t.Fatalf("ledger path = %q, want %q", got, want)
	}
}

func TestConcurrentAppendsKeepEveryEntry(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "kept-temp-v1.json")

	const writers, each = 2, 20
	var group sync.WaitGroup
	failures := make(chan error, writers*each)
	for writer := range writers {
		group.Add(1)
		go func() {
			defer group.Done()
			for index := range each {
				name := fmt.Sprintf("goatest-run-%d-%d", writer, index)
				if err := keptledger.Append(path, keptledger.Entry{
					Path: "/tmp/" + name, RunID: name, KeptAt: moment(10).Add(time.Duration(index) * time.Second),
				}); err != nil {
					failures <- err
					return
				}
			}
		}()
	}
	group.Wait()
	close(failures)
	for err := range failures {
		t.Fatalf("append = %v", err)
	}
	ledger, err := keptledger.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(ledger.Entries) != writers*each {
		t.Fatalf("ledger holds %d entries, want the %d that were appended", len(ledger.Entries), writers*each)
	}
}
