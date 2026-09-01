// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package checkpoint defines the strict, exact-input interrupted-run state.
// A checkpoint is scheduling state, never a completed assurance claim.
package checkpoint

import (
	"bytes"
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
	Capability   string   `json:"capability"`
	Capabilities []string `json:"capabilities"`
	Dependencies []string `json:"dependencies"`
}

type TargetEvidence struct {
	Target       Target   `json:"target"`
	CoveredFiles []string `json:"covered_files"`
	Environment  []string `json:"environment"`
	DurationNS   int64    `json:"duration_ns"`
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
	Repairs  []report.Repair   `json:"repairs"`
	Applied  bool              `json:"applied"`
}

type Mutation struct {
	CatalogFingerprint string           `json:"catalog_fingerprint"`
	Complete           bool             `json:"complete"`
	Results            []MutationResult `json:"results"`
}

type State struct {
	Schema      string    `json:"schema"`
	InputDigest string    `json:"input_digest"`
	Attempts    int       `json:"attempts"`
	Baseline    Baseline  `json:"baseline"`
	Race        *Race     `json:"race,omitempty"`
	Mutation    *Mutation `json:"mutation,omitempty"`
}

// Decode reads one strict checkpoint. Unknown fields, trailing values and
// semantically incomplete completed units are refused.
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

// Validate checks the checkpoint identity and the completeness of every unit
// that a later attempt is allowed to skip.
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
			if unit.Target.Target.ID != unit.ID || unit.Target.DurationNS < 0 {
				return fmt.Errorf("goatest: checkpoint target evidence %s has an invalid identity", unit.ID)
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
	return unit.Applied
}

func validSHA256(value string) bool {
	if len(value) != 64 {
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

// JSON returns deterministic, indented checkpoint bytes with one newline.
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
	if result.Baseline.Evidence == nil {
		result.Baseline.Evidence = []report.Evidence{}
	}
	if result.Baseline.Findings == nil {
		result.Baseline.Findings = []report.Finding{}
	}
	if result.Baseline.Targets == nil {
		result.Baseline.Targets = []BaselineTarget{}
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
			target.Target.Capabilities = slices.Clone(target.Target.Capabilities)
			target.Target.Dependencies = slices.Clone(target.Target.Dependencies)
			slices.Sort(target.CoveredFiles)
			slices.Sort(target.Environment)
			slices.Sort(target.Target.Capabilities)
			slices.Sort(target.Target.Dependencies)
			if target.CoveredFiles == nil {
				target.CoveredFiles = []string{}
			}
			if target.Environment == nil {
				target.Environment = []string{}
			}
			if target.Target.Capabilities == nil {
				target.Target.Capabilities = []string{}
			}
			if target.Target.Dependencies == nil {
				target.Target.Dependencies = []string{}
			}
			unit.Target = &target
		}
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
		mutation.Results = slices.Clone(mutation.Results)
		if mutation.Results == nil {
			mutation.Results = []MutationResult{}
		}
		slices.SortFunc(mutation.Results, func(a, b MutationResult) int { return strings.Compare(a.ID, b.ID) })
		for index := range mutation.Results {
			mutation.Results[index].Evidence = slices.Clone(mutation.Results[index].Evidence)
			mutation.Results[index].Findings = slices.Clone(mutation.Results[index].Findings)
			mutation.Results[index].Repairs = slices.Clone(mutation.Results[index].Repairs)
			if mutation.Results[index].Evidence == nil {
				mutation.Results[index].Evidence = []report.Evidence{}
			}
			if mutation.Results[index].Findings == nil {
				mutation.Results[index].Findings = []report.Finding{}
			}
			if mutation.Results[index].Repairs == nil {
				mutation.Results[index].Repairs = []report.Repair{}
			}
		}
		result.Mutation = &mutation
	}
	return result
}
