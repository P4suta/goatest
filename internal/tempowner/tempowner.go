// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package tempowner

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/P4suta/goatest/internal/advisorylock"
	"github.com/P4suta/goatest/internal/filemode"
)

const (
	Schema = "goatest-temp-owner-v1"

	LockName = "owner.lock"

	MarkerName = "owner.json"

	markerPerm fs.FileMode = filemode.PrivateFile
	lockPerm   fs.FileMode = filemode.PrivateFile
)

type Marker struct {
	Schema string `json:"schema"`

	RunID string `json:"run_id"`

	PID int `json:"pid"`

	Started time.Time `json:"started"`

	Root string `json:"root"`

	Kept bool `json:"kept"`
}

func LockPath(dir string) string { return filepath.Join(dir, LockName) }

func MarkerPath(dir string) string { return filepath.Join(dir, MarkerName) }

var ErrOwned = errors.New("already owned by another process")

type Owner struct {
	dir    string
	lock   *os.File
	marker Marker
}

func Claim(dir string, marker Marker, now time.Time) (*Owner, error) {
	lock, held, err := acquire(LockPath(dir))
	if err != nil {
		return nil, fmt.Errorf("locking %s: %w", dir, err)
	}
	if !held {
		return nil, fmt.Errorf("%s is %w", dir, ErrOwned)
	}
	marker.Schema = Schema
	marker.PID = os.Getpid()
	marker.Started = now.UTC()
	marker.Kept = false
	if err := writeMarker(dir, marker); err != nil {
		return nil, errors.Join(fmt.Errorf("marking %s: %w", dir, err), release(lock))
	}
	return &Owner{dir: dir, lock: lock, marker: marker}, nil
}

func (owner *Owner) Dir() string {
	if owner == nil {
		return ""
	}
	return owner.dir
}

func (owner *Owner) Release() error {
	if owner == nil || owner.lock == nil {
		return nil
	}
	lock := owner.lock
	owner.lock = nil
	return release(lock)
}

func (owner *Owner) Keep() error {
	if owner == nil {
		return nil
	}
	marker := owner.marker
	marker.Kept = true
	if err := writeMarker(owner.dir, marker); err != nil {
		return errors.Join(fmt.Errorf("keeping %s: %w", owner.dir, err), owner.Release())
	}
	owner.marker = marker
	return owner.Release()
}

func KeptBy(dir, runID string) (bool, error) {
	raw, err := os.ReadFile(MarkerPath(dir))
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var marker struct {
		Schema string `json:"schema"`
		RunID  string `json:"run_id"`
		Kept   bool   `json:"kept"`
	}
	if err := json.Unmarshal(raw, &marker); err != nil {
		return false, fmt.Errorf("reading the marker of %s: %w", dir, err)
	}
	return marker.Schema == Schema && marker.Kept && marker.RunID == runID, nil
}

func ReadMarker(dir string) (Marker, error) {
	raw, err := os.ReadFile(MarkerPath(dir))
	if err != nil {
		return Marker{}, err
	}
	var marker Marker
	if err := json.Unmarshal(raw, &marker); err != nil {
		return Marker{}, err
	}
	return marker, nil
}

func writeMarker(dir string, marker Marker) error {
	raw, err := json.Marshal(marker)
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(dir, ".owner-*.tmp")
	if err != nil {
		return err
	}
	name := temporary.Name()
	defer func() { _ = os.Remove(name) }()
	if err := writeAndSync(temporary, append(raw, '\n')); err != nil {
		return err
	}
	if err := os.Chmod(name, markerPerm); err != nil {
		return err
	}
	return os.Rename(name, MarkerPath(dir))
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

func acquire(path string) (*os.File, bool, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, lockPerm)
	if err != nil {
		return nil, false, err
	}
	held, err := advisorylock.Try(file)
	if err != nil || !held {
		return nil, held, errors.Join(err, file.Close())
	}
	return file, true, nil
}

func release(file *os.File) error {
	return errors.Join(advisorylock.Release(file), file.Close())
}
