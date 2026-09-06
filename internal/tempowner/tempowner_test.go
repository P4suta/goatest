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

	"github.com/P4suta/goatest/internal/filemode"
	"github.com/P4suta/goatest/internal/tempowner"
)

func claimed(t *testing.T, marker tempowner.Marker, now time.Time) (string, *tempowner.Owner) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "goatest-run-fixture")
	if err := os.Mkdir(dir, filemode.PrivateDirectory); err != nil {
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

func writeMarkerDocument(t *testing.T, dir, document string) string {
	t.Helper()
	if err := os.MkdirAll(dir, filemode.PrivateDirectory); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tempowner.MarkerPath(dir), []byte(document), filemode.PrivateFile); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestKeptByAsksTheDirectoryWhetherSomebodyKeptIt(t *testing.T) {
	t.Parallel()
	parent := t.TempDir()
	for _, test := range []struct {
		name     string
		document string
		want     bool
	}{
		{
			name:     "kept by the run that recorded it",
			document: `{"schema":"goatest-temp-owner-v1","run_id":"goatest-run-a","pid":1,"started":"2026-09-04T12:00:00Z","root":"/repository","kept":true}`,
			want:     true,
		},
		{
			name:     "kept by another run",
			document: `{"schema":"goatest-temp-owner-v1","run_id":"goatest-run-b","pid":1,"started":"2026-09-04T12:00:00Z","root":"/repository","kept":true}`,
		},
		{
			name:     "owned by a run that never kept it",
			document: `{"schema":"goatest-temp-owner-v1","run_id":"goatest-run-a","pid":1,"started":"2026-09-04T12:00:00Z","root":"/repository","kept":false}`,
		},
		{
			name:     "a marker of some other tool",
			document: `{"schema":"somebody-elses-v1","kept":true}`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			dir := writeMarkerDocument(t, filepath.Join(parent, test.name), test.document)
			kept, err := tempowner.KeptBy(dir, "goatest-run-a")
			if err != nil || kept != test.want {
				t.Fatalf("KeptBy = (%t, %v), want %t", kept, err, test.want)
			}
		})
	}
}

func TestKeptByRefusesADirectoryThatSaysNothing(t *testing.T) {
	t.Parallel()

	unrelated := t.TempDir()
	if err := os.WriteFile(filepath.Join(unrelated, "notes.txt"), []byte("mine"), filemode.PrivateFile); err != nil {
		t.Fatal(err)
	}
	if kept, err := tempowner.KeptBy(unrelated, "goatest-run-a"); err != nil || kept {
		t.Fatalf("KeptBy of a directory with no marker = (%t, %v), want it refused without a failure", kept, err)
	}
	if kept, err := tempowner.KeptBy(filepath.Join(unrelated, "absent"), "goatest-run-a"); err != nil || kept {
		t.Fatalf("KeptBy of a missing directory = (%t, %v), want it refused without a failure", kept, err)
	}

	torn := writeMarkerDocument(t, filepath.Join(t.TempDir(), "torn"), `{"schema":"goatest-temp-`)
	if kept, err := tempowner.KeptBy(torn, "goatest-run-a"); err == nil || kept {
		t.Fatalf("KeptBy of a torn marker = (%t, %v), want the failure reported", kept, err)
	}
}
