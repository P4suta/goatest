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

type evidenceWritableFile interface {
	Name() string
	Write([]byte) (int, error)
	Sync() error
	Close() error
}

func LoadGraph(path string) (GraphRecord, bool, error) {
	return loadGraphWithHooks(path, graphHooks{})
}

// loadGraphWithHooks is LoadGraph against a filesystem the caller supplies.
func loadGraphWithHooks(path string, hooks graphHooks) (GraphRecord, bool, error) {
	data, err := hooks.resolved().readGraph(path)
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
	return saveGraphWithHooks(path, record, graphHooks{})
}

// saveGraphWithHooks is SaveGraph against a filesystem the caller supplies.
func saveGraphWithHooks(path string, record GraphRecord, hooks graphHooks) error {
	hooks = hooks.resolved()
	if record.ModulePath == "" {
		return fmt.Errorf("goatest: evidence graph requires a module path")
	}
	record.Schema = GraphSchemaV1
	graphData, err := record.Graph.jsonWithHooks(hooks)
	if err != nil {
		return err
	}
	var canonicalGraph Graph
	if err := hooks.unmarshalGraph(graphData, &canonicalGraph); err != nil {
		return err
	}
	record.Graph = canonicalGraph
	data, err := hooks.marshalRecord(record, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := hooks.mkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	temporary, err := hooks.createTemporary(filepath.Dir(path), ".graph-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer func() { _ = hooks.remove(temporaryPath) }()
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
	if err := hooks.rename(temporaryPath, path); err != nil {
		if removeErr := hooks.remove(path); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			return errors.Join(err, removeErr)
		}
		return hooks.rename(temporaryPath, path)
	}
	return nil
}
