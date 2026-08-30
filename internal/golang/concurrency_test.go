// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package golang_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
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

func TestConcurrencyPackagesDetectsEveryPrimitiveAndCanonicalizesResult(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	sources := map[string]string{
		"atomic/atomic.go": `package atomic
import "sync/atomic"
var value atomic.Int64
`,
		"channel/channel.go": "package channel\nvar values chan int\n",
		"go_stmt/go.go":      "package gostmt\nfunc start() { go func() {}() }\n",
		"receive/receive.go": "package receive\nimport \"fixture/source\"\nfunc read() { _ = <-source.Values() }\n",
		"select/select.go":   "package selectstmt\nfunc wait() { select {} }\n",
		"send/send.go":       "package send\nfunc write(values chan int) { values <- 1 }\n",
		"sync/sync.go":       "package syncpkg\nimport \"sync\"\nvar lock sync.RWMutex\n",
		"plain/plain.go":     "package plain\nimport \"fmt\"\nfunc value() string { return fmt.Sprint(1) }\n",
	}
	for relative, source := range sources {
		writeGo(t, root, relative, source)
	}
	writeGo(t, root, "plain/ignored.txt", "package ignored\nvar values chan int\n")
	writeGo(t, root, "plain/ignored.go/nested.go", "package ignored\nvar values chan int\n")

	packages := []goanalysis.Package{
		{ImportPath: "fixture/sync", RelativeDir: "sync"},
		{ImportPath: "fixture/send", RelativeDir: "send"},
		{ImportPath: "fixture/select", RelativeDir: "select"},
		{ImportPath: "fixture/receive", RelativeDir: "receive"},
		{ImportPath: "fixture/plain", RelativeDir: "plain"},
		{ImportPath: "fixture/go", RelativeDir: "go_stmt"},
		{ImportPath: "fixture/channel", RelativeDir: "channel"},
		{ImportPath: "fixture/atomic", RelativeDir: "atomic"},
		{ImportPath: "fixture/send", RelativeDir: "send"},
	}
	got, err := goanalysis.ConcurrencyPackages(root, packages)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"fixture/atomic", "fixture/channel", "fixture/go", "fixture/receive",
		"fixture/select", "fixture/send", "fixture/sync",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("ConcurrencyPackages = %v, want %v", got, want)
	}
}

func TestConcurrencyPackagesTreatsEmptyRelativeDirectoryAsRoot(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeGo(t, root, "root.go", "package root\nvar values chan int\n")
	got, err := goanalysis.ConcurrencyPackages(root, []goanalysis.Package{{ImportPath: "fixture", RelativeDir: ""}})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, []string{"fixture"}) {
		t.Fatalf("ConcurrencyPackages = %v", got)
	}
}

func TestConcurrencyPackagesReportsDirectoryFailure(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	_, err := goanalysis.ConcurrencyPackages(root, []goanalysis.Package{{ImportPath: "fixture/missing", RelativeDir: "missing"}})
	if err == nil || !strings.HasPrefix(err.Error(), "goatest: inspect concurrency in fixture/missing: ") {
		t.Fatalf("ConcurrencyPackages error = %v", err)
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
