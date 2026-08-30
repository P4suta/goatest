// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package app

import (
	"fmt"
	"path/filepath"
	"time"

	"github.com/P4suta/goatest/internal/cache"
	"github.com/P4suta/goatest/internal/config"
	"github.com/P4suta/goatest/internal/report"
)

func (service Service) cache(root, action string) (report.Report, error) {
	loaded, err := config.Load(root)
	if err != nil {
		return report.Report{}, err
	}
	cacheRoot := filepath.Join(root, ".goatest", "cache")
	result := report.Report{
		Schema: report.SchemaV1, RunKind: report.RunOperation, Verdict: report.VerdictCompleted,
		Evidence: []report.Evidence{{
			Kind: "cache", ID: "policy", Status: "configured",
			Detail: fmt.Sprintf("max-bytes=%d ttl=%s", loaded.Cache.MaxBytes, loaded.Cache.TTL),
		}},
	}
	switch action {
	case "status":
		status, err := cache.Inspect(cacheRoot)
		if err != nil {
			return report.Report{}, err
		}
		result.Evidence = append(result.Evidence, cacheStatusEvidence("status", status))
		return result, nil
	case "gc":
		now := time.Now
		if service.Now != nil {
			now = service.Now
		}
		collected, err := cache.Collect(cacheRoot, loaded.Cache.MaxBytes, loaded.Cache.TTL, now().UTC())
		if err != nil {
			return report.Report{}, err
		}
		result.Evidence = append(result.Evidence,
			cacheStatusEvidence("before", collected.Before),
			report.Evidence{Kind: "cache", ID: "gc", Status: "completed", Detail: fmt.Sprintf("removed-entries=%d removed-bytes=%d", collected.RemovedEntries, collected.RemovedBytes)},
			cacheStatusEvidence("after", collected.After),
		)
		return result, nil
	default:
		return report.Report{}, fmt.Errorf("goatest: cache action %q is unsupported", action)
	}
}

func cacheStatusEvidence(id string, status cache.Status) report.Evidence {
	detail := fmt.Sprintf("entries=%d bytes=%d", status.Entries, status.Bytes)
	if !status.Oldest.IsZero() {
		detail += " oldest=" + status.Oldest.UTC().Format(time.RFC3339Nano)
	}
	if !status.Newest.IsZero() {
		detail += " newest=" + status.Newest.UTC().Format(time.RFC3339Nano)
	}
	return report.Evidence{Kind: "cache", ID: id, Status: "ready", Detail: detail}
}
