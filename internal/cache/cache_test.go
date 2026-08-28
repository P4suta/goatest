// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package cache_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/P4suta/goatest/internal/cache"
	"github.com/P4suta/goatest/internal/report"
)

func TestStoreAndLoadReuseOnlyTheExactDigest(t *testing.T) {
	store := cache.New(t.TempDir())
	want := report.Report{Schema: report.SchemaV1, Verdict: report.VerdictAssured, Contract: "standard-v1", Snapshot: "digest-a"}
	if err := store.Put("digest-a", want); err != nil {
		t.Fatal(err)
	}
	got, ok, err := store.Get("digest-a")
	if err != nil || !ok || got.Snapshot != "digest-a" || got.Verdict != report.VerdictAssured {
		t.Fatalf("Get exact = %+v, %v, %v", got, ok, err)
	}
	if _, ok, err := store.Get("digest-b"); err != nil || ok {
		t.Fatalf("Get other = ok %v err %v", ok, err)
	}
}

func TestCorruptOrMismatchedEntriesFailClosed(t *testing.T) {
	root := t.TempDir()
	store := cache.New(root)
	if err := store.Put("digest-a", report.Report{Schema: report.SchemaV1, Verdict: report.VerdictAssured, Snapshot: "different"}); err == nil {
		t.Fatal("Put accepted a mismatched snapshot")
	}
	directory := filepath.Join(root, "v1", "digest-a")
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "report.json"), []byte("not-json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := store.Get("digest-a"); err == nil || ok {
		t.Fatalf("corrupt Get = ok %v err %v", ok, err)
	}
}
