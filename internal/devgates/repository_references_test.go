//go:build integration

// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package devgates

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/P4suta/goatest/internal/testkit"
)

// The repository-reference ledger.
//
// `P4suta/goatest` is two different things wearing one spelling. In an import
// path it is where the code lives, and when this module is folded into
// go-mutants every one of those is rewritten to
// github.com/P4suta/go-mutants/goatest. As the name of a GitHub repository it
// is where the project lives, and that does not move: a repository does not
// gain a path segment because a module inside it did.
//
// The ledger exists because the two cannot be told apart by how wide a search
// pattern is. Of the five references this repository makes to itself as a
// repository, four are spelled `https://github.com/P4suta/goatest…`, which is
// exactly what a rewrite of import paths looks for; the fifth is a bare
// `--repo P4suta/goatest`, which a pattern narrow enough to spare the four
// misses. go-mutants found the fifth by rehearsing the migration and reading
// the diff. This file is so that the other four are not found the same way.

const (
	// repositoryReferenceLedger names the files that refer to this repository
	// as a repository.
	repositoryReferenceLedger = "internal/devgates/repository_references.txt"

	// repositoryName is the spelling both meanings share.
	repositoryName = "P4suta/goatest"

	// importPrefix precedes the spelling when it is an import path.
	importPrefix = "github.com/"

	// urlPrefix precedes the spelling when it is a repository URL, which is a
	// reference to the repository however much of an import path it resembles.
	urlPrefix = "https://github.com/"

	// ledgerTestFile is this file, which quotes both forms and is therefore
	// not evidence about the tree.
	ledgerTestFile = "internal/devgates/repository_references_test.go"
)

// readLedgerPaths reads a one-path-per-line ledger.
func readLedgerPaths(t *testing.T, root, ledger string) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(ledger)))
	if err != nil {
		t.Fatalf("read %s: %v", ledger, err)
	}
	var paths []string
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		paths = append(paths, trimmed)
	}
	// Fail closed. Every assertion below is a loop, and a loop over nothing
	// passes: an empty read and a repository that names itself nowhere look
	// the same from inside the range statement.
	if len(paths) == 0 {
		t.Fatalf("%s lists no file, and this repository refers to itself by name in several", ledger)
	}
	return paths
}

// referencesRepositoryByName reports whether a file names this repository as a
// repository rather than as an import path.
//
// The classification is by what precedes the name: a URL scheme means a link to
// the repository, no `github.com/` at all means a bare `owner/name`, and
// anything else is an import path. That is mechanical and it is a choice of
// granularity, which is to say nothing checks that it is the right one - a
// sixth form of reference would be classified as an import path and pass.
func referencesRepositoryByName(t *testing.T, path string) bool {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	remaining := string(data)
	for {
		index := strings.Index(remaining, repositoryName)
		if index < 0 {
			return false
		}
		before := remaining[:index]
		switch {
		case strings.HasSuffix(before, urlPrefix):
			return true
		case !strings.HasSuffix(before, importPrefix):
			return true
		}
		remaining = remaining[index+len(repositoryName):]
	}
}

// TestEveryFileNamingTheRepositoryIsInTheLedger pins the tree to the ledger.
func TestEveryFileNamingTheRepositoryIsInTheLedger(t *testing.T) {
	t.Parallel()
	root := repositoryRoot(t)
	listed := readLedgerPaths(t, root, repositoryReferenceLedger)
	for _, relative := range trackedFiles(t, testkit.GitBinary(t), root) {
		if relative == repositoryReferenceLedger || relative == ledgerTestFile {
			continue
		}
		if !referencesRepositoryByName(t, filepath.Join(root, filepath.FromSlash(relative))) {
			continue
		}
		if !slices.Contains(listed, relative) {
			t.Errorf("%s names this repository as a repository and is absent from %s, so a migration that rewrites import paths would rewrite it too",
				relative, repositoryReferenceLedger)
		}
	}
}

// TestEveryLedgeredFileStillNamesTheRepository pins the ledger to the tree.
//
// This is the direction that keeps the file honest after the migration. Once
// the references are decided one way or another, an entry that no longer names
// the repository is a claim about work still to do that has already been done,
// and a ledger nobody can finish is a ledger nobody reads.
func TestEveryLedgeredFileStillNamesTheRepository(t *testing.T) {
	t.Parallel()
	root := repositoryRoot(t)
	for _, relative := range readLedgerPaths(t, root, repositoryReferenceLedger) {
		path := filepath.Join(root, filepath.FromSlash(relative))
		if _, err := os.Stat(path); err != nil {
			t.Errorf("%s lists %s, which this repository does not hold", repositoryReferenceLedger, relative)
			continue
		}
		if !referencesRepositoryByName(t, path) {
			t.Errorf("%s lists %s, which no longer names this repository as a repository", repositoryReferenceLedger, relative)
		}
	}
}

// TestTheRepositoryReferenceLedgerIsSorted keeps it searchable by eye.
func TestTheRepositoryReferenceLedgerIsSorted(t *testing.T) {
	t.Parallel()
	listed := readLedgerPaths(t, repositoryRoot(t), repositoryReferenceLedger)
	if !slices.IsSorted(listed) {
		t.Errorf("%s lists %v, which is not sorted", repositoryReferenceLedger, listed)
	}
}

// TestTheClassificationTellsAnImportPathFromARepositoryName proves the reading
// this ledger rests on, rather than assuming it.
func TestTheClassificationTellsAnImportPathFromARepositoryName(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name    string
		content string
		want    bool
	}{
		{name: "import", content: `import "github.com/P4suta/goatest/internal/report"`},
		{name: "module", content: "module github.com/P4suta/goatest"},
		{name: "url", content: "see https://github.com/P4suta/goatest/releases", want: true},
		{name: "bare", content: "gh attestation verify x --repo P4suta/goatest", want: true},
		{name: "import-then-url", content: "github.com/P4suta/goatest/internal and https://github.com/P4suta/goatest", want: true},
		{name: "absent", content: "nothing here"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "sample.txt")
			if err := os.WriteFile(path, []byte(testCase.content), 0o600); err != nil {
				t.Fatal(err)
			}
			if got := referencesRepositoryByName(t, path); got != testCase.want {
				t.Errorf("referencesRepositoryByName(%q) = %t, want %t", testCase.content, got, testCase.want)
			}
		})
	}
}
