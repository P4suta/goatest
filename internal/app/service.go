// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package app connects the deterministic CLI command layer to repository
// assurance and durable report/config artifacts.
package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/P4suta/goatest/internal/assure"
	"github.com/P4suta/goatest/internal/cli"
	"github.com/P4suta/goatest/internal/config"
	"github.com/P4suta/goatest/internal/repair"
	"github.com/P4suta/goatest/internal/report"
	"github.com/P4suta/goatest/internal/trace"
	"github.com/P4suta/goatest/internal/ui"
)

type RunFunc func(context.Context, assure.Options) (report.Report, error)

type Service struct {
	Root          string
	GoBinary      string
	TempDirectory string
	Environment   []string
	Progress      io.Writer
	// Output is the stream the final report is rendered to. A jsonl UI streams
	// its progress events there so that one pipe carries the whole stream; a
	// service without one falls back to the plain progress stream.
	Output io.Writer
	// Interactive reports whether the progress stream reaches a terminal that
	// can render an in-place dashboard. Its zero value is not interactive, so
	// that only a composition root that probed a real terminal turns the
	// dashboard on.
	Interactive  func(io.Writer) bool
	Run          RunFunc
	Plan         RunFunc
	FixValidator repair.Validator
	Now          func() time.Time
	// ProcessID identifies the process a default trace directory is named
	// after, so that two goatest processes tracing one repository never write
	// into the same recording. A nil value is the running process.
	ProcessID func() int
	// TraceFilesystem is the filesystem a recording is written through. Its
	// zero value is the os package, which is what a traced run records into.
	// A caller fills in the one operation it wants to answer for, because the
	// failures a recording must survive are not failures a disk produces on
	// demand.
	TraceFilesystem trace.Filesystem
	// DiagnosticsFilesystem is the filesystem a failure bundle is written
	// through, filled in the same way and for the same reason.
	DiagnosticsFilesystem DiagnosticsFilesystem
	absolute              func(string) (string, error)
	// notes is the renderer the current run reports its progress through,
	// selected per request by runAndWrite; every note of a run funnels through
	// it. Its zero value renders plain lines to Progress, which is what every
	// path outside a verify or replay reports through.
	notes ui.Notes
	// doctorFilesystem is the filesystem the doctor's writability probe runs
	// through; its zero value is the os package.
	doctorFilesystem doctorProbeFilesystem
}

