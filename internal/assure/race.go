// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package assure

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	gomutants "github.com/P4suta/go-mutants"
	goanalysis "github.com/P4suta/goatest/internal/golang"
	"github.com/P4suta/goatest/internal/report"
)

type RaceResult struct {
	Evidence []report.Evidence
	Findings []report.Finding
}

func CollectRace(ctx context.Context, workspace CommandWorkspace, model goanalysis.Model, concurrentPackages []string, contract string, environment []string) (RaceResult, error) {
	packages := slices.Clone(concurrentPackages)
	if contract == "deep-v1" {
		packages = packages[:0]
		for _, pkg := range model.Packages {
			packages = append(packages, pkg.ImportPath)
		}
	}
	slices.Sort(packages)
	packages = slices.Compact(packages)
	if len(packages) == 0 {
		return RaceResult{Evidence: []report.Evidence{{Kind: "race", ID: "packages", Status: "not-applicable"}}}, nil
	}
	argv := []string{"go", "test", "-race", "-count=1"}
	argv = append(argv, packages...)
	run, err := workspace.Exec(ctx, gomutants.Command{
		Argv: argv, Env: slices.Clone(environment), Timeout: 30 * time.Minute,
	})
	if err != nil {
		return RaceResult{}, fmt.Errorf("goatest: race verification: %w", err)
	}
	if run.TimedOut {
		return RaceResult{}, fmt.Errorf("goatest: race verification timed out")
	}
	if run.ExitCode != 0 {
		output := string(run.Output)
		if strings.Contains(output, "WARNING: DATA RACE") {
			id := report.FindingID("data-race", strings.Join(packages, "\x00"))
			return RaceResult{Findings: []report.Finding{{
				ID: id, Kind: "data-race", Summary: "Go race detector reproduced a data race: " + summarize(run.Output),
				Replay: "go test -race -count=1 " + strings.Join(packages, " "),
			}}}, nil
		}
		return RaceResult{}, fmt.Errorf("goatest: race verification failed (exit=%d): %s", run.ExitCode, summarize(run.Output))
	}
	return RaceResult{Evidence: []report.Evidence{{
		Kind: "race", ID: strings.Join(packages, ","), Status: "passed",
	}}}, nil
}
