// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package app

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/P4suta/goatest/internal/assure"
	"github.com/P4suta/goatest/internal/buildcache"
	"github.com/P4suta/goatest/internal/keptledger"
	"github.com/P4suta/goatest/internal/report"
	"github.com/P4suta/goatest/internal/tempowner"
)

func unnamedTemporaryDirectory(id string) report.Evidence {
	return report.Evidence{
		Kind: "temp", ID: id, Status: "skipped",
		Detail: "no temporary directory was named",
	}
}

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

func (service Service) nativeCacheStatus(root string, moment time.Time) report.Evidence {
	parent := service.nativeCacheParent(root)
	if parent == "" {
		return report.Evidence{Kind: "temp", ID: "native-cache-orphans", Status: "skipped", Detail: "no build cache directory was named"}
	}
	result, err := tempowner.Inspect(parent, []string{buildcache.NativeDirectoryPrefix}, moment)
	if err != nil {
		result.Errors = append(result.Errors, err)
	}
	return report.Evidence{Kind: "temp", ID: "native-cache-orphans", Status: "ready", Detail: result.Detail("abandoned")}
}

func (service Service) nativeCacheSweep(root string, moment time.Time) report.Evidence {
	parent := service.nativeCacheParent(root)
	if parent == "" {
		return report.Evidence{Kind: "temp", ID: "native-cache-sweep", Status: "skipped", Detail: "no build cache directory was named"}
	}
	result, err := tempowner.Sweep(parent, []string{buildcache.NativeDirectoryPrefix}, moment)
	if err != nil {
		result.Errors = append(result.Errors, err)
	}
	return report.Evidence{Kind: "temp", ID: "native-cache-sweep", Status: "completed", Detail: result.Detail("removed")}
}

func (service Service) nativeCacheParent(root string) string {
	base := service.buildCacheDirectory(root)
	if base == "" {
		return ""
	}
	return filepath.Dir(base)
}

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

			status = "unreadable"
			unreadable++
			detail += " error=" + statErr.Error()
		default:

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

func collectKeptTemporaries(root string, ttl time.Duration, moment time.Time) report.Evidence {
	var removedBytes int64
	removed, failures := 0, 0
	remaining := 0

	err := keptledger.Update(keptledger.Path(root), func(ledger *keptledger.Ledger) error {
		kept := make([]keptledger.Entry, 0, len(ledger.Entries))
		for _, entry := range ledger.Entries {
			info, statErr := os.Stat(entry.Path)
			if errors.Is(statErr, fs.ErrNotExist) {
				removed++
				continue
			}
			if statErr != nil {
				failures++
				kept = append(kept, entry)
				continue
			}
			if ttl <= 0 || moment.Sub(entry.KeptAt) < ttl {
				kept = append(kept, entry)
				continue
			}

			vouched, markerErr := tempowner.KeptBy(entry.Path, entry.RunID)
			if markerErr != nil || !vouched || !info.IsDir() {
				failures++
				kept = append(kept, entry)
				continue
			}

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
