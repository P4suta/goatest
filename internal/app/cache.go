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
			Detail: fmt.Sprintf("max-bytes=%d ttl=%s build-max-bytes=%d", loaded.Cache.MaxBytes, loaded.Cache.TTL, loaded.Cache.BuildMaxBytes),
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
		result.Evidence = append(result.Evidence, cacheStatusEvidence("status", status),
			retentionStatusEvidence("trace-status", traceStatus), retentionStatusEvidence("diagnostics-status", diagnosticsStatus))
		buildStatus, err := service.buildCacheLayer(root).Inspect()
		if err != nil {
			return report.Report{}, err
		}
		result.Evidence = append(result.Evidence, buildCacheStatusEvidence("build-status", buildStatus))
		return result, nil
	case "gc":
		now := time.Now
		if service.Now != nil {
			now = service.Now
		}
		moment := now().UTC()
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
		result.Evidence = append(result.Evidence,
			cacheStatusEvidence("before", collected.Before),
			report.Evidence{Kind: "cache", ID: "gc", Status: "completed", Detail: fmt.Sprintf("removed-entries=%d removed-bytes=%d", collected.RemovedEntries, collected.RemovedBytes)},
			cacheStatusEvidence("after", collected.After),
			retentionGCStatusEvidence("trace", traceCollected),
			retentionGCStatusEvidence("diagnostics", diagnosticsCollected),
		)
		// Every run already collects the build cache when it ends, so this is
		// the same collection on demand rather than the only one there is. It
		// takes the layer's own lock, so it yields to a run collecting beside
		// it, and MinIdle comes from the layer rather than from a constant
		// here: the window a collection must leave alone is two of the layer's
		// touch intervals, and only the layer knows what its interval is.
		buildLayer := service.buildCacheLayer(root)
		buildCollected, _, err := buildLayer.CollectLocked(buildcache.Policy{
			MaxBytes: loaded.Cache.BuildMaxBytes, TTL: loaded.Cache.TTL, MinIdle: buildLayer.MinIdle(),
		}, 0, moment)
		if err != nil {
			return report.Report{}, err
		}
		result.Evidence = append(result.Evidence,
			buildCacheStatusEvidence("build-before", buildCollected.Before),
			report.Evidence{Kind: "build-cache", ID: "build-gc", Status: "completed",
				Detail: fmt.Sprintf("removed-actions=%d removed-objects=%d removed-bytes=%d",
					buildCollected.RemovedActions, buildCollected.RemovedObjects, buildCollected.RemovedBytes)},
			buildCacheStatusEvidence("build-after", buildCollected.After))
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

func buildCacheStatusEvidence(id string, status buildcache.Status) report.Evidence {
	detail := fmt.Sprintf("entries=%d bytes=%d", status.Entries, status.Bytes)
	if !status.Oldest.IsZero() {
		detail += " oldest=" + status.Oldest.UTC().Format(time.RFC3339Nano)
	}
	return report.Evidence{Kind: "build-cache", ID: id, Status: "ready", Detail: detail}
}

func retentionStatusEvidence(id string, status retention.Status) report.Evidence {
	detail := fmt.Sprintf("entries=%d bytes=%d", status.Entries, status.Bytes)
	if !status.Oldest.IsZero() {
		detail += " oldest=" + status.Oldest.UTC().Format(time.RFC3339Nano)
	}
	if !status.Newest.IsZero() {
		detail += " newest=" + status.Newest.UTC().Format(time.RFC3339Nano)
	}
	return report.Evidence{Kind: "diagnostic-retention", ID: id, Status: "ready", Detail: detail}
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
	now := time.Now
	if service.Now != nil {
		now = service.Now
	}
	moment := now().UTC()
	for _, directory := range []string{"trace", "diagnostics"} {
		if _, err := retention.Collect(filepath.Join(root, ".goatest", directory), loaded.Cache.MaxBytes, loaded.Cache.TTL, moment); err != nil {
			service.note("diagnostic-gc-unavailable", directory+": "+err.Error())
		}
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
