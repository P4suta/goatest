// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package app

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/P4suta/goatest/internal/cli"
	"github.com/P4suta/goatest/internal/filemode"
	"github.com/P4suta/goatest/internal/keptledger"
	"github.com/P4suta/goatest/internal/report"
	"github.com/P4suta/goatest/internal/tempowner"
)

func abandonedRunScratch(t *testing.T, parent, name string, bytes int) string {
	t.Helper()
	directory := filepath.Join(parent, name)
	if err := os.MkdirAll(directory, filemode.PrivateDirectory); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "payload"), make([]byte, bytes), filemode.PrivateFile); err != nil {
		t.Fatal(err)
	}
	owner, err := tempowner.Claim(directory, tempowner.Marker{RunID: name}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := owner.Release(); err != nil {
		t.Fatal(err)
	}
	return directory
}

func keptDirectory(t *testing.T, parent, name string, bytes int) string {
	t.Helper()
	directory := filepath.Join(parent, name)
	if err := os.MkdirAll(directory, filemode.PrivateDirectory); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "payload"), make([]byte, bytes), filemode.PrivateFile); err != nil {
		t.Fatal(err)
	}
	owner, err := tempowner.Claim(directory, tempowner.Marker{RunID: name}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := owner.Keep(); err != nil {
		t.Fatal(err)
	}
	return directory
}

func TestCacheStatusReportsTheOrphansAndTheDirectoriesRunsKept(t *testing.T) {
	t.Parallel()
	root, temporary := t.TempDir(), t.TempDir()
	orphan := abandonedRunScratch(t, temporary, "goatest-run-dead", 4096)
	kept := keptDirectory(t, temporary, "goatest-run-kept", 1024)
	moment := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	if err := keptledger.Append(keptledger.Path(root),
		keptledger.Entry{Path: kept, RunID: "goatest-run-kept", KeptAt: moment, Bytes: 1024},
		keptledger.Entry{Path: filepath.Join(temporary, "goatest-run-gone"), RunID: "goatest-run-gone", KeptAt: moment, Bytes: 8},
	); err != nil {
		t.Fatal(err)
	}
	service := Service{
		Root: root, Progress: io.Discard, TempDirectory: temporary,
		Now: func() time.Time { return moment.Add(time.Hour) },
	}
	status, err := service.Execute(t.Context(), cli.CommandCache, cli.Request{}, "status")
	if err != nil || status.Verdict != report.VerdictCompleted {
		t.Fatalf("cache status = %+v, %v", status, err)
	}

	orphaned := fmt.Sprintf("abandoned=1 bytes=%d live=0 kept=1", tempowner.Size(orphan))
	if !hasEvidenceDetail(status, "orphans", orphaned) {
		t.Fatalf("cache status evidence = %+v, want %q", status.Evidence, orphaned)
	}

	if !hasEvidenceStatus(status, "goatest-run-kept", "kept") || !hasEvidenceStatus(status, "goatest-run-gone", "missing") {
		t.Fatalf("cache status evidence = %+v, want both ledger entries reported", status.Evidence)
	}
	if !hasEvidenceDetail(status, "kept-temp-status", "entries=2 bytes=1032 missing=1") {
		t.Fatalf("cache status evidence = %+v, want the kept total reported", status.Evidence)
	}

	if _, err := os.Stat(orphan); err != nil {
		t.Fatalf("stat the orphan after a status = %v, want it untouched", err)
	}
}

