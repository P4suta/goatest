// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package assure_test

import (
	"strings"
	"testing"

	gomutants "github.com/P4suta/go-mutants"
	"github.com/P4suta/goatest/internal/assure"
	goanalysis "github.com/P4suta/goatest/internal/golang"
)

func TestCollectRaceSelectsConcurrentPackagesOrAllPackagesForDeep(t *testing.T) {
	model := goanalysis.Model{Packages: []goanalysis.Package{
		{ImportPath: "fixture/plain"}, {ImportPath: "fixture/worker"},
	}}
	for _, testCase := range []struct {
		contract string
		want     string
	}{
		{contract: "standard-v1", want: "go test -race -count=1 fixture/worker"},
		{contract: "deep-v1", want: "go test -race -count=1 fixture/plain fixture/worker"},
	} {
		workspace := &fakeWorkspace{run: func(gomutants.Command) gomutants.CommandResult { return gomutants.CommandResult{} }}
		result, err := assure.CollectRace(t.Context(), workspace, model, []string{"fixture/worker"}, testCase.contract, []string{"DB=ready"})
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Findings) != 0 || len(result.Evidence) != 1 || len(workspace.commands) != 1 {
			t.Fatalf("result=%+v commands=%+v", result, workspace.commands)
		}
		if got := strings.Join(workspace.commands[0].Argv, " "); got != testCase.want {
			t.Fatalf("command = %q, want %q", got, testCase.want)
		}
	}
}

func TestCollectRaceReturnsDefectOnlyForDetectedRace(t *testing.T) {
	workspace := &fakeWorkspace{run: func(gomutants.Command) gomutants.CommandResult {
		return gomutants.CommandResult{ExitCode: 1, Output: []byte("WARNING: DATA RACE\nRead at 0x00")}
	}}
	result, err := assure.CollectRace(t.Context(), workspace, goanalysis.Model{}, []string{"fixture/worker"}, "standard-v1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Findings) != 1 || result.Findings[0].Kind != "data-race" {
		t.Fatalf("result = %+v", result)
	}
}

func TestCollectRaceTreatsUnavailableRaceToolchainAsInfrastructureError(t *testing.T) {
	workspace := &fakeWorkspace{run: func(gomutants.Command) gomutants.CommandResult {
		return gomutants.CommandResult{ExitCode: 2, Output: []byte("go: -race requires cgo")}
	}}
	_, err := assure.CollectRace(t.Context(), workspace, goanalysis.Model{}, []string{"fixture/worker"}, "standard-v1", nil)
	if err == nil || !strings.Contains(err.Error(), "race verification failed") {
		t.Fatalf("error = %v", err)
	}
}
