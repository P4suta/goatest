// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package resource manages versioned external integration-resource providers.
package resource

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/P4suta/goatest/internal/processtree"
)

const protocolVersion = 1

type Request struct {
	Version    int    `json:"version"`
	Action     string `json:"action"`
	Capability string `json:"capability"`
	RequestID  string `json:"request_id"`
	Instance   string `json:"instance,omitempty"`
}

type Response struct {
	Version     int               `json:"version"`
	Status      string            `json:"status"`
	Instance    string            `json:"instance,omitempty"`
	Environment map[string]string `json:"environment,omitempty"`
	Error       string            `json:"error,omitempty"`
}

type Spec struct {
	Command   []string
	Timeout   time.Duration
	Shared    bool
	Exclusive bool
}

type Manager struct {
	mu     sync.Mutex
	specs  map[string]Spec
	shared map[string]*instance
	active map[*instance]bool
	nextID uint64
	closed bool
}

type Lease struct {
	manager  *Manager
	instance *instance
	env      []string
	once     sync.Once
	err      error
}

type instance struct {
	capability  string
	requestID   string
	instanceID  string
	timeout     time.Duration
	refs        int
	cmd         *exec.Cmd
	tree        *processtree.Tree
	stdin       io.WriteCloser
	encoder     *json.Encoder
	decoder     *json.Decoder
	stderr      *bytes.Buffer
	environment map[string]string
	stopOnce    sync.Once
	stopErr     error
}

func New(specs map[string]Spec) *Manager {
	cloned := make(map[string]Spec, len(specs))
	for name, spec := range specs {
		spec.Command = slices.Clone(spec.Command)
		cloned[name] = spec
	}
	return &Manager{specs: cloned, shared: make(map[string]*instance), active: make(map[*instance]bool)}
}

