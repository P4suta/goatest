// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package goatest_test

import (
	"os"
	"strings"
	"testing"
)

func TestDogfoodTaskRunsBuiltCLIWithoutGoRunWrapper(t *testing.T) {
	data, err := os.ReadFile("mise.toml")
	if err != nil {
		t.Fatal(err)
	}
	const marker = "[tasks.dogfood]"
	parts := strings.SplitN(string(data), marker, 2)
	if len(parts) != 2 {
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
		"./dist/dogfood/goatest --ui=plain",
		"go build -o ./dist/dogfood/goatest.exe ./cmd/goatest",
		`.\dist\dogfood\goatest.exe --ui=plain`,
	} {
		if !strings.Contains(task, required) {
			t.Errorf("dogfood task omitted %q", required)
		}
	}
}
