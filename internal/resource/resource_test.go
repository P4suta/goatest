// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package resource_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/P4suta/goatest/internal/filemode"
	"github.com/P4suta/goatest/internal/resource"
)

const (
	resourceStartDecodeExitCode = 20
	resourceStopDecodeExitCode  = 21
	resourceLogExitCode         = 22
	resourceProviderDeadline    = 5 * time.Second
	resourceFailureDeadline     = 150 * time.Millisecond
	resourceAcquireDeadline     = 3 * time.Second
	oversizedResourcePayload    = resource.ProtocolOutputLimit * 2
)

func TestProviderHelper(t *testing.T) {
	if os.Getenv("GOATEST_RESOURCE_HELPER") != "1" {
		return
	}
	decoder := json.NewDecoder(os.Stdin)
	encoder := json.NewEncoder(os.Stdout)
	var start resource.Request
	if err := decoder.Decode(&start); err != nil {
		os.Exit(resourceStartDecodeExitCode)
	}
	appendLog(os.Getenv("GOATEST_RESOURCE_LOG"), start.Action)
	switch os.Getenv("GOATEST_RESOURCE_MODE") {
	case "slow":
		time.Sleep(resourceHelperDelay())
	case "invalid":
		_ = encoder.Encode(resource.Response{Version: resource.ProtocolVersion + 1, Status: "ready"})
		return
	case "oversized-ready":
		_ = encoder.Encode(resource.Response{
			Version: resource.ProtocolVersion, Status: "ready", Instance: "postgres-1",
			Environment: map[string]string{"DATABASE_URL": strings.Repeat("x", oversizedResourcePayload)},
		})
		return
	case "oversized-stderr":
		_, _ = fmt.Fprint(os.Stderr, strings.Repeat("diagnostic", oversizedResourcePayload/len("diagnostic")))
		_ = encoder.Encode(resource.Response{Version: resource.ProtocolVersion + 1, Status: "ready"})
		return
	case "reserved-environment":
		_ = encoder.Encode(resource.Response{
			Version: resource.ProtocolVersion, Status: "ready", Instance: "postgres-1",
			Environment: map[string]string{"GOPROXY": "https://example.invalid"},
		})
		var stop resource.Request
		if decoder.Decode(&stop) == nil {
			_ = encoder.Encode(resource.Response{Version: resource.ProtocolVersion, Status: "stopped", Instance: "postgres-1"})
		}
		return
	}
	_ = encoder.Encode(resource.Response{
		Version:  resource.ProtocolVersion,
		Status:   "ready",
		Instance: "postgres-1",
		Environment: map[string]string{
			"DATABASE_URL": "postgres://local/test",
			"A_FIRST":      "yes",
		},
	})
	var stop resource.Request
	if err := decoder.Decode(&stop); err != nil {
		os.Exit(resourceStopDecodeExitCode)
	}
	appendLog(os.Getenv("GOATEST_RESOURCE_LOG"), stop.Action)
	_ = encoder.Encode(resource.Response{Version: resource.ProtocolVersion, Status: "stopped", Instance: "postgres-1"})
}

func resourceHelperDelay() time.Duration {
	if configured, err := time.ParseDuration(os.Getenv("GOATEST_RESOURCE_DELAY")); err == nil && configured > 0 {
		return configured
	}
	return time.Second
}

func appendLog(path, value string) {
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, filemode.PrivateFile)
	if err != nil {
		os.Exit(resourceLogExitCode)
	}
	_, _ = fmt.Fprintln(file, value)
	_ = file.Close()
}

func TestSharedProviderUsesReferenceCountingAndSortedEnvironment(t *testing.T) {
	log := filepath.Join(t.TempDir(), "provider.log")
	t.Setenv("GOATEST_RESOURCE_HELPER", "1")
	t.Setenv("GOATEST_RESOURCE_LOG", log)
	manager := resource.New(map[string]resource.Spec{
		"postgres": {Command: []string{os.Args[0], "-test.run=^TestProviderHelper$"}, Timeout: resourceProviderDeadline, Shared: true},
	})
	t.Cleanup(func() { _ = manager.Close() })

	first, err := acquireResource(t, manager, "postgres")
	if err != nil {
		t.Fatal(err)
	}
	second, err := acquireResource(t, manager, "postgres")
	if err != nil {
		t.Fatal(err)
	}
	wantEnv := []string{"A_FIRST=yes", "DATABASE_URL=postgres://local/test"}
	if got := first.Environment(); !slices.Equal(got, wantEnv) || !slices.Equal(second.Environment(), wantEnv) {
		t.Errorf("environment = %v / %v", got, second.Environment())
	}
	if err := first.Release(); err != nil {
		t.Fatal(err)
	}
	if got := readLog(t, log); !slices.Equal(got, []string{"start"}) {
		t.Fatalf("log after first release = %v", got)
	}
	if err := second.Release(); err != nil {
		t.Fatal(err)
	}
	if got := readLog(t, log); !slices.Equal(got, []string{"start", "stop"}) {
		t.Fatalf("log after final release = %v", got)
	}
	if err := second.Release(); err != nil {
		t.Errorf("second release is not idempotent: %v", err)
	}
}

