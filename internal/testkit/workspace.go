// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package testkit

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"

	gomutants "github.com/P4suta/go-mutants"
)

// ErrNoRule reports that a scripted fake was asked for work no rule covers.
// The fakes fail closed for the same reason the runner does: an unscripted
// call is a defect in the test, not a case for a silent default.
var ErrNoRule = errors.New("goatest: no scripted rule matches the request")

// ScriptedWorkspace answers commands from a rule table instead of starting
// processes, and records every call for later assertions. It satisfies the
// assurance runner's command-workspace contract structurally so that testkit
// itself never imports the package under test.
type ScriptedWorkspace struct {
	mutex sync.Mutex
	rules []*Rule
	calls []gomutants.Command
}

// Rule is one scripted command response. A rule registered without a response
// answers with the zero result.
type Rule struct {
	mutex   *sync.Mutex
	prefix  []string
	handler func(gomutants.Command) (gomutants.CommandResult, error)
}

// NewWorkspace returns a workspace that refuses every command until a rule is
// registered.
func NewWorkspace() *ScriptedWorkspace { return &ScriptedWorkspace{} }

// On registers a rule for every command whose argv starts with argvPrefix; an
// empty prefix matches every command. The longest matching prefix wins
// whatever the registration order, and equal prefixes resolve to the rule
// registered first, so a specific rule may be added after a general one.
func (workspace *ScriptedWorkspace) On(argvPrefix ...string) *Rule {
	rule := &Rule{
		mutex:  &workspace.mutex,
		prefix: slices.Clone(argvPrefix),
		handler: func(gomutants.Command) (gomutants.CommandResult, error) {
			return gomutants.CommandResult{}, nil
		},
	}
	workspace.mutex.Lock()
	defer workspace.mutex.Unlock()
	workspace.rules = append(workspace.rules, rule)
	return rule
}

// Return answers every matching command with result. The result is copied when
// the rule is registered and again for every response, so that neither the
// caller that scripted it nor one that receives it shares the storage a later
// response reads.
func (rule *Rule) Return(result gomutants.CommandResult) *Rule {
	scripted := cloneCommandResult(result)
	return rule.Do(func(gomutants.Command) (gomutants.CommandResult, error) {
		return cloneCommandResult(scripted), nil
	})
}

// Fail answers every matching command with err and the zero result, modelling
// infrastructure that never produced an exit code.
func (rule *Rule) Fail(err error) *Rule {
	return rule.Do(func(gomutants.Command) (gomutants.CommandResult, error) {
		return gomutants.CommandResult{}, err
	})
}

// Do answers every matching command with handler, for the responses that
// depend on the command itself.
func (rule *Rule) Do(handler func(gomutants.Command) (gomutants.CommandResult, error)) *Rule {
	rule.mutex.Lock()
	defer rule.mutex.Unlock()
	rule.handler = handler
	return rule
}

// Exec records the command and answers it from the rule table. An unscripted
// command is still recorded, so that a failing test can report what the code
// under test actually attempted.
func (workspace *ScriptedWorkspace) Exec(_ context.Context, command gomutants.Command) (gomutants.CommandResult, error) {
	handler := workspace.route(command)
	if handler == nil {
		return gomutants.CommandResult{}, fmt.Errorf(
			"goatest: scripted workspace has no rule for command %q: %w", command.Argv, ErrNoRule)
	}
	return handler(command)
}

// Calls returns every recorded command in call order, detached from the
// workspace so that an assertion cannot corrupt the record.
func (workspace *ScriptedWorkspace) Calls() []gomutants.Command {
	workspace.mutex.Lock()
	defer workspace.mutex.Unlock()
	calls := make([]gomutants.Command, len(workspace.calls))
	for index, call := range workspace.calls {
		calls[index] = cloneCommand(call)
	}
	return calls
}

// route records one call and selects its handler under a single lock, so that
// concurrent executions can neither interleave the two nor observe a rule
// half-registered.
func (workspace *ScriptedWorkspace) route(command gomutants.Command) func(gomutants.Command) (gomutants.CommandResult, error) {
	workspace.mutex.Lock()
	defer workspace.mutex.Unlock()
	workspace.calls = append(workspace.calls, cloneCommand(command))
	var selected *Rule
	for _, rule := range workspace.rules {
		if !hasPrefix(command.Argv, rule.prefix) {
			continue
		}
		if selected == nil || len(rule.prefix) > len(selected.prefix) {
			selected = rule
		}
	}
	if selected == nil {
		return nil
	}
	return selected.handler
}

func cloneCommandResult(result gomutants.CommandResult) gomutants.CommandResult {
	result.Output = slices.Clone(result.Output)
	return result
}

func cloneCommand(command gomutants.Command) gomutants.Command {
	command.Argv = slices.Clone(command.Argv)
	command.Env = slices.Clone(command.Env)
	return command
}

// hasPrefix reports whether values starts with prefix. The empty prefix
// matches everything.
func hasPrefix(values, prefix []string) bool {
	return len(prefix) <= len(values) && slices.Equal(values[:len(prefix)], prefix)
}
