// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package resource

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestManagerSharedLifecycleIsReferenceCountedAndDeterministic(t *testing.T) {
	originalStart, originalStop := startResource, stopResource
	t.Cleanup(func() { startResource, stopResource = originalStart, originalStop })
	var requests []string
	var captured []Spec
	startResource = func(_ context.Context, capability, requestID string, spec Spec) (*instance, error) {
		requests = append(requests, requestID)
		captured = append(captured, spec)
		return &instance{
			capability: capability, requestID: requestID, instanceID: requestID,
			environment: map[string]string{"Z_LAST": "z", "A_FIRST": "a"},
		}, nil
	}
	stopFailure := errors.New("stop failed")
	stops := 0
	stopResource = func(*instance) error {
		stops++
		return stopFailure
	}

	providerEnvironment := []string{"PATH=provider", "TOKEN=allowed"}
	specs := map[string]Spec{"db": {Command: []string{"provider", "arg"}, Shared: true, Environment: providerEnvironment}}
	manager := New(specs)
	providerEnvironment[0] = "MUTATED=yes"
	specs["db"] = Spec{}
	first, err := acquireInternal(manager, "db")
	if err != nil {
		t.Fatal(err)
	}
	second, err := acquireInternal(manager, "db")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(requests, []string{"resource-000001"}) || len(captured) != 1 || !slices.Equal(captured[0].Command, []string{"provider", "arg"}) ||
		!slices.Equal(captured[0].Environment, []string{"PATH=provider", "TOKEN=allowed"}) {
		t.Fatalf("requests = %v, specs = %+v", requests, captured)
	}
	wantEnvironment := []string{"A_FIRST=a", "Z_LAST=z"}
	if got := first.Environment(); !slices.Equal(got, wantEnvironment) || !slices.Equal(second.Environment(), wantEnvironment) {
		t.Fatalf("environment = %v / %v", got, second.Environment())
	}
	mutated := first.Environment()
	mutated[0] = "MUTATED=yes"
	if !slices.Equal(first.Environment(), wantEnvironment) {
		t.Fatal("lease environment aliases caller storage")
	}
	if err := first.Release(); err != nil || stops != 0 {
		t.Fatalf("first Release = %v, stops = %d", err, stops)
	}
	if err := second.Release(); !errors.Is(err, stopFailure) || stops != 1 {
		t.Fatalf("second Release = %v, stops = %d", err, stops)
	}
	if err := second.Release(); !errors.Is(err, stopFailure) || stops != 1 {
		t.Fatalf("repeated Release = %v, stops = %d", err, stops)
	}
	third, err := acquireInternal(manager, "db")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(requests, []string{"resource-000001", "resource-000002"}) {
		t.Fatalf("requests = %v", requests)
	}
	_ = third.Release()
}

