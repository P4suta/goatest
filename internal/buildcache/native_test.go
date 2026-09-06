// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package buildcache_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/P4suta/goatest/internal/buildcache"
	"github.com/P4suta/goatest/internal/filemode"
)

func storeNativeCacheEntry(t *testing.T, root string, action []byte, body string) []byte {
	t.Helper()
	output := sha256.Sum256([]byte(body))
	actionName := hex.EncodeToString(action)
	outputName := hex.EncodeToString(output[:])
	for _, name := range []string{actionName, outputName} {
		if err := os.MkdirAll(filepath.Join(root, name[:2]), filemode.ReadableDirectory); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, outputName[:2], outputName+"-d"), []byte(body), filemode.ReadableFile); err != nil {
		t.Fatal(err)
	}
	record := fmt.Sprintf("v1 %s %s %d %d\n", actionName, outputName, len(body), time.Now().UnixNano())
	if err := os.WriteFile(filepath.Join(root, actionName[:2], actionName+"-a"), []byte(record), filemode.ReadableFile); err != nil {
		t.Fatal(err)
	}
	return output[:]
}

func TestLayersImportOnlyVerifiedNativeEntriesIntoOwnedStorage(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name    string
		persist bool
		want    buildcache.Source
	}{
		{name: "run-local", want: buildcache.SourceScratch},
		{name: "persistent", persist: true, want: buildcache.SourceBase},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			native := filepath.Join(root, "native")
			action := bytes.Repeat([]byte{0x41}, sha256.Size)
			output := storeNativeCacheEntry(t, native, action, "compiled archive")
			layers := buildcache.Layers{
				Scratch:      buildcache.Layer{Dir: filepath.Join(root, "scratch")},
				Base:         buildcache.Layer{Dir: filepath.Join(root, "base")},
				NativeSource: native, Persist: test.persist,
			}
			if err := layers.Scratch.Prepare(); err != nil {
				t.Fatal(err)
			}
			if err := layers.Base.Prepare(); err != nil {
				t.Fatal(err)
			}
			entry, source, err := layers.Get(action, time.Now())
			if err != nil || source != buildcache.SourceNative || !bytes.Equal(entry.OutputID, output) {
				t.Fatalf("native Get = (%+v, %s, %v)", entry, source, err)
			}
			owned := layers.Scratch
			if test.persist {
				owned = layers.Base
			}
			if !strings.HasPrefix(entry.DiskPath, owned.Dir+string(filepath.Separator)) {
				t.Fatalf("native path = %q, want owned storage below %q", entry.DiskPath, owned.Dir)
			}
			data, err := os.ReadFile(entry.DiskPath)
			if err != nil || string(data) != "compiled archive" {
				t.Fatalf("imported object = %q, %v", data, err)
			}
			_, source, err = layers.Get(action, time.Now())
			if err != nil || source != test.want {
				t.Fatalf("second Get source = (%s, %v), want %s", source, err, test.want)
			}
		})
	}
}

func TestLayersRejectCorruptNativeEntries(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	native := filepath.Join(root, "native")
	action := bytes.Repeat([]byte{0x42}, sha256.Size)
	output := storeNativeCacheEntry(t, native, action, "expected bytes")
	outputName := hex.EncodeToString(output)
	if err := os.WriteFile(filepath.Join(native, outputName[:2], outputName+"-d"), []byte("tampered bytes"), filemode.ReadableFile); err != nil {
		t.Fatal(err)
	}
	layers := buildcache.Layers{
		Scratch:      buildcache.Layer{Dir: filepath.Join(root, "scratch")},
		Base:         buildcache.Layer{Dir: filepath.Join(root, "base")},
		NativeSource: native,
	}
	if err := layers.Scratch.Prepare(); err != nil {
		t.Fatal(err)
	}
	if err := layers.Base.Prepare(); err != nil {
		t.Fatal(err)
	}
	entry, source, err := layers.Get(action, time.Now())
	if err != nil || source != buildcache.SourceNone || entry.DiskPath != "" {
		t.Fatalf("corrupt native Get = (%+v, %s, %v), want a clean miss", entry, source, err)
	}
	for _, layer := range []buildcache.Layer{layers.Scratch, layers.Base} {
		if actions, objects := files(t, layer, "actions"), files(t, layer, "objects"); len(actions) != 0 || len(objects) != 0 {
			t.Fatalf("corrupt native import published actions %v and objects %v", actions, objects)
		}
	}
}

