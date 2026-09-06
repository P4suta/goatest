// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

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
	"github.com/P4suta/goatest/internal/buildcache"
	"github.com/P4suta/goatest/internal/cache"
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

	Executable string

	UserCacheDir func() (string, error)
	Progress     io.Writer

	Output io.Writer

	Interactive  func(io.Writer) bool
	Run          RunFunc
	Plan         RunFunc
	FixValidator repair.Validator
	Now          func() time.Time

	ProcessID func() int

	TraceFilesystem trace.Filesystem

	DiagnosticsFilesystem DiagnosticsFilesystem
	absolute              func(string) (string, error)

	notes ui.Notes

	doctorFilesystem doctorProbeFilesystem
}

var (
	reportRunSequence     atomic.Uint64
	readConfigurationFile = os.ReadFile
)

const (
	goCacheEnvironmentVariable = "GOCACHE"
	nativeGoCacheDirectoryName = "go-build"
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
		return finishOperation(service.cache(ctx, absolute, id))
	case cli.CommandTrace:
		return finishOperation(service.readTrace(absolute, id, request.IDs))
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
		request, err = replayRequest(request, latest)
		if err != nil {
			return report.Report{}, err
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

func replayRequest(request cli.Request, latest report.Report) (cli.Request, error) {
	execution := latest.Execution
	if execution.MutationJobs <= 0 ||
		execution.CommandTimeoutNS <= 0 || execution.TargetTimeoutNS <= 0 {
		return cli.Request{}, errors.New("goatest: selected report has incomplete execution metadata")
	}
	request.Contract = latest.Contract
	if len(latest.Scope.Requested.Packages) != 0 {
		request.Packages = slices.Clone(latest.Scope.Requested.Packages)
	}
	execution.TestArgs = slices.Clone(execution.TestArgs)
	execution.BuildTags = slices.Clone(execution.BuildTags)
	execution.MutationOperators = slices.Clone(execution.MutationOperators)
	request.TestArgs = slices.Clone(execution.TestArgs)
	request.ReplayExecution = &execution
	return request, nil
}

func (service Service) runAndWrite(ctx context.Context, root string, request cli.Request) (report.Report, error) {
	clock := service.clock()
	started := clock().UTC()

	notes := service.selectNotes(request)
	defer notes.Close()
	service.notes = notes

	recording, finishRecording := service.startTrace(root, request)
	cacheRoot := filepath.Join(root, ".goatest", "cache")
	lease, err := cache.Acquire(ctx, cacheRoot, func() {
		service.note("cache-wait", "another goatest process is using this repository cache")
	})
	ownsCacheLease := err == nil
	if ownsCacheLease {
		defer func() {
			if releaseErr := lease.Release(); releaseErr != nil {
				service.note("cache-lock-warning", releaseErr.Error())
			}
		}()
	}
	var result report.Report
	if err == nil {
		result, err = service.run(ctx, root, request, recording.recorder)
	} else if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		runResult, runErr := service.run(ctx, root, request, recording.recorder)
		result = runResult
		err = errors.Join(err, runErr)
	}
	finishRecording(result, err)
	if ownsCacheLease {
		service.collectDiagnosticRetention(root)
		service.collectVerdictCache(root)
	}
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return report.Report{}, err
		}
		result = infrastructureErrorReport(result, request, err)
		result = finalizeReport(ctx, root, request, result, started, clock().UTC())
		service.writeDiagnostics(root, result, recording, err)
		if ownsCacheLease {
			service.collectDiagnosticRetention(root)
		}
		if writeErr := WriteReports(root, result); writeErr != nil {
			return result, errors.Join(err, writeErr)
		}
		if ownsCacheLease {
			service.collectDurableArtifacts(root, cacheRoot)
		}
		return result, err
	}
	result = selectReplayFinding(result, request.ReplayFindingID)
	result = finalizeReport(ctx, root, request, result, started, clock().UTC())
	if err := WriteReports(root, result); err != nil {
		return report.Report{}, err
	}
	if checkpointDigest(result.Snapshot) {
		if err := cache.New(cacheRoot).DeleteCheckpoint(result.Snapshot); err != nil {
			service.note("checkpoint-warning", err.Error())
		}
	}
	if ownsCacheLease {
		service.collectDurableArtifacts(root, cacheRoot)
	}
	return result, nil
}

