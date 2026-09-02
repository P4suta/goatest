// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package golang

import (
	"bufio"
	"bytes"
	"cmp"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// CoverageBlock is one basic block of a Go coverage profile: the source span
// cmd/cover instruments and counts as a unit. The span is half-open, so the
// start position belongs to the block and the end position belongs to whatever
// follows it. Lines and columns are 1-based, and a column counts bytes, which
// is the unit both cmd/cover and go-mutants report positions in.
type CoverageBlock struct {
	StartLine   int
	StartColumn int
	EndLine     int
	EndColumn   int
}

// Contains reports whether a 1-based line and byte column lies inside the
// block. A position on the start line before the start column, or on the end
// line at or after the end column, belongs to a neighbouring block instead.
func (block CoverageBlock) Contains(line, column int) bool {
	if line < block.StartLine || line > block.EndLine {
		return false
	}
	if line == block.StartLine && column < block.StartColumn {
		return false
	}
	if line == block.EndLine && column >= block.EndColumn {
		return false
	}
	return true
}

// FileCoverage is every block a coverage profile recorded for one
// module-relative file, in a stable order and without duplicates.
type FileCoverage struct {
	Path   string
	Blocks []CoverageBlock
}

// Contains reports whether any block of the file contains the position. A file
// with no blocks contains nothing, so the zero value answers every position
// with false.
func (file FileCoverage) Contains(line, column int) bool {
	for _, block := range file.Blocks {
		if block.Contains(line, column) {
			return true
		}
	}
	return false
}

// Coverage is one parsed coverage profile: the blocks the profiled execution
// actually ran, and every block the profile instrumented whether it ran or
// not. Both are sorted by path and free of duplicates, and both are non-nil
// even when the profile carries nothing, so that an absent measurement stays
// distinguishable from a measurement that found nothing.
type Coverage struct {
	Covered      []FileCoverage
	Instrumented []FileCoverage
}

// ParseCoverage reads a Go coverage profile and separates the blocks that ran
// from the blocks that were instrumented. Every path is reported relative to
// the module and with forward slashes; a path outside the module is rejected,
// as is any line the profile format does not allow.
func ParseCoverage(profile []byte, modulePath string) (Coverage, error) {
	scanner := bufio.NewScanner(bytes.NewReader(profile))
	if !scanner.Scan() || !strings.HasPrefix(scanner.Text(), "mode: ") {
		return Coverage{}, fmt.Errorf("goatest: coverage profile has no mode header")
	}
	covered := make(map[string]map[CoverageBlock]struct{})
	instrumented := make(map[string]map[CoverageBlock]struct{})
	lines := 0
	for scanner.Scan() {
		lines++
		fields := strings.Fields(scanner.Text())
		if len(fields) != 3 {
			return Coverage{}, fmt.Errorf("goatest: malformed coverage line %d", lines+1)
		}
		count, err := strconv.ParseUint(fields[2], 10, 64)
		if err != nil {
			return Coverage{}, fmt.Errorf("goatest: malformed coverage count on line %d", lines+1)
		}
		colon := strings.LastIndex(fields[0], ":")
		if colon < 1 {
			return Coverage{}, fmt.Errorf("goatest: malformed coverage location on line %d", lines+1)
		}
		block, ok := parseCoverageSpan(fields[0][colon+1:])
		if !ok {
			return Coverage{}, fmt.Errorf("goatest: malformed coverage span on line %d", lines+1)
		}
		path := filepathSlash(fields[0][:colon])
		prefix := strings.TrimSuffix(modulePath, "/") + "/"
		if !strings.HasPrefix(path, prefix) {
			return Coverage{}, fmt.Errorf("goatest: coverage path %q is outside module %q", path, modulePath)
		}
		relative := strings.TrimPrefix(path, prefix)
		rememberCoverageBlock(instrumented, relative, block)
		if count > 0 {
			rememberCoverageBlock(covered, relative, block)
		}
	}
	if err := scanner.Err(); err != nil {
		return Coverage{}, err
	}
	return Coverage{Covered: sortedFileCoverage(covered), Instrumented: sortedFileCoverage(instrumented)}, nil
}

// CoverageFiles returns the module-relative files some part of which the
// profiled execution ran.
func CoverageFiles(profile []byte, modulePath string) ([]string, error) {
	coverage, err := ParseCoverage(profile, modulePath)
	if err != nil {
		return nil, err
	}
	return CoveredPaths(coverage.Covered), nil
}

// CoveredPaths returns the paths of a file coverage set, keeping its order.
func CoveredPaths(files []FileCoverage) []string {
	paths := make([]string, 0, len(files))
	for _, file := range files {
		paths = append(paths, file.Path)
	}
	return paths
}

// MergeFileCoverage returns the union of two file coverage sets, sorted by
// path with the blocks of each file sorted and deduplicated. The result is
// independent of the order the two sets are given in and shares no memory with
// either of them.
func MergeFileCoverage(first, second []FileCoverage) []FileCoverage {
	blocks := make(map[string]map[CoverageBlock]struct{})
	for _, files := range [][]FileCoverage{first, second} {
		for _, file := range files {
			for _, block := range file.Blocks {
				rememberCoverageBlock(blocks, file.Path, block)
			}
		}
	}
	return sortedFileCoverage(blocks)
}

// FindFileCoverage returns the coverage recorded for one path. The files must
// be sorted by path, which is how ParseCoverage and MergeFileCoverage return
// them.
func FindFileCoverage(files []FileCoverage, path string) (FileCoverage, bool) {
	index, found := slices.BinarySearchFunc(files, path, func(file FileCoverage, wanted string) int {
		return strings.Compare(file.Path, wanted)
	})
	if !found {
		return FileCoverage{}, false
	}
	return files[index], true
}

// parseCoverageSpan reads the "startLine.startColumn,endLine.endColumn" span
// of a coverage profile line.
func parseCoverageSpan(span string) (CoverageBlock, bool) {
	start, end, ok := strings.Cut(span, ",")
	if !ok {
		return CoverageBlock{}, false
	}
	startLine, startColumn, ok := parseCoveragePosition(start)
	if !ok {
		return CoverageBlock{}, false
	}
	endLine, endColumn, ok := parseCoveragePosition(end)
	if !ok {
		return CoverageBlock{}, false
	}
	return CoverageBlock{StartLine: startLine, StartColumn: startColumn, EndLine: endLine, EndColumn: endColumn}, true
}

// parseCoveragePosition reads one "line.column" half of a coverage span.
func parseCoveragePosition(position string) (int, int, bool) {
	line, column, ok := strings.Cut(position, ".")
	if !ok {
		return 0, 0, false
	}
	lineNumber, err := strconv.Atoi(line)
	if err != nil {
		return 0, 0, false
	}
	columnNumber, err := strconv.Atoi(column)
	if err != nil {
		return 0, 0, false
	}
	return lineNumber, columnNumber, true
}

func rememberCoverageBlock(blocks map[string]map[CoverageBlock]struct{}, path string, block CoverageBlock) {
	file, ok := blocks[path]
	if !ok {
		file = make(map[CoverageBlock]struct{})
		blocks[path] = file
	}
	file[block] = struct{}{}
}

// sortedFileCoverage turns remembered blocks into the canonical form every
// caller reads: files sorted by path, blocks sorted by position, no
// duplicates, and never nil.
func sortedFileCoverage(blocks map[string]map[CoverageBlock]struct{}) []FileCoverage {
	files := make([]FileCoverage, 0, len(blocks))
	for path, unique := range blocks {
		file := FileCoverage{Path: path, Blocks: make([]CoverageBlock, 0, len(unique))}
		for block := range unique {
			file.Blocks = append(file.Blocks, block)
		}
		slices.SortFunc(file.Blocks, compareCoverageBlocks)
		files = append(files, file)
	}
	slices.SortFunc(files, func(first, second FileCoverage) int {
		return strings.Compare(first.Path, second.Path)
	})
	return files
}

func compareCoverageBlocks(first, second CoverageBlock) int {
	if compared := cmp.Compare(first.StartLine, second.StartLine); compared != 0 {
		return compared
	}
	if compared := cmp.Compare(first.StartColumn, second.StartColumn); compared != 0 {
		return compared
	}
	if compared := cmp.Compare(first.EndLine, second.EndLine); compared != 0 {
		return compared
	}
	return cmp.Compare(first.EndColumn, second.EndColumn)
}

func filepathSlash(path string) string { return strings.ReplaceAll(path, `\`, "/") }
