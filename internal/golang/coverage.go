// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package golang

import (
	"bufio"
	"bytes"
	"cmp"
	"fmt"
	"path"
	"slices"
	"strconv"
	"strings"
)

const (
	coverageLineFieldCount = 3
	coverageLocationField  = 0
	coverageCountField     = 2
)

type CoverageBlock struct {
	StartLine   int
	StartColumn int
	EndLine     int
	EndColumn   int
}

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

type FileCoverage struct {
	Path   string
	Blocks []CoverageBlock
}

func (file FileCoverage) Contains(line, column int) bool {
	for _, block := range file.Blocks {
		if block.Contains(line, column) {
			return true
		}
	}
	return false
}

type CoverageSpan struct {
	StartLine   int
	StartColumn int
	EndLine     int
	EndColumn   int
}

func (file FileCoverage) StartsWithin(span CoverageSpan) bool {
	for _, block := range file.Blocks {
		if comparePositions(span.StartLine, span.StartColumn, block.StartLine, block.StartColumn) <= 0 &&
			comparePositions(block.StartLine, block.StartColumn, span.EndLine, span.EndColumn) <= 0 {
			return true
		}
	}
	return false
}

type Coverage struct {
	Covered      []FileCoverage
	Instrumented []FileCoverage
}

func RestrictCoverageToPackages(coverage Coverage, packages []Package) Coverage {
	return Coverage{
		Covered:      RestrictFileCoverageToPackages(coverage.Covered, packages),
		Instrumented: RestrictFileCoverageToPackages(coverage.Instrumented, packages),
	}
}

func RestrictFileCoverageToPackages(files []FileCoverage, packages []Package) []FileCoverage {
	directories := make(map[string]bool, len(packages))
	for _, pkg := range packages {
		directories[path.Clean(pkg.RelativeDir)] = true
	}
	result := make([]FileCoverage, 0, len(files))
	for _, file := range files {
		if directories[path.Dir(file.Path)] {
			result = append(result, file)
		}
	}
	return result
}

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
		if len(fields) != coverageLineFieldCount {
			return Coverage{}, fmt.Errorf("goatest: malformed coverage line %d", lines+1)
		}
		count, err := strconv.ParseUint(fields[coverageCountField], 10, 64)
		if err != nil {
			return Coverage{}, fmt.Errorf("goatest: malformed coverage count on line %d", lines+1)
		}
		colon := strings.LastIndex(fields[coverageLocationField], ":")
		if colon < 1 {
			return Coverage{}, fmt.Errorf("goatest: malformed coverage location on line %d", lines+1)
		}
		block, ok := parseCoverageSpan(fields[coverageLocationField][colon+1:])
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

func CoverageFiles(profile []byte, modulePath string) ([]string, error) {
	coverage, err := ParseCoverage(profile, modulePath)
	if err != nil {
		return nil, err
	}
	return CoveredPaths(coverage.Covered), nil
}

func CoveredPaths(files []FileCoverage) []string {
	paths := make([]string, 0, len(files))
	for _, file := range files {
		paths = append(paths, file.Path)
	}
	return paths
}

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

func FindFileCoverage(files []FileCoverage, path string) (FileCoverage, bool) {
	index, found := slices.BinarySearchFunc(files, path, func(file FileCoverage, wanted string) int {
		return strings.Compare(file.Path, wanted)
	})
	if !found {
		return FileCoverage{}, false
	}
	return files[index], true
}

func parseCoverageSpan(span string) (CoverageBlock, bool) {
	start, end, ok := strings.Cut(span, ",")
	if !ok {
		return CoverageBlock{}, false
	}
	block := CoverageBlock{}
	if block.StartLine, block.StartColumn, ok = parseCoveragePosition(start); !ok {
		return CoverageBlock{}, false
	}
	if block.EndLine, block.EndColumn, ok = parseCoveragePosition(end); !ok {
		return CoverageBlock{}, false
	}
	if block.EndLine < block.StartLine || block.EndLine == block.StartLine && block.EndColumn < block.StartColumn {
		return CoverageBlock{}, false
	}
	return block, true
}

func parseCoveragePosition(position string) (int, int, bool) {
	line, column, ok := strings.Cut(position, ".")
	if !ok {
		return 0, 0, false
	}
	lineNumber, err := strconv.Atoi(line)
	if err != nil || lineNumber < 1 {
		return 0, 0, false
	}
	columnNumber, err := strconv.Atoi(column)
	if err != nil || columnNumber < 1 {
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
	if compared := comparePositions(first.StartLine, first.StartColumn, second.StartLine, second.StartColumn); compared != 0 {
		return compared
	}
	return comparePositions(first.EndLine, first.EndColumn, second.EndLine, second.EndColumn)
}

func comparePositions(firstLine, firstColumn, secondLine, secondColumn int) int {
	if compared := cmp.Compare(firstLine, secondLine); compared != 0 {
		return compared
	}
	return cmp.Compare(firstColumn, secondColumn)
}

func filepathSlash(path string) string { return strings.ReplaceAll(path, `\`, "/") }
