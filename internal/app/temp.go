// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package app

import (
	"fmt"
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

// temporaryStatus reports what a `gc` would reclaim from the temporary
// directory without reclaiming any of it.
func (service Service) temporaryStatus(moment time.Time) report.Evidence {
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
	missing := 0
	for _, entry := range ledger.Entries {
		status := "kept"
		if _, err := os.Stat(entry.Path); err != nil {
			status = "missing"
			missing++
		}
		bytes += entry.Bytes
		items = append(items, report.Evidence{
			Kind: "kept-temp", ID: entry.RunID, Status: status,
			Detail: fmt.Sprintf("path=%s bytes=%d kept-at=%s", entry.Path, entry.Bytes, entry.KeptAt.UTC().Format(time.RFC3339)),
		})
	}
	return append(items, report.Evidence{
		Kind: "kept-temp", ID: "kept-temp-status", Status: "ready",
		Detail: fmt.Sprintf("entries=%d bytes=%d missing=%d", len(ledger.Entries), bytes, missing),
	})
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
	path := keptledger.Path(root)
	ledger, err := keptledger.Load(path)
	if err != nil {
		return report.Evidence{Kind: "kept-temp", ID: "kept-temp-gc", Status: "unavailable", Detail: err.Error()}
	}
	remaining := make([]keptledger.Entry, 0, len(ledger.Entries))
	var removedBytes int64
	removed, failures := 0, 0
	for _, entry := range ledger.Entries {
		if _, statErr := os.Stat(entry.Path); statErr != nil {
			removed++
			continue
		}
		if ttl <= 0 || moment.Sub(entry.KeptAt) < ttl {
			remaining = append(remaining, entry)
			continue
		}
		// Measured now rather than trusted from the entry: what the collection
		// reclaimed is what was on the disk when it ran.
		size := tempowner.Size(entry.Path)
		if removeErr := os.RemoveAll(entry.Path); removeErr != nil {
			failures++
			remaining = append(remaining, entry)
			continue
		}
		removed++
		removedBytes += size
	}
	detail := fmt.Sprintf("removed-entries=%d removed-bytes=%d remaining=%d", removed, removedBytes, len(remaining))
	if failures != 0 {
		detail += fmt.Sprintf(" errors=%d", failures)
	}
	if removed == 0 {
		return report.Evidence{Kind: "kept-temp", ID: "kept-temp-gc", Status: "completed", Detail: detail}
	}
	ledger.Entries = remaining
	if err := keptledger.Save(path, ledger); err != nil {
		return report.Evidence{Kind: "kept-temp", ID: "kept-temp-gc", Status: "unavailable", Detail: err.Error()}
	}
	return report.Evidence{Kind: "kept-temp", ID: "kept-temp-gc", Status: "completed", Detail: detail}
}