func TestManagerValidatesSpecsAndExclusiveWaiting(t *testing.T) {
	originalStart, originalStop := startResource, stopResource
	t.Cleanup(func() { startResource, stopResource = originalStart, originalStop })
	startResource = func(_ context.Context, capability, requestID string, _ Spec) (*instance, error) {
		return &instance{capability: capability, requestID: requestID}, nil
	}
	stopResource = func(*instance) error { return nil }

	if _, err := (*Manager)(nil).Acquire(context.Background(), "db"); err == nil || err.Error() != "goatest: nil resource manager" {
		t.Fatalf("nil manager error = %v", err)
	}
	if err := (*Manager)(nil).Close(); err != nil {
		t.Fatalf("nil manager Close = %v", err)
	}
	manager := New(map[string]Spec{
		"invalid": {Shared: true, Exclusive: true},
		"db":      {Exclusive: true},
		"cache":   {Exclusive: true},
		"plain":   {},
	})
	if _, err := acquireInternal(manager, "missing"); err == nil || err.Error() != `goatest: no provider for capability "missing"` {
		t.Fatalf("unknown capability error = %v", err)
	}
	if _, err := acquireInternal(manager, "invalid"); err == nil || err.Error() != `goatest: resource "invalid" cannot be shared and exclusive` {
		t.Fatalf("invalid spec error = %v", err)
	}
	first, err := acquireInternal(manager, "db")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := manager.Acquire(ctx, "db"); !errors.Is(err, context.Canceled) || err.Error() != `goatest: wait for exclusive resource "db": context canceled` {
		t.Fatalf("exclusive cancellation error = %v", err)
	}
	other, err := acquireInternal(manager, "cache")
	if err != nil {
		t.Fatalf("other capability was blocked: %v", err)
	}
	plainOne, err := acquireInternal(manager, "plain")
	if err != nil {
		t.Fatal(err)
	}
	plainTwo, err := acquireInternal(manager, "plain")
	if err != nil {
		t.Fatalf("non-exclusive capability was blocked: %v", err)
	}
	for _, lease := range []*Lease{first, other, plainOne, plainTwo} {
		if err := lease.Release(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestManagerReleaseAndClosePropagateStopsAndWakeWaiters(t *testing.T) {
	originalStart, originalStop := startResource, stopResource
	t.Cleanup(func() { startResource, stopResource = originalStart, originalStop })
	startResource = func(_ context.Context, capability, requestID string, _ Spec) (*instance, error) {
		return &instance{capability: capability, requestID: requestID}, nil
	}
	firstFailure := errors.New("first stop failed")
	secondFailure := errors.New("second stop failed")
	stopResource = func(target *instance) error {
		if target.capability == "first" {
			return firstFailure
		}
		return secondFailure
	}
	manager := New(map[string]Spec{"first": {}, "second": {}, "exclusive": {Exclusive: true}})
	first, _ := acquireInternal(manager, "first")
	second, _ := acquireInternal(manager, "second")
	exclusive, _ := acquireInternal(manager, "exclusive")
	waiting := make(chan error, 1)
	go func() {
		_, err := acquireInternal(manager, "exclusive")
		waiting <- err
	}()

	if err := manager.Close(); !errors.Is(err, firstFailure) || !errors.Is(err, secondFailure) {
		t.Fatalf("Close error = %v", err)
	}
	select {
	case err := <-waiting:
		if err == nil || err.Error() != "goatest: resource manager is closed" {
			t.Fatalf("waiting Acquire error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Close did not wake waiting Acquire")
	}
	if err := manager.Close(); err != nil {
		t.Fatalf("second Close = %v", err)
	}
	if _, err := acquireInternal(manager, "first"); err == nil || err.Error() != "goatest: resource manager is closed" {
		t.Fatalf("Acquire after Close error = %v", err)
	}
	for _, lease := range []*Lease{first, second, exclusive} {
		if err := lease.Release(); err != nil {
			t.Fatalf("release after Close = %v", err)
		}
	}
}

func TestManagerInternalBookkeepingHandlesInactiveAndDifferentSharedInstance(t *testing.T) {
	originalStop := stopResource
	t.Cleanup(func() { stopResource = originalStop })
	stops := 0
	stopResource = func(*instance) error { stops++; return nil }
	manager := New(nil)
	target := &instance{capability: "db", refs: 1}
	other := &instance{capability: "db", refs: 1}
	manager.active[target] = true
	manager.shared["db"] = other
	if err := manager.release(target); err != nil || manager.shared["db"] != other || stops != 1 {
		t.Fatalf("release = %v, shared = %p, stops = %d", err, manager.shared["db"], stops)
	}
	if err := manager.release(target); err != nil || stops != 1 {
		t.Fatalf("inactive release = %v, stops = %d", err, stops)
	}
	if manager.capabilityActive("db") {
		t.Fatal("inactive capability reported active")
	}
	manager.active[other] = true
	if !manager.capabilityActive("db") || manager.capabilityActive("cache") {
		t.Fatal("capabilityActive returned wrong result")
	}
	oldChanged := manager.changed
	manager.notifyLocked()
	select {
	case <-oldChanged:
	default:
		t.Fatal("notifyLocked did not close previous channel")
	}
	manager.changed = nil
	manager.notifyLocked()
	if manager.changed == nil {
		t.Fatal("notifyLocked did not replace nil channel")
	}
}

func TestNilLeaseMethodsAreNoops(t *testing.T) {
	t.Parallel()
	var lease *Lease
	if lease.Environment() != nil {
		t.Fatal("nil lease returned environment")
	}
	if err := lease.Release(); err != nil {
		t.Fatalf("nil lease Release = %v", err)
	}
}

func TestEnvironmentValidationCoversInvalidAndReservedKeys(t *testing.T) {
	t.Parallel()
	if err := validateEnvironment(map[string]string{"DATABASE_URL": "local", "CUSTOM": "value"}); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name string
		env  map[string]string
		want string
	}{
		{name: "empty key", env: map[string]string{"": "x"}, want: `invalid environment entry ""`},
		{name: "equals", env: map[string]string{"A=B": "x"}, want: `invalid environment entry "A=B"`},
		{name: "nul key", env: map[string]string{"A\x00B": "x"}, want: `invalid environment entry "A\x00B"`},
		{name: "nul value", env: map[string]string{"A": "x\x00y"}, want: `invalid environment entry "A"`},
		{name: "go-mutants", env: map[string]string{"go_mutants_token": "x"}, want: `reserved environment key "go_mutants_token"`},
		{name: "goatest", env: map[string]string{"GoAtEsT_token": "x"}, want: `reserved environment key "GoAtEsT_token"`},
		{name: "tmp", env: map[string]string{"tmp": "x"}, want: `reserved environment key "tmp"`},
		{name: "temp", env: map[string]string{"temp": "x"}, want: `reserved environment key "temp"`},
		{name: "tmpdir", env: map[string]string{"tmpdir": "x"}, want: `reserved environment key "tmpdir"`},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := validateEnvironment(test.env); err == nil || err.Error() != test.want {
				t.Fatalf("validateEnvironment error = %v, want %q", err, test.want)
			}
		})
	}
	reserved := []string{
		"CGO_ENABLED", "GO111MODULE", "GOARCH", "GOCACHE", "GODEBUG", "GOENV", "GOEXPERIMENT", "GOFLAGS",
		"GOMOD", "GOMODCACHE", "GONOPROXY", "GONOSUMDB", "GOOS", "GOPATH", "GOPRIVATE", "GOPROXY",
		"GOROOT", "GOSUMDB", "GOTELEMETRY", "GOTOOLCHAIN", "GOWORK",
	}
	for _, key := range reserved {
		if !reservedGoEnvironment(key) {
			t.Errorf("%s is not reserved", key)
		}
	}
	if reservedGoEnvironment("GOATEST_SAFE_CUSTOM") || reservedGoEnvironment("") {
		t.Fatal("custom environment was reserved")
	}
}

func TestEnvironmentCloneAndRenderingDoNotAlias(t *testing.T) {
	t.Parallel()
	original := map[string]string{"Z": "last", "A": "first"}
	cloned := cloneEnvironment(original)
	original["A"] = "mutated"
	if cloned["A"] != "first" {
		t.Fatal("cloneEnvironment aliases input")
	}
	provider := &instance{environment: cloned}
	if got := environment(provider); !slices.Equal(got, []string{"A=first", "Z=last"}) {
		t.Fatalf("environment = %v", got)
	}
}

func TestResourceLimitedBufferBoundariesAndDiagnostics(t *testing.T) {
	t.Run("within limit", func(t *testing.T) {
		buffer := &limitedBuffer{remaining: 5}
		if written, err := buffer.Write([]byte("ab")); err != nil || written != 2 {
			t.Fatalf("first Write = (%d, %v)", written, err)
		}
		if written, err := buffer.Write([]byte("cd")); err != nil || written != 2 {
			t.Fatalf("second Write = (%d, %v)", written, err)
		}
		if buffer.remaining != 1 || buffer.String() != "abcd" {
			t.Fatalf("buffer = %q, remaining = %d", buffer.String(), buffer.remaining)
		}
	})
	t.Run("exact limit", func(t *testing.T) {
		buffer := &limitedBuffer{remaining: 3}
		if written, err := buffer.Write([]byte("abc")); err != nil || written != 3 || buffer.remaining != 0 || buffer.String() != "abc" {
			t.Fatalf("Write = (%d, %v), buffer = %q", written, err, buffer.String())
		}
	})
	t.Run("truncated", func(t *testing.T) {
		buffer := &limitedBuffer{remaining: 3}
		if written, err := buffer.Write([]byte("abcde")); err != nil || written != 5 || buffer.remaining != 0 {
			t.Fatalf("Write = (%d, %v), remaining = %d", written, err, buffer.remaining)
		}
		const want = "abc\n[goatest: provider stderr truncated]"
		if got := buffer.String(); got != want {
			t.Fatalf("String = %q, want %q", got, want)
		}
		if written, err := buffer.Write([]byte("more")); err != nil || written != 4 || buffer.String() != want {
			t.Fatalf("second Write = (%d, %v), String = %q", written, err, buffer.String())
		}
	})
	t.Run("empty", func(t *testing.T) {
		buffer := &limitedBuffer{}
		if written, err := buffer.Write(nil); err != nil || written != 0 || buffer.String() != "" {
			t.Fatalf("Write = (%d, %v), String = %q", written, err, buffer.String())
		}
	})
}

func TestDecodeAcceptsExactLimitAndRejectsProtocolFailures(t *testing.T) {
	t.Parallel()
	valid := Response{Version: 1, Status: "ready", Instance: "db-1"}
	validJSON, err := json.Marshal(valid)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name  string
		input []byte
		want  string
	}{
		{name: "malformed", input: []byte("{\n"), want: "unexpected EOF"},
		{name: "unknown", input: []byte(`{"version":1,"status":"ready","instance":"db-1","unknown":true}` + "\n"), want: `json: unknown field "unknown"`},
		{name: "trailing", input: append(append(slices.Clone(validJSON), []byte(" {}")...), '\n'), want: "goatest: resource response has trailing data"},
		{name: "no newline", input: validJSON, want: "EOF"},
	} {
		t.Run(test.name, func(t *testing.T) {
			provider := &instance{stdout: bufio.NewReaderSize(bytes.NewReader(test.input), protocolOutputLimit+1)}
			_, err := provider.decode(context.Background())
			if err == nil || err.Error() != test.want {
				t.Fatalf("decode error = %v, want %q", err, test.want)
			}
		})
	}

	base := Response{Version: 1, Status: "ready", Instance: "db-1", Error: "x"}
	baseJSON, err := json.Marshal(base)
	if err != nil {
		t.Fatal(err)
	}
	fillerLength := protocolOutputLimit - 1 - (len(baseJSON) - 1)
	base.Error = strings.Repeat("x", fillerLength)
	exactJSON, err := json.Marshal(base)
	if err != nil {
		t.Fatal(err)
	}
	exactLine := append(exactJSON, '\n')
	if len(exactLine) != protocolOutputLimit {
		t.Fatalf("exact line length = %d", len(exactLine))
	}
	provider := &instance{stdout: bufio.NewReaderSize(bytes.NewReader(exactLine), protocolOutputLimit+1)}
	response, err := provider.decode(context.Background())
	if err != nil || response.Error != base.Error {
		t.Fatalf("exact-limit decode = (%d byte error, %v)", len(response.Error), err)
	}

	overLimit := append(slices.Clone(exactJSON), 'x', '\n')
	provider = &instance{stdout: bufio.NewReaderSize(bytes.NewReader(overLimit), protocolOutputLimit+1)}
	if _, err := provider.decode(context.Background()); err == nil || err.Error() != "goatest: resource response exceeded output limit" {
		t.Fatalf("over-limit decode error = %v", err)
	}
}

