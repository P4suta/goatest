// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package devgates

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// The fixtures below are parsed, never compiled: they are written to a
// temporary directory outside the module so the scanner can be held to its
// rules on declarations this repository does not happen to contain.

// TestScanSeamsFindsReplaceableDeclarations holds the scanner to the four
// shapes that make a package-level var replaceable behaviour.
func TestScanSeamsFindsReplaceableDeclarations(t *testing.T) {
	t.Parallel()
	found := scanFixture(t, map[string]string{
		"hooked/hooks.go": `package hooked

import (
	"os"
	"path/filepath"
)

type fileHooks struct {
	remove func(string) error
	rename func(string, string) error
}

type cleanup func(string) error

func work(path string) error { return nil }

var literalSeam = func(path string) error { return nil }

var packageFunctionSeam = work

var importedSeam = os.Remove

var qualifiedSeam = filepath.Rel

var declaredTypeSeam func(string) error

var namedTypeSeam cleanup

var hookStructSeam = fileHooks{remove: os.Remove, rename: os.Rename}

var pointerHookSeam = &fileHooks{remove: os.Remove}

var firstPairSeam, secondPairSeam = os.Remove, os.Rename
`,
	})
	want := []string{
		"hooked declaredTypeSeam",
		"hooked firstPairSeam",
		"hooked hookStructSeam",
		"hooked importedSeam",
		"hooked literalSeam",
		"hooked namedTypeSeam",
		"hooked packageFunctionSeam",
		"hooked pointerHookSeam",
		"hooked qualifiedSeam",
		"hooked secondPairSeam",
	}
	if !slices.Equal(found, want) {
		t.Errorf("scanSeams reported\n%s\nwant\n%s", strings.Join(found, "\n"), strings.Join(want, "\n"))
	}
}

// TestScanSeamsIgnoresData keeps the gate off the declarations a test never
// replaces: sentinels, compiled-in assets, tables, zero-valued concurrency
// primitives, and the blank identifier.
func TestScanSeamsIgnoresData(t *testing.T) {
	t.Parallel()
	found := scanFixture(t, map[string]string{
		"data/data.go": `package data

import (
	"errors"
	"flag"
	"fmt"
	"html/template"
	"os"
	"sync"
	"sync/atomic"
)

type settings struct {
	name   string
	remove func(string) error
}

var errSentinel = errors.New("data: sentinel")

var errWrapped = fmt.Errorf("data: %w", errSentinel)

var page = template.Must(template.New("page").Parse("{{.}}"))

var updateGolden = flag.Bool("update", false, "rewrite the golden files")

var version = "v0.1.0-dev"

var names = []string{"one", "two"}

var limits = map[string]int{"one": 1}

var guard sync.Mutex

var once sync.Once

var sequence atomic.Uint64

var configuration = settings{name: "one"}

var _ = os.Remove

func build() {
	var localSeam = os.Remove
	_ = localSeam
}
`,
		"data/embedded.go": `package data

import (
	_ "embed"
	"os"
)

//go:embed schema.json
var schemaDocument []byte

//go:embed schema.json
var directiveWins = os.Remove
`,
	})
	if len(found) > 0 {
		t.Errorf("scanSeams reported data as seams:\n%s", strings.Join(found, "\n"))
	}
}

// TestScanSeamsReadsEveryFileOfAPackage keeps a build-constrained file inside
// the gate: the scan applies no constraints, so a seam behind //go:build is
// counted once wherever it is declared and a function declared in one file is
// recognised from another.
func TestScanSeamsReadsEveryFileOfAPackage(t *testing.T) {
	t.Parallel()
	found := scanFixture(t, map[string]string{
		"platform/tree.go": `package platform

var prepareCommand = prepare
`,
		"platform/tree_unix.go": `//go:build unix

package platform

import "syscall"

func prepare() error { return nil }

var killGroup = syscall.Kill

var killProcess = prepare
`,
		"platform/tree_windows.go": `//go:build windows

package platform

import "golang.org/x/sys/windows"

var closeHandle = windows.CloseHandle

var killProcess = prepare
`,
	})
	want := []string{
		"platform closeHandle",
		"platform killGroup",
		"platform killProcess",
		"platform prepareCommand",
	}
	if !slices.Equal(found, want) {
		t.Errorf("scanSeams reported\n%s\nwant\n%s", strings.Join(found, "\n"), strings.Join(want, "\n"))
	}
}

