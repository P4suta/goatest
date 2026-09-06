// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

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
	"github.com/P4suta/goatest/internal/filemode"
)

const (
	Schema = "goatest-kept-temp-v1"

	FileName = "kept-temp-v1.json"

	ledgerPerm fs.FileMode = filemode.PrivateFile

	lockSuffix = ".lock"

	lockPatience = time.Minute

	lockPoll = 5 * time.Millisecond
)

type Entry struct {
	Path string `json:"path"`

	RunID string `json:"run_id"`

	KeptAt time.Time `json:"kept_at"`

	Bytes int64 `json:"bytes"`
}

type Ledger struct {
	Schema  string  `json:"schema"`
	Entries []Entry `json:"entries"`
}

func Path(root string) string { return filepath.Join(root, ".goatest", FileName) }

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

func lock(path string) (func(), error) {
	return lockWithWait(path, time.Sleep)
}

func lockWithWait(path string, wait func(time.Duration)) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(path), filemode.ReadableDirectory); err != nil {
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
		wait(lockPoll)
	}
}

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
	if err := os.MkdirAll(directory, filemode.ReadableDirectory); err != nil {
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

func writeAndSync(file *os.File, data []byte) error {
	if _, err := file.Write(data); err != nil {
		return errors.Join(err, file.Close())
	}
	if err := file.Sync(); err != nil {
		return errors.Join(err, file.Close())
	}
	return file.Close()
}

func compareEntries(first, second Entry) int {
	if order := first.KeptAt.Compare(second.KeptAt); order != 0 {
		return order
	}
	return strings.Compare(first.Path, second.Path)
}