func TestDecodeHonoursContextCancellation(t *testing.T) {
	reader, writer := io.Pipe()
	t.Cleanup(func() { _ = reader.Close(); _ = writer.Close() })
	provider := &instance{stdout: bufio.NewReader(reader)}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := provider.decode(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("decode error = %v", err)
	}
}

func acquireInternal(manager *Manager, capability string) (*Lease, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	return manager.Acquire(ctx, capability)
}

func TestStartValidatesCommandAndDefaultTimeout(t *testing.T) {
	for _, command := range [][]string{nil, {}, {" \t"}} {
		_, err := start(context.Background(), "db", "request-1", Spec{Command: command})
		const want = `goatest: resource "db" has no provider command`
		if err == nil || err.Error() != want {
			t.Fatalf("start(%q) error = %v, want %q", command, err, want)
		}
	}
	for _, timeout := range []time.Duration{0, -time.Second, 7 * time.Second} {
		t.Run(timeout.String(), func(t *testing.T) {
			preserveResourceProcessSeams(t)
			stdin, _, _ := installFakeResourceStart(t, readyLine(t, Response{Version: 1, Status: "ready", Instance: "db-1"}))
			provider, err := start(context.Background(), "db", "request-1", Spec{Command: []string{"provider"}, Timeout: timeout})
			if err != nil {
				t.Fatal(err)
			}
			wantTimeout := timeout
			if wantTimeout <= 0 {
				wantTimeout = 30 * time.Second
			}
			if provider.timeout != wantTimeout {
				t.Fatalf("timeout = %s, want %s", provider.timeout, wantTimeout)
			}
			var request Request
			if err := json.NewDecoder(bytes.NewReader(stdin.Bytes())).Decode(&request); err != nil {
				t.Fatal(err)
			}
			if request != (Request{Version: 1, Action: "start", Capability: "db", RequestID: "request-1"}) {
				t.Fatalf("start request = %+v", request)
			}
		})
	}
}

