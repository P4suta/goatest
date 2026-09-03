// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package tempowner_test

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/P4suta/goatest/internal/tempowner"
)

// claimed makes one directory and claims it, which is the two lines every test
// below starts with.
func claimed(t *testing.T, marker tempowner.Marker, now time.Time) (string, *tempowner.Owner) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "goatest-run-fixture")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	owner, err := tempowner.Claim(dir, marker, now)
	if err != nil {
		t.Fatalf("claim %s = %v", dir, err)
	}
	t.Cleanup(func() { _ = owner.Release() })
	return dir, owner
}

func TestClaimWritesTheOwnerPairAndSaysWhoMadeTheDirectory(t *testing.T) {
	t.Parallel()
	moment := time.Date(2026, 9, 4, 10, 30, 0, 0, time.UTC)
	dir, _ := claimed(t, tempowner.Marker{RunID: "goatest-run-fixture", Root: "/repository"}, moment)
	marker, err := tempowner.ReadMarker(dir)
	if err != nil {
		t.Fatalf("read the marker of %s = %v", dir, err)
	}
	// The marker is read by a person looking at a full disk, so what it says
	// has to identify the run rather than merely prove that something wrote it.
	if marker.Schema != tempowner.Schema || marker.RunID != "goatest-run-fixture" || marker.Root != "/repository" {
		t.Fatalf("marker = %+v, want the schema, the run and the repository it was made for", marker)
	}
	if marker.PID != os.Getpid() || !marker.Started.Equal(moment) || marker.Kept {
		t.Fatalf("marker = %+v, want this process, %s, and no deliberate keep", marker, moment)
	}
	if _, err := os.Stat(tempowner.LockPath(dir)); err != nil {
		t.Fatalf("stat the lock of %s = %v", dir, err)
	}
	if tempowner.MarkerPath(dir) != filepath.Join(dir, "owner.json") ||
		tempowner.LockPath(dir) != filepath.Join(dir, "owner.lock") {
		t.Fatalf("owner pair = %q and %q", tempowner.MarkerPath(dir), tempowner.LockPath(dir))
	}
}

func TestClaimRefusesADirectorySomebodyElseHolds(t *testing.T) {
	t.Parallel()
	dir, _ := claimed(t, tempowner.Marker{RunID: "first"}, time.Now())
	// The lock is the liveness signal, so a second claim has to be refused as
	// long as the first holder is alive, whatever the marker says.
	second, err := tempowner.Claim(dir, tempowner.Marker{RunID: "second"}, time.Now())
	if !errors.Is(err, tempowner.ErrOwned) || second != nil {
		t.Fatalf("second claim = (%v, %v), want it refused as owned", second, err)
	}
	marker, err := tempowner.ReadMarker(dir)
	if err != nil || marker.RunID != "first" {
		t.Fatalf("marker after a refused claim = (%+v, %v), want the first holder's", marker, err)
	}
}

func TestReleaseFreesTheLockAndRemovesNothing(t *testing.T) {
	t.Parallel()
	dir, owner := claimed(t, tempowner.Marker{RunID: "first"}, time.Now())
	if err := owner.Release(); err != nil {
		t.Fatalf("release %s = %v", dir, err)
	}
	// Releasing twice is what a run does when it keeps a directory and then
	// unwinds the defer that would have released it.
	if err := owner.Release(); err != nil {
		t.Fatalf("second release = %v, want it to be idempotent", err)
	}
	if _, err := os.Stat(tempowner.MarkerPath(dir)); err != nil {
		t.Fatalf("the marker after a release = %v, want the directory untouched", err)
	}
	next, err := tempowner.Claim(dir, tempowner.Marker{RunID: "second"}, time.Now())
	if err != nil {
		t.Fatalf("claim after a release = %v, want the lock free", err)
	}
	if err := next.Release(); err != nil {
		t.Fatal(err)
	}
}