var (
	reportRunSequence     atomic.Uint64
	readConfigurationFile = os.ReadFile
)

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
	clock := service.clock()
	started := clock().UTC()
	finishOperation := func(result report.Report, operationErr error) (report.Report, error) {
		if operationErr != nil {
			return result, operationErr
		}
		if result.Contract == "" {
			if loaded, loadErr := config.Load(absolute); loadErr == nil {
				result.Contract = loaded.Contract
			}
		}
		result = finalizeReportKind(ctx, absolute, request, result, report.RunOperation, started, clock().UTC())
		if validationErr := report.ValidateForPersistence(result); validationErr != nil {
			return result, fmt.Errorf("goatest: finalize %s report: %w", command, validationErr)
		}
		return result, nil
	}
	switch command {
	case cli.CommandInit:
		if err := config.Init(absolute); err != nil {
			return report.Report{}, err
		}
		return finishOperation(report.Report{
			Schema: report.SchemaV1, RunKind: report.RunOperation, Verdict: report.VerdictCompleted,
			Evidence: []report.Evidence{
				{Kind: "configuration", ID: config.FileName, Status: "initialized"},
				// The next steps a fresh project takes, in the report itself so
				// that every UI renders them: runs write caches under .goatest/
				// and reports under reports/, and nothing else says so first.
				{Kind: "next-step", ID: "gitignore", Status: "suggested", Detail: "add .goatest/ and reports/ to .gitignore; verifications write caches and reports there"},
				{Kind: "next-step", ID: "doctor", Status: "suggested", Detail: "run 'goatest doctor' to check everything a verification needs"},
				{Kind: "next-step", ID: "verify", Status: "suggested", Detail: "run 'goatest verify ./...' for a first full assurance"},
			},
		}, nil)
	case cli.CommandReport:
		return loadSelected(absolute, request)
	case cli.CommandPlan:
		planner := service.Plan
		if planner == nil {
			planner = assure.Plan
		}
		return finishOperation(planner(ctx, service.assureOptions(absolute, request)))
	case cli.CommandDoctor:
		return finishOperation(service.doctor(ctx, absolute))
	case cli.CommandFix:
		return finishOperation(service.fix(ctx, absolute, request))
	case cli.CommandCache:
		return finishOperation(service.cache(absolute, id))
	case cli.CommandExplain:
		latest, err := loadLatestAny(absolute)
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
		latest, err := loadLatestAny(absolute)
		if err != nil {
			return report.Report{}, err
		}
		_, ok := find(latest, id)
		if !ok {
			return report.Report{}, fmt.Errorf("goatest: finding %q is absent from the latest report", id)
		}
		reason := strings.TrimSpace(request.Reason)
		owner := strings.TrimSpace(request.Owner)
		ticket := strings.TrimSpace(request.Ticket)
		if reason == "" || strings.TrimSpace(request.Expires) == "" {
			return report.Report{}, errors.New("goatest: acceptance requires a reason and expiry")
		}
		expires, parseErr := time.Parse(time.RFC3339, request.Expires)
		if parseErr != nil {
			return report.Report{}, fmt.Errorf("goatest: acceptance expiry: %w", parseErr)
		}
		if !expires.After(clock().UTC()) {
			return report.Report{}, errors.New("goatest: acceptance expiry must be in the future")
		}
		if err := config.AddAcceptance(absolute, config.Acceptance{
			ID: id, Reason: reason, Expires: expires, Owner: owner, Ticket: ticket,
		}); err != nil {
			return report.Report{}, err
		}
		return finishOperation(report.Report{
			Schema: report.SchemaV1, RunKind: report.RunOperation, Verdict: report.VerdictCompleted, Contract: latest.Contract, Snapshot: latest.Snapshot,
			Evidence: []report.Evidence{{Kind: "acceptance", ID: id, Status: "recorded", Detail: "expires " + expires.UTC().Format(time.RFC3339)}},
		}, nil)
	case cli.CommandReplay:
		latest, err := loadLatestAny(absolute)
		if err != nil {
			return report.Report{}, err
		}
		finding, ok := find(latest, id)
		if !ok {
			return report.Report{}, fmt.Errorf("goatest: finding %q is absent from the latest report", id)
		}
		if finding.MutantID == "" {
			return report.Report{}, fmt.Errorf("goatest: finding %q is not replayable because it has no mutant identity", id)
		}
		request.ReplayFindingID = finding.ID
		request.ReplayMutantID = finding.MutantID
		return service.runAndWrite(ctx, absolute, request)
	case cli.CommandVerify:
		return service.runAndWrite(ctx, absolute, request)
	default:
		return report.Report{}, fmt.Errorf("goatest: command %q is unsupported", command)
	}
}

func (service Service) runAndWrite(ctx context.Context, root string, request cli.Request) (report.Report, error) {
	clock := service.clock()
	started := clock().UTC()
	// The renderer the request asked for reports everything below: the run's
	// own progress, and what the recording and the diagnostics bundle note on
	// the way out. The service is a value, so the selection lives exactly as
	// long as this run.
	notes := service.selectNotes(request)
	defer notes.Close()
	service.notes = notes
	// The recording outlives the run it records, because what a run left
	// behind is read after it ended and, on the paths below, after it failed.
	recording, finishRecording := service.startTrace(root, request)
	result, err := service.run(ctx, root, request, recording.recorder)
	finishRecording(result, err)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return report.Report{}, err
		}
		result = infrastructureErrorReport(result, request, err)
		result = finalizeReport(ctx, root, request, result, started, clock().UTC())
		service.writeDiagnostics(root, result, recording, err)
		if writeErr := WriteReports(root, result); writeErr != nil {
			return result, errors.Join(err, writeErr)
		}
		return result, err
	}
	result = selectReplayFinding(result, request.ReplayFindingID)
	result = finalizeReport(ctx, root, request, result, started, clock().UTC())
	if err := WriteReports(root, result); err != nil {
		return report.Report{}, err
	}
	return result, nil
}