func TestStartPropagatesPipeProcessAndEncodeFailures(t *testing.T) {
	t.Run("stdin", func(t *testing.T) {
		preserveResourceProcessSeams(t)
		sentinel := errors.New("stdin pipe failed")
		resourceStdinPipe = func(*exec.Cmd) (io.WriteCloser, error) { return nil, sentinel }
		_, err := start(context.Background(), "db", "request-1", Spec{Command: []string{"provider"}, Timeout: time.Second})
		if !errors.Is(err, sentinel) {
			t.Fatalf("start error = %v", err)
		}
	})
	t.Run("stdout closes stdin", func(t *testing.T) {
		preserveResourceProcessSeams(t)
		stdin := &trackingWriteCloser{}
		sentinel := errors.New("stdout pipe failed")
		resourceStdinPipe = func(*exec.Cmd) (io.WriteCloser, error) { return stdin, nil }
		resourceStdoutPipe = func(*exec.Cmd) (io.ReadCloser, error) { return nil, sentinel }
		_, err := start(context.Background(), "db", "request-1", Spec{Command: []string{"provider"}, Timeout: time.Second})
		if !errors.Is(err, sentinel) || !stdin.closed {
			t.Fatalf("start error = %v, stdin closed = %t", err, stdin.closed)
		}
	})
	t.Run("process start closes stdin", func(t *testing.T) {
		preserveResourceProcessSeams(t)
		stdin := &trackingWriteCloser{}
		sentinel := errors.New("process start failed")
		resourceStdinPipe = func(*exec.Cmd) (io.WriteCloser, error) { return stdin, nil }
		resourceStdoutPipe = func(*exec.Cmd) (io.ReadCloser, error) { return io.NopCloser(strings.NewReader("")), nil }
		startResourceProcess = func(*exec.Cmd) (resourceProcessTree, error) { return nil, sentinel }
		_, err := start(context.Background(), "db", "request-1", Spec{Command: []string{"provider"}, Timeout: time.Second})
		if !errors.Is(err, sentinel) || !stdin.closed || !strings.HasPrefix(err.Error(), `goatest: start resource "db": `) {
			t.Fatalf("start error = %v, stdin closed = %t", err, stdin.closed)
		}
	})
	t.Run("encode aborts process", func(t *testing.T) {
		preserveResourceProcessSeams(t)
		_, tree, _ := installFakeResourceStart(t, nil)
		sentinel := errors.New("encode start failed")
		encodeResourceRequest = func(*json.Encoder, Request) error { return sentinel }
		_, err := start(context.Background(), "db", "request-1", Spec{Command: []string{"provider"}, Timeout: time.Second})
		if !errors.Is(err, sentinel) || tree.kills != 1 || tree.closes != 1 {
			t.Fatalf("start error = %v, tree = %+v", err, tree)
		}
	})
}

