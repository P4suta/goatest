//go:build integration

// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package app_test

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/P4suta/goatest/internal/app"
	"github.com/P4suta/goatest/internal/cli"
	"github.com/P4suta/goatest/internal/testkit"
	"github.com/P4suta/goatest/internal/trace"
)

func TestKeepTempLeavesTheBaselineScratchOfARealRunWhereItSaysItDid(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name string
		args []string
		kept bool
	}{
		{name: "removed by default"},
		{name: "kept on request", args: []string{"--keep-temp"}, kept: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			repository := testkit.NewRepo(t).BoundaryFixture().Git()
			directory := filepath.Join(t.TempDir(), "trace")
			service := app.Service{
				Root: repository.Root(), GoBinary: testkit.GoBinary(t),

				TempDirectory: t.TempDir(), Environment: os.Environ(),
			}
			var stdout, stderr bytes.Buffer
			arguments := append([]string{"verify", "--trace=" + directory}, test.args...)
			if exit := cli.Run(t.Context(), arguments, &stdout, &stderr, service); exit != cli.ExitAssured {
				t.Fatalf("verify exit = %d\nstdout: %s\nstderr: %s", exit, stdout.String(), stderr.String())
			}
			var scratch []string
			for _, event := range traceOfType(readTrace(t, traceRun(t, directory)), trace.TypeArtifact) {
				if event.Artifact.Kind == "baseline-scratch" {
					scratch = append(scratch, event.Artifact.Path)
				}
			}
			if !test.kept {
				if len(scratch) != 0 {
					t.Fatalf("a run that kept nothing recorded %v", scratch)
				}
				return
			}
			if len(scratch) != 1 {
				t.Fatalf("recorded scratch directories = %v, want the one the round made", scratch)
			}
			if info, err := os.Stat(scratch[0]); err != nil || !info.IsDir() {
				t.Fatalf("kept scratch %s = %v", scratch[0], err)
			}
		})
	}
}
