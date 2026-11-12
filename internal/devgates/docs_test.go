// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package devgates

import (
	"errors"
	"fmt"
	"go/ast"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// documentedPackagesPath is the ledger of packages whose exported identifiers
// must each carry a doc comment, relative to the module root.
//
// It is the mirror image of seam_allowlist.txt. That ledger may only shrink,
// because every line on it is a debt. This one may only grow, because every
// line on it is a package that has been documented and must stay documented.
// A package the ledger names but the tree no longer holds fails the gate just
// as loudly as an undocumented identifier, so a package cannot be deleted and
// leave its claim behind.
//
// The ledger exists because the repository had no doc comments at all when the
// gate was written. A gate that demanded them everywhere on the first day would
// have been turned off on the first day. This one is a ratchet a package passes
// through once.
const documentedPackagesPath = "internal/devgates/documented_packages.txt"

// docLinkPattern matches a Go doc link: [Name], [Type.Method] or [pkg.Name].
//
// The three forms are indistinguishable by shape — [T.Scope] and [testing.TB]
// are the same characters in the same order — so the qualifier is resolved
// rather than classified: a qualifier this package declares is a type, and a
// qualifier the file imports is a package. That ambiguity is Go's, not this
// gate's, and resolving is the only way through it.
//
// A span containing a space or a slash is not matched at all, so a doc comment
// may write [something like this] as ordinary prose without the gate reading it
// as a broken link. A gate that fails on English is a gate that gets switched
// off.
var docLinkPattern = regexp.MustCompile(`\[([A-Za-z_][A-Za-z0-9_]*(?:\.[A-Za-z_][A-Za-z0-9_]*)?)\]`)

// backquotedPattern matches a `backquoted` span inside a comment.
var backquotedPattern = regexp.MustCompile("`([^`\n]+)`")

// pathishPattern matches a token that is shaped like a path into this
// repository: at least one slash, and a final element carrying an extension.
var pathishPattern = regexp.MustCompile(`^[A-Za-z0-9_.\-/]+/[A-Za-z0-9_.\-]+\.[A-Za-z0-9]+$`)

// TestDocumentedPackagesDocumentEveryExportedIdentifier is the coverage half of
// the documentation policy.
//
// An exported identifier with no doc comment is a name a reader has to guess
// from, and `go doc` and pkg.go.dev show nothing but the signature. For a
// package on the ledger that is a failure rather than a style note.
func TestDocumentedPackagesDocumentEveryExportedIdentifier(t *testing.T) {
	t.Parallel()
	root := repositoryRoot(t)
	documented, err := readDocumentedPackages(filepath.Join(root, filepath.FromSlash(documentedPackagesPath)))
	if err != nil {
		t.Fatalf("read %s: %v", documentedPackagesPath, err)
	}
	files, err := parseProduction(root)
	if err != nil {
		t.Fatalf("parse the production sources: %v", err)
	}
	present := make(map[string]bool, len(documented))
	for _, pkg := range documented {
		present[pkg] = false
	}
	var missing []string
	for _, parsed := range files {
		if _, ok := present[parsed.pkg]; !ok {
			continue
		}
		present[parsed.pkg] = true
		missing = append(missing, undocumentedExports(parsed)...)
	}
	for _, pkg := range documented {
		if !present[pkg] {
			t.Errorf("%s records package %q, which holds no production Go file.\n\n"+
				"The ledger may only grow, and a line it keeps after the package "+
				"is gone is a claim nothing checks. Delete the line in the commit "+
				"that removed the package.", documentedPackagesPath, pkg)
		}
	}
	if len(missing) > 0 {
		slices.Sort(missing)
		t.Errorf("%d exported identifier(s) in a documented package carry no doc comment:\n%s\n\n"+
			"Every package named in %s documents all of its exported names. Write "+
			"the comment, or take the package off the ledger in the same commit and "+
			"say why in the review.",
			len(missing), strings.Join(missing, "\n"), documentedPackagesPath)
	}
}

// TestDocumentedPackagesLedgerIsSortedAndFreeOfDuplicates keeps the ledger
// readable as a diff, the way the seam ledger is kept.
func TestDocumentedPackagesLedgerIsSortedAndFreeOfDuplicates(t *testing.T) {
	t.Parallel()
	root := repositoryRoot(t)
	documented, err := readDocumentedPackages(filepath.Join(root, filepath.FromSlash(documentedPackagesPath)))
	if err != nil {
		t.Fatalf("read %s: %v", documentedPackagesPath, err)
	}
	if !slices.IsSorted(documented) {
		t.Errorf("%s is not sorted; sort the entries by package path", documentedPackagesPath)
	}
	for index := 1; index < len(documented); index++ {
		if documented[index] == documented[index-1] {
			t.Errorf("%s records %q twice", documentedPackagesPath, documented[index])
		}
	}
}

// TestDocCommentsNamePathsThatExist is the staleness half of the documentation
// policy, for the claims a comment makes about the tree.
//
// This gate is what replaced the ban on comments that this package used to
// enforce. The ban rested on a true premise — a comment rots, and a rotted
// comment sends a reader somewhere that is not there — but it drew the wrong
// conclusion from it. Refusing every comment does not stop the rot; it removes
// the material a reader needs in order to diagnose anything, which is the
// opposite of what this repository is for. Checking the claims keeps the
// premise and discards the conclusion.
func TestDocCommentsNamePathsThatExist(t *testing.T) {
	t.Parallel()
	root := repositoryRoot(t)
	files, err := parseProduction(root)
	if err != nil {
		t.Fatalf("parse the production sources: %v", err)
	}
	var broken []string
	for _, parsed := range files {
		for _, group := range parsed.file.Comments {
			for _, reference := range repositoryPaths(group.Text()) {
				if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(reference))); err != nil {
					broken = append(broken, fmt.Sprintf("%s: %s", parsed.path, reference))
				}
			}
		}
	}
	if len(broken) > 0 {
		slices.Sort(broken)
		broken = slices.Compact(broken)
		t.Errorf("%d comment(s) name a path this repository does not hold:\n%s\n\n"+
			"A comment that points somewhere that is not there costs a reader more "+
			"than no comment at all. Fix the path, or drop the reference.",
			len(broken), strings.Join(broken, "\n"))
	}
}

