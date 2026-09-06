// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package app

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/P4suta/goatest/internal/buildcache"
	"github.com/P4suta/goatest/internal/cache"
	"github.com/P4suta/goatest/internal/config"
	"github.com/P4suta/goatest/internal/evidence"
	"github.com/P4suta/goatest/internal/report"
	"github.com/P4suta/goatest/internal/retention"
)

func (service Service) cache(ctx context.Context, root, action string) (report.Report, error) {
	loaded, err := config.Load(root)
	if err != nil {
		return report.Report{}, err
	}
	cacheRoot := filepath.Join(root, ".goatest", "cache")
	mutationEvidencePath := filepath.Join(cacheRoot, evidence.MutationFileName)
	lease, err := cache.Acquire(ctx, cacheRoot, func() {
		service.note("cache-wait", "another goatest process is using this repository cache")
	})
	if err != nil {
		return report.Report{}, err
	}
	defer func() { _ = lease.Release() }()
	traceRoot := filepath.Join(root, ".goatest", "trace")
	diagnosticsRoot := filepath.Join(root, ".goatest", "diagnostics")
	result := report.Report{
		Schema: report.SchemaV1, RunKind: report.RunOperation, Verdict: report.VerdictCompleted,
		Evidence: []report.Evidence{{
			Kind: "cache", ID: "policy", Status: "configured",
			Detail: fmt.Sprintf("max-bytes=%d ttl=%s build-max-bytes=%d reports-keep=%d",
				loaded.Cache.MaxBytes, loaded.Cache.TTL, loaded.Cache.BuildMaxBytes, loaded.Reports.Keep),
		}},
	}
	switch action {
	case "status":
		moment := service.clock()().UTC()
		status, err := cache.Inspect(cacheRoot)
		if err != nil {
			return report.Report{}, err
		}
		mutationStatus, err := evidence.InspectMutation(mutationEvidencePath)
		if err != nil {
			return report.Report{}, err
		}
		traceStatus, err := retention.Inspect(traceRoot)
		if err != nil {
			return report.Report{}, err
		}
		diagnosticsStatus, err := retention.Inspect(diagnosticsRoot)
		if err != nil {
			return report.Report{}, err
		}
		history, err := reportsStatus(root, loaded.Reports.Keep)
		if err != nil {
			return report.Report{}, err
		}
		repair, err := repairStatus(root)
		if err != nil {
			return report.Report{}, err
		}
		result.Evidence = append(result.Evidence, cacheStatusEvidence("status", status),
			retentionStatusEvidence("trace-status", traceStatus), retentionStatusEvidence("diagnostics-status", diagnosticsStatus),
			history)
		result.Evidence = append(result.Evidence, repair...)
		buildStatus, err := service.buildCacheLayer(root).Inspect()
		if err != nil {
			return report.Report{}, err
		}
		result.Evidence = append(result.Evidence, buildCacheStatusEvidence("build-status", buildStatus))
		result.Evidence = append(result.Evidence, service.nativeCacheStatus(root, moment))

		result.Evidence = append(result.Evidence, service.temporaryStatus(moment))
		result.Evidence = append(result.Evidence, keptTemporaryStatus(root)...)
		result.Evidence = append(result.Evidence, mutationEvidenceStatusEvidence("status", "ready", mutationStatus))
		return result, nil
	case "gc":
		moment := service.clock()().UTC()

		mutationStatus, err := evidence.InspectMutation(mutationEvidencePath)
		if err != nil {
			return report.Report{}, err
		}
		collected, err := cache.Collect(cacheRoot, loaded.Cache.MaxBytes, loaded.Cache.TTL, moment)
		if err != nil {
			return report.Report{}, err
		}
		traceCollected, err := retention.Collect(traceRoot, loaded.Cache.MaxBytes, loaded.Cache.TTL, moment)
		if err != nil {
			return report.Report{}, err
		}
		diagnosticsCollected, err := retention.Collect(diagnosticsRoot, loaded.Cache.MaxBytes, loaded.Cache.TTL, moment)
		if err != nil {
			return report.Report{}, err
		}
		history, err := collectReports(root, loaded.Reports.Keep, moment)
		if err != nil {
			return report.Report{}, err
		}
		repair, err := collectRepair(root, cacheRoot, loaded.Cache.MaxBytes, loaded.Cache.TTL, moment)
		if err != nil {
			return report.Report{}, err
		}
		result.Evidence = append(result.Evidence,
			cacheStatusEvidence("before", collected.Before),
			report.Evidence{Kind: "cache", ID: "gc", Status: "completed", Detail: fmt.Sprintf("removed-entries=%d removed-bytes=%d", collected.RemovedEntries, collected.RemovedBytes)},
			cacheStatusEvidence("after", collected.After),
			retentionGCStatusEvidence("trace", traceCollected),
			retentionGCStatusEvidence("diagnostics", diagnosticsCollected),
			reportsGCEvidence(history),
		)
		result.Evidence = append(result.Evidence, repair...)

		buildLayer := service.buildCacheLayer(root)
		buildCollected, ran, err := buildLayer.CollectLocked(buildcache.Policy{
			MaxBytes: loaded.Cache.BuildMaxBytes, TTL: loaded.Cache.TTL, MinIdle: buildLayer.MinIdle(),
		}, 0, moment)
		if err != nil {
			return report.Report{}, err
		}
		result.Evidence = append(result.Evidence,
			buildCacheStatusEvidence("build-before", buildCollected.Before),
			buildCacheGCEvidence(buildCollected, ran),
			buildCacheStatusEvidence("build-after", buildCollected.After),
			service.nativeCacheSweep(root, moment),
			service.temporarySweep(moment),
			collectKeptTemporaries(root, loaded.Cache.TTL, moment))
		result.Evidence = append(result.Evidence, mutationEvidenceStatusEvidence("gc", "retained", mutationStatus))
		return result, nil
	case "flush":

		if _, err := cache.Inspect(cacheRoot); err != nil {
			return report.Report{}, err
		}
		mutationBefore, err := evidence.InspectMutation(mutationEvidencePath)
		if err != nil {
			return report.Report{}, err
		}
		if mutationBefore.Present && !mutationBefore.Removable {
			return report.Report{}, fmt.Errorf("goatest: refusing to flush mutation evidence path %q: %s", mutationEvidencePath, mutationBefore.Problem)
		}
		flushed, err := cache.Flush(cacheRoot)
		if err != nil {
			return report.Report{}, err
		}
		mutationFlushed, err := evidence.FlushMutation(mutationEvidencePath)
		if err != nil {
			return report.Report{}, err
		}
		result.Evidence = append(result.Evidence,
			cacheStatusEvidence("flush-before", flushed.Before),
			mutationEvidenceStatusEvidence("flush-before", "ready", mutationFlushed.Before),
			report.Evidence{Kind: "cache", ID: "flush", Status: "completed", Detail: fmt.Sprintf(
				"removed-entries=%d removed-bytes=%d mutation-removed=%t mutation-records=%d mutation-bytes=%d",
				flushed.RemovedEntries, flushed.RemovedBytes, mutationFlushed.Removed,
				mutationFlushed.Before.Records, mutationFlushed.Before.Bytes)},
			cacheStatusEvidence("flush-after", flushed.After),
			mutationEvidenceStatusEvidence("flush-after", "ready", mutationFlushed.After),
		)
		return result, nil
	default:
		return report.Report{}, fmt.Errorf("goatest: cache action %q is unsupported", action)
	}
}

