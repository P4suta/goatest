// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package devgates

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// The ledgers for the two documentation indices.
//
// An index is the one page whose whole job is to describe other pages, which
// makes it the page that rots first and the page a reader trusts most. Both
// directions matter and for different reasons: a page the index does not name
// is a page nobody will find, and a line naming a page that is gone sends a
// reader to a 404 and tells them the project does not know its own shape.

const (
	// documentationDirectory and recordDirectory hold the pages each index
	// describes, relative to the module root.
	documentationDirectory = "docs"
	recordDirectory        = "docs/adr"

	// indexName is the file that indexes a directory.
	indexName = "README.md"
)

// markdownLink matches one [text](target.md) link.
var markdownLink = regexp.MustCompile(`\[[^\]]*\]\(([^)]+)\)`)

func TestTheDocumentationIndexNamesEveryPage(t *testing.T) {
	t.Parallel()
	pinIndex(t, documentationDirectory, []string{indexName})
}

func TestTheRecordIndexNamesEveryRecord(t *testing.T) {
	t.Parallel()
	pinIndex(t, recordDirectory, []string{indexName})
}

// TestTheIndexLedgerSeesAPageThatIsNotLinked proves the ledger can fail. Two
// agreeing sets is also what an index that links nothing looks like.
func TestTheIndexLedgerSeesAPageThatIsNotLinked(t *testing.T) {
	t.Parallel()
	root := repositoryRoot(t)
	linked := indexTargets(t, filepath.Join(root, filepath.FromSlash(recordDirectory), indexName))
	if len(linked) == 0 {
		t.Fatal("the index links nothing, so the ledger would agree with anything")
	}
	if slices.Contains(linked, "0000-a-record-that-does-not-exist.md") {
		t.Fatal("the index links the fixture record, so this test proves nothing")
	}
}

// pinIndex compares the index of a directory against the directory, in both
// directions.
func pinIndex(t *testing.T, directory string, exempt []string) {
	t.Helper()
	root := repositoryRoot(t)
	full := filepath.Join(root, filepath.FromSlash(directory))
	entries, err := os.ReadDir(full)
	if err != nil {
		t.Fatalf("read %s: %v", directory, err)
	}
	var pages []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") || slices.Contains(exempt, entry.Name()) {
			continue
		}
		pages = append(pages, entry.Name())
	}
	linked := indexTargets(t, filepath.Join(full, indexName))
	for _, page := range pages {
		if !slices.Contains(linked, page) {
			t.Errorf("%s/%s does not link %s.\n\n"+
				"A page the index does not name is a page nobody will find.", directory, indexName, page)
		}
	}
	for _, target := range linked {
		if strings.HasSuffix(target, "/"+indexName) || !strings.HasSuffix(target, ".md") {
			continue
		}
		if !slices.Contains(pages, target) {
			t.Errorf("%s/%s links %s, which %s no longer holds.\n\n"+
				"A line naming a page that is gone sends a reader nowhere and says the\n"+
				"project does not know its own shape.", directory, indexName, target, directory)
		}
	}
}

// indexTargets reads the link targets of one index.
func indexTargets(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var targets []string
	for _, match := range markdownLink.FindAllStringSubmatch(string(data), -1) {
		target := match[1]
		if strings.Contains(target, "://") || strings.HasPrefix(target, "#") {
			continue
		}
		if !slices.Contains(targets, target) {
			targets = append(targets, target)
		}
	}
	return targets
}

// TestEveryDocumentedPathExists is the staleness rule the Go doc comments live
// under, applied to the Markdown.
//
// A page names files constantly - a ledger table is nothing but file names -
// and a renamed file leaves every sentence about it pointing at nothing. The
// failure is worse in prose than in code, because a reader has no compiler to
// tell them the reference is dead and will assume the project moved rather than
// that the page did not.
func TestEveryDocumentedPathExists(t *testing.T) {
	t.Parallel()
	root := repositoryRoot(t)
	var broken []string
	for _, page := range markdownPages(t, root) {
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(page)))
		if err != nil {
			t.Fatalf("read %s: %v", page, err)
		}
		for _, reference := range repositoryPaths(string(data)) {
			if written(t, root, reference) {
				continue
			}
			if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(reference))); err != nil {
				broken = append(broken, page+": "+reference)
			}
		}
	}
	if len(broken) > 0 {
		slices.Sort(broken)
		broken = slices.Compact(broken)
		t.Errorf("%d documented path(s) do not exist:\n%s\n\n"+
			"A page that names a file that is gone tells a reader the project moved,\n"+
			"when what moved was the page.", len(broken), strings.Join(broken, "\n"))
	}
}

// TestEveryDocumentedLinkResolves checks the other kind of reference: the ones
// a reader clicks.
func TestEveryDocumentedLinkResolves(t *testing.T) {
	t.Parallel()
	root := repositoryRoot(t)
	var broken []string
	for _, page := range markdownPages(t, root) {
		directory := filepath.Dir(filepath.Join(root, filepath.FromSlash(page)))
		for _, target := range indexTargets(t, filepath.Join(root, filepath.FromSlash(page))) {
			path, _, _ := strings.Cut(target, "#")
			if path == "" {
				continue
			}
			if _, err := os.Stat(filepath.Join(directory, filepath.FromSlash(path))); err != nil {
				broken = append(broken, page+": "+target)
			}
		}
	}
	if len(broken) > 0 {
		slices.Sort(broken)
		broken = slices.Compact(broken)
		t.Errorf("%d documented link(s) resolve to nothing:\n%s",
			len(broken), strings.Join(broken, "\n"))
	}
}

// written reports whether a documented path is one a run writes rather than one
// the repository holds.
//
// The distinction is read out of .gitignore rather than listed here, because
// .gitignore is already the answer to "what does a run leave behind" and a
// second list would be a second thing to keep in step. `reports/latest-any.json`
// is the case that found this: a page describing what a verification produces
// names files that do not exist until one has run, and a gate that called those
// broken would have taught everyone to stop describing outputs.
func written(t *testing.T, root, reference string) bool {
	t.Helper()
	first, _, _ := strings.Cut(reference, "/")
	for _, pattern := range ignoredRoots(t, root) {
		if pattern == first {
			return true
		}
	}
	return false
}

// ignoredRoots reads the top-level names .gitignore excludes.
func ignoredRoots(t *testing.T, root string) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, ".gitignore"))
	if err != nil {
		t.Fatalf("read .gitignore: %v", err)
	}
	var roots []string
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		name := strings.Trim(trimmed, "/")
		if name != "" && !strings.ContainsAny(name, "*?[") {
			roots = append(roots, name)
		}
	}
	return roots
}

// markdownPages lists the Markdown git holds, outside the directories a walk
// should not enter.
func markdownPages(t *testing.T, root string) []string {
	t.Helper()
	var pages []string
	walk := func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if entry.IsDir() {
			if relative != "." && skipDirectory(relative, entry.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(entry.Name(), ".md") {
			pages = append(pages, relative)
		}
		return nil
	}
	if err := filepath.WalkDir(root, walk); err != nil {
		t.Fatalf("walk the repository: %v", err)
	}
	return pages
}