// TestDocLinksNameIdentifiersThatExist is the staleness half for the claims a
// comment makes about code.
//
// A [Name] that resolves to nothing renders as literal brackets in `go doc`
// and as a dead link on pkg.go.dev, which is how a renamed identifier announces
// that its documentation was not renamed with it.
func TestDocLinksNameIdentifiersThatExist(t *testing.T) {
	t.Parallel()
	root := repositoryRoot(t)
	files, err := parseProduction(root)
	if err != nil {
		t.Fatalf("parse the production sources: %v", err)
	}
	declared := make(map[string]map[string]struct{})
	for _, parsed := range files {
		names, ok := declared[parsed.pkg]
		if !ok {
			names = make(map[string]struct{})
			declared[parsed.pkg] = names
		}
		for _, name := range declaredNames(parsed.file) {
			names[name] = struct{}{}
		}
	}
	var broken []string
	for _, parsed := range files {
		imported, err := importedNames(parsed)
		if err != nil {
			t.Fatalf("read the imports of %s: %v", parsed.path, err)
		}
		for _, group := range parsed.file.Comments {
			for _, link := range docLinkPattern.FindAllStringSubmatch(group.Text(), -1) {
				if resolvableDocLink(link[1], declared[parsed.pkg], imported) {
					continue
				}
				broken = append(broken, fmt.Sprintf("%s: [%s]", parsed.path, link[1]))
			}
		}
	}
	if len(broken) > 0 {
		slices.Sort(broken)
		broken = slices.Compact(broken)
		t.Errorf("%d doc link(s) name an identifier that is not reachable from the file:\n%s\n\n"+
			"A bare [Name] must be declared in the same package, and a [pkg.Name] "+
			"must name a package the file imports. Fix the link, or write the name "+
			"as ordinary prose.",
			len(broken), strings.Join(broken, "\n"))
	}
}