func TestInvalidAndSlowProvidersFailClosedAndAreCleanedUp(t *testing.T) {
	for _, mode := range []string{"invalid", "slow", "reserved-environment"} {
		t.Run(mode, func(t *testing.T) {
			log := filepath.Join(t.TempDir(), "provider.log")
			t.Setenv("GOATEST_RESOURCE_HELPER", "1")
			t.Setenv("GOATEST_RESOURCE_LOG", log)
			t.Setenv("GOATEST_RESOURCE_MODE", mode)
			timeout := resourceProviderDeadline
			if mode == "slow" {
				timeout = resourceFailureDeadline
				t.Setenv("GOATEST_RESOURCE_DELAY", "1s")
			}
			manager := resource.New(map[string]resource.Spec{
				"postgres": {Command: []string{os.Args[0], "-test.run=^TestProviderHelper$"}, Timeout: timeout},
			})
			started := time.Now()
			if _, err := acquireResource(t, manager, "postgres"); err == nil {
				t.Fatal("Acquire succeeded")
			}
			if elapsed := time.Since(started); elapsed > resourceProviderDeadline {
				t.Errorf("Acquire returned after %s", elapsed)
			}
			if err := manager.Close(); err != nil && !strings.Contains(err.Error(), "already") {
				t.Errorf("Close: %v", err)
			}
		})
	}
}

func TestProviderOutputIsBoundedAndFailsClosed(t *testing.T) {
	for _, mode := range []string{"oversized-ready", "oversized-stderr"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("GOATEST_RESOURCE_HELPER", "1")
			t.Setenv("GOATEST_RESOURCE_LOG", filepath.Join(t.TempDir(), "provider.log"))
			t.Setenv("GOATEST_RESOURCE_MODE", mode)
			manager := resource.New(map[string]resource.Spec{
				"postgres": {Command: []string{os.Args[0], "-test.run=^TestProviderHelper$"}, Timeout: 5 * time.Second},
			})
			lease, err := acquireResource(t, manager, "postgres")
			if lease != nil {
				_ = lease.Release()
			}
			_ = manager.Close()
			if err == nil {
				t.Fatal("oversized provider output was accepted")
			}
			if len(err.Error()) > (1<<20)+(64<<10) {
				t.Fatalf("provider diagnostic was not bounded: %d bytes", len(err.Error()))
			}
		})
	}
}

func TestUnknownCapabilityAndClosedManagerAreErrors(t *testing.T) {
	manager := resource.New(nil)
	if _, err := acquireResource(t, manager, "missing"); err == nil {
		t.Fatal("unknown capability was accepted")
	}
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := acquireResource(t, manager, "missing"); err == nil {
		t.Fatal("closed manager accepted work")
	}
}

func TestProviderUsesDefaultTimeoutForNonPositiveValues(t *testing.T) {
	for _, timeout := range []time.Duration{0, -time.Second} {
		t.Run(timeout.String(), func(t *testing.T) {
			t.Setenv("GOATEST_RESOURCE_HELPER", "1")
			t.Setenv("GOATEST_RESOURCE_LOG", filepath.Join(t.TempDir(), "provider.log"))
			manager := resource.New(map[string]resource.Spec{
				"postgres": {Command: []string{os.Args[0], "-test.run=^TestProviderHelper$"}, Timeout: timeout},
			})
			lease, err := acquireResource(t, manager, "postgres")
			if err != nil {
				t.Fatal(err)
			}
			if err := lease.Release(); err != nil {
				t.Fatal(err)
			}
			if err := manager.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func readLog(t *testing.T, path string) []string {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	var lines []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return lines
}

func acquireResource(t *testing.T, manager *resource.Manager, capability string) (*resource.Lease, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), resourceAcquireDeadline)
	defer cancel()
	return manager.Acquire(ctx, capability)
}