func TestKeepRecordsTheDecisionWhereTheNextRunReadsIt(t *testing.T) {
	t.Parallel()
	dir, owner := claimed(t, tempowner.Marker{RunID: "kept", Root: "/repository"}, time.Now())
	if err := owner.Keep(); err != nil {
		t.Fatalf("keep %s = %v", dir, err)
	}
	marker, err := tempowner.ReadMarker(dir)
	if err != nil || !marker.Kept || marker.RunID != "kept" || marker.Root != "/repository" {
		t.Fatalf("marker after a keep = (%+v, %v), want the same run, deliberately kept", marker, err)
	}
	// Keeping releases the lock too. A kept directory whose lock nobody holds
	// is exactly what the marker bit exists to protect from the next sweep.
	next, err := tempowner.Claim(dir, tempowner.Marker{RunID: "second"}, time.Now())
	if err != nil {
		t.Fatalf("claim after a keep = %v, want the lock free", err)
	}
	if err := next.Release(); err != nil {
		t.Fatal(err)
	}
}

func TestKeepingLeavesNothingBesideTheOwnerPair(t *testing.T) {
	t.Parallel()
	dir, owner := claimed(t, tempowner.Marker{RunID: "kept"}, time.Now())
	if err := owner.Keep(); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	// The marker is replaced through a temporary file and a rename, so that a
	// process killed in the middle of recording a keep leaves the marker it had
	// rather than half of the new one: a torn marker does not say kept, and the
	// next sweep would collect the directory somebody asked to keep. What the
	// rename can be seen to do from outside is leave nothing behind.
	names := []string{entries[0].Name(), entries[len(entries)-1].Name()}
	if len(entries) != 2 || names[0] != tempowner.MarkerName || names[1] != tempowner.LockName {
		t.Fatalf("directory after a keep = %v, want the owner pair alone", names)
	}
}

func TestReadMarkerReportsADirectoryThatCarriesNone(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	marker, err := tempowner.ReadMarker(dir)
	if !errors.Is(err, fs.ErrNotExist) || marker != (tempowner.Marker{}) {
		t.Fatalf("marker of an unowned directory = (%+v, %v), want it reported as missing", marker, err)
	}
}

func TestClaimFailsOnADirectoryThatIsNotThere(t *testing.T) {
	t.Parallel()
	// A run whose owner pair cannot be written carries on without one, so the
	// failure has to arrive as an error the caller can report rather than as a
	// panic or a half-claimed directory.
	missing := filepath.Join(t.TempDir(), "absent")
	owner, err := tempowner.Claim(missing, tempowner.Marker{RunID: "run"}, time.Now())
	if err == nil || owner != nil {
		t.Fatalf("claim of a missing directory = (%v, %v), want a failure", owner, err)
	}
}

func TestTheMarkerIsTheDocumentTheSchemaPromises(t *testing.T) {
	t.Parallel()
	moment := time.Date(2026, 9, 4, 10, 30, 0, 0, time.UTC)
	dir, _ := claimed(t, tempowner.Marker{RunID: "goatest-run-fixture", Root: "/repository"}, moment)
	raw, err := os.ReadFile(tempowner.MarkerPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	// A person reading a full disk reads this file with their eyes, so its
	// field names are part of the convention and not an encoding detail.
	var document map[string]any
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatalf("decode %s = %v", tempowner.MarkerPath(dir), err)
	}
	want := map[string]any{
		"schema": "goatest-temp-owner-v1", "run_id": "goatest-run-fixture",
		"pid": float64(os.Getpid()), "started": "2026-09-04T10:30:00Z",
		"root": "/repository", "kept": false,
	}
	for name, value := range want {
		if document[name] != value {
			t.Fatalf("marker field %q = %v, want %v", name, document[name], value)
		}
	}
	if len(document) != len(want) {
		t.Fatalf("marker = %v, want exactly %d fields", document, len(want))
	}
}
