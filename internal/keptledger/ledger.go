// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package keptledger records the temporary directories a run was asked to keep.
//
// A --keep-temp run leaves directories on the disk on purpose, and until this
// ledger existed it named them in the recording and nowhere else: a successful,
// untraced run left gigabytes that only the temporary directory itself listed,
// and nothing would ever remove them. The ledger is the second half of that
// bargain. What a run keeps it writes down, `goatest cache status` lists, and
// `goatest cache gc` removes once it is older than the cache TTL.
//
// It lives in the repository's own .goatest directory rather than beside the
// directories it names, because it has to survive the removal of every one of
// them and because the commands that read it are already run from a repository.
package keptledger

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

const (
	// Schema is the document's schema field. It carries the version, so that a
	// later shape can never be read as this one.
	Schema = "goatest-kept-temp-v1"

	// FileName is the ledger inside a repository's .goatest directory.
	FileName = "kept-temp-v1.json"

	// ledgerPerm is owner-only, like everything else goatest writes about a
	// developer's machine.
	ledgerPerm fs.FileMode = 0o600
)

// An Entry is one directory a run kept and nothing has removed yet.
type Entry struct {
	// Path is where the directory is, as the run recorded it: absolute, and
	// outside the repository, because that is where a temporary directory is
	// made.
	Path string `json:"path"`
	// RunID names the run that kept it.
	RunID string `json:"run_id"`
	// KeptAt is when the run kept it, in UTC. It is what the cache TTL is
	// measured against.
	KeptAt time.Time `json:"kept_at"`
	// Bytes is what the directory held when it was kept, as far as the walk
	// could measure. It is what a person deciding whether to care reads.
	Bytes int64 `json:"bytes"`
}

// A Ledger is the whole document: every kept directory, in the order a reader
// wants them, oldest first.
type Ledger struct {
	Schema  string  `json:"schema"`
	Entries []Entry `json:"entries"`
}

// Path is the ledger of one repository.
func Path(root string) string { return filepath.Join(root, ".goatest", FileName) }

// Load reads the ledger. A ledger nobody has written yet is an empty one rather
// than a failure: it is a repository whose runs have never been asked to keep
// anything.
//
// Everything else is refused. Decoding is strict about both the schema and the
// fields, because the one thing a reader of this file goes on to do is remove
// the directories it names, and a document this version does not understand is
// not something to act on.
func Load(path string) (Ledger, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Ledger{Schema: Schema}, nil
	}
	if err != nil {
		return Ledger{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var ledger Ledger
	if err := decoder.Decode(&ledger); err != nil {
		return Ledger{}, fmt.Errorf("goatest: read %s: %w", path, err)
	}
	if ledger.Schema != Schema {
		return Ledger{}, fmt.Errorf("goatest: %s carries schema %q, want %q", path, ledger.Schema, Schema)
	}
	return ledger, nil
}

// Append records what one run kept, keeping what earlier runs recorded.
//
// A path the ledger already holds is replaced rather than repeated: one
// directory is one entry, and a path recorded twice would be counted twice by
// every reader and removed once by the collector.
func Append(path string, entries ...Entry) error {
	ledger, err := Load(path)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		entry.KeptAt = entry.KeptAt.UTC()
		if index := slices.IndexFunc(ledger.Entries, func(existing Entry) bool { return existing.Path == entry.Path }); index >= 0 {
			ledger.Entries[index] = entry
			continue
		}
		ledger.Entries = append(ledger.Entries, entry)
	}
	return Save(path, ledger)
}

// Save writes the ledger whole, sorted oldest first.
//
// The write goes through a temporary file in the same directory and a rename,
// so that a reader never sees half a ledger and a crash in the middle of a
// write leaves the previous one intact. It is the same commit the report cache
// makes, for the same reason.
func Save(path string, ledger Ledger) error {
	ledger.Schema = Schema
	slices.SortFunc(ledger.Entries, compareEntries)
	if ledger.Entries == nil {
		ledger.Entries = []Entry{}
	}
	encoded, err := json.Marshal(ledger)
	if err != nil {
		return err
	}
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, ".kept-temp-*.tmp")
	if err != nil {
		return err
	}
	name := temporary.Name()
	defer func() { _ = os.Remove(name) }()
	if err := writeAndSync(temporary, append(encoded, '\n')); err != nil {
		return err
	}
	if err := os.Chmod(name, ledgerPerm); err != nil {
		return err
	}
	return os.Rename(name, path)
}

// writeAndSync commits the bytes to the disk and closes the file whatever
// happened, because a temporary file left open is a temporary file Windows
// will not let anybody rename.
func writeAndSync(file *os.File, data []byte) error {
	if _, err := file.Write(data); err != nil {
		return errors.Join(err, file.Close())
	}
	if err := file.Sync(); err != nil {
		return errors.Join(err, file.Close())
	}
	return file.Close()
}

// compareEntries orders the ledger the way it is read: oldest first, and by
// path where two runs kept a directory in the same instant, so that the file a
// person diffs between two runs changes only where something changed.
func compareEntries(first, second Entry) int {
	if order := first.KeptAt.Compare(second.KeptAt); order != 0 {
		return order
	}
	return strings.Compare(first.Path, second.Path)
}
