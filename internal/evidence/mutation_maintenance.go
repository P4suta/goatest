// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package evidence

import (
	"errors"
	"fmt"
	"os"
	"time"
)

type MutationStatus struct {
	Present    bool
	Valid      bool
	Removable  bool
	ModulePath string
	Records    int
	Killed     int
	Survived   int
	Unreached  int
	Bytes      int64
	Modified   time.Time
	Problem    string
}

type MutationFlushResult struct {
	Before  MutationStatus
	After   MutationStatus
	Removed bool
}

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
		Present: true, Bytes: info.Size(), Modified: info.ModTime(),
	}
	switch {
	case info.Mode()&os.ModeSymlink != 0:
		status.Removable = true
		status.Problem = "stored path is a symbolic link"
		return status, nil
	case !info.Mode().IsRegular():
		status.Problem = "stored path is not a regular file"
		return status, nil
	}
	status.Removable = true
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
		}
	}
	return status, nil
}

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
		return MutationFlushResult{}, fmt.Errorf("goatest: refusing to flush mutation evidence path %q: %s", path, before.Problem)
	}
	if err := hooks.remove(path); err != nil {
		return MutationFlushResult{}, fmt.Errorf("goatest: flush mutation evidence: %w", err)
	}
	result.Removed = true
	result.After, err = inspectMutationWithHooks(path, hooks)
	return result, err
}
