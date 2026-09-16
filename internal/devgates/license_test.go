//go:build integration

// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package devgates

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"

	"github.com/P4suta/goatest/internal/testkit"
)

// The licence ledger.
//
// Every file this repository ships says under what terms, either in a header it
// carries or in an entry in REUSE.toml. Both directions are checked: a file
// that says neither fails, and an entry matching nothing fails as a claim about
// a file that is gone.
//
// The second direction is the one that earns its keep. An annotation covers a
// path pattern, so an entry survives the deletion of everything it described and
// keeps asserting a licence for nothing. That is invisible to any check that
// only asks whether every file is covered.

const (
	// reuseManifest annotates the files that cannot carry a header.
	reuseManifest = "REUSE.toml"

	// spdxIdentifier is the tag a file carries to say under what terms it is
	// offered.
	spdxIdentifier = "SPDX-License-Identifier"
)

func TestEveryTrackedFileSaysUnderWhatTermsItIsOffered(t *testing.T) {
	t.Parallel()
	root := repositoryRoot(t)
	tracked := trackedFiles(t, root)
	annotated := annotatedPaths(t, filepath.Join(root, reuseManifest))

	var silent []string
	matched := make(map[string]bool, len(annotated))
	for _, relative := range tracked {
		if carriesLicenceHeader(t, filepath.Join(root, filepath.FromSlash(relative))) {
			continue
		}
		pattern, ok := matchAnnotation(annotated, relative)
		if !ok {
			silent = append(silent, relative)
			continue
		}
		matched[pattern] = true
	}
	if len(silent) > 0 {
		slices.Sort(silent)
		t.Errorf("%d tracked file(s) carry no %s header and no %s entry:\n%s\n\n"+
			"Add the header where the format has a comment, and an annotation where it\n"+
			"does not - a golden file compared byte for byte cannot hold one, and a\n"+
			"JSON Lines fixture has nowhere to put it.",
			len(silent), spdxIdentifier, reuseManifest, strings.Join(silent, "\n"))
	}
	for _, pattern := range annotated {
		if !matched[pattern] {
			t.Errorf("%s annotates %q, which matches no tracked file that needs it.\n\n"+
				"An annotation covers a pattern, so it outlives everything it described\n"+
				"and goes on asserting a licence for nothing.", reuseManifest, pattern)
		}
	}
}

// TestTheLicenceLedgerSeesAFileThatSaysNothing proves the ledger can fail.
func TestTheLicenceLedgerSeesAFileThatSaysNothing(t *testing.T) {
	t.Parallel()
	annotated := annotatedPaths(t, filepath.Join(repositoryRoot(t), reuseManifest))
	if len(annotated) == 0 {
		t.Fatal("the manifest annotates nothing, so the ledger would agree with anything")
	}
	if _, ok := matchAnnotation(annotated, "a/file/nothing/covers.bin"); ok {
		t.Fatal("an uncovered path matched an annotation, so the ledger proves nothing")
	}
}

// trackedFiles lists what git holds, which is what a release ships.
//
// Reading git rather than walking the tree is deliberate: a file the working
// tree holds and git does not is not shipped, and asking about its licence
// would fail this gate on every developer's untracked scratch file.
//
// It resolves the binary through the harness rather than naming "git" in an
// exec call. That is not politeness. The tier scan finds the spellings this
// repository uses, and the first version of this file reached for git directly
// and so ran in the unit tier undetected - a gate about honesty, hiding a
// process from the gate about processes. Going through GitBinary also applies
// the missing-tool policy, so a CI job without git fails here instead of
// quietly not checking the licences.
func trackedFiles(t *testing.T, root string) []string {
	t.Helper()
	command := exec.CommandContext(t.Context(), testkit.GitBinary(t), "-C", root, "ls-files")
	output, err := command.Output()
	if err != nil {
		t.Fatalf("list the tracked files: %v", err)
	}
	var tracked []string
	for _, line := range strings.Split(string(output), "\n") {
		if path := strings.TrimSpace(line); path != "" {
			tracked = append(tracked, path)
		}
	}
	return tracked
}

// carriesLicenceHeader reports whether a file states its terms itself.
func carriesLicenceHeader(t *testing.T, path string) bool {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	return strings.Contains(string(data), spdxIdentifier)
}

// annotatedPaths reads the path patterns REUSE.toml annotates.
//
// It decodes the document. The first version read it as lines, on the reasoning
// that the patterns were all this gate needed and a decoder would tie it to a
// shape REUSE owns - which had the fragility exactly backwards. Reading lines
// tied it to the *formatting*, and `taplo fmt` collapsing a multi-line array
// onto one line was enough to make it report every annotated file as unlicensed
// and the string `precedence = "aggregate` as a path pattern. A decoder does not
// care where the newlines are.
func annotatedPaths(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", reuseManifest, err)
	}
	var manifest struct {
		Annotations []struct {
			Path []string `toml:"path"`
		} `toml:"annotations"`
	}
	if err := toml.Unmarshal(data, &manifest); err != nil {
		t.Fatalf("decode %s: %v", reuseManifest, err)
	}
	var patterns []string
	for _, annotation := range manifest.Annotations {
		patterns = append(patterns, annotation.Path...)
	}
	return patterns
}

// matchAnnotation reports which pattern covers one path.
//
// Only the one wildcard form this manifest uses is understood: a trailing `**`
// covering a directory. A pattern this function cannot read is a pattern the
// gate would silently accept everything for, so anything else is compared
// literally.
func matchAnnotation(patterns []string, relative string) (string, bool) {
	for _, pattern := range patterns {
		if prefix, found := strings.CutSuffix(pattern, "/**"); found {
			if strings.HasPrefix(relative, prefix+"/") {
				return pattern, true
			}
			continue
		}
		if pattern == relative {
			return pattern, true
		}
	}
	return "", false
}
