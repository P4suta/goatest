// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package cache_test

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
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

func TestGetRejectsInvalidDigestWithoutTouchingTheFilesystem(t *testing.T) {
	root := t.TempDir()
	store := cache.New(root)
	for _, digest := range []string{"", ".", "..", "parent/child", `parent\child`} {
		t.Run(strings.ReplaceAll(digest, "\\", "backslash"), func(t *testing.T) {
			if _, ok, err := store.Get(digest); err == nil || ok || !strings.Contains(err.Error(), "invalid cache digest") {
				t.Fatalf("Get(%q) = ok %v err %v", digest, ok, err)
			}
			if err := store.Put(digest, report.Report{Snapshot: digest}); err == nil || !strings.Contains(err.Error(), "invalid cache digest") {
				t.Fatalf("Put(%q) error = %v", digest, err)
			}
		})
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("invalid digests created %v", entries)
	}
}

func TestGetRejectsReadStrictnessAndIdentityFailures(t *testing.T) {
	for _, testCase := range []struct {
		name      string
		data      []byte
		want      string
		directory bool
	}{
		{name: "read", directory: true},
		{name: "malformed", data: []byte("{"), want: "cache decode"},
		{name: "unknown-field", data: []byte(`{"schema":"assurance-report-v1","snapshot":"digest-a","extra":true}`), want: "cache decode"},
		{name: "trailing", data: append(report.JSON(report.Report{Schema: report.SchemaV1, Snapshot: "digest-a"}), []byte("{}")...), want: "trailing data"},
		{name: "wrong-schema", data: report.JSON(report.Report{Schema: "future-v2", Snapshot: "digest-a"}), want: "identity mismatch"},
		{name: "wrong-snapshot", data: report.JSON(report.Report{Schema: report.SchemaV1, Snapshot: "digest-b"}), want: "identity mismatch"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "v1", "digest-a", "report.json")
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if testCase.directory {
				if err := os.Mkdir(path, 0o755); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(path, testCase.data, 0o644); err != nil {
				t.Fatal(err)
			}
			got, ok, err := cache.New(root).Get("digest-a")
			wrongMessage := err != nil && testCase.want != "" && !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(testCase.want))
			if err == nil || ok || !reflect.DeepEqual(got, report.Report{}) || wrongMessage {
				t.Fatalf("Get = %+v, ok %v, err %v; want %q", got, ok, err, testCase.want)
			}
		})
	}
}

func TestPutRejectsMismatchedSnapshotBeforeCreatingDirectories(t *testing.T) {
	root := t.TempDir()
	err := cache.New(root).Put("digest-a", report.Report{Schema: report.SchemaV1, Snapshot: "digest-b"})
	if err == nil || !strings.Contains(err.Error(), "snapshot does not match") {
		t.Fatalf("Put error = %v", err)
	}
	entries, readErr := os.ReadDir(root)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("mismatched Put created %v", entries)
	}
}

func TestPutReportsDirectoryFailureAndLeavesNoTemporaryFile(t *testing.T) {
	root := t.TempDir()
	blocking := filepath.Join(root, "v1")
	if err := os.WriteFile(blocking, []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := cache.New(root).Put("digest-a", report.Report{Schema: report.SchemaV1, Snapshot: "digest-a"})
	if err == nil {
		t.Fatal("Put succeeded through a non-directory cache root")
	}
	entries, readErr := os.ReadDir(root)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(entries) != 1 || entries[0].Name() != "v1" {
		t.Fatalf("failed Put left entries %v", entries)
	}
}

func TestPutWritesTheDeterministicFinalPathWithoutTemporaryArtifacts(t *testing.T) {
	root := t.TempDir()
	want := report.Report{Schema: report.SchemaV1, Verdict: report.VerdictAssured, Contract: "standard-v1", Snapshot: "digest-a"}
	if err := cache.New(root).Put("digest-a", want); err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(root, "v1", "digest-a")
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "report.json" {
		t.Fatalf("cache entries = %v", entries)
	}
	data, err := os.ReadFile(filepath.Join(directory, "report.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != string(report.JSON(want)) {
		t.Fatalf("cache bytes = %q, want %q", data, report.JSON(want))
	}
}
