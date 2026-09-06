// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package devgates

import (
	"bufio"
	"fmt"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestSourcesContainOnlyRequiredComments(t *testing.T) {
	t.Parallel()
	found, err := scanComments(repositoryRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 0 {
		t.Fatalf("%d unnecessary comment(s):\n%s", len(found), strings.Join(found, "\n"))
	}
}

func scanComments(root string) ([]string, error) {
	fileSet := token.NewFileSet()
	var found []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if entry.IsDir() {
			if relative != "." && skipCommentDirectory(relative, entry.Name()) {
				return fs.SkipDir
			}
			return nil
		}
		switch filepath.Ext(path) {
		case ".go":
			file, err := parser.ParseFile(fileSet, path, nil, parser.ParseComments|parser.SkipObjectResolution)
			if err != nil {
				return err
			}
			for _, group := range file.Comments {
				for _, comment := range group.List {
					if requiredGoComment(comment.Text) {
						continue
					}
					position := fileSet.Position(comment.Pos())
					found = append(found, fmt.Sprintf("%s:%d: %s", relative, position.Line, comment.Text))
				}
			}
		case ".toml", ".yaml", ".yml":
			comments, err := scanHashComments(path, relative)
			if err != nil {
				return err
			}
			found = append(found, comments...)
		}
		return nil
	})
	slices.Sort(found)
	return found, err
}

func skipCommentDirectory(relative, name string) bool {
	if relative == "dist" || relative == "reports" || name == "vendor" {
		return true
	}
	return name == ".git" || name == ".goatest"
}

func requiredGoComment(comment string) bool {
	return strings.HasPrefix(comment, "// SPDX-") ||
		strings.HasPrefix(comment, "//go:") ||
		strings.HasPrefix(comment, "// Code generated ") ||
		strings.HasPrefix(comment, "//nolint") ||
		strings.HasPrefix(comment, "//lint:")
}

func scanHashComments(path, relative string) ([]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	var found []string
	scanner := bufio.NewScanner(file)
	line := 0
	for scanner.Scan() {
		line++
		trimmed := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(trimmed, "#") && !strings.HasPrefix(trimmed, "# SPDX-") {
			found = append(found, fmt.Sprintf("%s:%d: %s", relative, line, trimmed))
		}
	}
	return found, scanner.Err()
}