func TestSeedNativeProjectsExactObjectsWithoutAliasingIndexes(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	destination := t.TempDir()
	if err := (buildcache.Layer{Dir: base}).Prepare(); err != nil {
		t.Fatal(err)
	}
	action := bytes.Repeat([]byte{0x12}, sha256.Size)
	output := bytes.Repeat([]byte{0x34}, sha256.Size)
	body := "compiled archive"
	moment := time.Date(2026, 9, 5, 12, 0, 0, 123, time.UTC)
	if _, err := (buildcache.Layers{Base: buildcache.Layer{Dir: base}, Persist: true}).Put(
		action, output, strings.NewReader(body), int64(len(body)), moment,
	); err != nil {
		t.Fatal(err)
	}
	seed, err := buildcache.SeedNative(base, destination, moment.Add(time.Minute))
	if err != nil || seed.Actions != 1 || seed.Objects != 1 || seed.Bytes != int64(len(body)) || seed.Skipped != 0 {
		t.Fatalf("SeedNative = (%+v, %v)", seed, err)
	}
	actionName := fmt.Sprintf("%x", action)
	outputName := fmt.Sprintf("%x", output)
	nativeAction := filepath.Join(destination, actionName[:2], actionName+"-a")
	nativeObject := filepath.Join(destination, outputName[:2], outputName+"-d")
	aged := moment.Add(-time.Hour)
	if err := os.Chtimes(nativeAction, aged, aged); err != nil {
		t.Fatal(err)
	}
	stamped, err := os.Stat(nativeAction)
	if err != nil {
		t.Fatal(err)
	}
	preserved := stamped.ModTime()
	seed, err = buildcache.SeedNative(base, destination, moment.Add(2*time.Minute))
	if err != nil || seed.Actions != 1 || seed.Objects != 1 || seed.Bytes != int64(len(body)) || seed.Skipped != 0 {
		t.Fatalf("idempotent SeedNative = (%+v, %v), want the complete logical projection", seed, err)
	}
	if info, err := os.Stat(nativeAction); err != nil || !info.ModTime().Equal(preserved) {
		t.Fatalf("idempotent seed rewrote an unchanged action: info=%v error=%v", info, err)
	}
	data, err := os.ReadFile(nativeAction)
	if err != nil {
		t.Fatal(err)
	}
	fields := strings.Fields(string(data))
	if len(fields) != 5 || fields[0] != "v1" || fields[1] != actionName || fields[2] != outputName || fields[3] != fmt.Sprint(len(body)) {
		t.Fatalf("native action = %q, want the exact identifiers and size", data)
	}
	baseObject := filepath.Join(base, "objects", outputName[:2], outputName)
	baseInfo, err := os.Stat(baseObject)
	if err != nil {
		t.Fatal(err)
	}
	nativeInfo, err := os.Stat(nativeObject)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(baseInfo, nativeInfo) {
		t.Fatal("native object is a second data copy, want a hard link")
	}
	baseAction := filepath.Join(base, "actions", actionName[:2], actionName)
	before, err := os.ReadFile(baseAction)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(nativeAction, []byte("replaced run-local index"), filemode.PrivateFile); err != nil {
		t.Fatal(err)
	}
	if _, err := buildcache.SeedNative(base, destination, moment.Add(3*time.Minute)); err == nil {
		t.Fatal("SeedNative accepted an unrelated destination action")
	}
	after, err := os.ReadFile(baseAction)
	if err != nil || !bytes.Equal(after, before) {
		t.Fatalf("native index write changed base = %q, want %q (error %v)", after, before, err)
	}
	if _, err := buildcache.RefreshNative(base, destination, moment.Add(3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	repaired, err := os.ReadFile(nativeAction)
	if err != nil || !bytes.HasPrefix(repaired, []byte("v1 "+actionName+" "+outputName+" ")) {
		t.Fatalf("refreshed native action = %q, error %v", repaired, err)
	}
	if err := os.Remove(nativeObject); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(baseObject); err != nil || string(got) != body {
		t.Fatalf("removing native object changed base = %q, error %v", got, err)
	}
}

func TestSeedNativeSkipsDanglingAndMalformedActions(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	destination := t.TempDir()
	if err := (buildcache.Layer{Dir: base}).Prepare(); err != nil {
		t.Fatal(err)
	}
	action := strings.Repeat("1", hex.EncodedLen(sha256.Size))
	directory := filepath.Join(base, "actions", action[:2])
	if err := os.MkdirAll(directory, filemode.ReadableDirectory); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, action), []byte(`{"output":"short","size":3,"time":"2026-09-05T12:00:00Z"}`+"\n"), filemode.PrivateFile); err != nil {
		t.Fatal(err)
	}
	seed, err := buildcache.SeedNative(base, destination, time.Now())
	if err != nil || seed.Actions != 0 || seed.Objects != 0 || seed.Skipped != 1 {
		t.Fatalf("SeedNative = (%+v, %v), want one safely skipped action", seed, err)
	}
	if _, err := os.Stat(filepath.Join(destination, "trim.txt")); err != nil {
		t.Fatalf("native cache was not initialized: %v", err)
	}
}

