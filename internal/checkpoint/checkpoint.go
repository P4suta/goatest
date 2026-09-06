// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package checkpoint

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/P4suta/goatest/internal/report"
)

const SchemaV1 = "assurance-checkpoint-v1"

type Target struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Kind         string   `json:"kind"`
	Package      string   `json:"package"`
	RelativeDir  string   `json:"relative_dir"`
	Path         string   `json:"path"`
	Line         int      `json:"line"`
	Capabilities []string `json:"capabilities"`
	Dependencies []string `json:"dependencies"`
}

type TargetEvidence struct {
	Target          Target    `json:"target"`
	CoveredFiles    []string  `json:"covered_files"`
	Environment     []string  `json:"environment"`
	DurationNS      int64     `json:"duration_ns"`
	WholeTree       bool      `json:"whole_tree,omitempty"`
	Coverage        *Coverage `json:"coverage,omitempty"`
	Probed          bool      `json:"probed"`
	ProbeDurationNS int64     `json:"probe_duration_ns"`
	Infected        []uint32  `json:"infected"`

	Instrumented *Coverage `json:"instrumented,omitempty"`
}

type Coverage struct {
	Files []FileCoverage `json:"files"`
}

type FileCoverage struct {
	Path   string          `json:"path"`
	Blocks []CoverageBlock `json:"blocks"`
}

type CoverageBlock struct {
	StartLine   int `json:"start_line"`
	StartColumn int `json:"start_column"`
	EndLine     int `json:"end_line"`
	EndColumn   int `json:"end_column"`
}

type BaselineTarget struct {
	ID        string                   `json:"id"`
	Executed  bool                     `json:"executed"`
	Skipped   bool                     `json:"skipped"`
	Evidence  []report.Evidence        `json:"evidence"`
	Findings  []report.Finding         `json:"findings"`
	Inventory report.TargetDisposition `json:"inventory"`
	Target    *TargetEvidence          `json:"target,omitempty"`
}

type Baseline struct {
	BuildVetComplete bool              `json:"build_vet_complete"`
	Complete         bool              `json:"complete"`
	Evidence         []report.Evidence `json:"evidence"`
	Findings         []report.Finding  `json:"findings"`
	Targets          []BaselineTarget  `json:"targets"`

	Suites  []BaselineSuite  `json:"suites,omitempty"`
	Routing *BaselineRouting `json:"routing,omitempty"`
}

type BaselineSuite struct {
	Package      string    `json:"package"`
	Measured     bool      `json:"measured"`
	Covered      *Coverage `json:"covered,omitempty"`
	Instrumented *Coverage `json:"instrumented,omitempty"`
	DurationNS   int64     `json:"duration_ns"`
	WholeTree    bool      `json:"whole_tree,omitempty"`
}

type BaselineRouting struct {
	Instrumented Coverage        `json:"instrumented"`
	Suites       []SuiteCoverage `json:"suites"`
}

type SuiteCoverage struct {
	Package      string   `json:"package"`
	Covered      Coverage `json:"covered"`
	Instrumented Coverage `json:"instrumented"`
	DurationNS   int64    `json:"duration_ns"`
	WholeTree    bool     `json:"whole_tree,omitempty"`
}

type Race struct {
	Complete bool              `json:"complete"`
	Packages []string          `json:"packages"`
	Evidence []report.Evidence `json:"evidence"`
	Findings []report.Finding  `json:"findings"`
}

type MutationResult struct {
	ID       string            `json:"id"`
	Evidence []report.Evidence `json:"evidence"`
	Findings []report.Finding  `json:"findings"`

	Provenance string `json:"provenance,omitempty"`
}

type Mutation struct {
	CatalogFingerprint string           `json:"catalog_fingerprint"`
	Complete           bool             `json:"complete"`
	Probe              *MutationProbe   `json:"probe,omitempty"`
	Results            []MutationResult `json:"results"`
}

type MutationProbe struct {
	IndexFingerprint string        `json:"index_fingerprint"`
	Targets          []TargetProbe `json:"targets"`
	Suites           []SuiteProbe  `json:"suites"`
}

type TargetProbe struct {
	ID         string   `json:"id"`
	Measured   bool     `json:"measured"`
	DurationNS int64    `json:"duration_ns"`
	Infected   []uint32 `json:"infected"`
}

type SuiteProbe struct {
	Package    string   `json:"package"`
	Measured   bool     `json:"measured"`
	DurationNS int64    `json:"duration_ns"`
	Infected   []uint32 `json:"infected"`
	WholeTree  bool     `json:"whole_tree,omitempty"`
}

