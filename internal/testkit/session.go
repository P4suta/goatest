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

// ScriptedSession answers mutant executions from a routing table instead of
// compiling and running mutated binaries, and records every request. Like
// ScriptedWorkspace it satisfies the assurance runner's session contract
// structurally, so testkit stays free of an import cycle.
type ScriptedSession struct {
	mutex         sync.Mutex
	catalog       gomutants.Catalog
	rules         []*MutantRule
	requests      []gomutants.ExecRequest
	probeRules    []*ProbeRule
	probeRequests []gomutants.ProbeRequest
}

// MutantRule is one scripted mutant outcome. A rule registered without a
// response answers with the zero result.
type MutantRule struct {
	mutex   *sync.Mutex
	mutant  string
	args    []string
	handler func(gomutants.ExecRequest) (gomutants.MutantResult, error)
}

// NewSession returns a session serving an independent copy of catalog, so that
// a later edit of the caller's fixture cannot change what the fake reports.
func NewSession(catalog gomutants.Catalog) *ScriptedSession {
	return &ScriptedSession{catalog: cloneCatalog(catalog)}
}

// On registers a rule for mutantID, restricted to the requests whose arguments
// start with args. The longest matching argument prefix wins whatever the
// registration order, and equal prefixes resolve to the rule registered first,
// so one mutant can answer differently per target.
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

// Return answers every matching request with result. The result is copied when
// the rule is registered and again for every response, artifact contents
// included, so that neither the caller that scripted it nor one that receives
// it shares the storage a later response reads.
func (rule *MutantRule) Return(result gomutants.MutantResult) *MutantRule {
	scripted := cloneMutantResult(result)
	return rule.Do(func(gomutants.ExecRequest) (gomutants.MutantResult, error) {
		return cloneMutantResult(scripted), nil
	})
}

// Fail answers every matching request with err and the zero result, modelling
// an execution that produced no outcome at all.
func (rule *MutantRule) Fail(err error) *MutantRule {
	return rule.Do(func(gomutants.ExecRequest) (gomutants.MutantResult, error) {
		return gomutants.MutantResult{}, err
	})
}

// Do answers every matching request with handler, for the outcomes that depend
// on the request itself.
func (rule *MutantRule) Do(handler func(gomutants.ExecRequest) (gomutants.MutantResult, error)) *MutantRule {
	rule.mutex.Lock()
	defer rule.mutex.Unlock()
	rule.handler = handler
	return rule
}

// Catalog returns an independent copy of the scripted catalog on every call,
// matching the go-mutants contract the scheduler is written against.
func (session *ScriptedSession) Catalog() gomutants.Catalog {
	session.mutex.Lock()
	defer session.mutex.Unlock()
	return cloneCatalog(session.catalog)
}

// Exec records the request and answers it from the routing table. An
// unscripted request is still recorded, so that a failing test can report what
// the scheduler actually asked for.
func (session *ScriptedSession) Exec(_ context.Context, request gomutants.ExecRequest) (gomutants.MutantResult, error) {
	handler := session.route(request)
	if handler == nil {
		return gomutants.MutantResult{}, fmt.Errorf(
			"goatest: scripted session has no rule for mutant %q with arguments %q: %w",
			request.Mutant, request.Args, ErrNoRule)
	}
	return handler(request)
}

// Requests returns every recorded request in call order, detached from the
// session so that an assertion cannot corrupt the record.
func (session *ScriptedSession) Requests() []gomutants.ExecRequest {
	session.mutex.Lock()
	defer session.mutex.Unlock()
	requests := make([]gomutants.ExecRequest, len(session.requests))
	for index, request := range session.requests {
		requests[index] = cloneExecRequest(request)
	}
	return requests
}

// ProbeRule is one scripted probe outcome. A rule registered without a response
// answers with the zero result.
type ProbeRule struct {
	mutex   *sync.Mutex
	pkg     string
	args    []string
	handler func(gomutants.ProbeRequest) (gomutants.ProbeResult, error)
}

// OnProbe registers a rule for probes of pkg, restricted to the requests whose
// arguments start with args. The longest matching argument prefix wins whatever
// the registration order, and equal prefixes resolve to the rule registered
// first, so one package can answer differently per target.
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

// Return answers every matching request with result. The result is copied when
// the rule is registered and again for every response, infections included, so
// that neither the caller that scripted it nor one that receives it shares the
// storage a later response reads.
func (rule *ProbeRule) Return(result gomutants.ProbeResult) *ProbeRule {
	scripted := cloneProbeResult(result)
	return rule.Do(func(gomutants.ProbeRequest) (gomutants.ProbeResult, error) {
		return cloneProbeResult(scripted), nil
	})
}

// Fail answers every matching request with err and the zero result, modelling a
// pass that produced no measurement at all.
func (rule *ProbeRule) Fail(err error) *ProbeRule {
	return rule.Do(func(gomutants.ProbeRequest) (gomutants.ProbeResult, error) {
		return gomutants.ProbeResult{}, err
	})
}

// Do answers every matching request with handler, for the outcomes that depend
// on the request itself.
func (rule *ProbeRule) Do(handler func(gomutants.ProbeRequest) (gomutants.ProbeResult, error)) *ProbeRule {
	rule.mutex.Lock()
	defer rule.mutex.Unlock()
	rule.handler = handler
	return rule
}

// Probe records the request and answers it from the routing table. A request no
// rule covers answers that no probe runtime was available, which is the outcome
// that carries no facts: a fake that answered "measured, nothing infected"
// would hand a test the one reading that is unsound.
func (session *ScriptedSession) Probe(_ context.Context, request gomutants.ProbeRequest) (gomutants.ProbeResult, error) {
	handler := session.routeProbe(request)
	if handler == nil {
		return gomutants.ProbeResult{Outcome: gomutants.ProbeUnavailable}, nil
	}
	return handler(request)
}

// ProbeRequests returns every recorded probe request in call order, detached
// from the session so that an assertion cannot corrupt the record.
func (session *ScriptedSession) ProbeRequests() []gomutants.ProbeRequest {
	session.mutex.Lock()
	defer session.mutex.Unlock()
	requests := make([]gomutants.ProbeRequest, len(session.probeRequests))
	for index, request := range session.probeRequests {
		requests[index] = cloneProbeRequest(request)
	}
	return requests
}

// routeProbe records one probe request and selects its handler under a single
// lock, for the same reason route does.
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

// route records one request and selects its handler under a single lock, for
// the same reason ScriptedWorkspace.route does.
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

// cloneMutantResult detaches the result's artifacts, whose bytes a caller may
// legitimately read, edit, or write to disk.
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

// cloneProbeResult detaches the infections, which a caller may legitimately
// sort, filter, or hand on.
func cloneProbeResult(result gomutants.ProbeResult) gomutants.ProbeResult {
	result.Infected = slices.Clone(result.Infected)
	return result
}

func cloneProbeRequest(request gomutants.ProbeRequest) gomutants.ProbeRequest {
	request.Args = slices.Clone(request.Args)
	request.Env = slices.Clone(request.Env)
	return request
}
