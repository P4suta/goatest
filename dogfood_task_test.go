// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package goatest_test

import (
	"os"
	"strings"
	"testing"
)

// TestDogfoodTaskRunsBuiltCLIWithoutGoRunWrapper covers both dogfood tasks.
//
// A `go run` wrapper can outlive Ctrl-C, which is why the full-scope task is
// pinned against one. The changeset task is pinned for the same reason and for
// a second: it is the one CI waits for, and a check that cannot be interrupted
// cleanly is a check that leaves a process behind on every cancelled run.
func TestDogfoodTaskRunsBuiltCLIWithoutGoRunWrapper(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile("mise.toml")
	if err != nil {
		t.Fatal(err)
	}
	const (
		marker           = "[tasks.dogfood]"
		dogfoodTaskParts = 2
	)
	parts := strings.SplitN(string(data), marker, dogfoodTaskParts)
	if len(parts) != dogfoodTaskParts {
		t.Fatal("mise.toml has no dogfood task")
	}
	task := parts[1]
	if next := strings.Index(task, "\n["); next >= 0 {
		task = task[:next]
	}
	if strings.Contains(task, "go run") {
		t.Fatal("dogfood uses a go run wrapper that can retain the CLI after Ctrl-C")
	}

	for _, required := range []string{
		"go build -o ./dist/dogfood/goatest ./cmd/goatest",
		"./dist/dogfood/goatest verify --ui=plain",
		"go build -o ./dist/dogfood/goatest.exe ./cmd/goatest",
		`.\dist\dogfood\goatest.exe verify --ui=plain`,
	} {
		if !strings.Contains(task, required) {
			t.Errorf("dogfood task omitted %q", required)
		}
	}

	changed := taskBody(t, string(data), "[tasks.dogfood-changed]")
	if strings.Contains(changed, "go run") {
		t.Error("dogfood-changed uses a go run wrapper that can retain the CLI after Ctrl-C")
	}
	for _, required := range []string{
		"./dist/dogfood/goatest verify --changed=origin/main --ui=plain",
		`.\dist\dogfood\goatest.exe verify --changed=origin/main --ui=plain`,
	} {
		if !strings.Contains(changed, required) {
			t.Errorf("dogfood-changed task omitted %q", required)
		}
	}
}

// taskBody reads one mise task, from its heading to the next one.
func taskBody(t *testing.T, document, heading string) string {
	t.Helper()
	const bodyParts = 2
	parts := strings.SplitN(document, heading, bodyParts)
	if len(parts) != bodyParts {
		t.Fatalf("mise.toml has no %s task", heading)
	}
	body := parts[1]
	if next := strings.Index(body, "\n["); next >= 0 {
		body = body[:next]
	}
	return body
}