func infrastructureErrorReport(partial report.Report, request cli.Request, cause error) report.Report {
	result := partial
	result.Schema = report.SchemaV1
	result.Verdict = report.VerdictError
	if result.Contract == "" {
		result.Contract = request.Contract
	}
	result.Findings = append(result.Findings, report.Finding{
		ID:      report.FindingID("infrastructure", "assurance-run"),
		Kind:    "infrastructure",
		Summary: cause.Error(),
	})
	result.Limitations = appendLimitation(result.Limitations, report.Limitation{
		Code: "assurance-incomplete", Summary: "Assurance stopped before the configured contract could be completed",
	})
	return result
}

func (service Service) run(ctx context.Context, root string, request cli.Request, recorder *trace.Recorder) (report.Report, error) {
	runner := service.Run
	if runner == nil {
		runner = assure.Run
	}
	options := service.assureOptions(root, request)
	options.Trace = recorder
	cacheHit := false
	var mutex sync.Mutex
	options.Progress = func(event assure.Event) {
		mutex.Lock()
		defer mutex.Unlock()
		if event.Kind == "cache-hit" {
			cacheHit = true
		}
		service.note(event.Kind, event.Detail)
	}
	result, err := runner(ctx, options)
	mutex.Lock()
	derived := cacheHit
	mutex.Unlock()
	if derived {
		source := result.RunID
		if source == "" {
			source = "evidence-" + result.Snapshot
		}
		result.Cache = report.Cache{Derived: true, SourceRunID: source}
	}
	return result, err
}

// clock is the time source the service measures with, which is the wall clock
// unless a caller injected one.
func (service Service) clock() func() time.Time {
	if service.Now != nil {
		return service.Now
	}
	return time.Now
}

// note reports one progress note through the renderer of the current run, and
// through a deterministic plain line on the progress stream wherever no run
// selected one. A service without a progress stream reports nothing.
func (service Service) note(kind, detail string) {
	if service.notes != nil {
		service.notes.Note(kind, detail)
		return
	}
	ui.NewPlain(service.Progress).Note(kind, detail)
}

// selectNotes picks the renderer a request's --ui asks for. A jsonl stream
// belongs on the output stream its final report event will follow; a service
// without one falls back to plain rather than guessing where the stream went.
// The dashboard renders only where a composition root probed an interactive
// terminal, so that every test and every pipe keeps deterministic lines.
func (service Service) selectNotes(request cli.Request) ui.Notes {
	switch {
	case request.UI == cli.UIJSONL && service.Output != nil:
		return ui.NewJSONL(service.Output, service.Now)
	case request.UI == cli.UIAuto && service.Interactive != nil && service.Interactive(service.Progress):
		return ui.NewDashboard(service.Progress, ui.DashboardOptions{Now: service.Now})
	default:
		return ui.NewPlain(service.Progress)
	}
}

func (service Service) assureOptions(root string, request cli.Request) assure.Options {
	return assure.Options{
		Root: root, Contract: request.Contract, NoApply: true,
		Changed: request.Changed, ChangedRef: request.ChangedRef,
		ReplayFindingID: request.ReplayFindingID, ReplayMutantID: request.ReplayMutantID,
		Packages: slices.Clone(request.Packages), PackageScope: explicitPackageScope(request.Packages),
		TestArgs: slices.Clone(request.TestArgs),
		GoBinary: service.GoBinary, TempDirectory: service.TempDirectory, Environment: service.Environment, Now: service.Now,
		KeepTemp: request.KeepTemp,
	}
}

func finalizeReport(ctx context.Context, root string, request cli.Request, input report.Report, started, finished time.Time) report.Report {
	return finalizeReportKind(ctx, root, request, input, requestedRunKind(request), started, finished)
}

