// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package golang

import (
	"bufio"
	"bytes"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

func CoverageFiles(profile []byte, modulePath string) ([]string, error) {
	scanner := bufio.NewScanner(bytes.NewReader(profile))
	if !scanner.Scan() || !strings.HasPrefix(scanner.Text(), "mode: ") {
		return nil, fmt.Errorf("goatest: coverage profile has no mode header")
	}
	files := make(map[string]bool)
	lines := 0
	for scanner.Scan() {
		lines++
		fields := strings.Fields(scanner.Text())
		if len(fields) != 3 {
			return nil, fmt.Errorf("goatest: malformed coverage line %d", lines+1)
		}
		count, err := strconv.ParseUint(fields[2], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("goatest: malformed coverage count on line %d", lines+1)
		}
		colon := strings.LastIndex(fields[0], ":")
		if colon < 1 {
			return nil, fmt.Errorf("goatest: malformed coverage location on line %d", lines+1)
		}
		path := filepathSlash(fields[0][:colon])
		prefix := strings.TrimSuffix(modulePath, "/") + "/"
		if !strings.HasPrefix(path, prefix) {
			return nil, fmt.Errorf("goatest: coverage path %q is outside module %q", path, modulePath)
		}
		if count > 0 {
			files[strings.TrimPrefix(path, prefix)] = true
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	result := make([]string, 0, len(files))
	for path := range files {
		result = append(result, path)
	}
	slices.Sort(result)
	return result, nil
}

func filepathSlash(path string) string { return strings.ReplaceAll(path, `\`, "/") }