func TestCacheGCCollectsTheOrphansAndTheKeptDirectoriesTheTTLHasExpired(t *testing.T) {
	t.Parallel()
	root, temporary := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".goatest.toml"),
		[]byte("version = 1\ncontract = \"standard-v1\"\n[cache]\nttl = \"1h\"\n"), filemode.PrivateFile); err != nil {
		t.Fatal(err)
	}
	orphan := abandonedRunScratch(t, temporary, "goatest-run-dead", 4096)
	expired := keptDirectory(t, temporary, "goatest-run-old", 2048)
	current := keptDirectory(t, temporary, "goatest-run-new", 512)
	moment := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	if err := keptledger.Append(keptledger.Path(root),
		keptledger.Entry{Path: expired, RunID: "goatest-run-old", KeptAt: moment.Add(-2 * time.Hour), Bytes: 2048},
		keptledger.Entry{Path: current, RunID: "goatest-run-new", KeptAt: moment, Bytes: 512},
		keptledger.Entry{Path: filepath.Join(temporary, "goatest-run-gone"), RunID: "goatest-run-gone", KeptAt: moment, Bytes: 8},
	); err != nil {
		t.Fatal(err)
	}
	service := Service{
		Root: root, Progress: io.Discard, TempDirectory: temporary,
		Now: func() time.Time { return moment.Add(time.Minute) },
	}
	reclaimed := fmt.Sprintf("removed=1 bytes=%d live=0 kept=2", tempowner.Size(orphan))

	expiredBytes := fmt.Sprintf("removed-entries=2 removed-bytes=%d remaining=1", tempowner.Size(expired))
	collected, err := service.Execute(t.Context(), cli.CommandCache, cli.Request{}, "gc")
	if err != nil || collected.Verdict != report.VerdictCompleted {
		t.Fatalf("cache gc = %+v, %v", collected, err)
	}
	if !hasEvidenceDetail(collected, "sweep", reclaimed) {
		t.Fatalf("cache gc evidence = %+v, want %q", collected.Evidence, reclaimed)
	}
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Fatalf("stat the orphan after a gc = %v, want it gone", err)
	}

	if !hasEvidenceDetail(collected, "kept-temp-gc", expiredBytes) {
		t.Fatalf("cache gc evidence = %+v, want the expired keep collected", collected.Evidence)
	}
	if _, err := os.Stat(expired); !os.IsNotExist(err) {
		t.Fatalf("stat the expired keep after a gc = %v, want it gone", err)
	}
	if _, err := os.Stat(current); err != nil {
		t.Fatalf("stat the current keep after a gc = %v, want it kept", err)
	}
	ledger, err := keptledger.Load(keptledger.Path(root))
	if err != nil {
		t.Fatal(err)
	}
	if len(ledger.Entries) != 1 || ledger.Entries[0].Path != current {
		t.Fatalf("ledger after a gc = %+v, want the one directory still on the disk", ledger.Entries)
	}
}

func TestCacheMaintenanceNeverSweepsADirectoryNobodyNamed(t *testing.T) {
	t.Parallel()

	fixture, err := os.MkdirTemp(os.TempDir(), "goatest-run-unnamed-parent-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(fixture) })
	stale := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(fixture, stale, stale); err != nil {
		t.Fatal(err)
	}
	service := Service{
		Root: t.TempDir(), Progress: io.Discard,
		Now: func() time.Time { return time.Now().Add(48 * time.Hour) },
	}
	for _, action := range []string{"status", "gc"} {
		result, err := service.Execute(t.Context(), cli.CommandCache, cli.Request{}, action)
		if err != nil || result.Verdict != report.VerdictCompleted {
			t.Fatalf("cache %s = %+v, %v", action, result, err)
		}
		for _, id := range []string{"orphans", "sweep"} {
			for _, item := range result.Evidence {
				if item.ID == id && item.Status != "skipped" {
					t.Fatalf("cache %s evidence %+v, want the temporary directory reported as skipped", action, item)
				}
			}
		}
		if _, err := os.Stat(fixture); err != nil {
			t.Fatalf("stat %s after a cache %s = %v, want it untouched", fixture, action, err)
		}
	}
}

func TestCacheStatusOfAMachineThatHasKeptNothingReportsNothing(t *testing.T) {
	t.Parallel()
	service := Service{Root: t.TempDir(), Progress: io.Discard, TempDirectory: t.TempDir()}
	status, err := service.Execute(t.Context(), cli.CommandCache, cli.Request{}, "status")
	if err != nil || status.Verdict != report.VerdictCompleted {
		t.Fatalf("cache status = %+v, %v", status, err)
	}
	if !hasEvidenceDetail(status, "orphans", "abandoned=0 bytes=0 live=0 kept=0") {
		t.Fatalf("cache status evidence = %+v, want an empty temporary directory reported", status.Evidence)
	}
	if !hasEvidenceDetail(status, "kept-temp-status", "entries=0 bytes=0 missing=0") {
		t.Fatalf("cache status evidence = %+v, want an empty ledger reported", status.Evidence)
	}
}

func TestAnEntryThatCannotBeStatedKeepsItsPlaceInTheLedger(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("this platform reports a path below a file as not existing")
	}
	root, temporary := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(temporary, "file"), []byte("not a directory"), filemode.PrivateFile); err != nil {
		t.Fatal(err)
	}

	unreadable := filepath.Join(temporary, "file", "child")
	moment := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	if err := keptledger.Append(keptledger.Path(root),
		keptledger.Entry{Path: unreadable, RunID: "goatest-run-unreadable", KeptAt: moment, Bytes: 64},
	); err != nil {
		t.Fatal(err)
	}
	service := Service{
		Root: root, Progress: io.Discard, TempDirectory: temporary,
		Now: func() time.Time { return moment.Add(48 * time.Hour) },
	}
	status, err := service.Execute(t.Context(), cli.CommandCache, cli.Request{}, "status")
	if err != nil {
		t.Fatal(err)
	}
	if !hasEvidenceStatus(status, "goatest-run-unreadable", "unreadable") {
		t.Fatalf("cache status evidence = %+v, want the entry reported as unreadable", status.Evidence)
	}
	if !hasEvidenceDetail(status, "kept-temp-status", "missing=0") {
		t.Fatalf("cache status evidence = %+v, want an entry nobody could stat counted as present", status.Evidence)
	}
	if err := os.WriteFile(filepath.Join(root, ".goatest.toml"),
		[]byte("version = 1\ncontract = \"standard-v1\"\n[cache]\nttl = \"1h\"\n"), filemode.PrivateFile); err != nil {
		t.Fatal(err)
	}
	collected, err := service.Execute(t.Context(), cli.CommandCache, cli.Request{}, "gc")
	if err != nil {
		t.Fatal(err)
	}
	if !hasEvidenceDetail(collected, "kept-temp-gc", "removed-entries=0 removed-bytes=0 remaining=1 errors=1") {
		t.Fatalf("cache gc evidence = %+v, want the entry retained and the failure counted", collected.Evidence)
	}
	ledger, err := keptledger.Load(keptledger.Path(root))
	if err != nil {
		t.Fatal(err)
	}
	if len(ledger.Entries) != 1 || ledger.Entries[0].Path != unreadable {
		t.Fatalf("ledger after a gc = %+v, want the entry nobody could judge still recorded", ledger.Entries)
	}
}