// TestThePathGateSeesAPathThatIsNotThere proves the path gate can fail.
//
// A gate that has only ever been observed passing is a gate whose failure has
// never been observed, and those are not the same claim.
func TestThePathGateSeesAPathThatIsNotThere(t *testing.T) {
	t.Parallel()
	found := repositoryPaths("the ledger lives at `internal/devgates/absent_ledger.txt` today")
	if len(found) != 1 || found[0] != "internal/devgates/absent_ledger.txt" {
		t.Fatalf("repositoryPaths = %q, want the one absent path", found)
	}
	root := repositoryRoot(t)
	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(found[0]))); err == nil {
		t.Fatal("the fixture path exists, so this test cannot show the gate failing")
	}
}

// TestThePathGateIgnoresWhatIsNotARepositoryPath pins the exclusions, because
// each of them is a way the gate could fail on something correct.
func TestThePathGateIgnoresWhatIsNotARepositoryPath(t *testing.T) {
	t.Parallel()
	for _, prose := range []string{
		"see `https://example.test/a/b.html` for the format",
		"the module is `github.com/P4suta/go-mutants`",
		"it returns `map[string]struct{}` for the caller",
		"the flag is `-test.run`",
	} {
		if found := repositoryPaths(prose); len(found) != 0 {
			t.Errorf("repositoryPaths(%q) = %q, want none", prose, found)
		}
	}
}

// TestTheDocLinkGateSeesALinkThatResolvesToNothing proves the link gate can
// fail, for the reason the path gate's twin exists.
func TestTheDocLinkGateSeesALinkThatResolvesToNothing(t *testing.T) {
	t.Parallel()
	declared := map[string]struct{}{"Present": {}}
	imported := map[string]struct{}{"testing": {}}
	if !resolvableDocLink("Present", declared, imported) {
		t.Error("a name declared in the package did not resolve")
	}
	if !resolvableDocLink("testing.TB", declared, imported) {
		t.Error("a name qualified by an imported package did not resolve")
	}
	if !resolvableDocLink("Present.Method", declared, imported) {
		t.Error("a method of a type declared in the package did not resolve")
	}
	if resolvableDocLink("Absent", declared, imported) {
		t.Error("a name declared nowhere resolved")
	}
	if resolvableDocLink("unimported.Name", declared, imported) {
		t.Error("a name qualified by a package the file does not import resolved")
	}
	if resolvableDocLink("Absent.Method", declared, imported) {
		t.Error("a method of a type declared nowhere resolved")
	}
}

// undocumentedExports reports the exported identifiers of one file that carry
// no doc comment, as "path:line name".
func undocumentedExports(parsed parsedFile) []string {
	var missing []string
	report := func(position ast.Node, name string) {
		missing = append(missing, fmt.Sprintf("%s: %s", parsed.path, name))
		_ = position
	}
	if parsed.file.Doc == nil && packageDocFile(parsed) {
		report(parsed.file, "package "+parsed.file.Name.Name)
	}
	for _, declaration := range parsed.file.Decls {
		switch typed := declaration.(type) {
		case *ast.FuncDecl:
			if !typed.Name.IsExported() || typed.Doc != nil {
				continue
			}
			if receiver := receiverName(typed); receiver != "" {
				report(typed, receiver+"."+typed.Name.Name)
				continue
			}
			report(typed, typed.Name.Name)
		case *ast.GenDecl:
			for _, name := range undocumentedSpecNames(typed) {
				report(typed, name)
			}
		}
	}
	return missing
}

// packageDocFile reports whether this file is the one that should carry the
// package comment: doc.go when the package has one, and otherwise nothing.
//
// Demanding a package comment on every file would demand the same paragraph
// many times, and Go would then concatenate them.
func packageDocFile(parsed parsedFile) bool {
	return filepath.Base(parsed.path) == "doc.go"
}

// undocumentedSpecNames reports the exported names a const, var or type
// declaration leaves undocumented.
//
// A comment on the declaration covers every spec inside it, which is how a
// grouped const block documents its members collectively.
func undocumentedSpecNames(declaration *ast.GenDecl) []string {
	var missing []string
	for _, specification := range declaration.Specs {
		switch typed := specification.(type) {
		case *ast.TypeSpec:
			if typed.Name.IsExported() && typed.Doc == nil && declaration.Doc == nil {
				missing = append(missing, typed.Name.Name)
			}
		case *ast.ValueSpec:
			if typed.Doc != nil || declaration.Doc != nil {
				continue
			}
			for _, name := range typed.Names {
				if name.IsExported() {
					missing = append(missing, name.Name)
				}
			}
		}
	}
	return missing
}

