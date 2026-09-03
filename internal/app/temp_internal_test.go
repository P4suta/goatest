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
	"github.com/P4suta/goatest/internal/keptledger"
	"github.com/P4suta/goatest/internal/report"
	"github.com/P4suta/goatest/internal/tempowner"
)

// abandonedRunScratch makes the directory a run that was killed would have left
// behind: claimed, released without being kept, and holding a payload of a
// known size.
func abandonedRunScratch(t *testing.T, parent, name string, bytes int) string {
	t.Helper()
	directory := filepath.Join(parent, name)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "payload"), make([]byte, bytes), 0o600); err != nil {
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

// keptDirectory makes a directory a --keep-temp run would have left behind.
func keptDirectory(t *testing.T, parent, name string, bytes int) string {
	t.Helper()
	directory := filepath.Join(parent, name)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "payload"), make([]byte, bytes), 0o600); err != nil {
		t.Fatal(err)
	}
	return directory
}

// A person who suspects goatest of filling their disk asks `cache status`, and
// until now it answered about the repository's own caches alone — while every
// byte the tool actually leaves behind is in the temporary directory.
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
	// The size is the fixture's own, measured rather than assumed: an
	// abandoned directory holds its owner pair as well as what the run wrote.
	orphaned := fmt.Sprintf("abandoned=1 bytes=%d live=0 kept=0", tempowner.Size(orphan))
	if !hasEvidenceDetail(status, "orphans", orphaned) {
		t.Fatalf("cache status evidence = %+v, want %q", status.Evidence, orphaned)
	}
	// One entry per kept directory, saying whether it is still there, and a
	// total for the reader who only wants to know whether to care.
	if !hasEvidenceStatus(status, "goatest-run-kept", "kept") || !hasEvidenceStatus(status, "goatest-run-gone", "missing") {
		t.Fatalf("cache status evidence = %+v, want both ledger entries reported", status.Evidence)
	}
	if !hasEvidenceDetail(status, "kept-temp-status", "entries=2 bytes=1032 missing=1") {
		t.Fatalf("cache status evidence = %+v, want the kept total reported", status.Evidence)
	}
	// Status inspects and never collects: a person asking what is on their
	// disk has not asked for any of it to be removed.
	if _, err := os.Stat(orphan); err != nil {
		t.Fatalf("stat the orphan after a status = %v, want it untouched", err)
	}
}

// `cache gc` is the one command that removes what runs left in the temporary
// directory: the orphans of runs that were killed, and the directories a
// --keep-temp run kept once they are older than the cache TTL.
func TestCacheGCCollectsTheOrphansAndTheKeptDirectoriesTheTTLHasExpired(t *testing.T) {
	t.Parallel()
	root, temporary := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".goatest.toml"),
		[]byte("version = 1\ncontract = \"standard-v1\"\n[cache]\nttl = \"1h\"\n"), 0o600); err != nil {
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
	reclaimed := fmt.Sprintf("removed=1 bytes=%d live=0 kept=0", tempowner.Size(orphan))
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
	// The expired directory goes with its entry; the one that was already gone
	// takes only its entry; and a keep the TTL has not reached is left where
	// the developer who asked for it expects to find it.
	if !hasEvidenceDetail(collected, "kept-temp-gc", "removed-entries=2 removed-bytes=2048 remaining=1") {
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

// A service that names no temporary directory has not named the machine's own:
// it has named nothing. Maintenance therefore sweeps nothing and says so, which
// is the whole of the lesson from the afternoon a `cache gc` in a test with a
// clock a day ahead collected the scratch directory of a run that was using it.
func TestCacheMaintenanceNeverSweepsADirectoryNobodyNamed(t *testing.T) {
	t.Parallel()
	// In the machine's own temporary directory, because that is the directory
	// this test exists to protect: unowned, old enough for the legacy rule, and
	// removed by this test whatever happens.
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

// The temporary directory of a machine nothing has run on is not a failure, and
// neither is a repository whose runs have never kept anything: `cache status`
// answers about them the same way it answers about an empty cache.
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

// A directory that cannot be stat'ed is not a directory that is gone. Treating
// every stat failure as "missing" drops the entry while the directory stays on
// the disk, which is the one outcome this ledger exists to prevent: a kept
// directory nothing tracks any more and nothing will ever collect.
func TestAnEntryThatCannotBeStatedKeepsItsPlaceInTheLedger(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		// A path below a regular file reports "not exist" there, so the case
		// this test is about cannot be produced the same way.
		t.Skip("this platform reports a path below a file as not existing")
	}
	root, temporary := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(temporary, "file"), []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Below a regular file: the stat fails with ENOTDIR, which says nothing
	// about whether anything is there.
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
		[]byte("version = 1\ncontract = \"standard-v1\"\n[cache]\nttl = \"1h\"\n"), 0o600); err != nil {
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
