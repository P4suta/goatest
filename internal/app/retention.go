// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package app

import (
	"fmt"
	"path/filepath"
	"time"

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

func reportsGCEvidence(result retention.Result) report.Evidence {
	return report.Evidence{
		Kind: "reports", ID: "runs-gc", Status: "completed",
		Detail: fmt.Sprintf("removed-entries=%d removed-bytes=%d remaining=%d",
			result.RemovedEntries, result.RemovedBytes, result.After.Entries),
	}
}