func (manager *Manager) Acquire(ctx context.Context, capability string) (*Lease, error) {
	if manager == nil {
		return nil, errors.New("goatest: nil resource manager")
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.closed {
		return nil, errors.New("goatest: resource manager is closed")
	}
	spec, ok := manager.specs[capability]
	if !ok {
		return nil, fmt.Errorf("goatest: no provider for capability %q", capability)
	}
	if spec.Shared {
		if existing := manager.shared[capability]; existing != nil {
			existing.refs++
			return &Lease{manager: manager, instance: existing, env: environment(existing)}, nil
		}
	}
	manager.nextID++
	requestID := fmt.Sprintf("resource-%06d", manager.nextID)
	started, err := start(ctx, capability, requestID, spec)
	if err != nil {
		return nil, err
	}
	started.refs = 1
	manager.active[started] = true
	if spec.Shared {
		manager.shared[capability] = started
	}
	return &Lease{manager: manager, instance: started, env: environment(started)}, nil
}

func (lease *Lease) Environment() []string {
	if lease == nil {
		return nil
	}
	return slices.Clone(lease.env)
}

func (lease *Lease) Release() error {
	if lease == nil {
		return nil
	}
	lease.once.Do(func() {
		lease.err = lease.manager.release(lease.instance)
	})
	return lease.err
}

func (manager *Manager) release(target *instance) error {
	manager.mu.Lock()
	if !manager.active[target] {
		manager.mu.Unlock()
		return nil
	}
	target.refs--
	if target.refs > 0 {
		manager.mu.Unlock()
		return nil
	}
	delete(manager.active, target)
	if manager.shared[target.capability] == target {
		delete(manager.shared, target.capability)
	}
	manager.mu.Unlock()
	return target.stop()
}

func (manager *Manager) Close() error {
	if manager == nil {
		return nil
	}
	manager.mu.Lock()
	if manager.closed {
		manager.mu.Unlock()
		return nil
	}
	manager.closed = true
	active := make([]*instance, 0, len(manager.active))
	for provider := range manager.active {
		active = append(active, provider)
	}
	clear(manager.active)
	clear(manager.shared)
	manager.mu.Unlock()
	var closeErr error
	for _, provider := range active {
		closeErr = errors.Join(closeErr, provider.stop())
	}
	return closeErr
}

func start(parent context.Context, capability, requestID string, spec Spec) (*instance, error) {
	if len(spec.Command) == 0 || strings.TrimSpace(spec.Command[0]) == "" {
		return nil, fmt.Errorf("goatest: resource %q has no provider command", capability)
	}
	timeout := spec.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	cmd := exec.Command(spec.Command[0], spec.Command[1:]...)
	cmd.Env = os.Environ()
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, err
	}
	stderr := &bytes.Buffer{}
	cmd.Stderr = stderr
	provider := &instance{
		capability: capability,
		requestID:  requestID,
		timeout:    timeout,
		cmd:        cmd,
		stdin:      stdin,
		encoder:    json.NewEncoder(stdin),
		decoder:    json.NewDecoder(stdout),
		stderr:     stderr,
	}
	provider.decoder.DisallowUnknownFields()
	tree, err := processtree.Start(cmd)
	if err != nil {
		_ = stdin.Close()
		return nil, fmt.Errorf("goatest: start resource %q: %w", capability, err)
	}
	provider.tree = tree
	if err := provider.encoder.Encode(Request{
		Version: protocolVersion, Action: "start", Capability: capability, RequestID: requestID,
	}); err != nil {
		return nil, provider.abort(fmt.Errorf("goatest: send resource start: %w", err))
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	response, err := provider.decode(ctx)
	if err != nil {
		return nil, provider.abort(fmt.Errorf("goatest: resource %q readiness: %w", capability, err))
	}
	if response.Version != protocolVersion || response.Status != "ready" || response.Instance == "" || response.Error != "" {
		return nil, provider.abort(fmt.Errorf("goatest: resource %q returned invalid ready response: version=%d status=%q instance=%q error=%q",
			capability, response.Version, response.Status, response.Instance, response.Error))
	}
	if err := validateEnvironment(response.Environment); err != nil {
		return nil, provider.abort(fmt.Errorf("goatest: resource %q environment: %w", capability, err))
	}
	provider.instanceID = response.Instance
	provider.environment = cloneEnvironment(response.Environment)
	return provider, nil
}

func cloneEnvironment(input map[string]string) map[string]string {
	result := make(map[string]string, len(input))
	for key, value := range input {
		result[key] = value
	}
	return result
}

func environment(provider *instance) []string {
	env := make([]string, 0, len(provider.environment))
	for key, value := range provider.environment {
		env = append(env, key+"="+value)
	}
	slices.Sort(env)
	return env
}

func validateEnvironment(environment map[string]string) error {
	for key, value := range environment {
		upper := strings.ToUpper(key)
		if key == "" || strings.ContainsAny(key, "=\x00") || strings.ContainsRune(value, 0) {
			return fmt.Errorf("invalid environment entry %q", key)
		}
		if strings.HasPrefix(upper, "GO_MUTANTS_") || strings.HasPrefix(upper, "GOATEST_") || upper == "TMP" || upper == "TEMP" || upper == "TMPDIR" {
			return fmt.Errorf("reserved environment key %q", key)
		}
	}
	return nil
}

func (provider *instance) decode(ctx context.Context) (Response, error) {
	type result struct {
		response Response
		err      error
	}
	resultChannel := make(chan result, 1)
	go func() {
		var response Response
		err := provider.decoder.Decode(&response)
		resultChannel <- result{response: response, err: err}
	}()
	select {
	case result := <-resultChannel:
		if result.err != nil {
			return Response{}, result.err
		}
		return result.response, nil
	case <-ctx.Done():
		return Response{}, ctx.Err()
	}
}

func (provider *instance) abort(cause error) error {
	_ = provider.stdin.Close()
	if provider.tree != nil {
		_ = provider.tree.Kill()
	} else if provider.cmd.Process != nil {
		_ = provider.cmd.Process.Kill()
	}
	waitErr := provider.cmd.Wait()
	var closeErr error
	if provider.tree != nil {
		closeErr = provider.tree.Close()
	}
	message := strings.TrimSpace(provider.stderr.String())
	provider.environment = nil
	if message != "" {
		cause = fmt.Errorf("%w: %s", cause, message)
	}
	if waitErr != nil && !errors.Is(waitErr, os.ErrProcessDone) {
		return errors.Join(cause, waitErr, closeErr)
	}
	return errors.Join(cause, closeErr)
}

func (provider *instance) stop() error {
	provider.stopOnce.Do(func() {
		defer func() { provider.environment = nil }()
		ctx, cancel := context.WithTimeout(context.Background(), provider.timeout)
		defer cancel()
		if err := provider.encoder.Encode(Request{
			Version: protocolVersion, Action: "stop", Capability: provider.capability,
			RequestID: provider.requestID, Instance: provider.instanceID,
		}); err != nil {
			provider.stopErr = provider.abort(fmt.Errorf("goatest: send resource stop: %w", err))
			return
		}
		response, err := provider.decode(ctx)
		if err != nil || response.Version != protocolVersion || response.Status != "stopped" || response.Instance != provider.instanceID {
			if err == nil {
				err = fmt.Errorf("invalid stopped response: version=%d status=%q instance=%q", response.Version, response.Status, response.Instance)
			}
			provider.stopErr = provider.abort(fmt.Errorf("goatest: stop resource %q: %w", provider.capability, err))
			return
		}
		_ = provider.stdin.Close()
		wait := make(chan error, 1)
		go func() { wait <- provider.cmd.Wait() }()
		select {
		case err := <-wait:
			if provider.tree != nil {
				err = errors.Join(err, provider.tree.Close())
			}
			if err != nil {
				provider.stopErr = fmt.Errorf("goatest: resource %q exit: %w", provider.capability, err)
			}
		case <-ctx.Done():
			if provider.tree != nil {
				_ = provider.tree.Kill()
				_ = provider.tree.Close()
			} else if provider.cmd.Process != nil {
				_ = provider.cmd.Process.Kill()
			}
			provider.stopErr = fmt.Errorf("goatest: resource %q stop timeout: %w", provider.capability, ctx.Err())
		}
	})
	return provider.stopErr
}