func TestSeedNativeRefusesUnsafeIdentities(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	if _, err := buildcache.SeedNative("", directory, time.Time{}); err == nil {
		t.Fatal("empty source was accepted")
	}
	if _, err := buildcache.SeedNative(directory, directory, time.Time{}); err == nil {
		t.Fatal("one directory was accepted as both source and destination")
	}
}

func TestSeedNativeRepairsAnIndependentlyProducedObjectAtAQuiescentRefresh(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	destination := t.TempDir()
	if err := (buildcache.Layer{Dir: base}).Prepare(); err != nil {
		t.Fatal(err)
	}
	action := bytes.Repeat([]byte{0x41}, sha256.Size)
	output := bytes.Repeat([]byte{0x42}, sha256.Size)
	body := "trusted object"
	if _, err := (buildcache.Layers{Base: buildcache.Layer{Dir: base}, Persist: true}).Put(
		action, output, strings.NewReader(body), int64(len(body)), time.Now(),
	); err != nil {
		t.Fatal(err)
	}
	outputName := fmt.Sprintf("%x", output)
	if _, err := buildcache.SeedNative(base, destination, time.Now()); err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(destination, outputName[:2])
	nativeObject := filepath.Join(directory, outputName+"-d")
	if err := os.Remove(nativeObject); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(nativeObject, []byte(body), filemode.PrivateFile); err != nil {
		t.Fatal(err)
	}
	if _, err := buildcache.SeedNative(base, destination, time.Now()); err == nil {
		t.Fatal("SeedNative accepted an independently produced destination object")
	}
	if _, err := buildcache.RefreshNative(base, destination, time.Now()); err != nil {
		t.Fatal(err)
	}
	baseInfo, err := os.Stat(filepath.Join(base, "objects", outputName[:2], outputName))
	if err != nil {
		t.Fatal(err)
	}
	nativeInfo, err := os.Stat(nativeObject)
	if err != nil || !os.SameFile(baseInfo, nativeInfo) {
		t.Fatalf("repaired native object = (%v, %v), want the trusted base hard link", nativeInfo, err)
	}
}

func TestCollectNativeRejectsMissingAndIrregularCacheDirectories(t *testing.T) {
	t.Parallel()
	parent := t.TempDir()
	missing := filepath.Join(parent, "missing")
	rootFile := filepath.Join(parent, "root-file")
	if err := os.WriteFile(rootFile, []byte("not a directory"), filemode.PrivateFile); err != nil {
		t.Fatal(err)
	}
	irregularPrefix := filepath.Join(parent, "irregular-prefix")
	if err := os.Mkdir(irregularPrefix, filemode.PrivateDirectory); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(irregularPrefix, "00"), []byte("not a directory"), filemode.PrivateFile); err != nil {
		t.Fatal(err)
	}
	for _, directory := range []string{missing, rootFile, irregularPrefix} {
		if _, err := buildcache.CollectNative(directory, 1); err == nil {
			t.Errorf("CollectNative(%q) accepted an unavailable or irregular cache", directory)
		}
	}
}

