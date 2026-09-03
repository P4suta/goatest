// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package assure_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/P4suta/goatest/internal/assure"
	"github.com/P4suta/goatest/internal/tempowner"
)

// abandonedDirectory makes what a run that was killed leaves behind: claimed in
// somebody's name, its lock freed by the operating system, and never kept.
func abandonedDirectory(t *testing.T, parent string) string {
	t.Helper()
	directory := filepath.Join(parent, "goatest-run-dead")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "payload"), make([]byte, 512), 0o600); err != nil {
		t.Fatal(err)
	}
	owner, err := tempowner.Claim(directory, tempowner.Marker{RunID: "goatest-run-dead"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := owner.Release(); err != nil {
		t.Fatal(err)
	}
	return directory
}

// A plan makes the same temporary directories a verify does, so an interrupted
// plan leaves the same leftovers. It has to collect them too: otherwise a
// developer who only ever plans accumulates them until somebody runs a verify
// or types `cache gc`.
func TestAPlanCollectsWhatRunsThatWereKilledLeftBehind(t *testing.T) {
	t.Parallel()
	temporary := t.TempDir()
	dead := abandonedDirectory(t, temporary)
	// The plan itself cannot get past locating a toolchain that is not there,
	// which is exactly the point: the sweep happens before the plan does any
	// work, so that the disk is back before this process asks for any of it.
	if _, err := assure.Plan(t.Context(), assure.Options{
		Root: t.TempDir(), Contract: "standard-v1", GoBinary: "definitely-missing-goatest-go",
		TempDirectory: temporary,
	}); err == nil {
		t.Fatal("plan without a toolchain = no error, want the failure that ends it")
	}
	if _, err := os.Stat(dead); !os.IsNotExist(err) {
		t.Fatalf("stat the abandoned directory after a plan = %v, want it collected", err)
	}
}

// And a plan sweeps only the directory it was given, for the reason every other
// sweep does: a value nobody set must never become the machine's own temporary
// directory.
func TestAPlanNeverSweepsATemporaryDirectoryNobodyNamed(t *testing.T) {
	t.Parallel()
	elsewhere := t.TempDir()
	dead := abandonedDirectory(t, elsewhere)
	if _, err := assure.Plan(t.Context(), assure.Options{
		Root: t.TempDir(), Contract: "standard-v1", GoBinary: "definitely-missing-goatest-go",
	}); err == nil {
		t.Fatal("plan without a toolchain = no error, want the failure that ends it")
	}
	if _, err := os.Stat(dead); err != nil {
		t.Fatalf("stat the abandoned directory after a plan that was given no temporary root = %v, want it untouched", err)
	}
}