type State struct {
	Schema      string    `json:"schema"`
	InputDigest string    `json:"input_digest"`
	Attempts    int       `json:"attempts"`
	Baseline    Baseline  `json:"baseline"`
	Race        *Race     `json:"race,omitempty"`
	Mutation    *Mutation `json:"mutation,omitempty"`
}

func Decode(data []byte) (State, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var state State
	if err := decoder.Decode(&state); err != nil {
		return State{}, fmt.Errorf("goatest: checkpoint decode: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return State{}, errors.New("goatest: checkpoint has trailing data")
	}
	if err := Validate(state); err != nil {
		return State{}, err
	}
	return state, nil
}

func Validate(state State) error {
	if state.Schema != SchemaV1 {
		return fmt.Errorf("goatest: checkpoint schema %q: expected %s", state.Schema, SchemaV1)
	}
	if !validSHA256(state.InputDigest) {
		return errors.New("goatest: checkpoint input digest is not a lowercase SHA-256")
	}
	if state.Attempts < 1 {
		return errors.New("goatest: checkpoint attempts must be positive")
	}
	seenTargets := make(map[string]struct{}, len(state.Baseline.Targets))
	instrumentationAnchors := make(map[string]string)
	for _, unit := range state.Baseline.Targets {
		if unit.ID == "" || unit.Inventory.ID != unit.ID || unit.Inventory.Name == "" || unit.Inventory.Status == "" || unit.Inventory.DurationMS < 0 {
			return errors.New("goatest: checkpoint baseline target has an invalid identity")
		}
		if unit.Executed == unit.Skipped {
			return fmt.Errorf("goatest: checkpoint baseline target %s is not completely classified", unit.ID)
		}
		if _, duplicate := seenTargets[unit.ID]; duplicate {
			return fmt.Errorf("goatest: checkpoint contains duplicate baseline target %s", unit.ID)
		}
		seenTargets[unit.ID] = struct{}{}
		if unit.Target != nil {
			if unit.Target.Target.ID != unit.ID || unit.Target.DurationNS < 0 || unit.Target.ProbeDurationNS < 0 ||
				!unit.Target.Probed && (unit.Target.ProbeDurationNS != 0 || len(unit.Target.Infected) != 0) ||
				!strictlyIncreasing(unit.Target.Infected) {
				return fmt.Errorf("goatest: checkpoint target evidence %s has an invalid identity", unit.ID)
			}
			if unit.Target.Coverage == nil {
				return fmt.Errorf("goatest: checkpoint target evidence %s has no exact coverage", unit.ID)
			}
			if err := validateCoverage(unit.Target.Coverage); err != nil {
				return fmt.Errorf("goatest: checkpoint target evidence %s has invalid coverage: %w", unit.ID, err)
			}
			if err := validateCoverage(unit.Target.Instrumented); err != nil {
				return fmt.Errorf("goatest: checkpoint target evidence %s has invalid instrumentation: %w", unit.ID, err)
			}
			if unit.Target.Instrumented != nil {
				if state.Baseline.Complete {
					return fmt.Errorf("goatest: completed checkpoint target %s duplicates routing instrumentation", unit.ID)
				}
				pkg := unit.Target.Target.Package
				if first := instrumentationAnchors[pkg]; first != "" {
					return fmt.Errorf("goatest: partial checkpoint package %s has instrumentation anchors %s and %s", pkg, first, unit.ID)
				}
				instrumentationAnchors[pkg] = unit.ID
			}
		}
	}
	seenBaselineSuites := make(map[string]struct{}, len(state.Baseline.Suites))
	for _, suite := range state.Baseline.Suites {
		if suite.Package == "" || suite.DurationNS < 0 ||
			!suite.Measured && (suite.Covered != nil || suite.Instrumented != nil || suite.DurationNS != 0 || suite.WholeTree) ||
			suite.Measured && (suite.Covered == nil || suite.Instrumented == nil) {
			return errors.New("goatest: checkpoint baseline suite has an invalid measurement")
		}
		if _, duplicate := seenBaselineSuites[suite.Package]; duplicate {
			return fmt.Errorf("goatest: checkpoint contains duplicate partial baseline suite %s", suite.Package)
		}
		seenBaselineSuites[suite.Package] = struct{}{}
		if err := validateCoverage(suite.Covered); err != nil {
			return fmt.Errorf("goatest: checkpoint partial baseline suite %s has invalid covered blocks: %w", suite.Package, err)
		}
		if err := validateCoverage(suite.Instrumented); err != nil {
			return fmt.Errorf("goatest: checkpoint partial baseline suite %s has invalid instrumentation: %w", suite.Package, err)
		}
	}
	if state.Baseline.Complete && len(state.Baseline.Suites) != 0 {
		return errors.New("goatest: completed checkpoint retains partial baseline suites")
	}
	if state.Baseline.Complete != (state.Baseline.Routing != nil) {
		return errors.New("goatest: checkpoint baseline completion and routing disagree")
	}
	if state.Baseline.Routing != nil {
		if err := validateCoverage(&state.Baseline.Routing.Instrumented); err != nil {
			return fmt.Errorf("goatest: checkpoint baseline instrumentation is invalid: %w", err)
		}
		seenSuites := make(map[string]struct{}, len(state.Baseline.Routing.Suites))
		for _, suite := range state.Baseline.Routing.Suites {
			if suite.Package == "" || suite.DurationNS < 0 {
				return errors.New("goatest: checkpoint baseline suite has an invalid identity")
			}
			if _, duplicate := seenSuites[suite.Package]; duplicate {
				return fmt.Errorf("goatest: checkpoint contains duplicate baseline suite %s", suite.Package)
			}
			seenSuites[suite.Package] = struct{}{}
			if err := validateCoverage(&suite.Covered); err != nil {
				return fmt.Errorf("goatest: checkpoint baseline suite %s has invalid covered blocks: %w", suite.Package, err)
			}
			if err := validateCoverage(&suite.Instrumented); err != nil {
				return fmt.Errorf("goatest: checkpoint baseline suite %s has invalid instrumentation: %w", suite.Package, err)
			}
		}
	}
	if state.Race != nil {
		seen := make(map[string]struct{}, len(state.Race.Packages))
		for _, pkg := range state.Race.Packages {
			if pkg == "" {
				return errors.New("goatest: checkpoint race package is empty")
			}
			if _, duplicate := seen[pkg]; duplicate {
				return fmt.Errorf("goatest: checkpoint contains duplicate race package %s", pkg)
			}
			seen[pkg] = struct{}{}
		}
	}
	if state.Mutation != nil {
		if !validSHA256(state.Mutation.CatalogFingerprint) {
			return errors.New("goatest: checkpoint mutation catalog fingerprint is invalid")
		}
		if err := validateMutationProbe(state.Mutation.Probe); err != nil {
			return fmt.Errorf("goatest: checkpoint mutation probe is invalid: %w", err)
		}
		seen := make(map[string]struct{}, len(state.Mutation.Results))
		for _, unit := range state.Mutation.Results {
			if unit.ID == "" || !terminalMutation(unit) {
				return errors.New("goatest: checkpoint mutation result is not terminal")
			}
			if _, duplicate := seen[unit.ID]; duplicate {
				return fmt.Errorf("goatest: checkpoint contains duplicate mutant %s", unit.ID)
			}
			seen[unit.ID] = struct{}{}
		}
	}
	return nil
}

func validateMutationProbe(probe *MutationProbe) error {
	if probe == nil {
		return nil
	}
	if !validSHA256(probe.IndexFingerprint) {
		return errors.New("index fingerprint is invalid")
	}
	seenTargets := make(map[string]struct{}, len(probe.Targets))
	for _, target := range probe.Targets {
		if target.ID == "" || target.DurationNS < 0 || !target.Measured && (target.DurationNS != 0 || len(target.Infected) != 0) {
			return errors.New("target has invalid measurement")
		}
		if _, duplicate := seenTargets[target.ID]; duplicate {
			return fmt.Errorf("duplicate target %s", target.ID)
		}
		seenTargets[target.ID] = struct{}{}
		if !strictlyIncreasing(target.Infected) {
			return fmt.Errorf("target %s infections are not ascending and distinct", target.ID)
		}
	}
	seenSuites := make(map[string]struct{}, len(probe.Suites))
	for _, suite := range probe.Suites {
		if suite.Package == "" || suite.DurationNS < 0 || !suite.Measured && (suite.DurationNS != 0 || len(suite.Infected) != 0 || suite.WholeTree) {
			return errors.New("suite has invalid measurement")
		}
		if _, duplicate := seenSuites[suite.Package]; duplicate {
			return fmt.Errorf("duplicate suite %s", suite.Package)
		}
		seenSuites[suite.Package] = struct{}{}
		if !strictlyIncreasing(suite.Infected) {
			return fmt.Errorf("suite %s infections are not ascending and distinct", suite.Package)
		}
	}
	return nil
}

func strictlyIncreasing(values []uint32) bool {
	for index := 1; index < len(values); index++ {
		if values[index-1] >= values[index] {
			return false
		}
	}
	return true
}

func validateCoverage(coverage *Coverage) error {
	if coverage == nil {
		return nil
	}
	for _, file := range coverage.Files {
		if file.Path == "" {
			return errors.New("empty file path")
		}
		for _, block := range file.Blocks {
			if block.StartLine < 1 || block.StartColumn < 1 || block.EndLine < 1 || block.EndColumn < 1 {
				return errors.New("coverage positions must be positive")
			}
			if block.EndLine < block.StartLine || block.EndLine == block.StartLine && block.EndColumn < block.StartColumn {
				return errors.New("coverage block ends before it starts")
			}
		}
	}
	return nil
}

func terminalMutation(unit MutationResult) bool {
	for _, item := range unit.Evidence {
		if item.Kind == "mutation" && item.ID == unit.ID {
			switch item.Status {
			case "killed", "compile-rejected", "accepted":
				return true
			}
		}
	}
	for _, finding := range unit.Findings {
		if finding.MutantID == unit.ID {
			return true
		}
	}
	return false
}

func validSHA256(value string) bool {
	if len(value) != hex.EncodedLen(sha256.Size) {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			if character < 'a' || character > 'f' {
				return false
			}
		}
	}
	return true
}

