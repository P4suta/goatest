// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package buildcache

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// claimMoment is the fixed moment these assertions are timed against, so a test
// states the age of a leftover file rather than racing the wall clock.
var claimMoment = time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)

// TestClaimSweepsOnlyTheMarkerTemporaryItsOwnCrashCouldHaveLeft is the recovery
// from a crash in the middle of Prepare.
//
// Prepare writes the marker through a temporary in the layer root and renames
// it into place, so a process killed between the two leaves `.marker-*.tmp`
// behind. That file is goatest's own, and refusing the layer over it would put
// the machine's build cache permanently out of reach of the tool that made it
// until a human deleted the file by hand.
//
// It is swept only once it is stale. A Prepare running in another process right
// now is between the create and the rename for microseconds, and its temporary
// has to still be there for it to rename. Anything else at the root is still
// somebody else's, which is the whole point of the check: a foreign directory
// may well hold hidden temporaries of its own.
func TestClaimSweepsOnlyTheMarkerTemporaryItsOwnCrashCouldHaveLeft(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name        string
		leftover    string
		modified    time.Time
		wantRemoved bool
		wantErr     bool
	}{
		{
			name:     "a marker temporary a killed process left behind",
			leftover: ".marker-abc.tmp", modified: claimMoment.Add(-24 * time.Hour), wantRemoved: true,
		},
		{
			name:     "a marker temporary another process is about to rename",
			leftover: ".marker-abc.tmp", modified: claimMoment,
		},
		{
			name:     "a temporary of some other program",
			leftover: ".other-abc.tmp", modified: claimMoment.Add(-24 * time.Hour), wantErr: true,
		},
		{
			name:     "a name that only looks like the marker temporary",
			leftover: "marker.tmp", modified: claimMoment.Add(-24 * time.Hour), wantErr: true,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			dir := filepath.Join(t.TempDir(), "layer")
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, testCase.leftover)
			if err := os.WriteFile(path, []byte("half a marker"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.Chtimes(path, testCase.modified, testCase.modified); err != nil {
				t.Fatal(err)
			}
			var removed []string
			err := (Layer{Dir: dir}).prepareWithHooks(layerHooks{
				now: func() time.Time { return claimMoment },
				remove: func(name string) error {
					removed = append(removed, name)
					return os.Remove(name)
				},
			})
			if testCase.wantErr {
				if err == nil || !strings.Contains(err.Error(), "not a goatest build cache") {
					t.Fatalf("Prepare error = %v, want it to refuse a directory goatest did not write", err)
				}
				if _, statErr := os.Stat(path); statErr != nil {
					t.Fatalf("the refused file = %v, want it left exactly where it was", statErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Prepare: %v", err)
			}
			_, statErr := os.Stat(path)
			if gone := errors.Is(statErr, os.ErrNotExist); gone != testCase.wantRemoved {
				t.Fatalf("marker temporary gone after Prepare = %t, want %t", gone, testCase.wantRemoved)
			}
			if swept := slices.Contains(removed, path); swept != testCase.wantRemoved {
				t.Fatalf("Prepare removed %v, want it to have swept %s = %t", removed, testCase.leftover, testCase.wantRemoved)
			}
			if _, err := os.Stat(filepath.Join(dir, MarkerName)); err != nil {
				t.Fatalf("marker after Prepare = %v, want it written", err)
			}
		})
	}
}

// TestClaimIgnoresAMarkerTemporaryThatVanishedWhileItLooked is the race two
// processes recovering from the same crash run into: both read the directory,
// both find the same stale temporary, and one of them gets to it first. Losing
// that race is not a failure — the file being gone is what the sweep wanted —
// and neither is losing it a moment earlier, before the age could be read.
func TestClaimIgnoresAMarkerTemporaryThatVanishedWhileItLooked(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name  string
		hooks layerHooks
	}{
		{
			name: "gone before its age could be read",
			hooks: layerHooks{stat: func(string) (fs.FileInfo, error) {
				return nil, fs.ErrNotExist
			}},
		},
		{
			name:  "gone before it could be removed",
			hooks: layerHooks{remove: func(string) error { return fs.ErrNotExist }},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			dir := filepath.Join(t.TempDir(), "layer")
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, ".marker-abc.tmp")
			if err := os.WriteFile(path, []byte("half a marker"), 0o644); err != nil {
				t.Fatal(err)
			}
			stale := claimMoment.Add(-24 * time.Hour)
			if err := os.Chtimes(path, stale, stale); err != nil {
				t.Fatal(err)
			}
			hooks := testCase.hooks
			hooks.now = func() time.Time { return claimMoment }
			if err := (Layer{Dir: dir}).prepareWithHooks(hooks); err != nil {
				t.Fatalf("Prepare: %v", err)
			}
		})
	}
}
