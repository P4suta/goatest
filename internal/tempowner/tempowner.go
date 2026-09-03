// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package tempowner gives every temporary directory a run makes an owner and a
// collector.
//
// # Why a directory needs an owner
//
// A run writes hundreds of megabytes outside the repository: the tree each
// candidate is validated in, the scratch layer its go commands compile into,
// the artifacts of every baseline target. It removes them when it ends, and
// "when it ends" is the problem. A SIGKILL, an out-of-memory kill, a closed
// terminal or a machine that lost power all end the process somewhere between
// the first mkdir and the last remove, and what is left behind is a directory
// nothing will ever delete.
//
// The rule this package implements is that no byte a run writes is anonymous:
// every top-level temporary directory says who made it, and the next run
// collects the ones whose maker is gone.
//
// # The pair
//
// A claimed directory holds two files:
//
//   - [LockName] is an exclusive advisory lock, held open for as long as the
//     directory is in use. It is the liveness signal, and it is the only one.
//     A lock that can be taken means the process that held it no longer exists,
//     whatever it was called and whatever its pid has been reused for since.
//   - [MarkerName] is a small JSON document naming the schema, the run, the
//     process, the start time, the repository the run was verifying, and
//     whether the directory was kept on purpose. It is read by people, and by
//     [Sweep] for exactly one bit: kept.
//
// Both live inside the directory, so they disappear with it and there is no
// second place to tidy up. A pid is deliberately not the liveness signal: it
// wraps, it is reused, and asking whether it is alive answers a question about
// some other process on a long-lived machine.
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
)

const (
	// Schema is the marker's schema field. It carries the version, so that a
	// later document shape can never be read as this one.
	Schema = "goatest-temp-owner-v1"

	// LockName is the advisory lock file inside a claimed directory.
	LockName = "owner.lock"

	// MarkerName is the JSON marker file inside a claimed directory.
	MarkerName = "owner.json"

	// markerPerm and lockPerm are owner-only: the bookkeeping of a temporary
	// directory is nobody else's business, and a lock file another user could
	// truncate is not a lock.
	markerPerm fs.FileMode = 0o600
	lockPerm   fs.FileMode = 0o600
)

// A Marker is the JSON document in a claimed directory. It is written once at
// the claim and rewritten only to record a deliberate keep.
type Marker struct {
	// Schema is [Schema]. Claim fills it in; a caller never sets it.
	Schema string `json:"schema"`
	// RunID names the run that made the directory, so that a kept directory and
	// the ledger entry that records it name the same thing.
	RunID string `json:"run_id"`
	// PID is the process that claimed the directory. It is diagnostic only:
	// see the package documentation on why liveness is the lock's job.
	PID int `json:"pid"`
	// Started is when the directory was claimed, in UTC.
	Started time.Time `json:"started"`
	// Root is the repository the run was verifying, because the one question a
	// person asks about a stray directory is which project it belongs to.
	Root string `json:"root"`
	// Kept says the directory was preserved on purpose and is not an orphan.
	Kept bool `json:"kept"`
}

// LockPath is the lock file inside dir.
func LockPath(dir string) string { return filepath.Join(dir, LockName) }

// MarkerPath is the marker file inside dir.
func MarkerPath(dir string) string { return filepath.Join(dir, MarkerName) }

// ErrOwned is what [Claim] wraps when the directory's lock is held by another
// process. It is the one failure a caller has to tell apart from the rest: the
// directory belongs to whoever holds the lock, however the caller came by it,
// and is not the caller's to write in or to remove.
var ErrOwned = errors.New("already owned by another process")

// An Owner is a claimed directory: the lock is held open and the marker is
// written. Releasing or keeping it closes the lock; neither removes anything,
// because the lifetime of the directory belongs to whoever created it.
type Owner struct {
	dir    string
	lock   *os.File
	marker Marker
}

// Claim writes the marker pair into an existing directory and takes its lock.
// The caller supplies the run and the repository; the schema, the process and
// the start time are this package's to say.
//
// The lock comes first and the marker second, so that a directory caught
// half-claimed by a concurrent [Sweep] has no marker and a modification time of
// a moment ago — which is the case the legacy rule leaves alone.
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

// Dir is the claimed directory.
func (owner *Owner) Dir() string {
	if owner == nil {
		return ""
	}
	return owner.dir
}

// Release closes the lock without touching the directory. It is idempotent, and
// it must be called before the directory is removed: on Windows an open handle
// inside a directory is what makes the removal fail.
func (owner *Owner) Release() error {
	if owner == nil || owner.lock == nil {
		return nil
	}
	lock := owner.lock
	owner.lock = nil
	return release(lock)
}

// Keep records that the directory was preserved on purpose and releases the
// lock, so that a later [Sweep] reads the marker rather than finding a lock
// nobody holds and concluding the directory was abandoned.
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

// ReadMarker decodes the marker in dir. A directory that carries none reports
// [fs.ErrNotExist], which is how an unowned directory is told from an owned
// one.
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

// writeMarker commits the marker through a temporary file and a rename, so that
// a process killed in the middle of a write leaves the marker it had rather
// than half of a new one.
//
// It matters for exactly one write, and it matters a lot for that one: [Sweep]
// reads a torn marker as one that does not say kept, so a keep interrupted
// halfway would leave a directory somebody asked to keep for the next sweep to
// collect. The claim is written the same way because there is no reason for the
// two to differ.
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

// writeAndSync commits the bytes to the disk and closes the file whatever
// happened, because a temporary file left open is a temporary file Windows will
// not let anybody rename.
func writeAndSync(file *os.File, data []byte) error {
	if _, err := file.Write(data); err != nil {
		return errors.Join(err, file.Close())
	}
	if err := file.Sync(); err != nil {
		return errors.Join(err, file.Close())
	}
	return file.Close()
}

// acquire opens one lock file and tries to take it. A lock somebody else holds
// is reported as not taken rather than as a failure, because that is an answer
// about the directory and not a problem with it.
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

// release drops the lock and closes the file. Both, in that order: closing
// frees the lock on its own, and unlocking first is what makes the intent
// legible on the platform where the two are separate operations.
func release(file *os.File) error {
	return errors.Join(advisorylock.Release(file), file.Close())
}