func JSON(input State) []byte {
	state := canonical(input)
	data, _ := json.MarshalIndent(state, "", "  ")
	return append(data, '\n')
}

func canonical(input State) State {
	result := input
	result.Baseline.Evidence = slices.Clone(input.Baseline.Evidence)
	result.Baseline.Findings = slices.Clone(input.Baseline.Findings)
	result.Baseline.Targets = slices.Clone(input.Baseline.Targets)
	result.Baseline.Suites = slices.Clone(input.Baseline.Suites)
	if result.Baseline.Evidence == nil {
		result.Baseline.Evidence = []report.Evidence{}
	}
	if result.Baseline.Findings == nil {
		result.Baseline.Findings = []report.Finding{}
	}
	if result.Baseline.Targets == nil {
		result.Baseline.Targets = []BaselineTarget{}
	}
	if result.Baseline.Suites == nil {
		result.Baseline.Suites = []BaselineSuite{}
	}
	slices.SortFunc(result.Baseline.Targets, func(a, b BaselineTarget) int { return strings.Compare(a.ID, b.ID) })
	for index := range result.Baseline.Targets {
		unit := &result.Baseline.Targets[index]
		unit.Evidence = slices.Clone(unit.Evidence)
		unit.Findings = slices.Clone(unit.Findings)
		if unit.Evidence == nil {
			unit.Evidence = []report.Evidence{}
		}
		if unit.Findings == nil {
			unit.Findings = []report.Finding{}
		}
		if unit.Target != nil {
			target := *unit.Target
			target.CoveredFiles = slices.Clone(target.CoveredFiles)
			target.Environment = slices.Clone(target.Environment)
			target.Infected = slices.Clone(target.Infected)
			target.Target.Capabilities = slices.Clone(target.Target.Capabilities)
			target.Target.Dependencies = slices.Clone(target.Target.Dependencies)
			slices.Sort(target.CoveredFiles)
			slices.Sort(target.Environment)
			slices.Sort(target.Infected)
			target.Infected = slices.Compact(target.Infected)
			slices.Sort(target.Target.Capabilities)
			slices.Sort(target.Target.Dependencies)
			if target.CoveredFiles == nil {
				target.CoveredFiles = []string{}
			}
			if target.Environment == nil {
				target.Environment = []string{}
			}
			if target.Infected == nil {
				target.Infected = []uint32{}
			}
			if target.Target.Capabilities == nil {
				target.Target.Capabilities = []string{}
			}
			if target.Target.Dependencies == nil {
				target.Target.Dependencies = []string{}
			}
			if target.Coverage != nil {
				coverage := canonicalCoverage(*target.Coverage)
				target.Coverage = &coverage
			}
			if target.Instrumented != nil {
				instrumented := canonicalCoverage(*target.Instrumented)
				target.Instrumented = &instrumented
			}
			unit.Target = &target
		}
	}
	for index := range result.Baseline.Suites {
		suite := &result.Baseline.Suites[index]
		if suite.Covered != nil {
			covered := canonicalCoverage(*suite.Covered)
			suite.Covered = &covered
		}
		if suite.Instrumented != nil {
			instrumented := canonicalCoverage(*suite.Instrumented)
			suite.Instrumented = &instrumented
		}
	}
	slices.SortFunc(result.Baseline.Suites, func(left, right BaselineSuite) int {
		return strings.Compare(left.Package, right.Package)
	})
	if input.Baseline.Routing != nil {
		routing := *input.Baseline.Routing
		routing.Instrumented = canonicalCoverage(routing.Instrumented)
		routing.Suites = slices.Clone(routing.Suites)
		if routing.Suites == nil {
			routing.Suites = []SuiteCoverage{}
		}
		for index := range routing.Suites {
			routing.Suites[index].Covered = canonicalCoverage(routing.Suites[index].Covered)
			routing.Suites[index].Instrumented = canonicalCoverage(routing.Suites[index].Instrumented)
		}
		slices.SortFunc(routing.Suites, func(left, right SuiteCoverage) int {
			return strings.Compare(left.Package, right.Package)
		})
		result.Baseline.Routing = &routing
	}
	if input.Race != nil {
		race := *input.Race
		race.Packages = slices.Clone(race.Packages)
		race.Evidence = slices.Clone(race.Evidence)
		race.Findings = slices.Clone(race.Findings)
		if race.Packages == nil {
			race.Packages = []string{}
		}
		if race.Evidence == nil {
			race.Evidence = []report.Evidence{}
		}
		if race.Findings == nil {
			race.Findings = []report.Finding{}
		}
		slices.Sort(race.Packages)
		result.Race = &race
	}
	if input.Mutation != nil {
		mutation := *input.Mutation
		if input.Mutation.Probe != nil {
			probe := *input.Mutation.Probe
			probe.Targets = slices.Clone(probe.Targets)
			probe.Suites = slices.Clone(probe.Suites)
			if probe.Targets == nil {
				probe.Targets = []TargetProbe{}
			}
			if probe.Suites == nil {
				probe.Suites = []SuiteProbe{}
			}
			for index := range probe.Targets {
				probe.Targets[index].Infected = slices.Clone(probe.Targets[index].Infected)
				if probe.Targets[index].Infected == nil {
					probe.Targets[index].Infected = []uint32{}
				}
				slices.Sort(probe.Targets[index].Infected)
			}
			for index := range probe.Suites {
				probe.Suites[index].Infected = slices.Clone(probe.Suites[index].Infected)
				if probe.Suites[index].Infected == nil {
					probe.Suites[index].Infected = []uint32{}
				}
				slices.Sort(probe.Suites[index].Infected)
			}
			slices.SortFunc(probe.Targets, func(left, right TargetProbe) int { return strings.Compare(left.ID, right.ID) })
			slices.SortFunc(probe.Suites, func(left, right SuiteProbe) int { return strings.Compare(left.Package, right.Package) })
			mutation.Probe = &probe
		}
		mutation.Results = slices.Clone(mutation.Results)
		if mutation.Results == nil {
			mutation.Results = []MutationResult{}
		}
		slices.SortFunc(mutation.Results, func(a, b MutationResult) int { return strings.Compare(a.ID, b.ID) })
		for index := range mutation.Results {
			mutation.Results[index].Evidence = slices.Clone(mutation.Results[index].Evidence)
			mutation.Results[index].Findings = slices.Clone(mutation.Results[index].Findings)
			if mutation.Results[index].Evidence == nil {
				mutation.Results[index].Evidence = []report.Evidence{}
			}
			if mutation.Results[index].Findings == nil {
				mutation.Results[index].Findings = []report.Finding{}
			}
		}
		result.Mutation = &mutation
	}
	return result
}

func canonicalCoverage(input Coverage) Coverage {
	result := Coverage{Files: slices.Clone(input.Files)}
	if result.Files == nil {
		result.Files = []FileCoverage{}
	}
	for index := range result.Files {
		result.Files[index].Blocks = slices.Clone(result.Files[index].Blocks)
		if result.Files[index].Blocks == nil {
			result.Files[index].Blocks = []CoverageBlock{}
		}
		slices.SortFunc(result.Files[index].Blocks, compareCoverageBlocks)
		result.Files[index].Blocks = slices.Compact(result.Files[index].Blocks)
	}
	slices.SortFunc(result.Files, func(left, right FileCoverage) int {
		return strings.Compare(left.Path, right.Path)
	})
	return result
}

func compareCoverageBlocks(left, right CoverageBlock) int {
	for _, pair := range [][2]int{
		{left.StartLine, right.StartLine},
		{left.StartColumn, right.StartColumn},
		{left.EndLine, right.EndLine},
		{left.EndColumn, right.EndColumn},
	} {
		switch {
		case pair[0] < pair[1]:
			return -1
		case pair[0] > pair[1]:
			return 1
		}
	}
	return 0
}