// receiverName reports the type a method hangs off, or "" for a plain function.
//
// Only methods on exported types are demanded, because a method on an
// unexported type is not reachable from outside the package however exported
// its own name is.
func receiverName(declaration *ast.FuncDecl) string {
	if declaration.Recv == nil || len(declaration.Recv.List) == 0 {
		return ""
	}
	expression := declaration.Recv.List[0].Type
	if star, ok := expression.(*ast.StarExpr); ok {
		expression = star.X
	}
	identifier, ok := expression.(*ast.Ident)
	if !ok || !identifier.IsExported() {
		return "unexported"
	}
	return identifier.Name
}

// declaredNames reports every top-level name a file declares, exported or not.
func declaredNames(file *ast.File) []string {
	var names []string
	for _, declaration := range file.Decls {
		switch typed := declaration.(type) {
		case *ast.FuncDecl:
			if typed.Recv == nil {
				names = append(names, typed.Name.Name)
			}
		case *ast.GenDecl:
			for _, specification := range typed.Specs {
				switch spec := specification.(type) {
				case *ast.TypeSpec:
					names = append(names, spec.Name.Name)
				case *ast.ValueSpec:
					for _, name := range spec.Names {
						names = append(names, name.Name)
					}
				}
			}
		}
	}
	return names
}

// importedNames reports the identifiers a file may qualify a name with: the
// explicit alias where there is one, and otherwise the last element of the
// import path.
//
// The last element is a guess — a package may name itself something else — but
// it is the same guess a reader makes, and this gate is about what a reader can
// follow.
func importedNames(parsed parsedFile) (map[string]struct{}, error) {
	names := make(map[string]struct{}, len(parsed.file.Imports))
	for _, imported := range parsed.file.Imports {
		path, err := strconv.Unquote(imported.Path.Value)
		if err != nil {
			return nil, err
		}
		if imported.Name != nil {
			names[imported.Name.Name] = struct{}{}
			continue
		}
		elements := strings.Split(path, "/")
		names[elements[len(elements)-1]] = struct{}{}
	}
	return names, nil
}

// resolvableDocLink reports whether one doc link points at something the file
// can reach.
//
// A qualified link resolves if its qualifier is either a type this package
// declares — [TestScope.Capabilities] — or a package the file imports —
// [testing.TB]. The method or field after the dot is not checked: doing so
// would need the type information this gate deliberately does without, and the
// qualifier is where a rename actually breaks the link.
func resolvableDocLink(link string, declared, imported map[string]struct{}) bool {
	qualifier, name, qualified := strings.Cut(link, ".")
	if !qualified {
		_, ok := declared[link]
		return ok
	}
	if name == "" {
		return false
	}
	if _, ok := declared[qualifier]; ok {
		return true
	}
	_, ok := imported[qualifier]
	return ok
}

// repositoryPaths reports the backquoted spans of one comment that are shaped
// like a path into this repository.
//
// The shape test is deliberately narrow. A token with no slash is a name, not a
// path; a token carrying a scheme is a URL; a token whose first element holds a
// dot is a module path, which is not a file. Each exclusion is a way the gate
// could otherwise fail on a comment that is telling the truth.
func repositoryPaths(text string) []string {
	var found []string
	for _, match := range backquotedPattern.FindAllStringSubmatch(text, -1) {
		token := strings.TrimSpace(match[1])
		if strings.Contains(token, "://") || !pathishPattern.MatchString(token) {
			continue
		}
		if first, _, _ := strings.Cut(token, "/"); strings.Contains(first, ".") {
			continue
		}
		found = append(found, token)
	}
	return found
}

// readDocumentedPackages reads the ledger, which is one package path per line
// with # comments and blank lines ignored.
func readDocumentedPackages(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var documented []string
	for number, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if strings.ContainsAny(trimmed, " \t") {
			return nil, fmt.Errorf("%s:%d: want one package path, got %q", path, number+1, trimmed)
		}
		documented = append(documented, trimmed)
	}
	return documented, nil
}
