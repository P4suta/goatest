// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	goanalysis "github.com/P4suta/goatest/internal/golang"
)

const profileSuffix = ".cover"

type targetEvidence struct {
	id           string
	covered      []goanalysis.FileCoverage
	instrumented []goanalysis.FileCoverage
}

type evidence struct {
	targets      map[string]targetEvidence
	instrumented []goanalysis.FileCoverage
}

func (recorded evidence) profileCounts() (targets, suites int) {
	for id := range recorded.targets {
		if strings.HasSuffix(id, ".suite") {
			suites++
			continue
		}
		targets++
	}
	return targets, suites
}

func readEvidence(directory, modulePath string) (evidence, error) {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return evidence{}, fmt.Errorf("read the profiles in %s: %w", directory, err)
	}
	recorded := evidence{targets: make(map[string]targetEvidence, len(entries))}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), profileSuffix) {
			continue
		}
		path := filepath.Join(directory, entry.Name())
		profile, err := os.ReadFile(path)
		if err != nil {
			return evidence{}, fmt.Errorf("read the profile %s: %w", path, err)
		}
		coverage, err := goanalysis.ParseCoverage(profile, modulePath)
		if err != nil {
			return evidence{}, fmt.Errorf("parse the profile %s: %w", path, err)
		}
		target := strings.TrimSuffix(entry.Name(), profileSuffix)
		recorded.targets[target] = targetEvidence{
			id: target, covered: coverage.Covered, instrumented: coverage.Instrumented,
		}
		recorded.instrumented = goanalysis.MergeFileCoverage(recorded.instrumented, coverage.Instrumented)
	}
	return recorded, nil
}

func (recorded evidence) coveredBy(target, path string) (goanalysis.FileCoverage, bool) {
	measured, known := recorded.targets[target]
	if !known {
		return goanalysis.FileCoverage{}, false
	}
	return goanalysis.FindFileCoverage(measured.covered, path)
}

func (recorded evidence) measured(target string) bool {
	_, known := recorded.targets[target]
	return known
}

func (recorded evidence) instrumentedBy(target, path string) goanalysis.FileCoverage {
	measured, known := recorded.targets[target]
	if !known {
		return goanalysis.FileCoverage{}
	}
	blocks, _ := goanalysis.FindFileCoverage(measured.instrumented, path)
	return blocks
}

func (recorded evidence) instrumentedAt(path string, line, column int) bool {
	return recorded.instrumentedIn(path).Contains(line, column)
}

func (recorded evidence) instrumentedIn(path string) goanalysis.FileCoverage {
	blocks, _ := goanalysis.FindFileCoverage(recorded.instrumented, path)
	return blocks
}