func TestStartRejectsEachInvalidReadyResponseAndMalformedProtocol(t *testing.T) {
	for _, test := range []struct {
		name     string
		response []byte
		want     string
	}{
		{name: "wrong version", response: readyLine(t, Response{Version: 2, Status: "ready", Instance: "db-1"}), want: "returned invalid ready response"},
		{name: "wrong status", response: readyLine(t, Response{Version: 1, Status: "waiting", Instance: "db-1"}), want: "returned invalid ready response"},
		{name: "missing instance", response: readyLine(t, Response{Version: 1, Status: "ready"}), want: "returned invalid ready response"},
		{name: "provider error", response: readyLine(t, Response{Version: 1, Status: "ready", Instance: "db-1", Error: "unavailable"}), want: "returned invalid ready response"},
		{name: "malformed", response: []byte("{\n"), want: "readiness: unexpected EOF"},
	} {
		t.Run(test.name, func(t *testing.T) {
			preserveResourceProcessSeams(t)
			_, tree, _ := installFakeResourceStart(t, test.response)
			provider, err := start(context.Background(), "db", "request-1", Spec{Command: []string{"provider"}, Timeout: time.Second})
			if err == nil || provider != nil || !strings.Contains(err.Error(), test.want) || tree.kills != 1 || tree.closes != 1 {
				t.Fatalf("start = (%v, %v), tree = %+v", provider, err, tree)
			}
		})
	}
}

