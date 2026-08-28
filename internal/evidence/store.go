// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package evidence

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const GraphSchemaV1 = "evidence-graph-v1"

type GraphRecord struct {
	Schema     string `json:"schema"`
	ModulePath string `json:"module_path"`
	Graph      Graph  `json:"graph"`
}

func LoadGraph(path string) (GraphRecord, bool, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return GraphRecord{}, false, nil
	}
	if err != nil {
		return GraphRecord{}, false, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var record GraphRecord
	if err := decoder.Decode(&record); err != nil {
		return GraphRecord{}, false, fmt.Errorf("goatest: decode evidence graph: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return GraphRecord{}, false, fmt.Errorf("goatest: evidence graph has trailing data")
	}
	if record.Schema != GraphSchemaV1 || record.ModulePath == "" {
		return GraphRecord{}, false, fmt.Errorf("goatest: evidence graph identity mismatch")
	}
	return record, true, nil
}

func SaveGraph(path string, record GraphRecord) error {
	if record.ModulePath == "" {
		return fmt.Errorf("goatest: evidence graph requires a module path")
	}
	record.Schema = GraphSchemaV1
	graphData, err := record.Graph.JSON()
	if err != nil {
		return err
	}
	var canonicalGraph Graph
	if err := json.Unmarshal(graphData, &canonicalGraph); err != nil {
		return err
	}
	record.Graph = canonicalGraph
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".graph-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		if removeErr := os.Remove(path); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			return errors.Join(err, removeErr)
		}
		return os.Rename(temporaryPath, path)
	}
	return nil
}
