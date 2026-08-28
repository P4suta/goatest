// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package golang_test

import (
	"os"
	"path/filepath"
	"testing"

	goanalysis "github.com/P4suta/goatest/internal/golang"
)

func TestConcurrencyPackagesDetectsLanguageAndLibraryPrimitives(t *testing.T) {
	root := t.TempDir()
	writeGo(t, root, "plain/plain.go", "package plain\n\nfunc Value() int { return 1 }\n")
	writeGo(t, root, "worker/worker.go", "package worker\n\nfunc Start(fn func()) { go fn() }\n")
	writeGo(t, root, "state/state.go", "package state\n\nimport \"sync\"\n\ntype State struct { mu sync.Mutex }\n")
	packages := []goanalysis.Package{
		{ImportPath: "fixture/plain", RelativeDir: "plain"},
		{ImportPath: "fixture/state", RelativeDir: "state"},
		{ImportPath: "fixture/worker", RelativeDir: "worker"},
	}
	got, err := goanalysis.ConcurrencyPackages(root, packages)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"fixture/state", "fixture/worker"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("ConcurrencyPackages = %v, want %v", got, want)
	}
}

func TestConcurrencyPackagesFailsClosedOnMalformedSource(t *testing.T) {
	root := t.TempDir()
	writeGo(t, root, "broken.go", "package broken\nfunc (")
	_, err := goanalysis.ConcurrencyPackages(root, []goanalysis.Package{{ImportPath: "fixture/broken", RelativeDir: "."}})
	if err == nil {
		t.Fatal("malformed source was ignored")
	}
}

func writeGo(t *testing.T, root, relative, contents string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}
