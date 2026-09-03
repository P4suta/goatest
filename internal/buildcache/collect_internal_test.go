// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package buildcache

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// collectMoment is the fixed moment these assertions are timed against.
var collectMoment = time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)

// collectKey renders an identifier of the length the go command uses.
func collectKey(value byte) []byte {
	identifier := make([]byte, 32)
	for index := range identifier {
		identifier[index] = value
	}
	return identifier
}

// TestMinIdleIsAtLeastTwoTouchIntervals is the inequality the whole LRU rests
// on, stated for every layer there is.
//
// A collection may only remove an entry whose last touch is older than
// MinIdle, and a read refreshes an entry's file time at most once per touch
// interval. An entry a live build reads continuously therefore carries a file
// time that is already up to one whole touch interval stale, and the go command
// opens the file a response named it *after* that response. MinIdle must cover
// both, so it is two touch intervals: one for the staleness the rate limit
// allows, one for the window the go command needs.
func TestMinIdleIsAtLeastTwoTouchIntervals(t *testing.T) {
	t.Parallel()
	for _, layer := range []Layer{
		{Dir: "base"},
		{Dir: "base with an explicit interval", Touch: BaseTouchInterval},
		{Dir: "scratch", Touch: ScratchTouchInterval},
	} {
		if layer.touchInterval() <= 0 {
			t.Fatalf("%s touch interval = %s, want a positive interval", layer.Dir, layer.touchInterval())
		}
		if layer.MinIdle() < MinIdleTouchIntervals*layer.touchInterval() {
			t.Errorf("%s MinIdle = %s, want at least %d x %s",
				layer.Dir, layer.MinIdle(), MinIdleTouchIntervals, layer.touchInterval())
		}
	}
	if MinIdleTouchIntervals < 2 {
		t.Fatalf("MinIdleTouchIntervals = %d, want at least two", MinIdleTouchIntervals)
	}
}

// TestAnEntryStaleByOneTouchIntervalSurvivesItsOwnCollection is the inequality
// as behaviour rather than as arithmetic: the entry a build is reading right
// now, whose file time the touch rate limit left a whole interval behind, is
// not removed however far over the bound the layer is.
func TestAnEntryStaleByOneTouchIntervalSurvivesItsOwnCollection(t *testing.T) {
	t.Parallel()
	for _, layer := range []Layer{{Dir: ""}, {Touch: ScratchTouchInterval}} {
		layer.Dir = filepath.Join(t.TempDir(), "layer")
		if err := layer.Prepare(); err != nil {
			t.Fatal(err)
		}
		layers := Layers{Scratch: layer}
		if _, err := layers.Put(collectKey(1), collectKey(2), strings.NewReader("0123456789"), 10, collectMoment); err != nil {
			t.Fatal(err)
		}
		// The read that refreshed this entry was one touch interval ago, which
		// is the stalest a continuously read entry can be.
		stale := collectMoment.Add(-layer.touchInterval())
		if err := os.Chtimes(layer.actionPath(collectKey(1)), stale, stale); err != nil {
			t.Fatal(err)
		}
		collected, err := layer.Collect(Policy{MaxBytes: 1, TTL: time.Nanosecond, MinIdle: layer.MinIdle()}, collectMoment)
		if err != nil {
			t.Fatal(err)
		}
		if collected.RemovedActions != 0 || collected.After.Entries != 1 {
			t.Fatalf("collection of a layer with touch=%s = %+v, want the entry a live build is reading spared",
				layer.touchInterval(), collected)
		}
	}
}

func TestCollectLockedRunsOncePerIntervalAndAgainAfterIt(t *testing.T) {
	t.Parallel()
	layer := Layer{Dir: filepath.Join(t.TempDir(), "layer"), Touch: ScratchTouchInterval}
	if err := layer.Prepare(); err != nil {
		t.Fatal(err)
	}
	walks := 0
	hooks := layerHooks{readDir: func(path string) ([]os.DirEntry, error) {
		walks++
		return os.ReadDir(path)
	}}
	// The first collection of a layer nothing has collected runs: there is no
	// record of an earlier one to be inside the interval of.
	if _, ran, err := layer.collectLockedWithHooks(Policy{}, time.Minute, collectMoment, hooks); err != nil || !ran {
		t.Fatalf("first collection = (%t, %v), want it to have run", ran, err)
	}
	after := walks
	if after == 0 {
		t.Fatal("the first collection walked nothing")
	}
	// A run issues thousands of go commands, and each one closes. Walking the
	// layer on every close is the cost this gate exists to remove.
	for index := range 8 {
		moment := collectMoment.Add(time.Duration(index) * 5 * time.Second)
		if _, ran, err := layer.collectLockedWithHooks(Policy{}, time.Minute, moment, hooks); err != nil || ran {
			t.Fatalf("collection %d inside the interval = (%t, %v), want it skipped", index, ran, err)
		}
	}
	if walks != after {
		t.Fatalf("walks = %d, want the %d of the one collection that ran", walks, after)
	}
	if _, ran, err := layer.collectLockedWithHooks(Policy{}, time.Minute, collectMoment.Add(2*time.Minute), hooks); err != nil || !ran {
		t.Fatalf("collection after the interval = (%t, %v), want it to have run", ran, err)
	}
	if walks <= after {
		t.Fatalf("walks = %d, want the collection after the interval to have walked again", walks)
	}
}

