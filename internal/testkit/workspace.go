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

var ErrNoRule = errors.New("goatest: no scripted rule matches the request")

type ScriptedWorkspace struct {
	mutex sync.Mutex
	rules []*Rule
	calls []gomutants.Command
}

type Rule struct {
	mutex   *sync.Mutex
	prefix  []string
	handler func(gomutants.Command) (gomutants.CommandResult, error)
}

func NewWorkspace() *ScriptedWorkspace { return &ScriptedWorkspace{} }

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

func (rule *Rule) Return(result gomutants.CommandResult) *Rule {
	scripted := cloneCommandResult(result)
	return rule.Do(func(gomutants.Command) (gomutants.CommandResult, error) {
		return cloneCommandResult(scripted), nil
	})
}

func (rule *Rule) Fail(err error) *Rule {
	return rule.Do(func(gomutants.Command) (gomutants.CommandResult, error) {
		return gomutants.CommandResult{}, err
	})
}

func (rule *Rule) Do(handler func(gomutants.Command) (gomutants.CommandResult, error)) *Rule {
	rule.mutex.Lock()
	defer rule.mutex.Unlock()
	rule.handler = handler
	return rule
}

func (workspace *ScriptedWorkspace) Exec(_ context.Context, command gomutants.Command) (gomutants.CommandResult, error) {
	handler := workspace.route(command)
	if handler == nil {
		return gomutants.CommandResult{}, fmt.Errorf(
			"goatest: scripted workspace has no rule for command %q: %w", command.Argv, ErrNoRule)
	}
	return handler(command)
}

func (workspace *ScriptedWorkspace) Calls() []gomutants.Command {
	workspace.mutex.Lock()
	defer workspace.mutex.Unlock()
	calls := make([]gomutants.Command, len(workspace.calls))
	for index, call := range workspace.calls {
		calls[index] = cloneCommand(call)
	}
	return calls
}

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

func hasPrefix(values, prefix []string) bool {
	return len(prefix) <= len(values) && slices.Equal(values[:len(prefix)], prefix)
}
