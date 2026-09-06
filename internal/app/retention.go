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
	"github.com/P4suta/goatest/internal/retention"
)

func reportsRoot(root string) string {
	return filepath.Join(root, "reports", "runs")
}

func protectedRunIDs(root string) map[string]struct{} {
	indexes := [...]string{"latest-any.json", "latest-full.json"}
	protected := make(map[string]struct{}, len(indexes))
	for _, name := range indexes {
		loaded, err := loadReport(filepath.Join(root, ".goatest", name), name)
		if err != nil || loaded.RunID == "" {
			continue
		}
		protected[loaded.RunID] = struct{}{}
	}
	return protected
}

func reportsStatus(root string, keep int) (report.Evidence, error) {
	status, err := retention.Inspect(reportsRoot(root))
	if err != nil {
		return report.Evidence{}, err
	}
	return report.Evidence{
		Kind: "reports", ID: "runs-status", Status: "ready",
		Detail: fmt.Sprintf("%s keep=%d protected=%d", retentionDetail(status), keep, len(protectedRunIDs(root))),
	}, nil
}

func collectReports(root string, keep int, moment time.Time) (retention.Result, error) {
	protected := protectedRunIDs(root)
	return retention.Keep(reportsRoot(root), keep, func(name string) bool {
		_, referenced := protected[name]
		return referenced
	}, moment)
}

func repairStore(root, name string) string {
	return filepath.Join(root, ".goatest", name)
}

func repairStatus(root string) ([]report.Evidence, error) {
	stores := [...]string{"candidates", "patches"}
	items := make([]report.Evidence, 0, len(stores))
	for _, name := range stores {
		status, err := retention.InspectFiles(repairStore(root, name))
		if err != nil {
			return nil, err
		}
		items = append(items, report.Evidence{
			Kind: "repair-retention", ID: name + "-status", Status: "ready", Detail: retentionDetail(status),
		})
	}
	return items, nil
}

func collectRepair(root, cacheRoot string, maxBytes int64, ttl time.Duration, moment time.Time) ([]report.Evidence, error) {
	pending, err := cache.New(cacheRoot).PendingCheckpoint()
	if err != nil {
		return nil, err
	}
	stores := [...]string{"candidates", "patches"}
	items := make([]report.Evidence, 0, len(stores))
	for _, name := range stores {
		if name == "candidates" && pending {
			items = append(items, report.Evidence{
				Kind: "repair-retention", ID: name + "-gc", Status: "skipped", Detail: "a checkpoint is in progress",
			})
			continue
		}
		result, err := retention.CollectFiles(repairStore(root, name), maxBytes, ttl, moment)
		if err != nil {
			return nil, err
		}
		items = append(items, report.Evidence{
			Kind: "repair-retention", ID: name + "-gc", Status: "completed",
			Detail: fmt.Sprintf("removed-entries=%d removed-bytes=%d", result.RemovedEntries, result.RemovedBytes),
		})
	}
	return items, nil
}

func (service Service) collectDurableArtifacts(root, cacheRoot string) {
	service.collectDurableHistory(root)
	service.collectRepairArtifacts(root, cacheRoot)
}

func (service Service) collectRepairArtifacts(root, cacheRoot string) {
	loaded, err := config.Load(root)
	if err != nil {
		service.note("repair-gc-unavailable", err.Error())
		return
	}
	if _, err := collectRepair(root, cacheRoot, loaded.Cache.MaxBytes, loaded.Cache.TTL, service.clock()().UTC()); err != nil {
		service.note("repair-gc-unavailable", err.Error())
	}
}

func (service Service) collectDurableHistory(root string) {
	loaded, err := config.Load(root)
	if err != nil {
		service.note("reports-gc-unavailable", err.Error())
		return
	}
	if _, err := collectReports(root, loaded.Reports.Keep, service.clock()().UTC()); err != nil {
		service.note("reports-gc-unavailable", err.Error())
	}
}

func reportsGCEvidence(result retention.Result) report.Evidence {
	return report.Evidence{
		Kind: "reports", ID: "runs-gc", Status: "completed",
		Detail: fmt.Sprintf("removed-entries=%d removed-bytes=%d remaining=%d",
			result.RemovedEntries, result.RemovedBytes, result.After.Entries),
	}
}
