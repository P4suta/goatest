// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package buildcache_test

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/P4suta/goatest/internal/buildcache"
)

// reference is a fixed moment every timed assertion is written against, so a
// test states the age of an entry rather than racing the wall clock.
var reference = time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)

// identifier renders a cache key or an output identifier of the length the go
// command uses, distinguished by its first byte.
func identifier(value byte) []byte {
	return append([]byte{value}, bytes.Repeat([]byte{0xab}, 31)...)
}

// prepared is a layer ready to store entries.
func prepared(t *testing.T) buildcache.Layer {
	t.Helper()
	layer := buildcache.Layer{Dir: filepath.Join(t.TempDir(), "layer")}
	if err := layer.Prepare(); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	return layer
}

// store puts one output and fails the test if it could not.
//
// It writes through Layers rather than through the layer, because Layers is the
// only way production reaches a layer at all: a test that stored an entry by
// some other route would be holding a path no run takes.
func store(t *testing.T, layer buildcache.Layer, action, output byte, content string, at time.Time) buildcache.Entry {
	t.Helper()
	entry, err := buildcache.Layers{Scratch: layer}.Put(
		identifier(action), identifier(output), strings.NewReader(content), int64(len(content)), at)
	if err != nil {
		t.Fatalf("Put(%x, %x): %v", action, output, err)
	}
	return entry
}

// lookup resolves one cache key against a single layer, reported as the hit or
// miss the layer-level assertions below are written in terms of.
func lookup(t *testing.T, layer buildcache.Layer, actionID []byte, now time.Time) (buildcache.Entry, bool, error) {
	t.Helper()
	entry, source, err := buildcache.Layers{Scratch: layer}.Get(actionID, now)
	return entry, source != buildcache.SourceNone, err
}

// files lists the paths below one half of a layer, relative and slash
// separated, so an assertion names what is on the disk.
func files(t *testing.T, layer buildcache.Layer, half string) []string {
	t.Helper()
	root := filepath.Join(layer.Dir, half)
	var found []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		found = append(found, filepath.ToSlash(relative))
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("walk %s: %v", root, err)
	}
	slices.Sort(found)
	return found
}

// collect bounds a layer through the entry point production uses, which takes
// the layer's collection lock and records that it ran. There is no unlocked
// collection to test against: every caller goes through this one.
func collect(t *testing.T, layer buildcache.Layer, policy buildcache.Policy, now time.Time) buildcache.Collected {
	t.Helper()
	collected, ran, err := layer.CollectLocked(policy, 0, now)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if !ran {
		t.Fatal("Collect did not run, and nothing else holds this layer's lock")
	}
	return collected
}

// age sets an entry's file time to a moment before the reference, which is how
// a test says how long ago something was read.
func age(t *testing.T, path string, before time.Duration) {
	t.Helper()
	moment := reference.Add(-before)
	if err := os.Chtimes(path, moment, moment); err != nil {
		t.Fatalf("age %s: %v", path, err)
	}
}

// actionPath is where a layer stores one cache key, as this test knows the
// layout rather than as the package computes it: the layout is the contract a
// concurrent run and a later goatest read the layer through.
func actionPath(layer buildcache.Layer, action byte) string {
	name := hexadecimalName(identifier(action))
	return filepath.Join(layer.Dir, "actions", name[:2], name)
}

// objectPath is where a layer stores one output.
func objectPath(layer buildcache.Layer, output byte) string {
	name := hexadecimalName(identifier(output))
	return filepath.Join(layer.Dir, "objects", name[:2], name)
}

func hexadecimalName(identifier []byte) string {
	const digits = "0123456789abcdef"
	name := make([]byte, 0, len(identifier)*2)
	for _, value := range identifier {
		name = append(name, digits[value>>4], digits[value&0x0f])
	}
	return string(name)
}