func TestStartAcceptsReadyResponseAtExactProtocolLimit(t *testing.T) {
	preserveResourceProcessSeams(t)
	response := Response{Version: 1, Status: "ready", Instance: "db-1", Environment: map[string]string{"PAD": "x"}}
	probe, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	response.Environment["PAD"] = strings.Repeat("x", protocolOutputLimit-1-(len(probe)-1))
	line := readyLine(t, response)
	if len(line) != protocolOutputLimit {
		t.Fatalf("line length = %d", len(line))
	}
	_, _, _ = installFakeResourceStart(t, line)
	// The line is 1 MiB and decoding it under -race on a loaded runner has taken over a second; the readiness timeout is not what this test measures.
	provider, err := start(context.Background(), "db", "request-1", Spec{Command: []string{"provider"}, Timeout: time.Minute})
	if err != nil {
		t.Fatalf("start error = %v", err)
	}
	if len(provider.environment["PAD"]) != len(response.Environment["PAD"]) {
		t.Fatalf("PAD length = %d, want %d", len(provider.environment["PAD"]), len(response.Environment["PAD"]))
	}
}

func TestAbortJoinsDiagnosticsWaitAndCloseFailures(t *testing.T) {
	for _, test := range []struct {
		name     string
		waitErr  error
		wantWait bool
	}{
		{name: "wait failure", waitErr: errors.New("wait failed"), wantWait: true},
		{name: "process already done", waitErr: os.ErrProcessDone},
		{name: "nil wait"},
	} {
		t.Run(test.name, func(t *testing.T) {
			preserveResourceProcessSeams(t)
			closeFailure := errors.New("close failed")
			cause := errors.New("readiness failed")
			stdin := &trackingWriteCloser{}
			tree := &fakeResourceTree{closeErr: closeFailure}
			stderr := &limitedBuffer{remaining: 64}
			_, _ = stderr.Write([]byte(" provider diagnostic "))
			waitResourceProcess = func(*exec.Cmd) error { return test.waitErr }
			provider := &instance{cmd: exec.Command("provider"), tree: tree, stdin: stdin, stderr: stderr, environment: map[string]string{"A": "B"}}
			err := provider.abort(cause)
			if !errors.Is(err, cause) || !errors.Is(err, closeFailure) || errors.Is(err, os.ErrProcessDone) || (test.wantWait && !errors.Is(err, test.waitErr)) {
				t.Fatalf("abort error = %v", err)
			}
			if !strings.Contains(err.Error(), "provider diagnostic") || !stdin.closed || tree.kills != 1 || tree.closes != 1 || provider.environment != nil {
				t.Fatalf("abort cleanup: error=%v stdin=%t tree=%+v env=%v", err, stdin.closed, tree, provider.environment)
			}
		})
	}
}

