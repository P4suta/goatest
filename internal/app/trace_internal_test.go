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
	// A repository named relatively and a trace directory named absolutely are
	// two paths no relative path connects. Nothing about such a pair says the
	// stream would land where the snapshot reads, and a comparison that could
	// not be made is not a reason to take a developer's trace away.
	got, err := Service{}.traceDirectory(filepath.Join("relative", "repository"), cli.Request{TraceDirectory: directory})
	if err != nil {
		t.Fatalf("traceDirectory = %v, want the directory it was given", err)
	}
	if got != directory {
		t.Fatalf("traceDirectory = %q, want %q", got, directory)
	}
}