func TestLayerStoresAndReturnsOutputsByteExact(t *testing.T) {
	t.Parallel()
	layer := prepared(t)
	content := "compiled\x00package\xffbytes"
	stored := store(t, layer, 1, 2, content, reference)
	if stored.Size != int64(len(content)) || !filepath.IsAbs(stored.DiskPath) {
		t.Fatalf("Put entry = %+v", stored)
	}
	entry, found, err := lookup(t, layer, identifier(1), reference)
	if err != nil || !found {
		t.Fatalf("Get = (%+v, %t, %v)", entry, found, err)
	}
	if !bytes.Equal(entry.OutputID, identifier(2)) || entry.Size != int64(len(content)) {
		t.Fatalf("Get entry = %+v", entry)
	}
	if !entry.Time.Equal(reference) {
		t.Fatalf("Get entry time = %s, want %s", entry.Time, reference)
	}
	data, err := os.ReadFile(entry.DiskPath)
	if err != nil || string(data) != content {
		t.Fatalf("stored content = %q (%v), want %q", data, err, content)
	}
}

func TestLayerReportsMissWithoutFailingWhateverItCannotResolve(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name    string
		prepare func(*testing.T, buildcache.Layer)
	}{
		{name: "unknown key", prepare: func(*testing.T, buildcache.Layer) {}},
		{
			name: "malformed action line",
			prepare: func(t *testing.T, layer buildcache.Layer) {
				store(t, layer, 1, 2, "content", reference)
				if err := os.WriteFile(actionPath(layer, 1), []byte("{not json"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "action names no output",
			prepare: func(t *testing.T, layer buildcache.Layer) {
				store(t, layer, 1, 2, "content", reference)
				if err := os.WriteFile(actionPath(layer, 1), []byte(`{"output":"","size":7}`), 0o644); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "missing object",
			prepare: func(t *testing.T, layer buildcache.Layer) {
				store(t, layer, 1, 2, "content", reference)
				if err := os.Remove(objectPath(layer, 2)); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "truncated object",
			prepare: func(t *testing.T, layer buildcache.Layer) {
				store(t, layer, 1, 2, "content", reference)
				if err := os.WriteFile(objectPath(layer, 2), []byte("cut"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			layer := prepared(t)
			testCase.prepare(t, layer)
			entry, found, err := lookup(t, layer, identifier(1), reference)
			if err != nil || found {
				t.Fatalf("Get = (%+v, %t, %v), want a miss and no error", entry, found, err)
			}
		})
	}
}

func TestLayerSharesOneObjectBetweenEveryKeyThatProducedIt(t *testing.T) {
	t.Parallel()
	layer := prepared(t)
	first := store(t, layer, 1, 9, "shared", reference)
	second := store(t, layer, 2, 9, "shared", reference)
	if first.DiskPath != second.DiskPath {
		t.Fatalf("two keys stored the same output at %q and %q", first.DiskPath, second.DiskPath)
	}
	if stored := files(t, layer, "objects"); len(stored) != 1 {
		t.Fatalf("objects on disk = %v, want exactly one", stored)
	}
	if keys := files(t, layer, "actions"); len(keys) != 2 {
		t.Fatalf("actions on disk = %v, want two", keys)
	}
}

func TestLayerPutIsIdempotent(t *testing.T) {
	t.Parallel()
	layer := prepared(t)
	first := store(t, layer, 1, 2, "content", reference)
	later := reference.Add(time.Hour)
	second := store(t, layer, 1, 2, "content", later)
	if first.DiskPath != second.DiskPath || second.Size != first.Size {
		t.Fatalf("repeated Put = %+v, first = %+v", second, first)
	}
	if stored := files(t, layer, "objects"); len(stored) != 1 {
		t.Fatalf("objects on disk = %v, want exactly one", stored)
	}
	entry, found, err := lookup(t, layer, identifier(1), later)
	if err != nil || !found || !entry.Time.Equal(later) {
		t.Fatalf("Get after repeated Put = (%+v, %t, %v)", entry, found, err)
	}
}

func TestLayerRefreshesAKeyOnlyOnceAnHour(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name      string
		since     time.Duration
		refreshed bool
	}{
		{name: "just read", since: time.Minute, refreshed: false},
		{name: "read an hour ago", since: time.Hour, refreshed: true},
		{name: "read a day ago", since: 24 * time.Hour, refreshed: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			layer := prepared(t)
			store(t, layer, 1, 2, "content", reference)
			path := actionPath(layer, 1)
			age(t, path, testCase.since)
			if _, found, err := lookup(t, layer, identifier(1), reference); err != nil || !found {
				t.Fatalf("Get = (%t, %v)", found, err)
			}
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			refreshed := info.ModTime().Equal(reference)
			if refreshed != testCase.refreshed {
				t.Fatalf("file time = %s, reference = %s, want refreshed=%t", info.ModTime(), reference, testCase.refreshed)
			}
		})
	}
}

func TestLayerCollectRemovesWhatThePolicyNames(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name     string
		policy   buildcache.Policy
		ages     map[byte]time.Duration
		want     []byte
		orphaned bool
	}{
		{
			name:   "an empty policy removes nothing",
			policy: buildcache.Policy{},
			ages:   map[byte]time.Duration{1: 90 * 24 * time.Hour, 2: 48 * time.Hour, 3: time.Minute},
			want:   []byte{1, 2, 3},
		},
		{
			name:   "expiry removes what nothing has read",
			policy: buildcache.Policy{TTL: 24 * time.Hour, MinIdle: time.Hour},
			ages:   map[byte]time.Duration{1: 90 * 24 * time.Hour, 2: 48 * time.Hour, 3: time.Minute},
			want:   []byte{3},
		},
		{
			name:   "the size bound removes the least recently read first",
			policy: buildcache.Policy{MaxBytes: 20, MinIdle: time.Hour},
			ages:   map[byte]time.Duration{1: 90 * 24 * time.Hour, 2: 48 * time.Hour, 3: 24 * time.Hour},
			want:   []byte{2, 3},
		},
		{
			name:   "a key read within the idle window survives its own expiry",
			policy: buildcache.Policy{MaxBytes: 1, TTL: time.Second, MinIdle: time.Hour},
			ages:   map[byte]time.Duration{1: 90 * 24 * time.Hour, 2: 48 * time.Hour, 3: time.Minute},
			want:   []byte{3},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			layer := prepared(t)
			for _, key := range []byte{1, 2, 3} {
				store(t, layer, key, key+0x10, "0123456789", reference)
				age(t, actionPath(layer, key), testCase.ages[key])
				age(t, objectPath(layer, key+0x10), testCase.ages[key])
			}
			collected := collect(t, layer, testCase.policy, reference)
			if collected.Before.Entries != 3 || collected.Before.Bytes != 30 {
				t.Fatalf("Collect before = %+v, want 3 entries of 30 bytes", collected.Before)
			}
			var survived []byte
			for _, key := range []byte{1, 2, 3} {
				if _, found, err := lookup(t, layer, identifier(key), reference); err != nil {
					t.Fatalf("Get(%x): %v", key, err)
				} else if found {
					survived = append(survived, key)
				}
			}
			if !slices.Equal(survived, testCase.want) {
				t.Fatalf("survivors = %v, want %v", survived, testCase.want)
			}
			wantRemoved := 3 - len(testCase.want)
			if collected.RemovedActions != wantRemoved || collected.RemovedObjects != wantRemoved {
				t.Fatalf("Collect = %+v, want %d actions and objects removed", collected, wantRemoved)
			}
			if collected.RemovedBytes != int64(10*wantRemoved) || collected.After.Entries != len(testCase.want) {
				t.Fatalf("Collect = %+v, want %d bytes removed and %d entries left", collected, 10*wantRemoved, len(testCase.want))
			}
			if stored := files(t, layer, "objects"); len(stored) != len(testCase.want) {
				t.Fatalf("objects on disk = %v, want %d", stored, len(testCase.want))
			}
		})
	}
}

func TestLayerCollectBreaksTiesByName(t *testing.T) {
	t.Parallel()
	layer := prepared(t)
	for _, key := range []byte{1, 2, 3} {
		store(t, layer, key, key+0x10, "0123456789", reference)
		age(t, actionPath(layer, key), 48*time.Hour)
		age(t, objectPath(layer, key+0x10), 48*time.Hour)
	}
	collect(t, layer, buildcache.Policy{MaxBytes: 20, MinIdle: time.Hour}, reference)
	var survived []byte
	for _, key := range []byte{1, 2, 3} {
		if _, found, _ := lookup(t, layer, identifier(key), reference); found {
			survived = append(survived, key)
		}
	}
	if !slices.Equal(survived, []byte{2, 3}) {
		t.Fatalf("survivors = %v, want the two whose keys sort last", survived)
	}
}

func TestLayerCollectRemovesObjectsNoKeyNames(t *testing.T) {
	t.Parallel()
	layer := prepared(t)
	store(t, layer, 1, 2, "0123456789", reference)
	if err := os.Remove(actionPath(layer, 1)); err != nil {
		t.Fatal(err)
	}
	age(t, objectPath(layer, 2), 48*time.Hour)
	collected := collect(t, layer, buildcache.Policy{MinIdle: time.Hour}, reference)
	if collected.RemovedObjects != 1 || collected.RemovedBytes != 10 || collected.After.Bytes != 0 {
		t.Fatalf("Collect = %+v, want the orphaned object removed", collected)
	}
	if stored := files(t, layer, "objects"); len(stored) != 0 {
		t.Fatalf("objects on disk = %v, want none", stored)
	}
}

func TestLayerCollectKeepsAnOrphanedObjectInsideTheIdleWindow(t *testing.T) {
	t.Parallel()
	layer := prepared(t)
	store(t, layer, 1, 2, "0123456789", reference)
	if err := os.Remove(actionPath(layer, 1)); err != nil {
		t.Fatal(err)
	}
	age(t, objectPath(layer, 2), time.Minute)
	collected := collect(t, layer, buildcache.Policy{MinIdle: time.Hour}, reference)
	if collected.RemovedObjects != 0 || collected.After.Bytes != 10 {
		t.Fatalf("Collect = %+v, want an object written a minute ago left alone", collected)
	}
}

func TestLayerInspectAndCollectAnswerForALayerThatHoldsNothing(t *testing.T) {
	t.Parallel()
	empty := buildcache.Layer{Dir: filepath.Join(t.TempDir(), "never-built")}
	status, err := empty.Inspect()
	if err != nil || status != (buildcache.Status{}) {
		t.Fatalf("Inspect = (%+v, %v), want the zero status", status, err)
	}
	// A layer no machine has built yet is nothing to collect rather than a
	// failure, which is what a first ever run and a first ever cache gc meet.
	collected, ran, err := empty.CollectLocked(buildcache.Policy{MaxBytes: 1, TTL: time.Hour, MinIdle: time.Hour}, 0, reference)
	if err != nil || ran || collected != (buildcache.Collected{}) {
		t.Fatalf("Collect = (%+v, %t, %v), want nothing collected", collected, ran, err)
	}
	if _, _, err := empty.CollectLocked(buildcache.Policy{MaxBytes: -1}, 0, reference); err == nil {
		t.Fatal("Collect accepted a negative bound")
	}
}

func TestLayerInspectReportsWhatTheLayerHolds(t *testing.T) {
	t.Parallel()
	layer := prepared(t)
	store(t, layer, 1, 2, "0123456789", reference)
	store(t, layer, 3, 4, "01234", reference)
	age(t, actionPath(layer, 1), 48*time.Hour)
	age(t, actionPath(layer, 3), time.Minute)
	status, err := layer.Inspect()
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if status.Entries != 2 || status.Bytes != 15 {
		t.Fatalf("Inspect = %+v, want two entries of 15 bytes", status)
	}
	if want := reference.Add(-48 * time.Hour); !status.Oldest.Equal(want) {
		t.Fatalf("Inspect oldest = %s, want %s", status.Oldest, want)
	}
}

func TestLayerPrepareLeavesItsMarkerAndRefusesAnUnusableDirectory(t *testing.T) {
	t.Parallel()
	layer := prepared(t)
	marker, err := os.ReadFile(filepath.Join(layer.Dir, buildcache.MarkerName))
	if err != nil || !strings.Contains(string(marker), "goatest") {
		t.Fatalf("marker = %q (%v), want a note naming goatest", marker, err)
	}
	// The marker carries goatest's own name rather than being a README. A
	// README is a file a project may already keep in a directory somebody
	// pointed build_dir at, and the marker's whole job is to be a name nothing
	// else writes.
	if strings.EqualFold(buildcache.MarkerName, "README") || strings.EqualFold(buildcache.MarkerName, "README.md") {
		t.Fatalf("marker name = %q, want a name only goatest writes", buildcache.MarkerName)
	}
	if !strings.Contains(buildcache.MarkerName, "goatest") {
		t.Fatalf("marker name = %q, want it to name its owner", buildcache.MarkerName)
	}
	unnamed := buildcache.Layer{}
	if err := unnamed.Prepare(); err == nil {
		t.Fatal("a layer with no directory prepared successfully")
	}
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := (buildcache.Layer{Dir: filepath.Join(file, "layer")}).Prepare(); err == nil {
		t.Fatal("a layer below a regular file prepared successfully")
	}
	if err := (buildcache.Layer{Dir: file}).Prepare(); err == nil {
		t.Fatal("a regular file prepared successfully as a layer")
	}
}

// TestLayerPrepareRefusesADirectoryThatIsNotAGoatestBuildCache is what stops a
// mistyped build_dir from ever being collected.
//
// goatest collects and removes files below a layer, so it must never adopt a
// directory somebody else owns. A directory that holds files and carries no
// marker is refused; an empty one, an absent one, and one goatest prepared
// before are accepted, because those are the three a run legally meets.
func TestLayerPrepareRefusesADirectoryThatIsNotAGoatestBuildCache(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name    string
		prepare func(*testing.T, string)
		wantErr bool
	}{
		{
			name:    "an absent directory",
			prepare: func(*testing.T, string) {},
		},
		{
			name: "an empty directory",
			prepare: func(t *testing.T, dir string) {
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "a layer goatest prepared before",
			prepare: func(t *testing.T, dir string) {
				if err := (buildcache.Layer{Dir: dir}).Prepare(); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "somebody's home directory",
			prepare: func(t *testing.T, dir string) {
				if err := os.MkdirAll(filepath.Join(dir, "Documents"), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, ".profile"), []byte("export PATH\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			wantErr: true,
		},
		{
			name: "a directory holding one unrelated file",
			prepare: func(t *testing.T, dir string) {
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "README"), []byte("my project\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			wantErr: true,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			dir := filepath.Join(t.TempDir(), "layer")
			testCase.prepare(t, dir)
			err := (buildcache.Layer{Dir: dir}).Prepare()
			if testCase.wantErr {
				if err == nil {
					t.Fatal("Prepare adopted a directory goatest did not make")
				}
				if !strings.Contains(err.Error(), "not a goatest build cache") {
					t.Fatalf("Prepare error = %v, want it to say the directory is not a goatest build cache", err)
				}
				// Refusing must leave the directory as it was: the point is not
				// to touch what somebody else owns.
				if _, statErr := os.Stat(filepath.Join(dir, buildcache.MarkerName)); statErr == nil {
					t.Fatal("Prepare wrote its marker into a directory it refused")
				}
				return
			}
			if err != nil {
				t.Fatalf("Prepare: %v", err)
			}
			if _, statErr := os.Stat(filepath.Join(dir, buildcache.MarkerName)); statErr != nil {
				t.Fatalf("marker after Prepare = %v, want it written", statErr)
			}
		})
	}
}

func TestLayerRefusesAPutItCannotAddress(t *testing.T) {
	t.Parallel()
	layer := prepared(t)
	layers := buildcache.Layers{Scratch: layer}
	if _, err := layers.Put(nil, identifier(2), strings.NewReader("x"), 1, reference); err == nil {
		t.Fatal("Put accepted an empty cache key")
	}
	if _, err := layers.Put(identifier(1), nil, strings.NewReader("x"), 1, reference); err == nil {
		t.Fatal("Put accepted an empty output identifier")
	}
	if _, err := layers.Put(identifier(1), identifier(2), strings.NewReader("x"), -1, reference); err == nil {
		t.Fatal("Put accepted a negative size")
	}
	if _, err := layers.Put(identifier(1), identifier(2), strings.NewReader("xx"), 1, reference); err == nil {
		t.Fatal("Put accepted a body longer than the declared size")
	}
	if _, found, err := lookup(t, layer, identifier(1), reference); found || err != nil {
		t.Fatalf("Get after a refused Put = (%t, %v), want a miss", found, err)
	}
	if _, err := (buildcache.Layers{}).Put(identifier(1), identifier(2), strings.NewReader("x"), 1, reference); err == nil {
		t.Fatal("a store with no layer at all stored an output")
	}
}

func TestLayerStoresAnEmptyOutput(t *testing.T) {
	t.Parallel()
	layer := prepared(t)
	store(t, layer, 1, 2, "", reference)
	entry, found, err := lookup(t, layer, identifier(1), reference)
	if err != nil || !found || entry.Size != 0 {
		t.Fatalf("Get = (%+v, %t, %v), want an empty output", entry, found, err)
	}
}
