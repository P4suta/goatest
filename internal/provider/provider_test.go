// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package provider_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/P4suta/goatest/internal/provider"
	"github.com/P4suta/goatest/internal/report"
)

const (
	generationRequestExitCode  = 30
	generationExplicitExitCode = 23
	generationDefaultDelay     = 2 * time.Second
	generationTimeout          = 150 * time.Millisecond
	generationPromptDeadline   = 5 * time.Second
)

func TestGenerationProviderHelper(t *testing.T) {
	if os.Getenv("GOATEST_GENERATION_HELPER") != "1" {
		return
	}
	var request provider.Request
	if err := json.NewDecoder(os.Stdin).Decode(&request); err != nil {
		os.Exit(generationRequestExitCode)
	}
	mode := os.Getenv("GOATEST_GENERATION_MODE")
	switch mode {
	case "slow":
		time.Sleep(generationHelperDelay())
	case "wrong":
		request.Finding.ID = "another-finding"
	case "empty":
		os.Exit(0)
	case "exit-error":
		_, _ = fmt.Fprintln(os.Stderr, "provider exploded")
		os.Exit(generationExplicitExitCode)
	case "malformed":
		_, _ = fmt.Fprint(os.Stdout, "{")
		os.Exit(0)
	case "unknown":
		_ = json.NewEncoder(os.Stdout).Encode(map[string]any{
			"version": provider.ProtocolVersion, "finding_id": request.Finding.ID, "candidates": []any{}, "unknown": true,
		})
		os.Exit(0)
	}
	response := provider.Response{
		Version:   provider.ProtocolVersion,
		FindingID: request.Finding.ID,
		Candidates: []provider.Candidate{{
			Kind: "patch", Path: "roundtrip_test.go", PreimageSHA256: "abc", Content: []byte("package fixture\n"),
		}},
	}
	switch mode {
	case "wrong-version":
		response.Version = provider.ProtocolVersion + 1
	case "too-many":
		response.Candidates = make([]provider.Candidate, provider.MaximumCandidates+1)
	case "at-limit":
		response.Candidates = make([]provider.Candidate, provider.MaximumCandidates)
	}
	_ = json.NewEncoder(os.Stdout).Encode(response)
	if mode == "trailing" {
		_, _ = fmt.Fprintln(os.Stdout, `{}`)
	}
	os.Exit(0)
}

func generationHelperDelay() time.Duration {
	if configured, err := time.ParseDuration(os.Getenv("GOATEST_GENERATION_DELAY")); err == nil && configured > 0 {
		return configured
	}
	return generationDefaultDelay
}

func TestGenerateUsesVersionedOneShotProtocol(t *testing.T) {
	t.Setenv("GOATEST_GENERATION_HELPER", "1")
	client := provider.Client{Command: []string{os.Args[0], "-test.run=^TestGenerationProviderHelper$"}, Timeout: generationPromptDeadline}
	request := provider.Request{
		Version:      provider.ProtocolVersion,
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
			timeout := generationPromptDeadline
			if mode == "slow" {
				timeout = generationTimeout
				t.Setenv("GOATEST_GENERATION_DELAY", "1s")
			}
			client := provider.Client{Command: []string{os.Args[0], "-test.run=^TestGenerationProviderHelper$"}, Timeout: timeout}
			started := time.Now()
			_, err := client.Generate(t.Context(), provider.Request{Version: provider.ProtocolVersion, Finding: report.Finding{ID: "finding-a"}})
			if err == nil {
				t.Fatal("Generate succeeded")
			}
			if time.Since(started) > generationPromptDeadline {
				t.Errorf("Generate did not stop promptly")
			}
		})
	}
}

