// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package testkit

import (
	"context"
	"fmt"
	"slices"
	"sync"

	gomutants "github.com/P4suta/go-mutants"
)

type ScriptedSession struct {
	mutex         sync.Mutex
	catalog       gomutants.Catalog
	rules         []*MutantRule
	requests      []gomutants.ExecRequest
	probeRules    []*ProbeRule
	probeRequests []gomutants.ProbeRequest

	controlRules    []*ControlRule
	controlRequests []gomutants.ControlRequest
}

type MutantRule struct {
	mutex   *sync.Mutex
	mutant  string
	args    []string
	handler func(gomutants.ExecRequest) (gomutants.MutantResult, error)
}

func NewSession(catalog gomutants.Catalog) *ScriptedSession {
	return &ScriptedSession{catalog: cloneCatalog(catalog)}
}

func (session *ScriptedSession) On(mutantID string, args ...string) *MutantRule {
	rule := &MutantRule{
		mutex:  &session.mutex,
		mutant: mutantID,
		args:   slices.Clone(args),
		handler: func(gomutants.ExecRequest) (gomutants.MutantResult, error) {
			return gomutants.MutantResult{}, nil
		},
	}
	session.mutex.Lock()
	defer session.mutex.Unlock()
	session.rules = append(session.rules, rule)
	return rule
}

func (rule *MutantRule) Return(result gomutants.MutantResult) *MutantRule {
	scripted := cloneMutantResult(result)
	return rule.Do(func(gomutants.ExecRequest) (gomutants.MutantResult, error) {
		return cloneMutantResult(scripted), nil
	})
}

func (rule *MutantRule) Fail(err error) *MutantRule {
	return rule.Do(func(gomutants.ExecRequest) (gomutants.MutantResult, error) {
		return gomutants.MutantResult{}, err
	})
}

func (rule *MutantRule) Do(handler func(gomutants.ExecRequest) (gomutants.MutantResult, error)) *MutantRule {
	rule.mutex.Lock()
	defer rule.mutex.Unlock()
	rule.handler = handler
	return rule
}

func (session *ScriptedSession) Catalog() gomutants.Catalog {
	session.mutex.Lock()
	defer session.mutex.Unlock()
	return cloneCatalog(session.catalog)
}

func (session *ScriptedSession) Exec(_ context.Context, request gomutants.ExecRequest) (gomutants.MutantResult, error) {
	handler := session.route(request)
	if handler == nil {
		return gomutants.MutantResult{}, fmt.Errorf(
			"goatest: scripted session has no rule for mutant %q with arguments %q: %w",
			request.Mutant, request.Args, ErrNoRule)
	}
	return handler(request)
}

func (session *ScriptedSession) Requests() []gomutants.ExecRequest {
	session.mutex.Lock()
	defer session.mutex.Unlock()
	requests := make([]gomutants.ExecRequest, len(session.requests))
	for index, request := range session.requests {
		requests[index] = cloneExecRequest(request)
	}
	return requests
}

type ProbeRule struct {
	mutex   *sync.Mutex
	pkg     string
	args    []string
	handler func(gomutants.ProbeRequest) (gomutants.ProbeResult, error)
}

func (session *ScriptedSession) OnProbe(pkg string, args ...string) *ProbeRule {
	rule := &ProbeRule{
		mutex: &session.mutex,
		pkg:   pkg,
		args:  slices.Clone(args),
		handler: func(gomutants.ProbeRequest) (gomutants.ProbeResult, error) {
			return gomutants.ProbeResult{}, nil
		},
	}
	session.mutex.Lock()
	defer session.mutex.Unlock()
	session.probeRules = append(session.probeRules, rule)
	return rule
}

func (rule *ProbeRule) Return(result gomutants.ProbeResult) *ProbeRule {
	scripted := cloneProbeResult(result)
	return rule.Do(func(gomutants.ProbeRequest) (gomutants.ProbeResult, error) {
		return cloneProbeResult(scripted), nil
	})
}