func (service Service) buildCacheLayer(root string) buildcache.Layer {
	return buildcache.Layer{Dir: service.buildCacheDirectory(root)}
}

func buildCacheGCEvidence(collected buildcache.Collected, ran bool) report.Evidence {
	if !ran {
		return report.Evidence{Kind: "build-cache", ID: "build-gc", Status: "skipped",
			Detail: "not collected: another process holds the layer, or it has not been built yet"}
	}
	return report.Evidence{Kind: "build-cache", ID: "build-gc", Status: "completed",
		Detail: fmt.Sprintf("removed-actions=%d removed-objects=%d removed-bytes=%d",
			collected.RemovedActions, collected.RemovedObjects, collected.RemovedBytes)}
}

func buildCacheStatusEvidence(id string, status buildcache.Status) report.Evidence {
	detail := fmt.Sprintf("entries=%d bytes=%d", status.Entries, status.Bytes)
	if !status.Oldest.IsZero() {
		detail += " oldest=" + status.Oldest.UTC().Format(time.RFC3339Nano)
	}
	return report.Evidence{Kind: "build-cache", ID: id, Status: "ready", Detail: detail}
}

func retentionStatusEvidence(id string, status retention.Status) report.Evidence {
	return report.Evidence{Kind: "diagnostic-retention", ID: id, Status: "ready", Detail: retentionDetail(status)}
}

