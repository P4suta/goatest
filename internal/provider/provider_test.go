// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package provider_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/P4suta/goatest/internal/provider"
	"github.com/P4suta/goatest/internal/report"
)

func TestGenerationProviderHelper(t *testing.T) {
	if os.Getenv("GOATEST_GENERATION_HELPER") != "1" {
		return
	}
	if os.Getenv("GOATEST_GENERATION_CHILD") == "1" {
		delay := time.Second
		if configured, err := time.ParseDuration(os.Getenv("GOATEST_GENERATION_CHILD_DELAY")); err == nil && configured > 0 {
			delay = configured
		}
		time.Sleep(delay)
		_ = os.WriteFile(os.Getenv("GOATEST_GENERATION_MARKER"), []byte("descendant survived"), 0o600)
		os.Exit(0)
	}
	var request provider.Request
	if err := json.NewDecoder(os.Stdin).Decode(&request); err != nil {
		os.Exit(30)
	}
	switch os.Getenv("GOATEST_GENERATION_MODE") {
	case "slow":
		time.Sleep(30 * time.Second)
	case "tree-parent":
		child := exec.Command(os.Args[0], "-test.run=^TestGenerationProviderHelper$")
		child.Env = append(os.Environ(), "GOATEST_GENERATION_CHILD=1")
		if err := child.Start(); err != nil {
			os.Exit(31)
		}
		time.Sleep(30 * time.Second)
	case "tree-parent-success":
		child := exec.Command(os.Args[0], "-test.run=^TestGenerationProviderHelper$")
		child.Env = append(os.Environ(), "GOATEST_GENERATION_CHILD=1")
		if err := child.Start(); err != nil {
			os.Exit(31)
		}
	case "wrong":
		request.Finding.ID = "another-finding"
	}
	_ = json.NewEncoder(os.Stdout).Encode(provider.Response{
		Version:   1,
		FindingID: request.Finding.ID,
		Candidates: []provider.Candidate{{
			Kind: "patch", Path: "roundtrip_test.go", PreimageSHA256: "abc", Content: []byte("package fixture\n"),
		}},
	})
	os.Exit(0)
}

func TestGenerationSuccessCleansUpDescendantProcessTree(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "descendant-marker")
	t.Setenv("GOATEST_GENERATION_HELPER", "1")
	t.Setenv("GOATEST_GENERATION_MODE", "tree-parent-success")
	t.Setenv("GOATEST_GENERATION_MARKER", marker)
	t.Setenv("GOATEST_GENERATION_CHILD_DELAY", "5s")
	client := provider.Client{Command: []string{os.Args[0], "-test.run=^TestGenerationProviderHelper$"}, Timeout: 5 * time.Second}
	if _, err := client.Generate(t.Context(), provider.Request{Version: 1, Finding: report.Finding{ID: "finding-a"}}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(6 * time.Second)
	if _, statErr := os.Stat(marker); !os.IsNotExist(statErr) {
		t.Fatalf("descendant survived successful provider exit: %v", statErr)
	}
}

func TestGenerationTimeoutCleansUpDescendantProcessTree(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "descendant-marker")
	t.Setenv("GOATEST_GENERATION_HELPER", "1")
	t.Setenv("GOATEST_GENERATION_MODE", "tree-parent")
	t.Setenv("GOATEST_GENERATION_MARKER", marker)
	client := provider.Client{Command: []string{os.Args[0], "-test.run=^TestGenerationProviderHelper$"}, Timeout: 150 * time.Millisecond}
	_, err := client.Generate(t.Context(), provider.Request{Version: 1, Finding: report.Finding{ID: "finding-a"}})
	if err == nil {
		t.Fatal("Generate succeeded")
	}
	time.Sleep(1500 * time.Millisecond)
	if _, statErr := os.Stat(marker); !os.IsNotExist(statErr) {
		t.Fatalf("descendant was not cleaned up: %v", statErr)
	}
}

func TestGenerateUsesVersionedOneShotProtocol(t *testing.T) {
	t.Setenv("GOATEST_GENERATION_HELPER", "1")
	client := provider.Client{Command: []string{os.Args[0], "-test.run=^TestGenerationProviderHelper$"}, Timeout: 5 * time.Second}
	request := provider.Request{
		Version:      1,
		Finding:      report.Finding{ID: "finding-a", Kind: "survivor"},
		AllowedPaths: []string{"internal/**"},
		Snapshot:     "snapshot-a",
	}
	response, err := client.Generate(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if response.FindingID != request.Finding.ID || len(response.Candidates) != 1 || !slices.Equal(response.Candidates[0].Content, []byte("package fixture\n")) {
		t.Errorf("response = %+v", response)
	}
}

func TestGenerateRejectsWrongFindingAndHonoursTimeout(t *testing.T) {
	for _, mode := range []string{"wrong", "slow"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("GOATEST_GENERATION_HELPER", "1")
			t.Setenv("GOATEST_GENERATION_MODE", mode)
			timeout := 5 * time.Second
			if mode == "slow" {
				timeout = 150 * time.Millisecond
			}
			client := provider.Client{Command: []string{os.Args[0], "-test.run=^TestGenerationProviderHelper$"}, Timeout: timeout}
			started := time.Now()
			_, err := client.Generate(t.Context(), provider.Request{Version: 1, Finding: report.Finding{ID: "finding-a"}})
			if err == nil {
				t.Fatal("Generate succeeded")
			}
			if time.Since(started) > 5*time.Second {
				t.Errorf("Generate did not stop promptly")
			}
		})
	}
}
