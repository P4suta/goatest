// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package resource_test

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/P4suta/goatest/internal/resource"
)

func TestProviderHelper(t *testing.T) {
	if os.Getenv("GOATEST_RESOURCE_HELPER") != "1" {
		return
	}
	decoder := json.NewDecoder(os.Stdin)
	encoder := json.NewEncoder(os.Stdout)
	var start resource.Request
	if err := decoder.Decode(&start); err != nil {
		os.Exit(20)
	}
	appendLog(os.Getenv("GOATEST_RESOURCE_LOG"), start.Action)
	switch os.Getenv("GOATEST_RESOURCE_MODE") {
	case "slow":
		time.Sleep(30 * time.Second)
	case "invalid":
		_ = encoder.Encode(resource.Response{Version: 2, Status: "ready"})
		return
	case "oversized-ready":
		_ = encoder.Encode(resource.Response{
			Version: 1, Status: "ready", Instance: "postgres-1",
			Environment: map[string]string{"DATABASE_URL": strings.Repeat("x", 2<<20)},
		})
		return
	case "oversized-stderr":
		_, _ = fmt.Fprint(os.Stderr, strings.Repeat("diagnostic", 256<<10))
		_ = encoder.Encode(resource.Response{Version: 2, Status: "ready"})
		return
	}
	_ = encoder.Encode(resource.Response{
		Version:  1,
		Status:   "ready",
		Instance: "postgres-1",
		Environment: map[string]string{
			"DATABASE_URL": "postgres://local/test",
			"A_FIRST":      "yes",
		},
	})
	var stop resource.Request
	if err := decoder.Decode(&stop); err != nil {
		os.Exit(21)
	}
	appendLog(os.Getenv("GOATEST_RESOURCE_LOG"), stop.Action)
	if os.Getenv("GOATEST_RESOURCE_MODE") == "delay-stop" {
		time.Sleep(500 * time.Millisecond)
	}
	_ = encoder.Encode(resource.Response{Version: 1, Status: "stopped", Instance: "postgres-1"})
}

func appendLog(path, value string) {
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		os.Exit(22)
	}
	_, _ = fmt.Fprintln(file, value)
	_ = file.Close()
}

func TestSharedProviderUsesReferenceCountingAndSortedEnvironment(t *testing.T) {
	log := filepath.Join(t.TempDir(), "provider.log")
	t.Setenv("GOATEST_RESOURCE_HELPER", "1")
	t.Setenv("GOATEST_RESOURCE_LOG", log)
	manager := resource.New(map[string]resource.Spec{
		"postgres": {Command: []string{os.Args[0], "-test.run=^TestProviderHelper$"}, Timeout: 5 * time.Second, Shared: true},
	})
	t.Cleanup(func() { _ = manager.Close() })

	first, err := manager.Acquire(t.Context(), "postgres")
	if err != nil {
		t.Fatal(err)
	}
	second, err := manager.Acquire(t.Context(), "postgres")
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
	for _, mode := range []string{"invalid", "slow"} {
		t.Run(mode, func(t *testing.T) {
			log := filepath.Join(t.TempDir(), "provider.log")
			t.Setenv("GOATEST_RESOURCE_HELPER", "1")
			t.Setenv("GOATEST_RESOURCE_LOG", log)
			t.Setenv("GOATEST_RESOURCE_MODE", mode)
			timeout := 5 * time.Second
			if mode == "slow" {
				timeout = 150 * time.Millisecond
			}
			manager := resource.New(map[string]resource.Spec{
				"postgres": {Command: []string{os.Args[0], "-test.run=^TestProviderHelper$"}, Timeout: timeout},
			})
			started := time.Now()
			if _, err := manager.Acquire(t.Context(), "postgres"); err == nil {
				t.Fatal("Acquire succeeded")
			}
			if elapsed := time.Since(started); elapsed > 5*time.Second {
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
			lease, err := manager.Acquire(t.Context(), "postgres")
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
	if _, err := manager.Acquire(t.Context(), "missing"); err == nil {
		t.Fatal("unknown capability was accepted")
	}
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Acquire(t.Context(), "missing"); err == nil {
		t.Fatal("closed manager accepted work")
	}
}

func TestExclusiveProviderSerializesLeases(t *testing.T) {
	log := filepath.Join(t.TempDir(), "provider.log")
	t.Setenv("GOATEST_RESOURCE_HELPER", "1")
	t.Setenv("GOATEST_RESOURCE_LOG", log)
	manager := resource.New(map[string]resource.Spec{
		"postgres": {Command: []string{os.Args[0], "-test.run=^TestProviderHelper$"}, Timeout: 5 * time.Second, Exclusive: true},
	})
	t.Cleanup(func() { _ = manager.Close() })
	first, err := manager.Acquire(t.Context(), "postgres")
	if err != nil {
		t.Fatal(err)
	}
	type acquired struct {
		lease *resource.Lease
		err   error
	}
	secondResult := make(chan acquired, 1)
	go func() {
		lease, acquireErr := manager.Acquire(t.Context(), "postgres")
		secondResult <- acquired{lease: lease, err: acquireErr}
	}()
	select {
	case result := <-secondResult:
		if result.lease != nil {
			_ = result.lease.Release()
		}
		t.Fatalf("exclusive second lease did not wait: %v", result.err)
	case <-time.After(100 * time.Millisecond):
	}
	if err := first.Release(); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-secondResult:
		if result.err != nil {
			t.Fatal(result.err)
		}
		if err := result.lease.Release(); err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("exclusive second lease was not awakened")
	}
}

func TestExclusiveProviderWaitsUntilPreviousProviderHasStopped(t *testing.T) {
	log := filepath.Join(t.TempDir(), "provider.log")
	t.Setenv("GOATEST_RESOURCE_HELPER", "1")
	t.Setenv("GOATEST_RESOURCE_LOG", log)
	t.Setenv("GOATEST_RESOURCE_MODE", "delay-stop")
	manager := resource.New(map[string]resource.Spec{
		"postgres": {Command: []string{os.Args[0], "-test.run=^TestProviderHelper$"}, Timeout: 5 * time.Second, Exclusive: true},
	})
	t.Cleanup(func() { _ = manager.Close() })
	first, err := manager.Acquire(t.Context(), "postgres")
	if err != nil {
		t.Fatal(err)
	}
	type acquired struct {
		lease *resource.Lease
		err   error
	}
	secondResult := make(chan acquired, 1)
	go func() {
		lease, acquireErr := manager.Acquire(t.Context(), "postgres")
		secondResult <- acquired{lease: lease, err: acquireErr}
	}()
	releaseResult := make(chan error, 1)
	go func() { releaseResult <- first.Release() }()
	select {
	case result := <-secondResult:
		if result.lease != nil {
			_ = result.lease.Release()
		}
		t.Fatalf("second provider started while the first was stopping: %v", result.err)
	case <-time.After(200 * time.Millisecond):
	}
	if err := <-releaseResult; err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-secondResult:
		if result.err != nil {
			t.Fatal(result.err)
		}
		if err := result.lease.Release(); err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("second provider was not started after the first stopped")
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
