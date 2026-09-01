// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package assure_test

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/P4suta/goatest/internal/assure"
)

func TestGoMutantsEvidenceVersionMatchesPinnedModule(t *testing.T) {
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("test source path is unavailable")
	}
	contents, err := os.ReadFile(filepath.Join(filepath.Dir(source), "..", "..", "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	match := regexp.MustCompile(`(?m)^\s*github\.com/P4suta/go-mutants\s+(v\S+)\s*$`).FindSubmatch(contents)
	if len(match) != 2 {
		t.Fatal("go-mutants version pin is absent from go.mod")
	}
	pinned := strings.TrimSpace(string(match[1]))
	resolved, err := assure.GoMutantsVersion()
	if err != nil {
		t.Fatal(err)
	}
	if pinned != resolved {
		t.Fatalf("evidence version = %q, pinned module = %q", resolved, pinned)
	}
}
