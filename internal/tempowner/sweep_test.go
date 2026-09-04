// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package tempowner_test

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/P4suta/goatest/internal/tempowner"
)

// sweepPrefixes are the names a goatest sweep is allowed to collect. The tests
// below use one of them wherever the name itself is not the point.
func sweepPrefixes() []string { return []string{"goatest-run-", "goatest-baseline-"} }

// directory makes one child of parent holding a file of a known size, so that
// what a sweep reports as reclaimed can be checked against what was there.
func directory(t *testing.T, parent, name string, bytes int) string {
	t.Helper()
	dir := filepath.Join(parent, name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "payload"), make([]byte, bytes), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// aged backdates a directory past the age at which an unowned one is a
// leftover.
func aged(t *testing.T, dir string, now time.Time) string {
	t.Helper()
	moment := now.Add(-tempowner.LegacyMaxAge - time.Hour)
	if err := os.Chtimes(dir, moment, moment); err != nil {
		t.Fatal(err)
	}
	return dir
}

// abandon claims a directory and then frees the lock without recording a keep,
// which is what an operating system does for a run it killed.
func abandon(t *testing.T, dir string) {
	t.Helper()
	owner, err := tempowner.Claim(dir, tempowner.Marker{RunID: filepath.Base(dir)}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := owner.Release(); err != nil {
		t.Fatal(err)
	}
}

func TestSweepCollectsTheDirectoryOfARunThatWasKilled(t *testing.T) {
	t.Parallel()
	parent := t.TempDir()
	dead := directory(t, parent, "goatest-run-dead", 4096)
	abandon(t, dead)
	result, err := tempowner.Sweep(parent, sweepPrefixes(), time.Now())
	if err != nil || len(result.Errors) != 0 {
		t.Fatalf("sweep = (%+v, %v), want no failure", result, err)
	}
	if !slices.Equal(result.Removed, []string{dead}) || result.Live != 0 || result.Kept != 0 {
		t.Fatalf("sweep = %+v, want the abandoned directory alone", result)
	}
	// The bytes are what a person watching a disk fill up wants reported, so
	// they have to be the ones the directory actually held.
	if result.RemovedBytes < 4096 {
		t.Fatalf("reclaimed bytes = %d, want at least the 4096 the directory held", result.RemovedBytes)
	}
	if _, err := os.Stat(dead); !os.IsNotExist(err) {
		t.Fatalf("stat the collected directory = %v, want it gone", err)
	}
}

func TestSweepLeavesTheDirectoryOfARunningProcessAlone(t *testing.T) {
	t.Parallel()
	parent := t.TempDir()
	live := directory(t, parent, "goatest-run-live", 16)
	owner, err := tempowner.Claim(live, tempowner.Marker{RunID: "live"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = owner.Release() })
	result, err := tempowner.Sweep(parent, sweepPrefixes(), time.Now())
	if err != nil || result.Live != 1 || len(result.Removed) != 0 {
		t.Fatalf("sweep = (%+v, %v), want the held directory counted and untouched", result, err)
	}
	if _, err := os.Stat(live); err != nil {
		t.Fatalf("stat the live directory = %v, want it where its owner left it", err)
	}
}

func TestSweepNeverCollectsADirectoryKeptOnPurpose(t *testing.T) {
	t.Parallel()
	parent := t.TempDir()
	kept := directory(t, parent, "goatest-run-kept", 16)
	owner, err := tempowner.Claim(kept, tempowner.Marker{RunID: "kept"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := owner.Keep(); err != nil {
		t.Fatal(err)
	}
	// A kept directory has no live process behind it and never will have one.
	// The marker is the whole of its protection, whatever its age.
	now := time.Now()
	aged(t, kept, now)
	result, err := tempowner.Sweep(parent, sweepPrefixes(), now)
	if err != nil {
		t.Fatal(err)
	}
	if result.Kept != 1 || len(result.Removed) != 0 {
		t.Fatalf("sweep = %+v, want the kept directory counted and untouched", result)
	}
	if _, err := os.Stat(kept); err != nil {
		t.Fatalf("stat the kept directory = %v, want it where the run left it", err)
	}
}

func TestSweepCollectsAnUnownedDirectoryOnlyOnceItIsOldEnough(t *testing.T) {
	t.Parallel()
	now := time.Now()
	parent := t.TempDir()
	// Both are directories no version of goatest ever claimed. Age is the only
	// evidence there is, and a young one may be a run in progress under a
	// binary from before the owner pair existed.
	old := aged(t, directory(t, parent, "goatest-baseline-old", 32), now)
	young := directory(t, parent, "goatest-baseline-young", 32)
	result, err := tempowner.Sweep(parent, sweepPrefixes(), now)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(result.Removed, []string{old}) {
		t.Fatalf("sweep = %+v, want the aged unowned directory alone", result)
	}
	if _, err := os.Stat(young); err != nil {
		t.Fatalf("stat the young unowned directory = %v, want it spared", err)
	}
	// A spared directory is not a live one: counting it as live would put a
	// number in the result that nothing on the disk backs up.
	if result.Live != 0 || result.Kept != 0 {
		t.Fatalf("sweep = %+v, want the young directory counted as neither live nor kept", result)
	}
}

func TestSweepTouchesNothingItWasNotToldAbout(t *testing.T) {
	t.Parallel()
	now := time.Now()
	parent := t.TempDir()
	unrelated := aged(t, directory(t, parent, "somebody-elses-work", 8), now)
	prefixedFile := filepath.Join(parent, "goatest-run-notadirectory")
	if err := os.WriteFile(prefixedFile, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(prefixedFile, now.Add(-2*tempowner.LegacyMaxAge), now.Add(-2*tempowner.LegacyMaxAge)); err != nil {
		t.Fatal(err)
	}
	result, err := tempowner.Sweep(parent, sweepPrefixes(), now)
	if err != nil || len(result.Removed) != 0 {
		t.Fatalf("sweep = (%+v, %v), want nothing collected", result, err)
	}
	for _, path := range []string{unrelated, prefixedFile} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("stat %s = %v, want it left exactly as it was found", path, err)
		}
	}
}

func TestSweepNeverFollowsASymbolicLink(t *testing.T) {
	t.Parallel()
	now := time.Now()
	parent := t.TempDir()
	target := directory(t, t.TempDir(), "real", 8)
	link := filepath.Join(parent, "goatest-run-link")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("this platform does not let the test make a symbolic link: %v", err)
	}
	// A link wearing a swept prefix is somebody's shortcut to a directory that
	// is none of the sweep's business, and removing it through the link would
	// take the target with it.
	result, err := tempowner.Sweep(parent, sweepPrefixes(), now.Add(2*tempowner.LegacyMaxAge))
	if err != nil || len(result.Removed) != 0 {
		t.Fatalf("sweep = (%+v, %v), want the link left alone", result, err)
	}
	if _, err := os.Lstat(link); err != nil {
		t.Fatalf("lstat the link = %v, want it where it was", err)
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("stat the link target = %v, want it untouched", err)
	}
}

func TestSweepFinishesTheOthersWhenOneEntryCannotBeJudged(t *testing.T) {
	t.Parallel()
	parent := t.TempDir()
	unreadable := directory(t, parent, "goatest-run-unreadable", 8)
	abandon(t, unreadable)
	// A lock that is a directory can be neither opened nor taken, which is the
	// shape every "this entry cannot be judged" failure has.
	if err := os.Remove(tempowner.LockPath(unreadable)); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(tempowner.LockPath(unreadable), 0o700); err != nil {
		t.Fatal(err)
	}
	dead := directory(t, parent, "goatest-run-dead", 8)
	abandon(t, dead)
	result, err := tempowner.Sweep(parent, sweepPrefixes(), time.Now())
	if err != nil {
		t.Fatalf("sweep = %v, want the parent itself readable", err)
	}
	// Leaving a gigabyte on the disk because of one unrelated permission
	// problem would be the wrong trade.
	if !slices.Equal(result.Removed, []string{dead}) || len(result.Errors) != 1 {
		t.Fatalf("sweep = %+v, want the other directory collected and one failure reported", result)
	}
	if _, err := os.Stat(unreadable); err != nil {
		t.Fatalf("stat the directory that could not be judged = %v, want it spared", err)
	}
}

func TestSweepOfAParentNothingHasRunInIsNotAFailure(t *testing.T) {
	t.Parallel()
	result, err := tempowner.Sweep(filepath.Join(t.TempDir(), "absent"), sweepPrefixes(), time.Now())
	if err != nil || len(result.Removed) != 0 || len(result.Errors) != 0 {
		t.Fatalf("sweep of a missing parent = (%+v, %v), want an empty answer", result, err)
	}
}

func TestInspectClassifiesExactlyAsASweepAndRemovesNothing(t *testing.T) {
	t.Parallel()
	now := time.Now()
	parent := t.TempDir()
	dead := directory(t, parent, "goatest-run-dead", 2048)
	abandon(t, dead)
	kept := directory(t, parent, "goatest-run-kept", 8)
	keeper, err := tempowner.Claim(kept, tempowner.Marker{RunID: "kept"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := keeper.Keep(); err != nil {
		t.Fatal(err)
	}
	result, err := tempowner.Inspect(parent, sweepPrefixes(), now)
	if err != nil {
		t.Fatal(err)
	}
	// `cache status` reports what a `cache gc` would reclaim, so an inspection
	// has to name the same directories a sweep would have taken.
	if !slices.Equal(result.Removed, []string{dead}) || result.Kept != 1 || result.RemovedBytes < 2048 {
		t.Fatalf("inspect = %+v, want the abandoned directory named and the kept one counted", result)
	}
	if _, err := os.Stat(dead); err != nil {
		t.Fatalf("stat the abandoned directory after an inspection = %v, want it still there", err)
	}
}

func TestADetailLineSaysWhatTheSweepDid(t *testing.T) {
	t.Parallel()
	result := tempowner.Result{
		Removed: []string{"a", "b"}, RemovedBytes: 2048, Live: 3, Kept: 1,
	}
	// The same four counts read from a progress note and from a report's
	// evidence, so they are rendered in one place; only the first count is
	// named by the caller, because a sweep removed what an inspection found.
	if got, want := result.Detail("removed"), "removed=2 bytes=2048 live=3 kept=1"; got != want {
		t.Fatalf("detail = %q, want %q", got, want)
	}
	if got, want := result.Detail("abandoned"), "abandoned=2 bytes=2048 live=3 kept=1"; got != want {
		t.Fatalf("detail = %q, want %q", got, want)
	}
	failed := tempowner.Result{Errors: []error{os.ErrPermission}}
	if got, want := failed.Detail("removed"), "removed=0 bytes=0 live=0 kept=0 errors=1"; got != want {
		t.Fatalf("failed detail = %q, want %q", got, want)
	}
}

// unnamedParentFixture makes a directory in this process's own temporary
// directory that a sweep would take if it swept there: goatest's name, no
// marker, and older than the age at which an unowned directory is a leftover.
func unnamedParentFixture(t *testing.T, now time.Time) string {
	t.Helper()
	dir, err := os.MkdirTemp(os.TempDir(), "goatest-run-unnamed-parent-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	if err := os.WriteFile(filepath.Join(dir, "payload"), make([]byte, 16), 0o600); err != nil {
		t.Fatal(err)
	}
	return aged(t, dir, now)
}

func TestASweepOfAParentNobodyNamedCollectsNothing(t *testing.T) {
	t.Parallel()
	now := time.Now()
	fixture := unnamedParentFixture(t, now)
	// The empty parent is the case that matters, because it is the one a caller
	// reaches by accident rather than on purpose: a value that names no
	// temporary directory. Reading it as the machine's own temporary directory
	// would collect the directories of every goatest on the machine, including
	// the ones live runs are working in — which is exactly what happened once.
	// A directory nobody named is nobody's to sweep.
	result, err := tempowner.Sweep("", sweepPrefixes(), now)
	if err != nil || len(result.Removed) != 0 || len(result.Errors) != 0 {
		t.Fatalf("sweep of an unnamed parent = (%+v, %v), want an empty answer", result, err)
	}
	if _, err := os.Stat(fixture); err != nil {
		t.Fatalf("stat %s after a sweep of an unnamed parent = %v, want it untouched", fixture, err)
	}
	inspected, err := tempowner.Inspect("", sweepPrefixes(), now)
	if err != nil || len(inspected.Removed) != 0 {
		t.Fatalf("inspection of an unnamed parent = (%+v, %v), want an empty answer", inspected, err)
	}
}