func TestStopCoversSuccessInvalidResponsesExitAndTimeout(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		preserveResourceProcessSeams(t)
		provider, stdin, tree := fakeStoppingResource(t, Response{Version: 1, Status: "stopped", Instance: "db-1"})
		if err := provider.stop(); err != nil {
			t.Fatal(err)
		}
		if !stdin.closed || tree.kills != 0 || tree.closes != 1 || provider.environment != nil {
			t.Fatalf("stop cleanup: stdin=%t tree=%+v env=%v", stdin.closed, tree, provider.environment)
		}
		var request Request
		if err := json.NewDecoder(bytes.NewReader(stdin.Bytes())).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if request != (Request{Version: 1, Action: "stop", Capability: "db", RequestID: "request-1", Instance: "db-1"}) {
			t.Fatalf("stop request = %+v", request)
		}
	})
	for _, test := range []struct {
		name     string
		response Response
	}{
		{name: "wrong version", response: Response{Version: 2, Status: "stopped", Instance: "db-1"}},
		{name: "wrong status", response: Response{Version: 1, Status: "ready", Instance: "db-1"}},
		{name: "wrong instance", response: Response{Version: 1, Status: "stopped", Instance: "other"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			preserveResourceProcessSeams(t)
			provider, _, tree := fakeStoppingResource(t, test.response)
			err := provider.stop()
			if err == nil || !strings.Contains(err.Error(), "invalid stopped response") || tree.kills != 1 || tree.closes != 1 {
				t.Fatalf("stop error = %v, tree = %+v", err, tree)
			}
		})
	}
	t.Run("encode failure is stable", func(t *testing.T) {
		preserveResourceProcessSeams(t)
		provider, _, tree := fakeStoppingResource(t, Response{})
		sentinel := errors.New("encode stop failed")
		calls := 0
		encodeResourceRequest = func(*json.Encoder, Request) error { calls++; return sentinel }
		first := provider.stop()
		second := provider.stop()
		if !errors.Is(first, sentinel) || !errors.Is(second, sentinel) || calls != 1 || tree.kills != 1 || tree.closes != 1 {
			t.Fatalf("stop errors = %v / %v, calls=%d tree=%+v", first, second, calls, tree)
		}
	})
	t.Run("decode failure", func(t *testing.T) {
		preserveResourceProcessSeams(t)
		provider, _, tree := fakeStoppingResource(t, Response{})
		provider.stdout = bufio.NewReader(strings.NewReader("{\n"))
		err := provider.stop()
		if err == nil || !strings.Contains(err.Error(), "unexpected EOF") || tree.kills != 1 || tree.closes != 1 {
			t.Fatalf("stop error = %v, tree=%+v", err, tree)
		}
	})
	t.Run("wait failure", func(t *testing.T) {
		preserveResourceProcessSeams(t)
		provider, _, tree := fakeStoppingResource(t, Response{Version: 1, Status: "stopped", Instance: "db-1"})
		sentinel := errors.New("wait failed")
		waitResourceProcess = func(*exec.Cmd) error { return sentinel }
		err := provider.stop()
		if !errors.Is(err, sentinel) || tree.closes != 1 {
			t.Fatalf("stop error = %v, tree=%+v", err, tree)
		}
	})
	t.Run("close failure", func(t *testing.T) {
		preserveResourceProcessSeams(t)
		provider, _, tree := fakeStoppingResource(t, Response{Version: 1, Status: "stopped", Instance: "db-1"})
		sentinel := errors.New("close failed")
		tree.closeErr = sentinel
		err := provider.stop()
		if !errors.Is(err, sentinel) || tree.closes != 1 {
			t.Fatalf("stop error = %v, tree=%+v", err, tree)
		}
	})
	t.Run("exit timeout", func(t *testing.T) {
		preserveResourceProcessSeams(t)
		provider, _, tree := fakeStoppingResource(t, Response{Version: 1, Status: "stopped", Instance: "db-1"})
		provider.timeout = 20 * time.Millisecond
		unblock := make(chan struct{})
		waitDone := make(chan struct{})
		waitResourceProcess = func(*exec.Cmd) error {
			defer close(waitDone)
			select {
			case <-unblock:
				return nil
			case <-time.After(250 * time.Millisecond):
				return errors.New("wait fixture deadline")
			}
		}
		err := provider.stop()
		close(unblock)
		select {
		case <-waitDone:
		case <-time.After(time.Second):
			t.Fatal("wait goroutine did not finish")
		}
		if err == nil || !strings.Contains(err.Error(), "stop timeout") || tree.kills != 1 || tree.closes != 1 {
			t.Fatalf("stop error = %v, tree=%+v", err, tree)
		}
	})
}