func TestCollectLockedWithoutAnIntervalRunsEveryTime(t *testing.T) {
	t.Parallel()
	layer := Layer{Dir: filepath.Join(t.TempDir(), "layer")}
	if err := layer.Prepare(); err != nil {
		t.Fatal(err)
	}
	// A run collects the base layer once, at its end, so it asks for no
	// interval at all: the only thing that may stop it is another process
	// already collecting.
	for index := range 3 {
		if _, ran, err := layer.collectLockedWithHooks(Policy{}, 0, collectMoment, layerHooks{}); err != nil || !ran {
			t.Fatalf("collection %d = (%t, %v), want every one to run", index, ran, err)
		}
	}
}

func TestCollectLockedSkipsALayerAnotherProcessIsCollecting(t *testing.T) {
	t.Parallel()
	layer := Layer{Dir: filepath.Join(t.TempDir(), "layer")}
	if err := layer.Prepare(); err != nil {
		t.Fatal(err)
	}
	// A separate open file description is what a concurrent cacheprog child or
	// a concurrent run holds, and flock contends between them even inside one
	// process.
	held, err := os.OpenFile(layer.collectionMarkerPath(), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = held.Close() }()
	locked, err := tryAdvisoryLock(held)
	if err != nil || !locked {
		t.Fatalf("holding the collection lock = (%t, %v)", locked, err)
	}
	collected, ran, err := layer.collectLockedWithHooks(Policy{MaxBytes: 1}, 0, collectMoment, layerHooks{})
	if err != nil {
		t.Fatalf("collection against a held lock = %v, want it skipped without an error", err)
	}
	if ran || collected != (Collected{}) {
		t.Fatalf("collection against a held lock = (%+v, %t), want nothing collected", collected, ran)
	}
	if err := unlockAdvisory(held); err != nil {
		t.Fatal(err)
	}
	if _, ran, err := layer.collectLockedWithHooks(Policy{MaxBytes: 1}, 0, collectMoment, layerHooks{}); err != nil || !ran {
		t.Fatalf("collection after the lock was released = (%t, %v), want it to have run", ran, err)
	}
}

// TestServeCloseCollectsTheScratchLayerOncePerInterval is the cost this gate
// exists to remove. A run issues thousands of go commands and every one of them
// closes; walking the whole scratch layer on each close is work proportional to
// the square of the run.
func TestServeCloseCollectsTheScratchLayerOncePerInterval(t *testing.T) {
	t.Parallel()
	scratch := Layer{Dir: filepath.Join(t.TempDir(), "scratch"), Touch: ScratchTouchInterval}
	if err := scratch.Prepare(); err != nil {
		t.Fatal(err)
	}
	layers := Layers{Scratch: scratch, MaxBytes: 1}
	walks := 0
	moment := collectMoment
	closed := 0
	closeOnce := func() {
		closed++
		var written strings.Builder
		var stats Stats
		name := fmt.Sprintf("%d-%d.json", closed, closed)
		err := serveWithHooks(t.Context(), strings.NewReader(`{"ID":1,"Command":"close"}`+"\n"), &written, layers, &stats,
			serveHooks{
				now:       func() time.Time { return moment },
				statsName: func() string { return name },
				layer: layerHooks{readDir: func(path string) ([]os.DirEntry, error) {
					walks++
					return os.ReadDir(path)
				}},
			})
		if err != nil {
			t.Fatalf("close %d: %v", closed, err)
		}
	}
	closeOnce()
	first := walks
	if first == 0 {
		t.Fatal("the first close walked nothing; the scratch layer is never collected")
	}
	for range 20 {
		moment = moment.Add(time.Second)
		closeOnce()
	}
	if walks != first {
		t.Fatalf("walks = %d after 21 closes, want the %d of the one collection inside the interval", walks, first)
	}
	moment = collectMoment.Add(ScratchCollectInterval + time.Second)
	closeOnce()
	if walks <= first {
		t.Fatalf("walks = %d, want a close after the interval to collect again", walks)
	}
}

func TestCollectLockedAnswersForALayerItHasNoDirectoryFor(t *testing.T) {
	t.Parallel()
	if _, ran, err := (Layer{}).collectLockedWithHooks(Policy{}, 0, collectMoment, layerHooks{}); err != nil || ran {
		t.Fatalf("collection of a layer with no directory = (%t, %v), want nothing", ran, err)
	}
	absent := Layer{Dir: filepath.Join(t.TempDir(), "file", "layer")}
	if err := os.WriteFile(filepath.Join(filepath.Dir(absent.Dir)), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ran, err := absent.collectLockedWithHooks(Policy{}, 0, collectMoment, layerHooks{}); err == nil || ran {
		t.Fatalf("collection of a layer it cannot lock = (%t, %v), want the failure reported", ran, err)
	}
}
