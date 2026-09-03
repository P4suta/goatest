// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package app_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"testing"
	"time"

	"github.com/P4suta/goatest/internal/app"
	"github.com/P4suta/goatest/internal/assure"
	"github.com/P4suta/goatest/internal/cache"
	"github.com/P4suta/goatest/internal/checkpoint"
	"github.com/P4suta/goatest/internal/cli"
	"github.com/P4suta/goatest/internal/report"
	"github.com/P4suta/goatest/internal/trace"
)

func TestVerificationCacheWaitIsVisibleAndContextCancellationStopsBeforeRunner(t *testing.T) {
	root := t.TempDir()
	entered := make(chan struct{})
	release := make(chan struct{})
	first := app.Service{Root: root, Run: func(context.Context, assure.Options) (report.Report, error) {
		close(entered)
		<-release
		return report.Report{Schema: report.SchemaV1, Verdict: report.VerdictAssured, Contract: "standard-v1", Snapshot: strings.Repeat("a", 64)}, nil
	}}
	firstDone := make(chan error, 1)
	go func() {
		_, err := first.Execute(context.Background(), cli.CommandVerify, cli.Request{}, "")
		firstDone <- err
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("first verification did not enter runner")
	}

	secondCalled := false
	var progress bytes.Buffer
	second := app.Service{Root: root, Progress: &progress, Run: func(context.Context, assure.Options) (report.Report, error) {
		secondCalled = true
		return report.Report{}, nil
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	_, err := second.Execute(ctx, cli.CommandVerify, cli.Request{UI: cli.UIPlain}, "")
	if !errors.Is(err, context.DeadlineExceeded) || secondCalled || !strings.Contains(progress.String(), "cache-wait") {
		t.Fatalf("waiting verification = error %v called=%t progress=%q", err, secondCalled, progress.String())
	}
	close(release)
	if err := <-firstDone; err != nil {
		t.Fatalf("first verification = %v", err)
	}
}

func TestARunCollectsExpiredRecordingsUnderTheLeaseItOwns(t *testing.T) {
	root := t.TempDir()
	// A recording an earlier run left behind. Retention dates a recording by
	// the newest regular file in it and by the directory itself only when it
	// holds none, so both carry the moment the earlier run ended.
	recorded := time.Date(2026, 8, 1, 10, 11, 12, 0, time.UTC)
	stale := filepath.Join(root, ".goatest", "trace", "20260801T101112Z-4242")
	if err := os.MkdirAll(stale, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stale, trace.FileName), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(stale, trace.FileName), stale} {
		if err := os.Chtimes(path, recorded, recorded); err != nil {
			t.Fatal(err)
		}
	}
	// The whole run happens a month later, past the thirty-day cache TTL a
	// repository without a configuration file keeps, so the recording is
	// expired for the retention this run collects.
	moment := recorded.Add(31 * 24 * time.Hour)
	// A lease the run leaks instead of releasing is unlocked anyway the moment
	// the collector finalises the file nobody closed, and then the wait below
	// succeeds for a reason that has nothing to do with the run. The run and
	// the wait happen with collection off, so the only thing that can unlock
	// this cache is the run releasing what it took.
	collection := debug.SetGCPercent(-1)
	defer debug.SetGCPercent(collection)
	var progress bytes.Buffer
	service := app.Service{
		Root: root, Progress: &progress,
		Now: func() time.Time { return moment },
		Run: func(context.Context, assure.Options) (report.Report, error) {
			return report.Report{Schema: report.SchemaV1, Verdict: report.VerdictAssured, Contract: "standard-v1"}, nil
		},
	}
	if _, err := service.Execute(t.Context(), cli.CommandVerify, cli.Request{}, ""); err != nil {
		t.Fatal(err)
	}
	// Only the run that owns the cache lease collects the diagnostic exhaust,
	// so the expired recording being gone is the run saying it owned it.
	if _, err := os.Stat(stale); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expired recording %s outlived the run that owned the lease: %v", stale, err)
	}
	// Ownership is a promise to give the lease back, and the release is the
	// only consequence of the field a run that skipped it leaves behind.
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	lease, err := cache.Acquire(ctx, filepath.Join(root, ".goatest", "cache"), nil)
	if err != nil {
		t.Fatalf("the cache lease the run owned was not released: %v", err)
	}
	if err := lease.Release(); err != nil {
		t.Fatal(err)
	}
	if progress.Len() != 0 {
		t.Fatalf("progress = %q", progress.String())
	}
}

func TestCompletedReportDeletesCheckpointButCancellationLeavesIt(t *testing.T) {
	for _, test := range []struct {
		name      string
		runErr    error
		wantExist bool
	}{
		{name: "completed"},
		{name: "cancelled", runErr: context.Canceled, wantExist: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			digest := strings.Repeat("b", 64)
			store := cache.New(filepath.Join(root, ".goatest", "cache"))
			service := app.Service{Root: root, Run: func(context.Context, assure.Options) (report.Report, error) {
				if err := store.PutCheckpoint(digest, checkpoint.State{Schema: checkpoint.SchemaV1, InputDigest: digest, Attempts: 1}); err != nil {
					t.Fatal(err)
				}
				return report.Report{Schema: report.SchemaV1, Verdict: report.VerdictAssured, Contract: "standard-v1", Snapshot: digest}, test.runErr
			}}
			_, err := service.Execute(t.Context(), cli.CommandVerify, cli.Request{}, "")
			if !errors.Is(err, test.runErr) {
				t.Fatalf("verify error = %v, want %v", err, test.runErr)
			}
			path := filepath.Join(root, ".goatest", "cache", "v1", digest, cache.CheckpointFileName)
			_, statErr := os.Stat(path)
			if test.wantExist && statErr != nil {
				t.Fatalf("checkpoint was not retained: %v", statErr)
			}
			if !test.wantExist && !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("checkpoint remained after completed report: %v", statErr)
			}
		})
	}
}
