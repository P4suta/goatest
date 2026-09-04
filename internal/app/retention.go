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

// The durable exhaust of a repository is the run history under reports/runs.
// Every other store a run writes is bounded by the cache policy; this one has
// only ever grown, because a report is the product of a verification and
// nothing dared remove one. What makes it safe to bound is that the runs the
// latest-* indexes point at are kept whatever the bound says, so the history
// can be small without any command losing the report it reads.

// reportsRoot names the immutable run history.
func reportsRoot(root string) string {
	return filepath.Join(root, "reports", "runs")
}

// protectedRunIDs is the set of runs an index still points at.
//
// The indexes are read with the strict loader, because a run is protected by
// being named in a report this tool would actually serve. An index that is
// missing or will not decode protects nothing and is not a failure: it is the
// ordinary state of a repository that has never run a full verification, and a
// maintenance command that refused to collect anything until every index parsed
// would be one more way for a disk to fill.
func protectedRunIDs(root string) map[string]struct{} {
	protected := make(map[string]struct{}, 2)
	for _, name := range []string{"latest-any.json", "latest-full.json"} {
		loaded, err := loadReport(filepath.Join(root, ".goatest", name), name)
		if err != nil || loaded.RunID == "" {
			continue
		}
		protected[loaded.RunID] = struct{}{}
	}
	return protected
}

// reportsStatus reports what the history holds and what the bound would spare,
// without removing any of it.
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

// collectReports bounds the history to the newest keep runs plus the ones an
// index names.
func collectReports(root string, keep int, moment time.Time) (retention.Result, error) {
	protected := protectedRunIDs(root)
	return retention.Keep(reportsRoot(root), keep, func(name string) bool {
		_, referenced := protected[name]
		return referenced
	}, moment)
}

// repairStore names one of the two stores the repair path writes into.
func repairStore(root, name string) string {
	return filepath.Join(root, ".goatest", name)
}

// repairStatus reports what the candidate and patch stores hold.
func repairStatus(root string) ([]report.Evidence, error) {
	items := make([]report.Evidence, 0, 2)
	for _, name := range []string{"candidates", "patches"} {
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

// collectRepair bounds both repair stores under the cache policy, sparing the
// candidates while any run could still resume.
//
// A resume re-validates the candidates its checkpoint recorded by ID and
// discards its whole saved mutation work when one is gone. That is safe — it
// costs the run its resumption rather than its correctness — but it is exactly
// the work a checkpoint exists to protect, so a collection that could cause it
// stands aside instead. A patch artifact is read by nothing and has no such
// claim on it.
func collectRepair(root, cacheRoot string, maxBytes int64, ttl time.Duration, moment time.Time) ([]report.Evidence, error) {
	pending, err := cache.New(cacheRoot).PendingCheckpoint()
	if err != nil {
		return nil, err
	}
	items := make([]report.Evidence, 0, 2)
	for _, name := range []string{"candidates", "patches"} {
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

// collectDurableArtifacts bounds everything a run leaves in the repository that
// nothing else collects: the run history, and the two repair stores.
func (service Service) collectDurableArtifacts(root, cacheRoot string) {
	service.collectDurableHistory(root)
	service.collectRepairArtifacts(root, cacheRoot)
}

// collectRepairArtifacts applies the same bounds a maintenance command would,
// reporting any failure as a note for the reason every collection here does.
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

// collectDurableHistory bounds the history a run has just extended.
//
// It runs after the report is published, so the run that called it is the
// newest entry and both indexes name it: it cannot collect its own report. A
// history that will not list is reported as a note, because nothing this
// function does may reach a verdict — the collection is housekeeping, and a
// verification that failed because of housekeeping would be a worse outcome
// than a directory that grew.
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