func finalizeReportKind(ctx context.Context, root string, request cli.Request, input report.Report, kind report.RunKind, started, finished time.Time) report.Report {
	result := input
	result.Schema = report.SchemaV1
	result.RunKind = kind
	result.RunID = newRunID(root, result.Snapshot, kind, finished)
	if result.Contract == "" {
		result.Contract = request.Contract
	}
	if result.Contract == "" {
		result.Contract = "unavailable"
		result.Limitations = appendLimitation(result.Limitations, report.Limitation{
			Code: "contract-metadata-unavailable", Summary: "The assurance contract could not be resolved before execution stopped",
		})
	}
	if result.Snapshot == "" {
		result.Snapshot = "unavailable"
		result.Limitations = appendLimitation(result.Limitations, report.Limitation{
			Code: "snapshot-metadata-unavailable", Summary: "The source snapshot identity could not be computed before execution stopped",
		})
	}
	requested := requestedScope(request, kind)
	if kind == report.RunReplay {
		result.Scope.Requested.Kind = string(report.RunReplay)
		result.Scope.Requested.Project = "."
	}
	if result.Scope.Requested.Kind == "" {
		result.Scope.Requested = requested
	}
	if result.Scope.Resolved.Kind == "" {
		result.Scope.Resolved = requested
	}
	result.Verdict = scopedVerdict(result.Verdict, kind, result.Scope.Resolved.Kind, len(result.Findings))
	result.Timing = report.Timing{
		StartedAt: started.UTC().Format(time.RFC3339Nano), FinishedAt: finished.UTC().Format(time.RFC3339Nano),
		DurationMS: max(0, finished.Sub(started).Milliseconds()),
	}
	if result.Configuration.Digest == "" {
		digest, digestErr := configurationDigest(root, request)
		result.Configuration.Digest = digest
		if digestErr != nil {
			result.Limitations = appendLimitation(result.Limitations, report.Limitation{
				Code: "configuration-metadata-unavailable", Summary: "The effective configuration could not be read while finalizing the report",
			})
		}
	}
	if result.Toolchain.Goatest == "" {
		result.Toolchain.Goatest = assure.GoatestVersion
	}
	if result.Toolchain.Go == "" {
		result.Toolchain.Go = "unavailable"
		result.Limitations = appendLimitation(result.Limitations, report.Limitation{
			Code: "go-toolchain-metadata-unavailable", Summary: "The Go toolchain identity could not be resolved before execution stopped",
		})
	}
	if result.Toolchain.GoMutants == "" {
		if version, versionErr := assure.GoMutantsVersion(); versionErr == nil {
			result.Toolchain.GoMutants = version
		} else {
			result.Toolchain.GoMutants = "unavailable"
			result.Limitations = appendLimitation(result.Limitations, report.Limitation{
				Code: "go-mutants-metadata-unavailable", Summary: "The go-mutants version could not be resolved from build info",
			})
		}
	}
	if result.Toolchain.OS == "" {
		result.Toolchain.OS = runtime.GOOS
	}
	if result.Toolchain.Arch == "" {
		result.Toolchain.Arch = runtime.GOARCH
	}
	if len(result.Repository.Packages) == 0 {
		result.Repository.Packages = slices.Clone(request.Packages)
	}
	if result.Repository.Module == "" {
		result.Repository.Module = "unavailable"
		result.Limitations = appendLimitation(result.Limitations, report.Limitation{
			Code: "module-metadata-unavailable", Summary: "The Go module identity could not be resolved before execution stopped",
		})
	}
	if result.Repository.Module != "" {
		if len(result.Scope.Requested.Modules) == 0 {
			result.Scope.Requested.Modules = []string{result.Repository.Module}
		}
		if len(result.Scope.Resolved.Modules) == 0 {
			result.Scope.Resolved.Modules = []string{result.Repository.Module}
		}
	}
	git, gitErr := inspectGit(ctx, root, request)
	if gitErr != nil {
		result.Repository.Git = report.Git{Commit: "unavailable", MergeBase: "unavailable"}
		result.Limitations = appendLimitation(result.Limitations, report.Limitation{
			Code: "git-metadata-unavailable", Summary: "Git identity or changeset metadata could not be resolved",
		})
	} else {
		result.Repository.Git = git
		if len(result.Scope.Requested.Files) == 0 && kind == report.RunChangeset {
			result.Scope.Requested.Files = slices.Clone(git.ChangedFiles)
		}
		if len(result.Scope.Resolved.Files) == 0 && result.Scope.Resolved.Kind == string(report.RunChangeset) {
			result.Scope.Resolved.Files = slices.Clone(git.ChangedFiles)
		}
	}
	return result
}

