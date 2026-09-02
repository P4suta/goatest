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

// profileSuffix is what a run names a target's coverage profile with. The
// directory a run leaves holds more than profiles, so the suffix is what
// separates the evidence from the rest of the scratch.
const profileSuffix = ".cover"

// targetEvidence is what one target's baseline profile proves: the blocks that
// target actually executed, by module-relative file. A target whose baseline
// failed leaves no profile and therefore no evidence, which is a state of its
// own rather than a target that covered nothing.
type targetEvidence struct {
	id      string
	covered []goanalysis.FileCoverage
}

// evidence is the coverage half of a recorded run: what each measured target
// ran, and the union of every block the profiles instrumented whether it ran
// or not. The union is what tells a position no test reached from a position
// the toolchain never measured, which is the difference between a mutant
// nothing runs and a gap between the blocks cmd/cover cut.
type evidence struct {
	targets      map[string]targetEvidence
	instrumented []goanalysis.FileCoverage
}

// readEvidence reads every coverage profile of a run's temporary directory.
// The profiles are read in name order and the result is a lookup, so the
// audit never depends on the order a directory happens to be listed in.
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
		recorded.targets[target] = targetEvidence{id: target, covered: coverage.Covered}
		recorded.instrumented = goanalysis.MergeFileCoverage(recorded.instrumented, coverage.Instrumented)
	}
	return recorded, nil
}

// coveredBy returns the blocks one target ran in one file, and whether the
// target ran any of that file at all. A target that ran none of it is not a
// candidate for the file under any rule routing has ever used.
func (recorded evidence) coveredBy(target, path string) (goanalysis.FileCoverage, bool) {
	measured, known := recorded.targets[target]
	if !known {
		return goanalysis.FileCoverage{}, false
	}
	return goanalysis.FindFileCoverage(measured.covered, path)
}

// measured reports whether a target left a profile at all.
func (recorded evidence) measured(target string) bool {
	_, known := recorded.targets[target]
	return known
}

// instrumentedAt reports whether any profile instrumented a block containing
// the position, whether or not a test executed it.
func (recorded evidence) instrumentedAt(path string, line, column int) bool {
	blocks, _ := goanalysis.FindFileCoverage(recorded.instrumented, path)
	return blocks.Contains(line, column)
}
