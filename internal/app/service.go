// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package app connects the deterministic CLI command layer to repository
// assurance and durable report/config artifacts.
package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/P4suta/goatest/internal/assure"
	"github.com/P4suta/goatest/internal/cli"
	"github.com/P4suta/goatest/internal/config"
	"github.com/P4suta/goatest/internal/report"
)

type RunFunc func(context.Context, assure.Options) (report.Report, error)

type Service struct {
	Root          string
	GoBinary      string
	TempDirectory string
	Environment   []string
	Progress      io.Writer
	Run           RunFunc
	Now           func() time.Time
	absolute      func(string) (string, error)
}

func (service Service) Execute(ctx context.Context, command cli.Command, request cli.Request, id string) (report.Report, error) {
	root := service.Root
	if root == "" {
		root = "."
	}
	resolveAbsolute := service.absolute
	if resolveAbsolute == nil {
		resolveAbsolute = filepath.Abs
	}
	absolute, err := resolveAbsolute(root)
	if err != nil {
		return report.Report{}, err
	}
	switch command {
	case cli.CommandInit:
		if err := config.Init(absolute); err != nil {
			return report.Report{}, err
		}
		return report.Report{
			Schema: report.SchemaV1, Verdict: report.VerdictAssured,
			Evidence: []report.Evidence{{Kind: "configuration", ID: config.FileName, Status: "initialized"}},
		}, nil
	case cli.CommandReport:
		return loadLatest(absolute)
	case cli.CommandExplain:
		latest, err := loadLatest(absolute)
		if err != nil {
			return report.Report{}, err
		}
		finding, ok := find(latest, id)
		if !ok {
			return report.Report{}, fmt.Errorf("goatest: finding %q is absent from the latest report", id)
		}
		latest.Findings = []report.Finding{finding}
		latest.Evidence = nil
		latest.Repairs = repairsFor(latest.Repairs, id)
		return latest, nil
	case cli.CommandAccept:
		latest, err := loadLatest(absolute)
		if err != nil {
			return report.Report{}, err
		}
		finding, ok := find(latest, id)
		if !ok {
			return report.Report{}, fmt.Errorf("goatest: finding %q is absent from the latest report", id)
		}
		now := time.Now
		if service.Now != nil {
			now = service.Now
		}
		if err := config.AddAcceptance(absolute, config.Acceptance{
			ID: id, Reason: "accepted through goatest CLI: " + finding.Summary, Expires: now().UTC().Add(30 * 24 * time.Hour),
		}); err != nil {
			return report.Report{}, err
		}
		return report.Report{
			Schema: report.SchemaV1, Verdict: report.VerdictAssured, Contract: latest.Contract, Snapshot: latest.Snapshot,
			Evidence: []report.Evidence{{Kind: "acceptance", ID: id, Status: "recorded", Detail: "expires in 30 days"}},
		}, nil
	case cli.CommandReplay:
		latest, err := loadLatest(absolute)
		if err != nil {
			return report.Report{}, err
		}
		if _, ok := find(latest, id); !ok {
			return report.Report{}, fmt.Errorf("goatest: finding %q is absent from the latest report", id)
		}
		request.NoApply = true
		result, err := service.run(ctx, absolute, request)
		if err != nil {
			return report.Report{}, err
		}
		if err := WriteReports(absolute, result); err != nil {
			return report.Report{}, err
		}
		return result, nil
	case cli.CommandVerify:
		result, err := service.run(ctx, absolute, request)
		if err != nil {
			return report.Report{}, err
		}
		if err := WriteReports(absolute, result); err != nil {
			return report.Report{}, err
		}
		return result, nil
	default:
		return report.Report{}, fmt.Errorf("goatest: command %q is unsupported", command)
	}
}

func (service Service) run(ctx context.Context, root string, request cli.Request) (report.Report, error) {
	runner := service.Run
	if runner == nil {
		runner = assure.Run
	}
	options := assure.Options{
		Root: root, Contract: request.Contract, NoApply: request.NoApply,
		Changed: request.Changed, ChangedRef: request.ChangedRef,
		GoBinary: service.GoBinary, TempDirectory: service.TempDirectory, Environment: service.Environment,
	}
	if service.Progress != nil {
		var mutex sync.Mutex
		options.Progress = func(event assure.Event) {
			mutex.Lock()
			defer mutex.Unlock()
			_, _ = fmt.Fprintf(service.Progress, "goatest: %-18s %s\n", report.LineText(event.Kind), report.LineText(event.Detail))
		}
	}
	return runner(ctx, options)
}

func loadLatest(root string) (report.Report, error) {
	path := filepath.Join(root, ".goatest", "report.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return report.Report{}, fmt.Errorf("goatest: read latest report: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var result report.Report
	if err := decoder.Decode(&result); err != nil {
		return report.Report{}, fmt.Errorf("goatest: decode latest report: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return report.Report{}, errors.New("goatest: latest report has trailing data")
	}
	if result.Schema != report.SchemaV1 {
		return report.Report{}, fmt.Errorf("goatest: latest report schema %q is unsupported", result.Schema)
	}
	return result, nil
}

func find(input report.Report, id string) (report.Finding, bool) {
	for _, finding := range input.Findings {
		if finding.ID == id {
			return finding, true
		}
	}
	return report.Finding{}, false
}

func repairsFor(repairs []report.Repair, finding string) []report.Repair {
	var result []report.Repair
	for _, repair := range repairs {
		if repair.Finding == finding {
			result = append(result, repair)
		}
	}
	return result
}
