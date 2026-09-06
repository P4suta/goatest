// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package assure

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/P4suta/goatest/internal/filemode"
	"github.com/P4suta/goatest/internal/tempowner"
)

const (
	runScratchPrefix = "goatest-run-"

	baselineScratchName       = "baseline-"
	candidateTreeName         = "candidate-"
	repositoryObservationName = "repository-observation-"

	buildScratchName = "build"

	goCacheScratchName = "go-cache"
)

func TemporaryPrefixes() []string {
	return []string{runScratchPrefix}
}

type runScratch struct {
	dir string

	root string

	id string

	owner *tempowner.Owner
}

func openRunScratch(makeScratch func(string, string) (string, error), removeScratch func(string) error, temporary, root string, now time.Time) (runScratch, error) {
	scratch := runScratch{root: root}
	directory, err := makeScratch(temporary, runScratchPrefix)
	if err != nil {
		return scratch, fmt.Errorf("goatest: create run scratch: %w", err)
	}

	identity := filepath.Base(directory)
	owner, err := tempowner.Claim(directory, tempowner.Marker{RunID: identity, Root: root}, now)
	if err != nil {
		failure := fmt.Errorf("goatest: claim run scratch: %w", err)
		if errors.Is(err, tempowner.ErrOwned) {
			return scratch, failure
		}
		return scratch, errors.Join(failure, removeScratch(directory))
	}
	scratch.dir, scratch.id, scratch.owner = directory, identity, owner
	return scratch, nil
}

func (scratch runScratch) subdirectory(name string) (string, string, error) {
	if scratch.dir == "" {
		return "", "", errors.New("goatest: run scratch is unavailable")
	}
	return scratch.dir, name, nil
}

func (scratch runScratch) buildCacheLayer() (string, error) {
	if scratch.dir == "" {
		return "", errors.New("goatest: run scratch is unavailable")
	}
	directory := filepath.Join(scratch.dir, buildScratchName)
	if err := os.Mkdir(directory, filemode.PrivateDirectory); err != nil {
		return "", err
	}
	return directory, nil
}

func sweepRunTemporaries(options Options, sweep func(string, []string, time.Time) (tempowner.Result, error), now time.Time) {
	if options.TempDirectory == "" {
		return
	}
	result, err := sweep(options.TempDirectory, TemporaryPrefixes(), now)
	if err != nil {
		result.Errors = append(result.Errors, err)
	}
	if len(result.Removed) == 0 && len(result.Errors) == 0 {
		return
	}
	emit(options, "temp-sweep", result.Detail("removed"))
}
