// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package buildcache_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/P4suta/goatest/internal/buildcache"
)

// twoLayers is a scratch layer over a base layer, both ready to store entries.
func twoLayers(t *testing.T, persist bool) buildcache.Layers {
	t.Helper()
	root := t.TempDir()
	layers := buildcache.Layers{
		Scratch: buildcache.Layer{Dir: filepath.Join(root, "scratch"), Touch: buildcache.ScratchTouchInterval},
		Base:    buildcache.Layer{Dir: filepath.Join(root, "base")},
		Persist: persist,
	}
	if err := layers.Scratch.Prepare(); err != nil {
		t.Fatalf("prepare scratch: %v", err)
	}
	if err := layers.Base.Prepare(); err != nil {
		t.Fatalf("prepare base: %v", err)
	}
	return layers
}

func TestLayersReadScratchBeforeBase(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name   string
		stored map[string]string
		want   buildcache.Source
		body   string
	}{
		{name: "neither layer holds the key", stored: map[string]string{}, want: buildcache.SourceNone},
		{name: "only base holds the key", stored: map[string]string{"base": "from base"}, want: buildcache.SourceBase, body: "from base"},
		{name: "only scratch holds the key", stored: map[string]string{"scratch": "from scratch"}, want: buildcache.SourceScratch, body: "from scratch"},
		{
			name:   "both hold the key",
			stored: map[string]string{"base": "from base!", "scratch": "from scratch"},
			want:   buildcache.SourceScratch, body: "from scratch",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			layers := twoLayers(t, false)
			if content, ok := testCase.stored["base"]; ok {
				store(t, layers.Base, 1, 2, content, reference)
			}
			if content, ok := testCase.stored["scratch"]; ok {
				store(t, layers.Scratch, 1, 3, content, reference)
			}
			entry, source, err := layers.Get(identifier(1), reference)
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			if source != testCase.want {
				t.Fatalf("Get source = %s, want %s", source, testCase.want)
			}
			if testCase.want != buildcache.SourceNone && entry.Size != int64(len(testCase.body)) {
				t.Fatalf("Get entry = %+v, want %d bytes", entry, len(testCase.body))
			}
		})
	}
}

func TestLayersWriteWhereTheProgramWasToldTo(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name    string
		persist bool
		want    buildcache.Source
	}{
		{name: "a run's own commands write to scratch", persist: false, want: buildcache.SourceScratch},
		{name: "goatest's own commands write to base", persist: true, want: buildcache.SourceBase},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			layers := twoLayers(t, testCase.persist)
			if _, err := layers.Put(identifier(1), identifier(2), strings.NewReader("content"), 7, reference); err != nil {
				t.Fatalf("Put: %v", err)
			}
			_, source, err := layers.Get(identifier(1), reference)
			if err != nil || source != testCase.want {
				t.Fatalf("Get source = (%s, %v), want %s", source, err, testCase.want)
			}
			written := layers.Scratch
			empty := layers.Base
			if testCase.persist {
				written, empty = layers.Base, layers.Scratch
			}
			if keys := files(t, written, "actions"); len(keys) != 1 {
				t.Fatalf("actions in the written layer = %v, want one", keys)
			}
			if keys := files(t, empty, "actions"); len(keys) != 0 {
				t.Fatalf("actions in the other layer = %v, want none", keys)
			}
		})
	}
}

func TestLayersRecordAKeyAgainstAnObjectBaseAlreadyHolds(t *testing.T) {
	t.Parallel()
	layers := twoLayers(t, false)
	stored := store(t, layers.Base, 1, 2, "shared bytes", reference)
	entry, err := layers.Put(identifier(9), identifier(2), strings.NewReader("shared bytes"), 12, reference)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if entry.DiskPath != stored.DiskPath {
		t.Fatalf("Put stored the object again at %q, base holds it at %q", entry.DiskPath, stored.DiskPath)
	}
	if objects := files(t, layers.Scratch, "objects"); len(objects) != 0 {
		t.Fatalf("scratch objects = %v, want none: base already holds the bytes", objects)
	}
	if keys := files(t, layers.Scratch, "actions"); len(keys) != 1 {
		t.Fatalf("scratch actions = %v, want the one key", keys)
	}
	read, source, err := layers.Get(identifier(9), reference)
	if err != nil || source != buildcache.SourceScratch || read.Size != 12 {
		t.Fatalf("Get = (%+v, %s, %v), want the scratch key resolved against the base object", read, source, err)
	}
}

func TestLayersKeepBaseSelfContained(t *testing.T) {
	t.Parallel()
	layers := twoLayers(t, true)
	store(t, layers.Scratch, 1, 2, "shared bytes", reference)
	if _, err := layers.Put(identifier(9), identifier(2), strings.NewReader("shared bytes"), 12, reference); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if objects := files(t, layers.Base, "objects"); len(objects) != 1 {
		t.Fatalf("base objects = %v, want its own copy: the scratch layer is removed with the run", objects)
	}
	entry, found, err := layers.Base.Get(identifier(9), reference)
	if err != nil || !found || !strings.HasPrefix(entry.DiskPath, layers.Base.Dir) {
		t.Fatalf("base Get = (%+v, %t, %v), want an object inside base", entry, found, err)
	}
}

func TestSourceNamesTheLayerItReadFrom(t *testing.T) {
	t.Parallel()
	for source, want := range map[buildcache.Source]string{
		buildcache.SourceNone:    "none",
		buildcache.SourceScratch: "scratch",
		buildcache.SourceBase:    "base",
	} {
		if got := source.String(); got != want {
			t.Fatalf("Source(%d).String() = %q, want %q", source, got, want)
		}
	}
}
