// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package testkit_test

import (
	"errors"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	gomutants "github.com/P4suta/go-mutants"
	"github.com/P4suta/goatest/internal/assure"
	"github.com/P4suta/goatest/internal/testkit"
)

// Only this external test package may import internal/assure: testkit itself
// must satisfy CommandWorkspace structurally so that assure can depend on the
// harness without an import cycle.
var _ assure.CommandWorkspace = (*testkit.ScriptedWorkspace)(nil)

func TestScriptedWorkspaceDispatchesTheLongestMatchingPrefix(t *testing.T) {
	t.Parallel()
	general := gomutants.CommandResult{ExitCode: 1, Output: []byte("general")}
	specific := gomutants.CommandResult{ExitCode: 0, Output: []byte("specific")}
	for _, order := range []string{"general-first", "specific-first"} {
		t.Run(order, func(t *testing.T) {
			t.Parallel()
			workspace := testkit.NewWorkspace()
			if order == "general-first" {
				workspace.On("go").Return(general)
				workspace.On("go", "test").Return(specific)
			} else {
				workspace.On("go", "test").Return(specific)
				workspace.On("go").Return(general)
			}

			matched, err := workspace.Exec(t.Context(), gomutants.Command{Argv: []string{"go", "test", "./..."}})
			if err != nil || string(matched.Output) != "specific" {
				t.Fatalf("longest prefix result = %+v, err = %v", matched, err)
			}
			fallback, err := workspace.Exec(t.Context(), gomutants.Command{Argv: []string{"go", "build", "./..."}})
			if err != nil || string(fallback.Output) != "general" {
				t.Fatalf("shorter prefix result = %+v, err = %v", fallback, err)
			}
		})
	}
}

func TestScriptedWorkspacePrefersTheFirstRuleOnEqualPrefixes(t *testing.T) {
	t.Parallel()
	workspace := testkit.NewWorkspace()
	workspace.On("go", "test").Return(gomutants.CommandResult{ExitCode: 3})
	workspace.On("go", "test").Return(gomutants.CommandResult{ExitCode: 4})

	result, err := workspace.Exec(t.Context(), gomutants.Command{Argv: []string{"go", "test"}})
	if err != nil || result.ExitCode != 3 {
		t.Fatalf("result = %+v, err = %v", result, err)
	}
}

func TestScriptedWorkspaceEmptyPrefixMatchesEveryCommand(t *testing.T) {
	t.Parallel()
	workspace := testkit.NewWorkspace()
	workspace.On().Return(gomutants.CommandResult{Duration: 5 * time.Millisecond})

	result, err := workspace.Exec(t.Context(), gomutants.Command{Argv: []string{"anything", "at", "all"}})
	if err != nil || result.Duration != 5*time.Millisecond {
		t.Fatalf("catch-all result = %+v, err = %v", result, err)
	}
}

func TestScriptedWorkspaceRuleHandlerObservesTheCommand(t *testing.T) {
	t.Parallel()
	workspace := testkit.NewWorkspace()
	workspace.On("go", "test").Do(func(command gomutants.Command) (gomutants.CommandResult, error) {
		return gomutants.CommandResult{ExitCode: len(command.Argv), Output: []byte(command.Dir)}, nil
	})

	result, err := workspace.Exec(t.Context(), gomutants.Command{Argv: []string{"go", "test", "./..."}, Dir: "module"})
	if err != nil || result.ExitCode != 3 || string(result.Output) != "module" {
		t.Fatalf("handler result = %+v, err = %v", result, err)
	}
}

func TestScriptedWorkspaceReturnsScriptedFailures(t *testing.T) {
	t.Parallel()
	failure := errors.New("workspace is frozen")
	workspace := testkit.NewWorkspace()
	workspace.On("go").Fail(failure)

	result, err := workspace.Exec(t.Context(), gomutants.Command{Argv: []string{"go", "build"}})
	if !errors.Is(err, failure) {
		t.Fatalf("err = %v, want %v", err, failure)
	}
	if !reflect.DeepEqual(result, gomutants.CommandResult{}) {
		t.Errorf("failed Exec returned %+v, want the zero result", result)
	}
}

func TestScriptedWorkspaceFailsClosedOnUnscriptedCommands(t *testing.T) {
	t.Parallel()
	workspace := testkit.NewWorkspace()
	workspace.On("go", "test").Return(gomutants.CommandResult{})

	result, err := workspace.Exec(t.Context(), gomutants.Command{Argv: []string{"git", "status"}})
	if !errors.Is(err, testkit.ErrNoRule) {
		t.Fatalf("err = %v, want ErrNoRule", err)
	}
	if !reflect.DeepEqual(result, gomutants.CommandResult{}) {
		t.Errorf("unscripted Exec returned %+v, want the zero result", result)
	}
	for _, argument := range []string{"git", "status"} {
		if !strings.Contains(err.Error(), argument) {
			t.Errorf("error %q does not name argv element %q", err, argument)
		}
	}
	if calls := workspace.Calls(); len(calls) != 1 || !slices.Equal(calls[0].Argv, []string{"git", "status"}) {
		t.Errorf("unscripted call was not recorded: %+v", calls)
	}
}

func TestScriptedWorkspaceRecordsCallsInOrderAsACopy(t *testing.T) {
	t.Parallel()
	workspace := testkit.NewWorkspace()
	workspace.On("go").Return(gomutants.CommandResult{})
	for _, argument := range []string{"build", "test", "vet"} {
		if _, err := workspace.Exec(t.Context(), gomutants.Command{Argv: []string{"go", argument}, Dir: argument}); err != nil {
			t.Fatal(err)
		}
	}

	calls := workspace.Calls()
	if len(calls) != 3 {
		t.Fatalf("calls = %+v, want three", calls)
	}
	for index, argument := range []string{"build", "test", "vet"} {
		if !slices.Equal(calls[index].Argv, []string{"go", argument}) || calls[index].Dir != argument {
			t.Fatalf("call %d = %+v", index, calls[index])
		}
	}
	calls[0].Dir = "mutated"
	if again := workspace.Calls(); again[0].Dir != "build" {
		t.Error("Calls returned an aliased slice")
	}
}

func TestScriptedWorkspaceSupportsConcurrentExec(t *testing.T) {
	t.Parallel()
	const executions = 16
	workspace := testkit.NewWorkspace()
	workspace.On("go").Do(func(command gomutants.Command) (gomutants.CommandResult, error) {
		return gomutants.CommandResult{Output: []byte(command.Dir)}, nil
	})

	var group sync.WaitGroup
	for index := range executions {
		group.Add(1)
		go func() {
			defer group.Done()
			dir := strconv.Itoa(index)
			result, err := workspace.Exec(t.Context(), gomutants.Command{Argv: []string{"go", "test"}, Dir: dir})
			if err != nil || string(result.Output) != dir {
				t.Errorf("concurrent Exec %s = %+v, err = %v", dir, result, err)
			}
		}()
	}
	group.Wait()

	calls := workspace.Calls()
	if len(calls) != executions {
		t.Fatalf("calls = %d, want %d", len(calls), executions)
	}
	seen := make([]string, 0, executions)
	for _, call := range calls {
		seen = append(seen, call.Dir)
	}
	slices.Sort(seen)
	want := make([]string, 0, executions)
	for index := range executions {
		want = append(want, strconv.Itoa(index))
	}
	slices.Sort(want)
	if !slices.Equal(seen, want) {
		t.Fatalf("recorded calls = %q, want %q", seen, want)
	}
}