func TestCacheGCRemovesOnlyADirectoryThatSaysItWasKept(t *testing.T) {
	t.Parallel()
	root, temporary := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".goatest.toml"),
		[]byte("version = 1\ncontract = \"standard-v1\"\n[cache]\nttl = \"1h\"\n"), filemode.PrivateFile); err != nil {
		t.Fatal(err)
	}

	unrelated := filepath.Join(t.TempDir(), "important")
	if err := os.MkdirAll(filepath.Join(unrelated, "src"), filemode.PrivateDirectory); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(unrelated, "src", "main.go"), []byte("package main\n"), filemode.PrivateFile); err != nil {
		t.Fatal(err)
	}
	kept := keptDirectory(t, temporary, "goatest-run-kept", 128)

	moment := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	stale := moment.Add(-2 * time.Hour)
	if err := keptledger.Append(keptledger.Path(root),
		keptledger.Entry{Path: unrelated, RunID: "goatest-run-kept", KeptAt: stale, Bytes: 16},
		keptledger.Entry{Path: kept, RunID: "goatest-run-kept", KeptAt: stale, Bytes: 128},
	); err != nil {
		t.Fatal(err)
	}
	service := Service{
		Root: root, Progress: io.Discard, TempDirectory: temporary,
		Now: func() time.Time { return moment },
	}
	status, err := service.Execute(t.Context(), cli.CommandCache, cli.Request{}, "status")
	if err != nil {
		t.Fatal(err)
	}
	if !hasEvidenceStatus(status, "goatest-run-kept", "unverified") {
		t.Fatalf("cache status evidence = %+v, want the entry nothing vouches for reported as unverified", status.Evidence)
	}
	collected, err := service.Execute(t.Context(), cli.CommandCache, cli.Request{}, "gc")
	if err != nil || collected.Verdict != report.VerdictCompleted {
		t.Fatalf("cache gc = %+v, %v", collected, err)
	}

	if _, err := os.Stat(kept); !os.IsNotExist(err) {
		t.Fatalf("stat %s after a gc = %v, want it collected", kept, err)
	}
	if _, err := os.Stat(filepath.Join(unrelated, "src", "main.go")); err != nil {
		t.Fatalf("stat the file in the unrelated directory = %v, want it untouched", err)
	}
	if !hasEvidenceDetail(collected, "kept-temp-gc", "removed-entries=1 removed-bytes=") {
		t.Fatalf("cache gc evidence = %+v, want the vouched run root collected", collected.Evidence)
	}
	if !hasEvidenceDetail(collected, "kept-temp-gc", "remaining=1 errors=1") {
		t.Fatalf("cache gc evidence = %+v, want the unvouched entry retained and counted", collected.Evidence)
	}
	ledger, err := keptledger.Load(keptledger.Path(root))
	if err != nil {
		t.Fatal(err)
	}
	if len(ledger.Entries) != 1 || ledger.Entries[0].Path != unrelated {
		t.Fatalf("ledger after a gc = %+v, want the entry nothing vouched for still listed", ledger.Entries)
	}
}
