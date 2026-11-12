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
	tracked := trackedFiles(t, testkit.GitBinary(t), root)
	annotated := annotatedPaths(t, filepath.Join(root, reuseManifest))

	var silent []string
	matched := make(map[string]bool, len(annotated))
	for _, relative := range tracked {
		if carriesLicenceHeader(t, filepath.Join(root, filepath.FromSlash(relative))) {
			continue
		}
		covering := matchingAnnotations(annotated, relative)
		if len(covering) == 0 {
			silent = append(silent, relative)
			continue
		}
		for _, pattern := range covering {
			matched[pattern] = true
		}
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
				"Two things look like this and the wording is deliberate. Either the\n"+
				"files it described are gone - an annotation covers a pattern, so it\n"+
				"outlives them and goes on asserting a licence for nothing - or they are\n"+
				"here and carry their own headers, which this manifest exists to avoid\n"+
				"needing. Both are entries to delete; neither is a file without a licence.",
				reuseManifest, pattern)
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
	if len(matchingAnnotations(annotated, "a/file/nothing/covers.bin")) != 0 {
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
// trackedFiles lists what git holds, given the git a caller resolved.
//
// The binary travels as an argument rather than being resolved here, so that
// every file which reaches git says so in its own text. internal/devgates
// refuses a file that carries //go:build integration and starts nothing, and it
// decides that by reading the file rather than by following calls - a helper
// that resolved the binary out of sight would leave its callers looking like
// unit tests wearing the tag.
func trackedFiles(t *testing.T, git, root string) []string {
	t.Helper()
	command := exec.CommandContext(t.Context(), git, "-C", root, "ls-files")
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
// matchingAnnotations returns every annotation covering a file, not the first.
//
// Every, because the caller uses the result twice: to decide that a file is
// covered, for which one would do, and to mark a pattern as having covered
// something, for which one will not. Two patterns can legitimately cover the
// same file, and stopping at the first reports the other as annotating nothing
// - the ledger accusing a correct entry of being stale.
//
// The case is narrower than it first looks, and the difference was found by
// trying it rather than by reasoning about it. Two entries listing the same
// path are indistinguishable here, because this marks patterns and identical
// patterns are one key; the version that stopped at the first match passes that
// test. What it does not pass is a specific path annotated alongside a glob
// that already covers it - `internal/testkit/testdata/golden_sample.txt` beside
// `internal/testkit/testdata/**` - where the glob is reached first and the
// specific entry is reported as matching nothing. Folding this tree into
// go-mutants merges two manifests, which is where a broad entry meets a narrow
// one for the first time.
//
// The only wildcard implemented is a trailing `/**`, matched as a prefix so
// that it crosses directory separators. That is what REUSE means by it, and
// path.Match would be the wrong tool for saying so: its wildcards stop at a
// separator, so `testdata/**` would cover a file one level down and not one two
// levels down. go-mutants had exactly that, and the way it fails is the worst
// available - the annotation still matches something, so it is not reported as
// stale, while the deeper files are reported as carrying no licence. A reader
// is then sent to look for a missing header in a file the manifest says is
// annotated: a true sentence about the wrong subject.
//
// go-mutants settled the behaviour by asking the reuse tool rather than by
// reading the specification again, on the ground that a misreading is not
// repaired by the same reading. With `path = ["data/**"]` and a file at
// data/deep/nested.txt, reuse 3.3 reports the tree compliant; changing the
// entry to `data/*` makes it report that file as missing its licensing
// information. Both directions, one tool, no interpretation.
func matchingAnnotations(patterns []string, relative string) []string {
	var covering []string
	for _, pattern := range patterns {
		if prefix, found := strings.CutSuffix(pattern, "/**"); found {
			if strings.HasPrefix(relative, prefix+"/") {
				covering = append(covering, pattern)
			}
			continue
		}
		if pattern == relative {
			covering = append(covering, pattern)
		}
	}
	return covering
}

// TestEveryAnnotationPatternIsOneThisGateImplements refuses a pattern whose
// meaning this matcher is guessing at.
//
// Returning false for an unimplemented wildcard would not be silent - the
// annotation would match nothing and the ledger would report it as stale - but
// it would be wrong about why, and a gate that misnames the cause costs more
// than one that stays quiet. Only the forms above are accepted: an exact path,
// or a path ending in `/**`. `?` and character classes are refused not because
// REUSE forbids them but because nobody here has watched REUSE apply them.
func TestEveryAnnotationPatternIsOneThisGateImplements(t *testing.T) {
	t.Parallel()
	root := repositoryRoot(t)
	patterns := annotatedPaths(t, filepath.Join(root, reuseManifest))
	if len(patterns) == 0 {
		t.Fatalf("%s yielded no annotated path, and this repository has several", reuseManifest)
	}
	for _, pattern := range patterns {
		bare, _ := strings.CutSuffix(pattern, "/**")
		if strings.ContainsAny(bare, "*?[") {
			t.Errorf("%s annotates %q, and this gate implements only an exact path or a trailing /**; it would report the entry as covering nothing, which is the wrong reason",
				reuseManifest, pattern)
		}
	}
}
