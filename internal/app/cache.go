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
	"github.com/P4suta/goatest/internal/report"
	"github.com/P4suta/goatest/internal/retention"
)

func (service Service) cache(ctx context.Context, root, action string) (report.Report, error) {
	loaded, err := config.Load(root)
	if err != nil {
		return report.Report{}, err
	}
	cacheRoot := filepath.Join(root, ".goatest", "cache")
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
		status, err := cache.Inspect(cacheRoot)
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
		// The temporary directory last, because it is the one store that is not
		// the repository's: what is reported before it is what this .goatest
		// holds, and what is reported here is what runs left on the machine.
		result.Evidence = append(result.Evidence, service.temporaryStatus(service.clock()().UTC()))
		result.Evidence = append(result.Evidence, keptTemporaryStatus(root)...)
		return result, nil
	case "gc":
		moment := service.clock()().UTC()
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
		// Every run already collects the build cache when it ends, so this is
		// the same collection on demand rather than the only one there is. It
		// takes the layer's own lock, so it yields to a run collecting beside
		// it, and MinIdle comes from the layer rather than from a constant
		// here: the window a collection must leave alone is two of the layer's
		// touch intervals, and only the layer knows what its interval is.
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
			service.temporarySweep(moment),
			collectKeptTemporaries(root, loaded.Cache.TTL, moment))
		return result, nil
	default:
		return report.Report{}, fmt.Errorf("goatest: cache action %q is unsupported", action)
	}
}

// buildCacheLayer names the build cache this repository's configuration points
// at. A directory nothing could resolve is the zero layer, which holds nothing
// and collects nothing, so status and gc report an empty cache rather than
// refusing to run.
//
// It asks for the directory alone. Maintenance never serves the cache, so
// whether this process could be re-executed as the cache program has nothing
// to do with whether it can report on the layer or collect it.
func (service Service) buildCacheLayer(root string) buildcache.Layer {
	return buildcache.Layer{Dir: service.buildCacheDirectory(root)}
}

// buildCacheGCEvidence reports what the collection of the build cache did, and
// a collection that did not run is not one that removed nothing.
//
// The layer the machine keeps is shared by every repository on it, so the
// collection yields when another process already holds it, and a layer no
// machine has built yet has nothing to hold at all. Both are ordinary answers
// rather than failures, and both leave the bound unapplied for now — so a
// reader who saw "completed, removed nothing" would draw the opposite
// conclusion from the true one.
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

// retentionDetail is how every retained store describes itself, so that a
// reader comparing two of them is comparing the same measurements.
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

// collectVerdictCache applies the cache policy to the store that policy is
// named after, at the end of the run that just read and wrote it.
//
// It belongs beside the diagnostic collection rather than after the report is
// published, because the entry a run adds is written during the run: by the
// time this runs the cache already holds it, and it is the newest, so the
// budget falls on older entries. A failure is a note for the same reason every
// collection here reports one — the run has already produced its verdict.
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
