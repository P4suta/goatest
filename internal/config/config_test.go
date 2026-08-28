// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package config_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/P4suta/goatest/internal/config"
)

func TestLoadIsOptionalStrictAndVersioned(t *testing.T) {
	empty, err := config.Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if empty.Contract != "standard-v1" || empty.Version != 1 {
		t.Errorf("defaults = %+v", empty)
	}

	root := t.TempDir()
	contents := `version = 1
contract = "deep-v1"

[resources.postgres]
command = ["provider", "postgres"]
timeout = "45s"
shared = true

[generation]
command = ["generator"]
allowed_paths = ["internal/**"]

[[acceptance]]
id = "finding-a"
reason = "equivalent"
expires = "2026-12-31T00:00:00Z"
`
	if err := os.WriteFile(filepath.Join(root, config.FileName), []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	loaded, err := config.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Contract != "deep-v1" || loaded.Resources["postgres"].Timeout != 45*time.Second || !loaded.Resources["postgres"].Shared {
		t.Errorf("loaded = %+v", loaded)
	}
	if len(loaded.Acceptance) != 1 || loaded.Acceptance[0].ID != "finding-a" {
		t.Errorf("acceptance = %+v", loaded.Acceptance)
	}
}

func TestLoadRejectsUnknownKeysAndInvalidContracts(t *testing.T) {
	for name, contents := range map[string]string{
		"unknown":  "version = 1\ncontrcat = \"standard-v1\"\n",
		"version":  "version = 2\n",
		"contract": "version = 1\ncontract = \"shallow\"\n",
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, config.FileName), []byte(contents), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := config.Load(root); err == nil {
				t.Fatal("Load succeeded, want strict rejection")
			}
		})
	}
}

func TestInitAndAddAcceptanceRoundTripWithoutWeakeningStrictness(t *testing.T) {
	root := t.TempDir()
	if err := config.Init(root); err != nil {
		t.Fatal(err)
	}
	if err := config.Init(root); err == nil {
		t.Fatal("Init overwrote an existing config")
	}
	expires := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	if err := config.AddAcceptance(root, config.Acceptance{ID: "finding-a", Reason: "reviewed boundary equivalence", Expires: expires}); err != nil {
		t.Fatal(err)
	}
	loaded, err := config.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Acceptance) != 1 || loaded.Acceptance[0].ID != "finding-a" || !loaded.Acceptance[0].Expires.Equal(expires) {
		t.Fatalf("acceptance = %+v", loaded.Acceptance)
	}
	if err := config.AddAcceptance(root, config.Acceptance{ID: "finding-a", Reason: "duplicate", Expires: expires}); err == nil {
		t.Fatal("duplicate acceptance was allowed")
	}
}
