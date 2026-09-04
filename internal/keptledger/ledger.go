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
//
// # Who may write it
//
// Every writer today already holds the repository's cache lease: a run records
// what it kept while `internal/app` still holds the lease it took around the
// whole verification, and `goatest cache gc` collects inside the lease its own
// command takes. That is what actually serializes two goatest processes on one
// repository.
//
// [Update] does not rely on it. It takes an exclusive advisory lock beside the
// ledger for the load-change-save that every write is, because the failure it
// prevents is silent: an interleaved write loses an entry, and a lost entry is
// a directory on somebody's disk that nothing will ever collect again.
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

	"github.com/P4suta/goatest/internal/advisorylock"
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

	// lockSuffix names the lock beside the ledger. It is a second file rather
	// than a lock on the ledger itself because the ledger is replaced by a
	// rename, and a lock belongs to a file rather than to a name: the writer of
	// the new file and the holder of the old one would contend over nothing.
	lockSuffix = ".lock"

	// lockPatience is how long a writer waits for another one. Whoever holds
	// the lock is another goatest writing the same file for a few
	// milliseconds, so waiting for it is always right, and the bound exists
	// only so that a holder that died with the lock cannot hang a run forever.
	// It is generous because the wait is measured on a loaded machine: under
	// the race detector, eight packages at a time, one writer's turn has been
	// seen to come more than two seconds after the other's, and a wait that
	// gave up there turned a slow neighbour into a lost record.
	lockPatience = time.Minute

	// lockPoll is how often the wait asks again.
	lockPoll = 5 * time.Millisecond
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
	return Update(path, func(ledger *Ledger) error {
		for _, entry := range entries {
			entry.KeptAt = entry.KeptAt.UTC()
			if index := slices.IndexFunc(ledger.Entries, func(existing Entry) bool { return existing.Path == entry.Path }); index >= 0 {
				ledger.Entries[index] = entry
				continue
			}
			ledger.Entries = append(ledger.Entries, entry)
		}
		return nil
	})
}

// Update reads the ledger, hands it to mutate, and writes back what mutate left
// — all of it under the lock beside the ledger, so that two writers cannot each
// read the same document and write back half of the result.
//
// A ledger nobody has written yet that mutate leaves empty is not created: an
// empty document would say a repository once kept something and no longer does,
// which is a different fact from never having kept anything.
func Update(path string, mutate func(*Ledger) error) error {
	release, err := lock(path)
	if err != nil {
		return err
	}
	defer release()
	ledger, err := Load(path)
	if err != nil {
		return err
	}
	recorded := len(ledger.Entries)
	if err := mutate(&ledger); err != nil {
		return err
	}
	if recorded == 0 && len(ledger.Entries) == 0 {
		return nil
	}
	return Save(path, ledger)
}

// lock takes the exclusive lock beside the ledger and answers with the release
// that must follow it.
//
// A lock somebody else holds is waited on rather than refused, because the
// wait is milliseconds and the caller has nothing better to do with the answer.
// A wait that runs out is reported: at that point the holder is not busy, it is
// stuck, and a run that hung on its own bookkeeping would be worse than one
// that says it could not record what it kept.
func lock(path string) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path+lockSuffix, os.O_CREATE|os.O_RDWR, ledgerPerm)
	if err != nil {
		return nil, err
	}
	for waited := time.Duration(0); ; waited += lockPoll {
		held, lockErr := advisorylock.Try(file)
		if lockErr != nil {
			return nil, errors.Join(fmt.Errorf("goatest: lock %s: %w", path, lockErr), file.Close())
		}
		if held {
			return func() {
				_ = advisorylock.Release(file)
				_ = file.Close()
			}, nil
		}
		if waited >= lockPatience {
			return nil, errors.Join(
				fmt.Errorf("goatest: %s is held by another process", path+lockSuffix), file.Close())
		}
		time.Sleep(lockPoll)
	}
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
