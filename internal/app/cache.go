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
		// The build cache is collected here and only here. A run never collects
		// it: the layer is shared by every repository on the machine, and a run
		// that pruned it would be deciding for repositories it knows nothing
		// about, in the middle of the work the layer exists to make fast.
		buildCollected, err := service.buildCacheLayer(root).Collect(buildcache.Policy{
			MaxBytes: loaded.Cache.BuildMaxBytes, TTL: loaded.Cache.TTL, MinIdle: buildCacheMinIdle,
		}, moment)
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

// buildCacheMinIdle protects an entry a live run read or wrote moments ago from
// a collection running beside it. It is the go command's own file-time
// granularity, which is the window the cache refreshes an entry within: an
// entry read inside it may have no fresher file time to prove it, and a
// collection that removed it would take a file a running build is about to
// open.
const buildCacheMinIdle = time.Hour

// buildCacheLayer names the build cache this repository's configuration points
// at. A location the composition root could not resolve is the zero layer,
// which holds nothing and collects nothing, so status and gc report an empty
// cache rather than refusing to run.
func (service Service) buildCacheLayer(root string) buildcache.Layer {
	_, base := service.buildCacheLocation(root)
	return buildcache.Layer{Dir: base}
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