func (rule *ProbeRule) Fail(err error) *ProbeRule {
	return rule.Do(func(gomutants.ProbeRequest) (gomutants.ProbeResult, error) {
		return gomutants.ProbeResult{}, err
	})
}

func (rule *ProbeRule) Do(handler func(gomutants.ProbeRequest) (gomutants.ProbeResult, error)) *ProbeRule {
	rule.mutex.Lock()
	defer rule.mutex.Unlock()
	rule.handler = handler
	return rule
}

func (session *ScriptedSession) Probe(_ context.Context, request gomutants.ProbeRequest) (gomutants.ProbeResult, error) {
	handler := session.routeProbe(request)
	if handler == nil {
		return gomutants.ProbeResult{Outcome: gomutants.ProbeUnavailable}, nil
	}
	return handler(request)
}

func (session *ScriptedSession) ProbeRequests() []gomutants.ProbeRequest {
	session.mutex.Lock()
	defer session.mutex.Unlock()
	requests := make([]gomutants.ProbeRequest, len(session.probeRequests))
	for index, request := range session.probeRequests {
		requests[index] = cloneProbeRequest(request)
	}
	return requests
}

func (session *ScriptedSession) routeProbe(request gomutants.ProbeRequest) func(gomutants.ProbeRequest) (gomutants.ProbeResult, error) {
	session.mutex.Lock()
	defer session.mutex.Unlock()
	session.probeRequests = append(session.probeRequests, cloneProbeRequest(request))
	var selected *ProbeRule
	for _, rule := range session.probeRules {
		if rule.pkg != request.Package || !hasPrefix(request.Args, rule.args) {
			continue
		}
		if selected == nil || len(rule.args) > len(selected.args) {
			selected = rule
		}
	}
	if selected == nil {
		return nil
	}
	return selected.handler
}

func (session *ScriptedSession) route(request gomutants.ExecRequest) func(gomutants.ExecRequest) (gomutants.MutantResult, error) {
	session.mutex.Lock()
	defer session.mutex.Unlock()
	session.requests = append(session.requests, cloneExecRequest(request))
	var selected *MutantRule
	for _, rule := range session.rules {
		if rule.mutant != request.Mutant || !hasPrefix(request.Args, rule.args) {
			continue
		}
		if selected == nil || len(rule.args) > len(selected.args) {
			selected = rule
		}
	}
	if selected == nil {
		return nil
	}
	return selected.handler
}

func cloneCatalog(catalog gomutants.Catalog) gomutants.Catalog {
	catalog.Mutants = slices.Clone(catalog.Mutants)
	catalog.Rejections = slices.Clone(catalog.Rejections)
	catalog.TestPackages = slices.Clone(catalog.TestPackages)
	return catalog
}

func cloneMutantResult(result gomutants.MutantResult) gomutants.MutantResult {
	result.Artifacts = slices.Clone(result.Artifacts)
	for index, artifact := range result.Artifacts {
		result.Artifacts[index].Data = slices.Clone(artifact.Data)
	}
	return result
}

func cloneExecRequest(request gomutants.ExecRequest) gomutants.ExecRequest {
	request.Args = slices.Clone(request.Args)
	request.Env = slices.Clone(request.Env)
	return request
}

func cloneProbeResult(result gomutants.ProbeResult) gomutants.ProbeResult {
	result.Infected = slices.Clone(result.Infected)
	return result
}

func cloneProbeRequest(request gomutants.ProbeRequest) gomutants.ProbeRequest {
	request.Args = slices.Clone(request.Args)
	request.Env = slices.Clone(request.Env)
	return request
}

// ControlRule is one scripted answer to a control of the original program.
type ControlRule struct {
	mutex   *sync.Mutex
	pkg     string
	args    []string
	handler func(gomutants.ControlRequest) (gomutants.ControlResult, error)
}

