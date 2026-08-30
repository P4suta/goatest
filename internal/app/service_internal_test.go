// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package app

import (
	"errors"
	"testing"
	"time"

	"github.com/P4suta/goatest/internal/cli"
	"github.com/P4suta/goatest/internal/report"
)

func TestServicePropagatesRepositoryRootResolutionFailure(t *testing.T) {
	sentinel := errors.New("absolute root failed")
	service := Service{
		Root: "relative",
		absolute: func(string) (string, error) {
			return "", sentinel
		},
	}
	_, err := service.Execute(t.Context(), cli.CommandReport, cli.Request{}, "")
	if !errors.Is(err, sentinel) {
		t.Fatalf("root resolution error = %v, want %v", err, sentinel)
	}
}

func TestFinalizeReportMarksUnreadableConfigurationMetadata(t *testing.T) {
	previous := readConfigurationFile
	t.Cleanup(func() { readConfigurationFile = previous })
	readConfigurationFile = func(string) ([]byte, error) {
		return nil, errors.New("configuration read failed")
	}
	now := time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC)
	result := finalizeReportKind(t.Context(), t.TempDir(), cli.Request{}, report.Report{
		Verdict: report.VerdictCompleted,
	}, report.RunOperation, now, now)
	if len(result.Configuration.Digest) != 64 {
		t.Fatalf("configuration digest = %q", result.Configuration.Digest)
	}
	found := false
	for _, limitation := range result.Limitations {
		found = found || limitation.Code == "configuration-metadata-unavailable"
	}
	if !found {
		t.Fatalf("configuration limitation missing: %+v", result.Limitations)
	}
}
