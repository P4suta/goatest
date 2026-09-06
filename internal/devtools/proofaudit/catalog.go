// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"encoding/json"
	"fmt"
	"os"
)

const (
	catalogDocumentType  = "go-mutants/catalog"
	catalogSchemaVersion = 1
)

type branchProof struct {
	BodyStartLine   int `json:"body_start_line"`
	BodyStartColumn int `json:"body_start_column"`
	BodyEndLine     int `json:"body_end_line"`
	BodyEndColumn   int `json:"body_end_column"`
}

type catalogMutant struct {
	ID     string       `json:"id"`
	Path   string       `json:"path"`
	Line   int          `json:"line"`
	Column int          `json:"column"`
	Branch *branchProof `json:"branch"`
}

type catalogDocument struct {
	DocumentType  string          `json:"document_type"`
	SchemaVersion int             `json:"schema_version"`
	Mutants       []catalogMutant `json:"mutants"`
}

type mutantCatalog struct {
	mutants map[string]catalogMutant
}

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

func (catalog *mutantCatalog) lookup(mutant string) (catalogMutant, bool) {
	if catalog == nil {
		return catalogMutant{}, false
	}
	listed, known := catalog.mutants[mutant]
	return listed, known
}

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

func (body branchProof) holds(line, column int) bool {
	return !positionBefore(line, column, body.BodyStartLine, body.BodyStartColumn) &&
		!positionBefore(body.BodyEndLine, body.BodyEndColumn, line, column)
}

func positionBefore(line, column, otherLine, otherColumn int) bool {
	if line != otherLine {
		return line < otherLine
	}
	return column < otherColumn
}