func TestGenerateRejectsInvalidRequestsBeforeStartingProvider(t *testing.T) {
	t.Parallel()
	valid := provider.Request{Version: provider.ProtocolVersion, Finding: report.Finding{ID: "finding-a"}}
	for _, test := range []struct {
		name    string
		client  provider.Client
		request provider.Request
		want    string
	}{
		{name: "missing command", request: valid, want: "goatest: generation provider has no command"},
		{name: "empty command", client: provider.Client{Command: []string{}}, request: valid, want: "goatest: generation provider has no command"},
		{name: "blank executable", client: provider.Client{Command: []string{" \t"}}, request: valid, want: "goatest: generation provider has no command"},
		{name: "wrong version", client: provider.Client{Command: []string{"must-not-start"}}, request: provider.Request{Version: provider.ProtocolVersion + 1, Finding: report.Finding{ID: "finding-a"}}, want: "goatest: generation request requires protocol v1 and a finding ID"},
		{name: "missing finding", client: provider.Client{Command: []string{"must-not-start"}}, request: provider.Request{Version: provider.ProtocolVersion}, want: "goatest: generation request requires protocol v1 and a finding ID"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := test.client.Generate(t.Context(), test.request)
			if err == nil || err.Error() != test.want {
				t.Fatalf("Generate error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestGenerateUsesDefaultTimeoutForNonPositiveValues(t *testing.T) {
	for _, timeout := range []time.Duration{0, -time.Second} {
		t.Run(timeout.String(), func(t *testing.T) {
			t.Setenv("GOATEST_GENERATION_HELPER", "1")
			client := provider.Client{Command: []string{os.Args[0], "-test.run=^TestGenerationProviderHelper$"}, Timeout: timeout}
			if _, err := client.Generate(t.Context(), provider.Request{Version: provider.ProtocolVersion, Finding: report.Finding{ID: "finding-a"}}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestGenerateReportsStartAndProviderFailures(t *testing.T) {
	t.Run("start", func(t *testing.T) {
		client := provider.Client{Command: []string{filepath.Join(t.TempDir(), "missing-provider")}, Timeout: time.Second}
		_, err := client.Generate(t.Context(), provider.Request{Version: provider.ProtocolVersion, Finding: report.Finding{ID: "finding-a"}})
		if err == nil || !strings.HasPrefix(err.Error(), "goatest: generation provider start: ") {
			t.Fatalf("Generate error = %v", err)
		}
	})
	t.Run("exit", func(t *testing.T) {
		t.Setenv("GOATEST_GENERATION_HELPER", "1")
		t.Setenv("GOATEST_GENERATION_MODE", "exit-error")
		client := provider.Client{Command: []string{os.Args[0], "-test.run=^TestGenerationProviderHelper$"}, Timeout: generationPromptDeadline}
		_, err := client.Generate(t.Context(), provider.Request{Version: provider.ProtocolVersion, Finding: report.Finding{ID: "finding-a"}})
		if err == nil || !strings.HasPrefix(err.Error(), "goatest: generation provider: ") || !strings.Contains(err.Error(), "provider exploded") {
			t.Fatalf("Generate error = %v", err)
		}
	})
}

func TestGenerateRejectsInvalidProviderResponses(t *testing.T) {
	for _, test := range []struct {
		mode string
		want string
	}{
		{mode: "empty", want: "goatest: generation response: EOF"},
		{mode: "malformed", want: "goatest: generation response: unexpected EOF"},
		{mode: "unknown", want: `goatest: generation response: json: unknown field "unknown"`},
		{mode: "trailing", want: "goatest: generation response has trailing data"},
		{mode: "wrong", want: `goatest: generation response identity mismatch: version=1 finding="another-finding"`},
		{mode: "wrong-version", want: `goatest: generation response identity mismatch: version=2 finding="finding-a"`},
		{mode: "too-many", want: "goatest: generation response has 65 candidates, maximum is 64"},
	} {
		t.Run(test.mode, func(t *testing.T) {
			t.Setenv("GOATEST_GENERATION_HELPER", "1")
			t.Setenv("GOATEST_GENERATION_MODE", test.mode)
			client := provider.Client{Command: []string{os.Args[0], "-test.run=^TestGenerationProviderHelper$"}, Timeout: generationPromptDeadline}
			_, err := client.Generate(t.Context(), provider.Request{Version: provider.ProtocolVersion, Finding: report.Finding{ID: "finding-a"}})
			if err == nil || err.Error() != test.want {
				t.Fatalf("Generate error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestGenerateAcceptsMaximumCandidates(t *testing.T) {
	t.Setenv("GOATEST_GENERATION_HELPER", "1")
	t.Setenv("GOATEST_GENERATION_MODE", "at-limit")
	client := provider.Client{Command: []string{os.Args[0], "-test.run=^TestGenerationProviderHelper$"}, Timeout: generationPromptDeadline}
	response, err := client.Generate(t.Context(), provider.Request{Version: provider.ProtocolVersion, Finding: report.Finding{ID: "finding-a"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Candidates) != provider.MaximumCandidates {
		t.Fatalf("candidates = %d", len(response.Candidates))
	}
}
