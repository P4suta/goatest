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

	"github.com/P4suta/goatest/internal/filemode"
)

var claimMoment = time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)

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
			if err := os.MkdirAll(dir, filemode.ReadableDirectory); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, testCase.leftover)
			if err := os.WriteFile(path, []byte("half a marker"), filemode.ReadableFile); err != nil {
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
			if err := os.MkdirAll(dir, filemode.ReadableDirectory); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, ".marker-abc.tmp")
			if err := os.WriteFile(path, []byte("half a marker"), filemode.ReadableFile); err != nil {
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
