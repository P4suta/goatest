// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"encoding/json"
	"fmt"
	"os"
)

const (
	// catalogDocumentType and catalogSchemaVersion are the one document this
	// audit reads: the catalog `go-mutants list --json` writes. A document of
	// another kind, or of a schema version this audit has never seen, may name
	// the same fields and mean something else by them, so it is refused rather
	// than read on the chance that it agrees.
	catalogDocumentType  = "go-mutants/catalog"
	catalogSchemaVersion = 1
)

// branchProof is the branch-never-taken proof a catalog attaches to a mutant
// whose mutated condition implies the original one. The condition is inert, so
// a test during which no statement of the body it gates ran cannot observe the
// mutation.
//
// The body is the span from its opening brace to its closing one, inclusive of
// both ends. The direction the catalog also records is diagnostic — it says
// which way the condition was weakened — and the rule never branches on it, so
// it is not read here.
type branchProof struct {
	BodyStartLine   int `json:"body_start_line"`
	BodyStartColumn int `json:"body_start_column"`
	BodyEndLine     int `json:"body_end_line"`
	BodyEndColumn   int `json:"body_end_column"`
}

// catalogMutant is the part of a catalogued mutant the branch layer decides by:
// where the engine placed the mutation, and the body its condition gates when
// the engine proved one. Everything else a catalog carries — the operator, the
// replacement text, the byte offsets — is read past.
type catalogMutant struct {
	ID     string       `json:"id"`
	Path   string       `json:"path"`
	Line   int          `json:"line"`
	Column int          `json:"column"`
	Branch *branchProof `json:"branch"`
}

// catalogDocument is a catalog as it is written: what it claims to be, and the
// mutants it lists.
type catalogDocument struct {
	DocumentType  string          `json:"document_type"`
	SchemaVersion int             `json:"schema_version"`
	Mutants       []catalogMutant `json:"mutants"`
}

// mutantCatalog is a decoded catalog: what it says about a mutant, by identity.
type mutantCatalog struct {
	mutants map[string]catalogMutant
}

// readCatalog reads a `go-mutants list --json` document. Only the fields the
// branch layer decides by are decoded and every other one is ignored, because
// an audit is run against catalogs older and newer than itself — that is what
// makes it an audit rather than a self-check — and a field a later version
// added is not a reason to refuse the proofs the document carries. What the
// document claims to be is checked all the same: reading another kind of
// document as a catalog would print a soundness result with nothing behind it.
func readCatalog(path string) (*mutantCatalog, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read the mutant catalog %s: %w", path, err)
	}
	var document catalogDocument
	if err := json.Unmarshal(data, &document); err != nil {
		return nil, fmt.Errorf("read the mutant catalog %s: %w", path, err)
	}
	if document.DocumentType != catalogDocumentType || document.SchemaVersion != catalogSchemaVersion {
		return nil, fmt.Errorf("%s is a %q document of schema version %d, not a %q of schema version %d",
			path, document.DocumentType, document.SchemaVersion, catalogDocumentType, catalogSchemaVersion)
	}
	catalog := &mutantCatalog{mutants: make(map[string]catalogMutant, len(document.Mutants))}
	for _, mutant := range document.Mutants {
		catalog.mutants[mutant.ID] = mutant
	}
	return catalog, nil
}

// lookup returns what the catalog says about one mutant. A run audited without
// -catalog has no catalog at all, and an absent catalog lists nothing rather
// than being a case every caller has to remember.
func (catalog *mutantCatalog) lookup(mutant string) (catalogMutant, bool) {
	if catalog == nil {
		return catalogMutant{}, false
	}
	listed, known := catalog.mutants[mutant]
	return listed, known
}

// proves reports whether the catalog carries a branch proof for one mutant that
// the layer may act on at all, and returns it.
//
// The checks are fail-closed, because every one of them is a state in which the
// audit does not know what the span means: coordinates below one are not source
// positions, a span whose end precedes its start is not a body, and a mutation
// that does not precede the body it is said to gate is not the condition of
// that body. Nothing is discharged from any of them.
func (listed catalogMutant) proves() (branchProof, bool) {
	body := listed.Branch
	if body == nil {
		return branchProof{}, false
	}
	if body.BodyStartLine < 1 || body.BodyStartColumn < 1 || body.BodyEndLine < 1 || body.BodyEndColumn < 1 {
		return branchProof{}, false
	}
	if listed.Line < 1 || listed.Column < 1 {
		return branchProof{}, false
	}
	if positionBefore(body.BodyEndLine, body.BodyEndColumn, body.BodyStartLine, body.BodyStartColumn) {
		return branchProof{}, false
	}
	if !positionBefore(listed.Line, listed.Column, body.BodyStartLine, body.BodyStartColumn) {
		return branchProof{}, false
	}
	return *body, true
}

// holds reports whether a position lies in the body, both ends included. The
// body runs from its opening brace to its closing one, and a block starting on
// either of them is a block of the body.
func (body branchProof) holds(line, column int) bool {
	return !positionBefore(line, column, body.BodyStartLine, body.BodyStartColumn) &&
		!positionBefore(body.BodyEndLine, body.BodyEndColumn, line, column)
}

// positionBefore compares two 1-based source positions: lines first, then byte
// columns. It is the whole of the ordering the branch layer decides by, which
// is a comparison of where a block starts and never of what it contains.
func positionBefore(line, column, otherLine, otherColumn int) bool {
	if line != otherLine {
		return line < otherLine
	}
	return column < otherColumn
}
