// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package devgates

import (
	"go/build/constraint"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// integrationTag is the build tag that separates the two tiers.
//
// `go test ./...` is the unit tier: everything that needs nothing but a
// compiler, which is what a developer runs on every save and what CI runs on
// three operating systems. `go test -tags integration ./...` adds the suites
// that drive a real toolchain - a go build, a go list, a git history, a whole
// mutation run - and costs tens of minutes.
//
// The split is not about speed, although speed is what it buys. It is about
// what a test needs in order to mean anything, which is the same subject the
// public API of this module is about. A suite that cannot say which of its
// tests need a toolchain cannot be told that it lost one.
const integrationTag = "integration"

// toolchainNeedles are the spellings that mean "this file starts a real
// process".
//
// They are the spellings this repository actually uses rather than a general
// analysis. GoBinary and GitBinary are the harness facades every toolchain test
// reaches a tool through; Git builds a repository with five git children;
// LookPath is the hand-rolled form that predates the facades and is currently
// absent, kept here so that its return is caught rather than welcomed.
//
// The first three are matched *unqualified*, which is the difference between a
// rule and a rule with a hole in it. Written as testkit.GoBinary( they would
// never match a call inside package testkit, and the one package whose whole
// subject is which tools a test may reach would be exempt from the tier policy
// by construction.
//
// Each is written as two pieces joined at compile time, so that this file is
// not an offender because it states the rule.
var toolchainNeedles = []string{
	"GoBinary" + "(",
	"GitBinary" + "(",
	".Git" + "()",
	"LookPath" + `("go")`,
	"LookPath" + `("git")`,
}

// TestEveryTestThatStartsAToolchainIsInTheIntegrationTier is checked in both
// directions.
//
// A file that starts a process without the tag is an offender: it costs the
// unit tier a process tree, and - worse - it makes the unit tier fail on a
// machine that has no toolchain, which is the one thing the unit tier is for.
//
// A file that carries the tag and starts nothing is the other half, and it is
// not a formality. The tag hides a file from `go test ./...` entirely, so a
// stale one is a suite that silently stopped running. Nothing else in the tree
// would notice: the tests are not skipped, not failed, not counted. They are
// not compiled.
func TestEveryTestThatStartsAToolchainIsInTheIntegrationTier(t *testing.T) {
	t.Parallel()
	root := repositoryRoot(t)
	files, err := scanTestFiles(root)
	if err != nil {
		t.Fatalf("scan the test files: %v", err)
	}
	var untagged, stale []string
	for _, found := range files {
		switch {
		case found.startsToolchain && !found.integration:
			untagged = append(untagged, found.path+": "+strings.Join(found.needles, ", "))
		case !found.startsToolchain && found.integration:
			stale = append(stale, found.path)
		}
	}
	if len(untagged) > 0 {
		slices.Sort(untagged)
		t.Errorf("%d test file(s) start a real toolchain without //go:build %s:\n%s\n\n"+
			"A unit tier that starts processes is a unit tier that fails on a machine\n"+
			"with no toolchain, which is the one situation it exists to survive. Add\n"+
			"the tag, or move the test that needs the tool into a file that carries it.",
			len(untagged), integrationTag, strings.Join(untagged, "\n"))
	}
	if len(stale) > 0 {
		slices.Sort(stale)
		t.Errorf("%d test file(s) carry //go:build %s and start nothing:\n%s\n\n"+
			"The tag hides a file from `go test ./...` completely. A stale one is a\n"+
			"suite that stopped running without being skipped, failed or counted - it\n"+
			"is not compiled, so nothing else in this tree can notice. Remove the tag.",
			len(stale), integrationTag, strings.Join(stale, "\n"))
	}
}

// TestTheTierScanReadsTheSpellingsThisRepositoryUses proves the scan can see an
// offender, and that it does not see an ordinary test.
//
// Its fixtures are assembled from toolchainNeedles rather than written out, for
// the same reason the needles themselves are joined at compile time: a file
// that spelt them plainly would be reported as starting a toolchain because it
// says what starting a toolchain looks like.
func TestTheTierScanReadsTheSpellingsThisRepositoryUses(t *testing.T) {
	t.Parallel()
	for _, source := range []string{
		"path := testkit." + toolchainNeedles[0] + "t)",
		"path := " + toolchainNeedles[1] + "t)",
		"repository := testkit.NewRepo(t).BoundaryFixture()" + toolchainNeedles[2],
		"path, err := exec." + toolchainNeedles[4],
	} {
		if found := matchedNeedles(source); len(found) == 0 {
			t.Errorf("matchedNeedles(%q) found nothing", source)
		}
	}
	for _, source := range []string{
		"result.Repository.Git.Commit = commit",
		"if metadata.Git.Available {",
		"binary := filepath.Join(root, \"go\")",
	} {
		if found := matchedNeedles(source); len(found) != 0 {
			t.Errorf("matchedNeedles(%q) = %q, want none", source, found)
		}
	}
}

// testFile is one test file and what the scan concluded about it.
type testFile struct {
	// path is the file, relative to the module root and slash-separated.
	path string

	// integration reports whether the file carries the build tag.
	integration bool

	// startsToolchain reports whether any needle matched.
	startsToolchain bool

	// needles are the spellings that matched, for the failure message.
	needles []string
}

// scanTestFiles reads every _test.go in the tree.
func scanTestFiles(root string) ([]testFile, error) {
	var found []testFile
	walk := func(path string, entry fs.DirEntry, err error) error {
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
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(entry.Name(), "_test.go") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		source := string(data)
		code, err := withoutComments(path, source)
		if err != nil {
			return err
		}
		needles := matchedNeedles(code)
		found = append(found, testFile{
			path:            relative,
			integration:     hasIntegrationConstraint(source),
			startsToolchain: len(needles) != 0,
			needles:         needles,
		})
		return nil
	}
	if err := filepath.WalkDir(root, walk); err != nil {
		return nil, err
	}
	return found, nil
}

// hasIntegrationConstraint reports whether the file's build constraint names
// the integration tag.
//
// The constraint is parsed rather than grepped, so that a file which mentions
// the tag in prose is not mistaken for one that is gated on it.
//
// What is asked of the parsed expression is whether it *names* the tag, not
// whether it evaluates true with the tag set. Evaluating was the first version
// of this function and it was wrong in a way worth recording: `//go:build
// !windows` evaluates true for a tag set holding nothing but "integration",
// because windows is absent and the negation of absent is present. Three files
// gated on an operating system were reported as stale integration files, which
// would have had somebody delete a correct build constraint on the gate's
// advice.
func hasIntegrationConstraint(source string) bool {
	for line := range strings.SplitSeq(source, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if !strings.HasPrefix(trimmed, "//") {
			if strings.HasPrefix(trimmed, "package ") {
				return false
			}
			continue
		}
		expression, err := constraint.Parse(trimmed)
		if err != nil {
			continue
		}
		if namesTag(expression, integrationTag) {
			return true
		}
	}
	return false
}

// namesTag reports whether a build expression mentions one tag anywhere,
// however it is combined.
func namesTag(expression constraint.Expr, tag string) bool {
	switch typed := expression.(type) {
	case *constraint.TagExpr:
		return typed.Tag == tag
	case *constraint.NotExpr:
		return namesTag(typed.X, tag)
	case *constraint.AndExpr:
		return namesTag(typed.X, tag) || namesTag(typed.Y, tag)
	case *constraint.OrExpr:
		return namesTag(typed.X, tag) || namesTag(typed.Y, tag)
	}
	return false
}

// withoutComments blanks every comment out of a source, keeping every other
// byte where it was.
//
// The rule this gate states is about calls, and a comment is not a call. The
// first version scanned the raw bytes and reported this very file as an
// offender, because the paragraph above explains the rule by writing out the
// spellings it looks for. Rewording the paragraph would have been the wrong
// repair: the gate would still have been unable to tell a call from a sentence,
// and the next file to describe its own behaviour would have paid for it.
//
// Comments are replaced with spaces rather than removed so that no byte moves,
// which keeps the guard on the preceding byte in matchedNeedles meaningful.
func withoutComments(path, source string) (string, error) {
	fileSet := token.NewFileSet()
	parsed, err := parser.ParseFile(fileSet, path, source, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		return "", err
	}
	blanked := []byte(source)
	base := fileSet.File(parsed.Pos()).Base()
	for _, group := range parsed.Comments {
		for _, comment := range group.List {
			start := int(comment.Pos()) - base
			end := int(comment.End()) - base
			for index := start; index < end && index < len(blanked); index++ {
				if blanked[index] != '\n' {
					blanked[index] = ' '
				}
			}
		}
	}
	return string(blanked), nil
}

// matchedNeedles reports which spellings of "starts a real process" a source
// holds.
//
// A needle that begins with an identifier byte is required not to follow one,
// so that AnyGoBinary( does not match GoBinary(. The guard is applied only to
// those needles: .Git() begins with a dot, and the byte before it is the end of
// whatever the method was called on, which is an identifier every time it
// matters. A guard applied to both would be a rule about spelling rather than
// about calls, and would exempt exactly the calls it exists to find.
func matchedNeedles(source string) []string {
	var found []string
	for _, needle := range toolchainNeedles {
		for index := 0; ; {
			at := strings.Index(source[index:], needle)
			if at < 0 {
				break
			}
			at += index
			index = at + len(needle)
			if isIdentifierByte(needle[0]) && at > 0 && isIdentifierByte(source[at-1]) {
				continue
			}
			found = append(found, needle)
			break
		}
	}
	return found
}

// isIdentifierByte reports whether a byte can appear inside a Go identifier.
func isIdentifierByte(character byte) bool {
	switch {
	case character >= 'a' && character <= 'z',
		character >= 'A' && character <= 'Z',
		character >= '0' && character <= '9',
		character == '_':
		return true
	}
	return false
}