// TestScanSeamsSkipsWhatIsNotProduction keeps the gate to the shipped tree:
// tests, fixtures, tool-ignored directories, and the generated output
// directories at the repository root are not production code.
func TestScanSeamsSkipsWhatIsNotProduction(t *testing.T) {
	t.Parallel()
	found := scanFixture(t, map[string]string{
		"root.go":                     "package root\n\nimport \"os\"\n\nvar rootSeam = os.Remove\n",
		"kept/keep_test.go":           "package kept\n\nimport \"os\"\n\nvar testSeam = os.Remove\n",
		"kept/testdata/fixture.go":    "package fixture\n\nimport \"os\"\n\nvar fixtureSeam = os.Remove\n",
		"kept/nested/dist/nested.go":  "package nested\n\nimport \"os\"\n\nvar nestedSeam = os.Remove\n",
		"dist/generated.go":           "package generated\n\nimport \"os\"\n\nvar distSeam = os.Remove\n",
		"reports/generated.go":        "package generated\n\nimport \"os\"\n\nvar reportSeam = os.Remove\n",
		"vendor/library/library.go":   "package library\n\nimport \"os\"\n\nvar vendorSeam = os.Remove\n",
		".hidden/hidden.go":           "package hidden\n\nimport \"os\"\n\nvar hiddenSeam = os.Remove\n",
		"_ignored/ignored.go":         "package ignored\n\nimport \"os\"\n\nvar ignoredSeam = os.Remove\n",
		"kept/notes.md":               "var notASeam = os.Remove\n",
		"kept/production/keep.go":     "package production\n\nimport \"os\"\n\nvar keptSeam = os.Remove\n",
		"kept/production/local.go":    "package production\n\ntype defaults struct{ limit int }\n\nvar values = defaults{limit: 1}\n\nvar limit = values.limit\n",
		"kept/production/aliased.go":  "package production\n\nimport toml \"github.com/pelletier/go-toml/v2\"\n\nvar aliasedSeam = toml.Marshal\n",
		"kept/production/versions.go": "package production\n\nimport \"github.com/santhosh-tekuri/jsonschema/v6\"\n\nvar versionedSeam = jsonschema.NewCompiler\n",
	})
	want := []string{
		". rootSeam",
		"kept/nested/dist nestedSeam",
		"kept/production aliasedSeam",
		"kept/production keptSeam",
		"kept/production versionedSeam",
	}
	if !slices.Equal(found, want) {
		t.Errorf("scanSeams reported\n%s\nwant\n%s", strings.Join(found, "\n"), strings.Join(want, "\n"))
	}
}

// TestScanSeamsRejectsUnparsableSource keeps a broken tree loud: a file the
// parser cannot read is never silently scanned as empty.
func TestScanSeamsRejectsUnparsableSource(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeSource(t, root, "broken/broken.go", "package broken\n\nvar = os.Remove\n")
	if _, err := scanSeams(root); err == nil {
		t.Fatal("scanSeams accepted a source the parser cannot read")
	}
}

// TestReadAllowlistReadsTheLedgerFormat holds the ledger parser to one seam
// per line, with room for the documentation that explains the ratchet.
func TestReadAllowlistReadsTheLedgerFormat(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	path := filepath.Join(root, "seam_allowlist.txt")
	ledger := "# a comment\n\ninternal/cache readCacheFile\ninternal/evidence removeGraphFile\n"
	if err := os.WriteFile(path, []byte(ledger), 0o644); err != nil {
		t.Fatalf("write the ledger: %v", err)
	}
	allowed, err := readAllowlist(path)
	if err != nil {
		t.Fatalf("read the ledger: %v", err)
	}
	want := []seam{
		{pkg: "internal/cache", name: "readCacheFile"},
		{pkg: "internal/evidence", name: "removeGraphFile"},
	}
	if !slices.Equal(allowed, want) {
		t.Errorf("readAllowlist returned %v, want %v", allowed, want)
	}
}

// TestReadAllowlistRejectsAMalformedEntry keeps a typo from quietly allowing
// nothing, or everything.
func TestReadAllowlistRejectsAMalformedEntry(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	path := filepath.Join(root, "seam_allowlist.txt")
	if err := os.WriteFile(path, []byte("internal/cache\n"), 0o644); err != nil {
		t.Fatalf("write the ledger: %v", err)
	}
	if _, err := readAllowlist(path); err == nil {
		t.Fatal("readAllowlist accepted an entry that names no seam")
	}
}

// TestReadAllowlistTreatsAMissingLedgerAsEmpty keeps a deleted ledger failing
// the ratchet with the seams it should have recorded, rather than erroring
// before the gate can report them.
func TestReadAllowlistTreatsAMissingLedgerAsEmpty(t *testing.T) {
	t.Parallel()
	allowed, err := readAllowlist(filepath.Join(t.TempDir(), "seam_allowlist.txt"))
	if err != nil {
		t.Fatalf("read a ledger that does not exist: %v", err)
	}
	if len(allowed) > 0 {
		t.Errorf("readAllowlist returned %v for a ledger that does not exist", allowed)
	}
}

// scanFixture writes the sources to a temporary tree, scans it, and returns
// the seams in ledger format.
func scanFixture(t *testing.T, sources map[string]string) []string {
	t.Helper()
	root := t.TempDir()
	for relative, source := range sources {
		writeSource(t, root, relative, source)
	}
	found, err := scanSeams(root)
	if err != nil {
		t.Fatalf("scan the fixture: %v", err)
	}
	return formatSeamLines(found)
}

// writeSource writes one fixture file, creating the directories it needs.
func writeSource(t *testing.T, root, relative, source string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create the directory of %s: %v", relative, err)
	}
	if err := os.WriteFile(path, []byte(source), 0o644); err != nil {
		t.Fatalf("write %s: %v", relative, err)
	}
}