func requestedRunKind(request cli.Request) report.RunKind {
	switch {
	case request.ReplayFindingID != "" || request.ReplayMutantID != "":
		return report.RunReplay
	case request.Changed:
		return report.RunChangeset
	case explicitPackageScope(request.Packages):
		return report.RunPackage
	default:
		return report.RunFull
	}
}

func explicitPackageScope(packages []string) bool {
	return len(packages) != 0 && (len(packages) != 1 || packages[0] != "./...")
}

func requestedScope(request cli.Request, kind report.RunKind) report.ScopeSpec {
	ref := request.ChangedRef
	if kind == report.RunChangeset && ref == "" {
		ref = "HEAD"
	}
	return report.ScopeSpec{
		Kind: string(kind), Project: ".", Packages: slices.Clone(request.Packages), Ref: ref,
	}
}

func scopedVerdict(verdict report.Verdict, kind report.RunKind, resolved string, findings int) report.Verdict {
	if kind == report.RunReplay {
		switch verdict {
		case report.VerdictError:
			return verdict
		default:
			if findings == 0 {
				return report.VerdictResolved
			}
			return report.VerdictReproduced
		}
	}
	if verdict != report.VerdictAssured {
		return verdict
	}
	switch {
	case resolved == string(report.RunFull):
		return report.VerdictAssured
	case kind == report.RunChangeset:
		return report.VerdictChangeAssured
	case kind == report.RunPackage:
		return report.VerdictScopeAssured
	default:
		return verdict
	}
}

func newRunID(root, snapshot string, kind report.RunKind, finished time.Time) string {
	sequence := reportRunSequence.Add(1)
	payload := fmt.Sprintf("%s\x00%s\x00%s\x00%d\x00%d", root, snapshot, kind, finished.UnixNano(), sequence)
	digest := sha256.Sum256([]byte(payload))
	return finished.UTC().Format("20060102T150405.000000000Z") + "-" + hex.EncodeToString(digest[:6])
}

func configurationDigest(root string, request cli.Request) (string, error) {
	data, err := readConfigurationFile(filepath.Join(root, config.FileName))
	var readErr error
	if errors.Is(err, os.ErrNotExist) {
		data = []byte("goatest-config-v1-defaults")
	} else if err != nil {
		data = []byte("goatest-config-v1-unreadable")
		readErr = fmt.Errorf("goatest: read effective configuration: %w", err)
	}
	invocation, _ := json.Marshal(struct {
		Contract        string   `json:"contract"`
		Packages        []string `json:"packages"`
		TestArgs        []string `json:"test_args"`
		Changed         bool     `json:"changed"`
		ChangedRef      string   `json:"changed_ref"`
		ReplayFindingID string   `json:"replay_finding_id"`
		ReplayMutantID  string   `json:"replay_mutant_id"`
	}{
		Contract: request.Contract, Packages: request.Packages, TestArgs: request.TestArgs,
		Changed: request.Changed, ChangedRef: request.ChangedRef,
		ReplayFindingID: request.ReplayFindingID, ReplayMutantID: request.ReplayMutantID,
	})
	hash := sha256.New()
	_, _ = hash.Write([]byte("goatest-effective-configuration-v1\x00"))
	_, _ = hash.Write(data)
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write(invocation)
	digest := hash.Sum(nil)
	return hex.EncodeToString(digest), readErr
}