func retentionDetail(status retention.Status) string {
	detail := fmt.Sprintf("entries=%d bytes=%d", status.Entries, status.Bytes)
	if !status.Oldest.IsZero() {
		detail += " oldest=" + status.Oldest.UTC().Format(time.RFC3339Nano)
	}
	if !status.Newest.IsZero() {
		detail += " newest=" + status.Newest.UTC().Format(time.RFC3339Nano)
	}
	return detail
}

func retentionGCStatusEvidence(id string, result retention.Result) report.Evidence {
	return report.Evidence{Kind: "diagnostic-retention", ID: id + "-gc", Status: "completed",
		Detail: fmt.Sprintf("before-entries=%d after-entries=%d removed-entries=%d removed-bytes=%d", result.Before.Entries, result.After.Entries, result.RemovedEntries, result.RemovedBytes)}
}

func (service Service) collectDiagnosticRetention(root string) {
	loaded, err := config.Load(root)
	if err != nil {
		service.note("diagnostic-gc-unavailable", err.Error())
		return
	}
	moment := service.clock()().UTC()
	for _, directory := range []string{"trace", "diagnostics"} {
		if _, err := retention.Collect(filepath.Join(root, ".goatest", directory), loaded.Cache.MaxBytes, loaded.Cache.TTL, moment); err != nil {
			service.note("diagnostic-gc-unavailable", directory+": "+err.Error())
		}
	}
}

func (service Service) collectVerdictCache(root string) {
	loaded, err := config.Load(root)
	if err != nil {
		service.note("cache-gc-unavailable", err.Error())
		return
	}
	cacheRoot := filepath.Join(root, ".goatest", "cache")
	if _, err := cache.Collect(cacheRoot, loaded.Cache.MaxBytes, loaded.Cache.TTL, service.clock()().UTC()); err != nil {
		service.note("cache-gc-unavailable", err.Error())
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

func mutationEvidenceStatusEvidence(id, validStatus string, status evidence.MutationStatus) report.Evidence {
	detail := fmt.Sprintf("records=%d killed=%d survived=%d unreached=%d bytes=%d",
		status.Records, status.Killed, status.Survived, status.Unreached, status.Bytes)
	if status.ModulePath != "" {
		detail += " module=" + status.ModulePath
	}
	if !status.Modified.IsZero() {
		detail += " modified=" + status.Modified.UTC().Format(time.RFC3339Nano)
	}
	switch {
	case !status.Present:
		return report.Evidence{Kind: "mutation-evidence", ID: id, Status: "missing", Detail: detail}
	case !status.Valid:
		return report.Evidence{Kind: "mutation-evidence", ID: id, Status: "invalid", Detail: detail + fmt.Sprintf(" problem=%q", status.Problem)}
	default:
		return report.Evidence{Kind: "mutation-evidence", ID: id, Status: validStatus, Detail: detail}
	}
}