// OnControl scripts what the original program does for one package and set of
// arguments.
//
// A control is scripted the way a mutant execution and a probe are, and for the
// same reason: a test about what a runner concludes should be able to say what
// the engine answered, without building a repository and compiling it. The
// unmatched case is deliberately the same as Exec's - an error naming what was
// asked for - rather than Probe's, which invents an unavailable outcome. A
// control nobody scripted is a test that forgot to say whether the user's suite
// passes, and inventing an answer for it would decide the very thing the test is
// about.
func (session *ScriptedSession) OnControl(pkg string, args ...string) *ControlRule {
	rule := &ControlRule{
		mutex: &session.mutex,
		pkg:   pkg,
		args:  slices.Clone(args),
		handler: func(gomutants.ControlRequest) (gomutants.ControlResult, error) {
			return gomutants.ControlResult{}, nil
		},
	}
	session.mutex.Lock()
	defer session.mutex.Unlock()
	session.controlRules = append(session.controlRules, rule)
	return rule
}

// Return answers this control with a fixed result.
func (rule *ControlRule) Return(result gomutants.ControlResult) *ControlRule {
	scripted := cloneControlResult(result)
	return rule.Do(func(gomutants.ControlRequest) (gomutants.ControlResult, error) {
		return cloneControlResult(scripted), nil
	})
}

// Fail answers this control with an error, which is the engine failing to
// measure rather than the program failing its tests.
func (rule *ControlRule) Fail(err error) *ControlRule {
	return rule.Do(func(gomutants.ControlRequest) (gomutants.ControlResult, error) {
		return gomutants.ControlResult{}, err
	})
}

// Do answers this control with a handler.
func (rule *ControlRule) Do(handler func(gomutants.ControlRequest) (gomutants.ControlResult, error)) *ControlRule {
	rule.mutex.Lock()
	defer rule.mutex.Unlock()
	rule.handler = handler
	return rule
}

// Control runs the scripted original program.
func (session *ScriptedSession) Control(_ context.Context, request gomutants.ControlRequest) (gomutants.ControlResult, error) {
	handler := session.routeControl(request)
	if handler == nil {
		return gomutants.ControlResult{}, fmt.Errorf(
			"goatest: scripted session has no control for package %q with arguments %q: %w",
			request.Package, request.Args, ErrNoRule)
	}
	return handler(request)
}

// ControlRequests is every control this session was asked for, in order.
//
// It is what a test asserts on to show that a control was taken once per
// distinct request rather than once per mutant, which is the whole of the memo's
// contract.
func (session *ScriptedSession) ControlRequests() []gomutants.ControlRequest {
	session.mutex.Lock()
	defer session.mutex.Unlock()
	requests := make([]gomutants.ControlRequest, len(session.controlRequests))
	for index, request := range session.controlRequests {
		requests[index] = cloneControlRequest(request)
	}
	return requests
}

// routeControl records the request and finds the rule that answers it.
func (session *ScriptedSession) routeControl(request gomutants.ControlRequest) func(gomutants.ControlRequest) (gomutants.ControlResult, error) {
	session.mutex.Lock()
	defer session.mutex.Unlock()
	session.controlRequests = append(session.controlRequests, cloneControlRequest(request))
	for _, rule := range session.controlRules {
		if rule.pkg != request.Package {
			continue
		}
		if len(rule.args) != 0 && !slices.Equal(rule.args, request.Args) {
			continue
		}
		return rule.handler
	}
	return nil
}

func cloneControlResult(result gomutants.ControlResult) gomutants.ControlResult {
	result.Output = slices.Clone(result.Output)
	result.Binaries = slices.Clone(result.Binaries)
	result.ExecSeqs = slices.Clone(result.ExecSeqs)
	return result
}

func cloneControlRequest(request gomutants.ControlRequest) gomutants.ControlRequest {
	request.Args = slices.Clone(request.Args)
	request.Env = slices.Clone(request.Env)
	return request
}
