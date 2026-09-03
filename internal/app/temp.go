// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package app

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"time"

	"github.com/P4suta/goatest/internal/assure"
	"github.com/P4suta/goatest/internal/keptledger"
	"github.com/P4suta/goatest/internal/report"
	"github.com/P4suta/goatest/internal/tempowner"
)

// The temporary directory is where goatest actually fills a disk: a snapshot of
// the module per run, a build cache layer, a tree per validated candidate. Both
// maintenance commands report on it, and only `gc` collects it, for the same
// reason `status` never collects a cache: a person asking what is on their disk
// has not asked for any of it to be removed.

// unnamedTemporaryDirectory is what both maintenance commands report when
// nobody named a temporary directory.
//
// Guessing the machine's own would be the obvious convenience and is exactly
// the mistake that once collected the scratch directory of a run that was
// working in it: a service that names none has named nothing, and the one
// layer allowed to say where the machine keeps its temporary files is the CLI.
func unnamedTemporaryDirectory(id string) report.Evidence {
	return report.Evidence{
		Kind: "temp", ID: id, Status: "skipped",
		Detail: "no temporary directory was named",
	}
}

// temporaryStatus reports what a `gc` would reclaim from the temporary
// directory without reclaiming any of it.
func (service Service) temporaryStatus(moment time.Time) report.Evidence {
	if service.TempDirectory == "" {
		return unnamedTemporaryDirectory("orphans")
	}
	result, err := tempowner.Inspect(service.TempDirectory, assure.TemporaryPrefixes(), moment)
	if err != nil {
		result.Errors = append(result.Errors, err)
	}
	return report.Evidence{Kind: "temp", ID: "orphans", Status: "ready", Detail: result.Detail("abandoned")}
}

// temporarySweep collects the directories of runs that were killed before they
// could remove their own.
//
// A temporary directory that cannot be read is folded into the evidence rather
// than failing the command: a maintenance command that refuses to report on the
// stores it can read because of the one it cannot is worse than an incomplete
// answer.
func (service Service) temporarySweep(moment time.Time) report.Evidence {
	if service.TempDirectory == "" {
		return unnamedTemporaryDirectory("sweep")
	}
	result, err := tempowner.Sweep(service.TempDirectory, assure.TemporaryPrefixes(), moment)
	if err != nil {
		result.Errors = append(result.Errors, err)
	}
	return report.Evidence{Kind: "temp", ID: "sweep", Status: "completed", Detail: result.Detail("removed")}
}

// keptTemporaryStatus lists the directories runs kept on purpose: one entry
// each, saying whether it is still on the disk, and a total for the reader who
// only wants to know whether to care.
func keptTemporaryStatus(root string) []report.Evidence {
	ledger, err := keptledger.Load(keptledger.Path(root))
	if err != nil {
		return []report.Evidence{{Kind: "kept-temp", ID: "kept-temp-status", Status: "unavailable", Detail: err.Error()}}
	}
	items := make([]report.Evidence, 0, len(ledger.Entries)+1)
	var bytes int64
	missing, unreadable := 0, 0
	for _, entry := range ledger.Entries {
		status := "kept"
		detail := fmt.Sprintf("path=%s bytes=%d kept-at=%s", entry.Path, entry.Bytes, entry.KeptAt.UTC().Format(time.RFC3339))
		switch _, statErr := os.Stat(entry.Path); {
		case errors.Is(statErr, fs.ErrNotExist):
			status = "missing"
			missing++
		case statErr != nil:
			// Not missing: nobody could tell. The entry stays, and so does
			// whatever is at that path, until somebody can read it.
			status = "unreadable"
			unreadable++
			detail += " error=" + statErr.Error()
		default:
			// The entry says a run kept this directory. Whether the directory
			// agrees is what decides if a collection may ever remove it, so a
			// reader is told now rather than left to wonder why `gc` keeps
			// passing one by.
			if kept, markerErr := tempowner.KeptBy(entry.Path, entry.RunID); !kept || markerErr != nil {
				status = "unverified"
				unreadable++
				if markerErr != nil {
					detail += " error=" + markerErr.Error()
				}
			}
		}
		bytes += entry.Bytes
		items = append(items, report.Evidence{Kind: "kept-temp", ID: entry.RunID, Status: status, Detail: detail})
	}
	total := fmt.Sprintf("entries=%d bytes=%d missing=%d", len(ledger.Entries), bytes, missing)
	if unreadable != 0 {
		total += fmt.Sprintf(" errors=%d", unreadable)
	}
	return append(items, report.Evidence{Kind: "kept-temp", ID: "kept-temp-status", Status: "ready", Detail: total})
}

// collectKeptTemporaries removes the directories a --keep-temp run kept once
// they are older than the cache TTL, and drops the entries of directories that
// are no longer there.
//
// The TTL is the cache's own, because a kept temporary directory is exactly
// what the setting is for: something a developer asked to have around while
// they were working on it, and nobody wants forever. A configuration with no
// TTL keeps them until somebody removes them, which is what it says.
func collectKeptTemporaries(root string, ttl time.Duration, moment time.Time) report.Evidence {
	var removedBytes int64
	removed, failures := 0, 0
	remaining := 0
	// The whole read-decide-write runs inside the ledger's own lock, so that a
	// run recording a keep beside this collection cannot lose its entry to it.
	err := keptledger.Update(keptledger.Path(root), func(ledger *keptledger.Ledger) error {
		kept := make([]keptledger.Entry, 0, len(ledger.Entries))
		for _, entry := range ledger.Entries {
			info, statErr := os.Stat(entry.Path)
			if errors.Is(statErr, fs.ErrNotExist) {
				// The directory is gone, so its entry goes with it.
				removed++
				continue
			}
			if statErr != nil {
				// A stat nobody could answer says nothing about whether the
				// directory is there, and an entry dropped on that evidence
				// would leave it on the disk with nothing tracking it.
				failures++
				kept = append(kept, entry)
				continue
			}
			if ttl <= 0 || moment.Sub(entry.KeptAt) < ttl {
				kept = append(kept, entry)
				continue
			}
			// The ledger names the path; the directory says whether it is ours.
			// Only the second is authority to remove anything, because the
			// ledger is a file in a repository that a person can edit and a bad
			// merge can mangle, and what happens next is a recursive delete.
			vouched, markerErr := tempowner.KeptBy(entry.Path, entry.RunID)
			if markerErr != nil || !vouched || !info.IsDir() {
				failures++
				kept = append(kept, entry)
				continue
			}
			// Measured now rather than trusted from the entry: what the
			// collection reclaimed is what was on the disk when it ran.
			size := tempowner.Size(entry.Path)
			if removeErr := os.RemoveAll(entry.Path); removeErr != nil {
				failures++
				kept = append(kept, entry)
				continue
			}
			removed++
			removedBytes += size
		}
		ledger.Entries = kept
		remaining = len(kept)
		return nil
	})
	if err != nil {
		return report.Evidence{Kind: "kept-temp", ID: "kept-temp-gc", Status: "unavailable", Detail: err.Error()}
	}
	detail := fmt.Sprintf("removed-entries=%d removed-bytes=%d remaining=%d", removed, removedBytes, remaining)
	if failures != 0 {
		detail += fmt.Sprintf(" errors=%d", failures)
	}
	return report.Evidence{Kind: "kept-temp", ID: "kept-temp-gc", Status: "completed", Detail: detail}
}
