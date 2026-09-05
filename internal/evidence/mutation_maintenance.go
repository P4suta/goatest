// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package evidence

import (
	"errors"
	"fmt"
	"os"
	"time"
)

// MutationStatus describes the reusable mutation evidence independently of
// the exact-input cache beside it. Invalid documents remain visible so cache
// status can diagnose them and cache flush can remove them.
type MutationStatus struct {
	Present    bool
	Valid      bool
	Removable  bool
	ModulePath string
	Records    int
	Killed     int
	Survived   int
	Unreached  int
	TimedOut   int
	Bytes      int64
	Modified   time.Time
	Problem    string
}

// MutationFlushResult describes the evidence file before and after an
// explicit flush. Missing evidence is an ordinary, idempotent result.
type MutationFlushResult struct {
	Before  MutationStatus
	After   MutationStatus
	Removed bool
}

// InspectMutation reads and validates a mutation evidence file without
// changing it. Content and read failures are reported in Problem instead of
// failing the whole cache status operation.
func InspectMutation(path string) (MutationStatus, error) {
	return inspectMutationWithHooks(path, mutationHooks{})
}

func inspectMutationWithHooks(path string, hooks mutationHooks) (MutationStatus, error) {
	hooks = hooks.resolved()
	info, err := hooks.lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return MutationStatus{}, nil
	}
	if err != nil {
		return MutationStatus{}, fmt.Errorf("goatest: inspect mutation evidence: %w", err)
	}
	status := MutationStatus{
		Present: true, Removable: !info.IsDir(), Bytes: info.Size(), Modified: info.ModTime(),
	}
	switch {
	case info.Mode()&os.ModeSymlink != 0:
		status.Problem = "stored path is a symbolic link"
		return status, nil
	case !info.Mode().IsRegular():
		status.Problem = "stored path is not a regular file"
		return status, nil
	}
	data, err := hooks.readStore(path)
	if err != nil {
		status.Problem = fmt.Sprintf("read failed: %v", err)
		return status, nil
	}
	store, err := decodeMutation(data)
	if err == nil && (store.Schema != MutationSchemaV1 || store.ModulePath == "") {
		err = errors.New("goatest: mutation evidence identity mismatch")
	}
	if err == nil {
		err = store.validate()
	}
	if err != nil {
		status.Problem = err.Error()
		return status, nil
	}
	status.Valid = true
	status.ModulePath = store.ModulePath
	status.Records = len(store.Records)
	for _, record := range store.Records {
		switch record.Outcome {
		case MutationOutcomeKilled:
			status.Killed++
		case MutationOutcomeSurvived:
			status.Survived++
		case MutationOutcomeUnreached:
			status.Unreached++
		case MutationOutcomeTimedOut:
			status.TimedOut++
		}
	}
	return status, nil
}

// FlushMutation removes exactly path without following it. A directory is
// refused; a regular, malformed, or symbolic-link entry can be unlinked so an
// operator can recover from a broken store safely.
func FlushMutation(path string) (MutationFlushResult, error) {
	return flushMutationWithHooks(path, mutationHooks{})
}

func flushMutationWithHooks(path string, hooks mutationHooks) (MutationFlushResult, error) {
	hooks = hooks.resolved()
	before, err := inspectMutationWithHooks(path, hooks)
	if err != nil {
		return MutationFlushResult{}, err
	}
	result := MutationFlushResult{Before: before}
	if !before.Present {
		return result, nil
	}
	if !before.Removable {
		return MutationFlushResult{}, fmt.Errorf("goatest: refusing to flush mutation evidence directory %q", path)
	}
	if err := hooks.remove(path); err != nil {
		return MutationFlushResult{}, fmt.Errorf("goatest: flush mutation evidence: %w", err)
	}
	result.Removed = true
	result.After, err = inspectMutationWithHooks(path, hooks)
	return result, err
}