type trackingWriteCloser struct {
	bytes.Buffer
	closed bool
}

func (writer *trackingWriteCloser) Close() error {
	writer.closed = true
	return nil
}

type fakeResourceTree struct {
	kills    int
	closes   int
	killErr  error
	closeErr error
}

func (tree *fakeResourceTree) Kill() error {
	tree.kills++
	return tree.killErr
}

func (tree *fakeResourceTree) Close() error {
	tree.closes++
	return tree.closeErr
}

func preserveResourceProcessSeams(t *testing.T) {
	t.Helper()
	stdinPipe := resourceStdinPipe
	stdoutPipe := resourceStdoutPipe
	startProcess := startResourceProcess
	encodeRequest := encodeResourceRequest
	waitProcess := waitResourceProcess
	t.Cleanup(func() {
		resourceStdinPipe = stdinPipe
		resourceStdoutPipe = stdoutPipe
		startResourceProcess = startProcess
		encodeResourceRequest = encodeRequest
		waitResourceProcess = waitProcess
	})
}

func installFakeResourceStart(t *testing.T, output []byte) (*trackingWriteCloser, *fakeResourceTree, *exec.Cmd) {
	t.Helper()
	stdin := &trackingWriteCloser{}
	tree := &fakeResourceTree{}
	resourceStdinPipe = func(*exec.Cmd) (io.WriteCloser, error) { return stdin, nil }
	resourceStdoutPipe = func(*exec.Cmd) (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(output)), nil
	}
	startResourceProcess = func(*exec.Cmd) (resourceProcessTree, error) { return tree, nil }
	waitResourceProcess = func(*exec.Cmd) error { return nil }
	return stdin, tree, exec.Command("provider")
}

func fakeStoppingResource(t *testing.T, response Response) (*instance, *trackingWriteCloser, *fakeResourceTree) {
	t.Helper()
	stdin := &trackingWriteCloser{}
	tree := &fakeResourceTree{}
	waitResourceProcess = func(*exec.Cmd) error { return nil }
	return &instance{
		capability: "db", requestID: "request-1", instanceID: "db-1", timeout: time.Second,
		cmd: exec.Command("provider"), tree: tree, stdin: stdin, encoder: json.NewEncoder(stdin),
		stdout: bufio.NewReader(bytes.NewReader(readyLine(t, response))), stderr: &limitedBuffer{remaining: 64},
		environment: map[string]string{"DATABASE_URL": "local"},
	}, stdin, tree
}

func readyLine(t *testing.T, response Response) []byte {
	t.Helper()
	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	return append(encoded, '\n')
}