func checkpointDigest(value string) bool {
	if len(value) != hex.EncodedLen(sha256.Size) {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			if character < 'a' || character > 'f' {
				return false
			}
		}
	}
	return true
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

func (service Service) clock() func() time.Time {
	if service.Now != nil {
		return service.Now
	}
	return time.Now
}

func (service Service) note(kind, detail string) {
	if service.notes != nil {
		service.notes.Note(kind, detail)
		return
	}
	ui.NewPlain(service.Progress).Note(kind, detail)
}

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
	program, base := service.buildCacheLocation(root)
	options := assure.Options{
		Root: root, Contract: request.Contract, NoApply: true,
		Changed: request.Changed, ChangedRef: request.ChangedRef,
		ReplayFindingID: request.ReplayFindingID, ReplayMutantID: request.ReplayMutantID,
		Packages: slices.Clone(request.Packages), PackageScope: explicitPackageScope(request.Packages),
		TestArgs: slices.Clone(request.TestArgs),
		GoBinary: service.GoBinary, TempDirectory: service.TempDirectory, Environment: service.Environment, Now: service.Now,
		KeepTemp:          request.KeepTemp,
		BuildCacheProgram: program, BuildCacheDir: base, BuildCacheNativeSource: service.nativeBuildCacheDirectory(),
	}
	if request.ReplayExecution != nil {
		execution := request.ReplayExecution
		options.TestArgs = slices.Clone(execution.TestArgs)
		options.BuildTags = slices.Clone(execution.BuildTags)
		options.MutationOperators = slices.Clone(execution.MutationOperators)
		options.MutationJobs = execution.MutationJobs
		options.CommandTimeout = time.Duration(execution.CommandTimeoutNS)
		options.TargetTimeout = time.Duration(execution.TargetTimeoutNS)
		options.ExecutionPinned = true
	}
	return options
}

func (service Service) nativeBuildCacheDirectory() string {
	environment := service.Environment
	if environment == nil {
		environment = os.Environ()
	}
	var configured string
	for _, entry := range environment {
		name, value, ok := strings.Cut(entry, "=")
		if ok && strings.EqualFold(name, goCacheEnvironmentVariable) {
			configured = value
		}
	}
	if configured != "" {
		if strings.EqualFold(configured, "off") || !filepath.IsAbs(configured) {
			return ""
		}
		return filepath.Clean(configured)
	}
	if service.UserCacheDir == nil {
		return ""
	}
	userCache, err := service.UserCacheDir()
	if err != nil || userCache == "" {
		return ""
	}
	return filepath.Join(userCache, nativeGoCacheDirectoryName)
}

func (service Service) buildCacheLocation(root string) (string, string) {
	return service.Executable, service.buildCacheDirectory(root)
}

func (service Service) buildCacheDirectory(root string) string {
	var fallback string
	if service.UserCacheDir != nil {
		if userCache, cacheErr := service.UserCacheDir(); cacheErr == nil && userCache != "" {
			fallback = filepath.Join(userCache, "goatest", buildcache.DefaultBaseName)
		}
	}
	loaded, err := config.Load(root)
	if err != nil {
		return ""
	}
	return buildcache.BaseDirectory(root, loaded.Cache.BuildDir, fallback)
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
		result.Toolchain.Goatest = assure.ResolvedGoatestVersion()
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
		path := filepath.Join(reportsRoot(root), request.ReportRunID, "assurance-report-v1.json")

		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			return report.Report{}, fmt.Errorf("goatest: report run %q is not in reports/runs: it was collected or never written", request.ReportRunID)
		}
		return loadReport(path, fmt.Sprintf("report run %q", request.ReportRunID))
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