func TestCollectNativeBoundsOnlyTheRunProjection(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	destination := t.TempDir()
	if err := (buildcache.Layer{Dir: base}).Prepare(); err != nil {
		t.Fatal(err)
	}
	layers := buildcache.Layers{Base: buildcache.Layer{Dir: base}, Persist: true}
	moment := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	for index, value := range []string{"old object", "new object"} {
		action := bytes.Repeat([]byte{byte(index + 1)}, sha256.Size)
		output := bytes.Repeat([]byte{byte(index + 0x11)}, sha256.Size)
		if _, err := layers.Put(action, output, strings.NewReader(value), int64(len(value)), moment); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := buildcache.SeedNative(base, destination, moment); err != nil {
		t.Fatal(err)
	}
	oldOutput := strings.Repeat("11", sha256.Size)
	newOutput := strings.Repeat("12", sha256.Size)
	oldPath := filepath.Join(destination, oldOutput[:2], oldOutput+"-d")
	newPath := filepath.Join(destination, newOutput[:2], newOutput+"-d")
	if err := os.Chtimes(oldPath, moment.Add(-time.Hour), moment.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(newPath, moment, moment); err != nil {
		t.Fatal(err)
	}
	collected, err := buildcache.CollectNative(destination, int64(len("new object")))
	if err != nil || collected.RemovedObjects != 1 || collected.AfterBytes > int64(len("new object")) {
		t.Fatalf("CollectNative = (%+v, %v)", collected, err)
	}
	if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
		t.Fatalf("old native object survived collection: %v", err)
	}
	if got, err := os.ReadFile(newPath); err != nil || string(got) != "new object" {
		t.Fatalf("new native object = %q, error %v", got, err)
	}

	for _, output := range []string{oldOutput, newOutput} {
		if _, err := os.Stat(filepath.Join(base, "objects", output[:2], output)); err != nil {
			t.Fatalf("base object %s changed by native collection: %v", output, err)
		}
	}
}

func TestCollectNativeUsesTheNewestActionAsAnObjectsLastUse(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	destination := t.TempDir()
	if err := (buildcache.Layer{Dir: base}).Prepare(); err != nil {
		t.Fatal(err)
	}
	layers := buildcache.Layers{Base: buildcache.Layer{Dir: base}, Persist: true}
	moment := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	for index, value := range []string{"hot-object", "cold-object"} {
		action := bytes.Repeat([]byte{byte(index + 1)}, sha256.Size)
		output := bytes.Repeat([]byte{byte(index + 0x21)}, sha256.Size)
		if _, err := layers.Put(action, output, strings.NewReader(value), int64(len(value)), moment); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := buildcache.SeedNative(base, destination, moment); err != nil {
		t.Fatal(err)
	}
	for _, output := range []string{strings.Repeat("21", sha256.Size), strings.Repeat("22", sha256.Size)} {
		path := filepath.Join(destination, output[:2], output+"-d")
		if err := os.Chtimes(path, moment.Add(-time.Hour), moment.Add(-time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	hotAction := strings.Repeat("01", sha256.Size)
	coldAction := strings.Repeat("02", sha256.Size)
	if err := os.Chtimes(filepath.Join(destination, hotAction[:2], hotAction+"-a"), moment, moment); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(filepath.Join(destination, coldAction[:2], coldAction+"-a"), moment.Add(-time.Hour), moment.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := buildcache.CollectNative(destination, int64(len("hot-object"))); err != nil {
		t.Fatal(err)
	}
	hotOutput := strings.Repeat("21", sha256.Size)
	coldOutput := strings.Repeat("22", sha256.Size)
	if _, err := os.Stat(filepath.Join(destination, hotOutput[:2], hotOutput+"-d")); err != nil {
		t.Fatalf("hot object was evicted: %v", err)
	}
	if _, err := os.Stat(filepath.Join(destination, coldOutput[:2], coldOutput+"-d")); !os.IsNotExist(err) {
		t.Fatalf("cold object survived collection: %v", err)
	}
}

func TestPersistNativePromotesOnlyRunLocalActions(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	native := t.TempDir()
	if err := (buildcache.Layer{Dir: base}).Prepare(); err != nil {
		t.Fatal(err)
	}
	layers := buildcache.Layers{Base: buildcache.Layer{Dir: base}, Persist: true}
	oldAction := bytes.Repeat([]byte{0x11}, sha256.Size)
	oldOutput := bytes.Repeat([]byte{0x21}, sha256.Size)
	oldBody := "old archive"
	if _, err := layers.Put(oldAction, oldOutput, strings.NewReader(oldBody), int64(len(oldBody)), time.Now()); err != nil {
		t.Fatal(err)
	}
	baseline, err := buildcache.SeedNative(base, native, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	newAction := bytes.Repeat([]byte{0x12}, sha256.Size)
	newOutput := bytes.Repeat([]byte{0x22}, sha256.Size)
	newActionName := hex.EncodeToString(newAction)
	newOutputName := hex.EncodeToString(newOutput)
	newBody := "new archive"
	if err := os.WriteFile(filepath.Join(native, newOutputName[:2], newOutputName+"-d"), []byte(newBody), filemode.PrivateFile); err != nil {
		t.Fatal(err)
	}
	record := fmt.Sprintf("v1 %s %s %d %d\n", newActionName, newOutputName, len(newBody), time.Now().UnixNano())
	if err := os.WriteFile(filepath.Join(native, newActionName[:2], newActionName+"-a"), []byte(record), filemode.PrivateFile); err != nil {
		t.Fatal(err)
	}
	persisted, err := buildcache.PersistNative(base, native, baseline, time.Now())
	if err != nil || persisted.Actions != 1 || persisted.Objects != 1 || persisted.Bytes != int64(len(newBody)) || persisted.Skipped != 0 || persisted.Deferred {
		t.Fatalf("PersistNative = (%+v, %v)", persisted, err)
	}
	entry, source, err := layers.Get(newAction, time.Now())
	if err != nil || source != buildcache.SourceBase || entry.Size != int64(len(newBody)) {
		t.Fatalf("persisted Get = (%+v, %s, %v)", entry, source, err)
	}
	baseInfo, err := os.Stat(filepath.Join(base, "objects", newOutputName[:2], newOutputName))
	if err != nil {
		t.Fatal(err)
	}
	nativeInfo, err := os.Stat(filepath.Join(native, newOutputName[:2], newOutputName+"-d"))
	if err != nil || !os.SameFile(baseInfo, nativeInfo) {
		t.Fatalf("persisted object = (%v, %v), want the native content-addressed inode", nativeInfo, err)
	}
	again, err := buildcache.PersistNative(base, native, baseline, time.Now())
	if err != nil || again.Actions != 0 || again.Objects != 0 || again.Bytes != 0 || again.Skipped != 0 || again.Deferred {
		t.Fatalf("idempotent PersistNative = (%+v, %v)", again, err)
	}
}

func TestPersistNativeDefersWhileCollectionOwnsTheBase(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	native := t.TempDir()
	if err := (buildcache.Layer{Dir: base}).Prepare(); err != nil {
		t.Fatal(err)
	}
	if _, err := buildcache.SeedNative(base, native, time.Now()); err != nil {
		t.Fatal(err)
	}
	release, held, err := (buildcache.Layer{Dir: base}).HoldCollection()
	if err != nil || !held {
		t.Fatalf("HoldCollection = (%t, %v)", held, err)
	}
	defer func() {
		if err := release(); err != nil {
			t.Error(err)
		}
	}()
	persisted, err := buildcache.PersistNative(base, native, buildcache.NativeSeed{}, time.Now())
	if err != nil || !persisted.Deferred || persisted.Actions != 0 || persisted.Objects != 0 {
		t.Fatalf("PersistNative under collection = (%+v, %v)", persisted, err)
	}
}
