// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package assure_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/P4suta/goatest/internal/assure"
	"github.com/P4suta/goatest/internal/filemode"
	"github.com/P4suta/goatest/internal/tempowner"
)

const abandonedPayloadBytes = 512

func abandonedDirectory(t *testing.T, parent string) string {
	t.Helper()
	directory := filepath.Join(parent, "goatest-run-dead")
	if err := os.MkdirAll(directory, filemode.PrivateDirectory); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "payload"), make([]byte, abandonedPayloadBytes), filemode.PrivateFile); err != nil {
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

func TestAPlanCollectsWhatRunsThatWereKilledLeftBehind(t *testing.T) {
	t.Parallel()
	temporary := t.TempDir()
	dead := abandonedDirectory(t, temporary)

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
