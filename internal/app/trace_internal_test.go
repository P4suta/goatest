// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package app

import (
	"path/filepath"
	"testing"

	"github.com/P4suta/goatest/internal/cli"
)

func TestATraceDirectoryIsKeptWhereTheRepositoryCannotBeComparedWithIt(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()

	got, err := Service{}.traceDirectory(filepath.Join("relative", "repository"), cli.Request{TraceDirectory: directory})
	if err != nil {
		t.Fatalf("traceDirectory = %v, want the directory it was given", err)
	}
	if got != directory {
		t.Fatalf("traceDirectory = %q, want %q", got, directory)
	}
}
