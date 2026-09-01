// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package app

import (
	"testing"
	"time"

	"github.com/P4suta/goatest/internal/report"
)

func TestABundleIsNamedForItsRun(t *testing.T) {
	t.Parallel()
	service := Service{
		Now:       func() time.Time { return time.Date(2026, 9, 1, 10, 11, 12, 0, time.UTC) },
		ProcessID: func() int { return 4242 },
	}
	const runID = "20260901T101112.000000000Z-a1b2c3d4e5f6"
	if name := service.diagnosticsName(report.Report{RunID: runID}); name != runID {
		t.Fatalf("bundle name = %q, want the run it diagnoses", name)
	}
	// A run may die before it has an identity, and a failure that early is the
	// one a developer most needs the bundle for. The moment and the process
	// name it instead, which is the name a recording of the same run takes.
	for _, id := range []string{"", ".", "..", "../escape", "run/id", "run\\id", "run id"} {
		if name := service.diagnosticsName(report.Report{RunID: id}); name != "20260901T101112Z-4242" {
			t.Fatalf("bundle name of run %q = %q", id, name)
		}
	}
}