func inspectGit(ctx context.Context, root string, request cli.Request) (report.Git, error) {
	commit, err := gitOutput(ctx, root, "rev-parse", "--verify", "HEAD")
	if err != nil {
		return report.Git{}, err
	}
	status, err := gitOutputBytes(ctx, root, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return report.Git{}, err
	}
	metadata := report.Git{Available: true, Commit: strings.TrimSpace(string(commit)), Dirty: len(status) != 0}
	base := "HEAD"
	if request.ChangedRef != "" {
		base = request.ChangedRef
	}
	mergeBase, mergeErr := gitOutput(ctx, root, "merge-base", "HEAD", base)
	if mergeErr == nil {
		metadata.MergeBase = strings.TrimSpace(string(mergeBase))
	} else if base == "HEAD" {
		metadata.MergeBase = metadata.Commit
	} else {
		return report.Git{}, mergeErr
	}
	diffBase := metadata.MergeBase
	if diffBase == "" {
		diffBase = base
	}
	changed, err := gitOutputBytes(ctx, root, "diff", "--name-only", "-z", "--find-renames", diffBase)
	if err != nil {
		return report.Git{}, err
	}
	untracked, err := gitOutputBytes(ctx, root, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return report.Git{}, err
	}
	metadata.ChangedFiles = nulPaths(append(changed, untracked...))
	return metadata, nil
}

const gitMetadataTimeout = 30 * time.Second

func gitOutput(ctx context.Context, root string, arguments ...string) ([]byte, error) {
	return gitOutputBytes(ctx, root, arguments...)
}

func gitOutputBytes(ctx context.Context, root string, arguments ...string) ([]byte, error) {
	bounded, cancel := context.WithTimeout(ctx, gitMetadataTimeout)
	defer cancel()
	command := exec.CommandContext(bounded, "git", arguments...)
	command.Dir = root
	output, err := command.Output()
	if err != nil {
		return nil, err
	}
	return output, nil
}

func nulPaths(data []byte) []string {
	var paths []string
	for _, raw := range bytes.Split(data, []byte{0}) {
		path := strings.TrimSpace(string(raw))
		if path != "" {
			paths = append(paths, filepath.ToSlash(path))
		}
	}
	slices.Sort(paths)
	return slices.Compact(paths)
}

func appendLimitation(limitations []report.Limitation, item report.Limitation) []report.Limitation {
	for _, existing := range limitations {
		if existing.Code == item.Code && existing.Summary == item.Summary {
			return limitations
		}
	}
	return append(limitations, item)
}

func loadSelected(root string, request cli.Request) (report.Report, error) {
	if request.ReportRunID != "" {
		if !safeRunID(request.ReportRunID) {
			return report.Report{}, fmt.Errorf("goatest: unsafe report run ID %q", request.ReportRunID)
		}
		return loadReport(filepath.Join(root, "reports", "runs", request.ReportRunID, "assurance-report-v1.json"), fmt.Sprintf("report run %q", request.ReportRunID))
	}
	if request.ReportLatestFull {
		return loadReport(filepath.Join(root, ".goatest", "latest-full.json"), "latest report")
	}
	return loadLatestAny(root)
}

func loadLatestAny(root string) (report.Report, error) {
	return loadReport(filepath.Join(root, ".goatest", "latest-any.json"), "latest report")
}

func loadReport(path, label string) (report.Report, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return report.Report{}, fmt.Errorf("goatest: read %s: %w", label, err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var result report.Report
	if err := decoder.Decode(&result); err != nil {
		return report.Report{}, fmt.Errorf("goatest: decode %s: %w", label, err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return report.Report{}, fmt.Errorf("goatest: %s has trailing data", label)
	}
	if result.Schema != report.SchemaV1 {
		return report.Report{}, fmt.Errorf("goatest: %s schema %q is unsupported", label, result.Schema)
	}
	if err := report.ValidateForPersistence(result); err != nil {
		return report.Report{}, fmt.Errorf("goatest: invalid %s: %w", label, err)
	}
	return result, nil
}

func selectReplayFinding(input report.Report, id string) report.Report {
	if id == "" {
		return input
	}
	finding, reproduced := find(input, id)
	if reproduced {
		input.Findings = []report.Finding{finding}
		input.Repairs = repairsFor(input.Repairs, id)
		return input
	}
	input.Findings = nil
	input.Repairs = nil
	return input
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
