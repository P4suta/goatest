// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package app_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/P4suta/goatest/internal/app"
	"github.com/P4suta/goatest/internal/assure"
	"github.com/P4suta/goatest/internal/cache"
	"github.com/P4suta/goatest/internal/checkpoint"
	"github.com/P4suta/goatest/internal/cli"
	"github.com/P4suta/goatest/internal/report"
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
