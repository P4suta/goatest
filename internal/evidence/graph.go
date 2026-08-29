// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package evidence

import (
	"encoding/json"
	"slices"
	"strings"
)

var marshalGraphJSON = json.MarshalIndent

type Target struct {
	ID           string   `json:"id"`
	Package      string   `json:"package"`
	Kind         string   `json:"kind,omitempty"`
	Dependencies []string `json:"dependencies"`
	CoveredFiles []string `json:"covered_files"`
}

type Graph struct {
	FilePackages map[string]string `json:"file_packages"`
	Targets      []Target          `json:"targets"`
}

type Impact struct {
	Targets []string
	Broad   bool
}

func (graph Graph) Affected(changed []string) Impact {
	all := make([]string, 0, len(graph.Targets))
	for _, target := range graph.Targets {
		all = append(all, target.ID)
	}
	slices.Sort(all)
	selected := make(map[string]struct{})
	for _, rawPath := range changed {
		path := filepathSlash(rawPath)
		changedPackage, known := graph.FilePackages[path]
		if !known {
			return Impact{Targets: all, Broad: true}
		}
		for _, target := range graph.Targets {
			testStructureChanged := strings.HasSuffix(path, "_test.go") && target.Package == changedPackage
			if testStructureChanged || slices.Contains(target.Dependencies, changedPackage) || slices.Contains(target.CoveredFiles, path) {
				selected[target.ID] = struct{}{}
			}
		}
	}
	result := make([]string, 0, len(selected))
	for id := range selected {
		result = append(result, id)
	}
	slices.Sort(result)
	return Impact{Targets: result}
}

func filepathSlash(path string) string { return strings.ReplaceAll(path, `\`, "/") }

func (graph Graph) canonical() Graph {
	result := graph
	result.FilePackages = cloneMap(graph.FilePackages)
	result.Targets = slices.Clone(graph.Targets)
	for i := range result.Targets {
		result.Targets[i].Dependencies = slices.Clone(result.Targets[i].Dependencies)
		result.Targets[i].CoveredFiles = slices.Clone(result.Targets[i].CoveredFiles)
		slices.Sort(result.Targets[i].Dependencies)
		slices.Sort(result.Targets[i].CoveredFiles)
	}
	slices.SortFunc(result.Targets, func(a, b Target) int { return strings.Compare(a.ID, b.ID) })
	return result
}

func (graph Graph) JSON() ([]byte, error) {
	data, err := marshalGraphJSON(graph.canonical(), "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}
