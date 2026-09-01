// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package assure

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	gomutants "github.com/P4suta/go-mutants"
	goanalysis "github.com/P4suta/goatest/internal/golang"
	"github.com/P4suta/goatest/internal/mutationbridge"
	"github.com/P4suta/goatest/internal/provider"
	"github.com/P4suta/goatest/internal/report"
	"github.com/P4suta/goatest/internal/trace"
)

type RepositoryValidatorOptions struct {
	Root              string
	Contract          string
	GoBinary          string
	TempDirectory     string
	Environment       []string
	MutationOperators []string
	Packages          []string
	BuildTags         []string
	TestArgs          []string
	Timeout           time.Duration
	// Trace records the commands that validate a candidate. A nil recorder
	// validates untraced.
	Trace *trace.Recorder
	// KeepTemp keeps the isolated tree a candidate was validated in instead of
	// removing it, and records the tree as an artifact of the recording. It is
	// a debugging aid: what a validation decided is decided the same way
	// whether the tree survives it or not.
	KeepTemp bool
}

type repositoryValidator struct{ options RepositoryValidatorOptions }

type validationWorkspace interface {
	CommandWorkspace
	Close() error
}

type validationReadCloser interface {
	io.Reader
	Close() error
}

type validationWriteCloser interface {
	io.Writer
	Close() error
}

func defaultPrepareValidationSession(ctx context.Context, workspace validationWorkspace, options mutationbridge.PrepareOptions) (MutationSession, error) {
	mutationWorkspace, ok := workspace.(*mutationbridge.Workspace)
	if !ok {
		return nil, fmt.Errorf("goatest: unsupported validation workspace %T", workspace)
	}
	return mutationWorkspace.Prepare(ctx, options)
}

var (
	openValidationWorkspace = func(ctx context.Context, root string, options mutationbridge.Options) (validationWorkspace, error) {
		return mutationbridge.Open(ctx, root, options)
	}
	prepareValidationSession      = defaultPrepareValidationSession
	decodeValidationPackages      = goanalysis.DecodePackages
	concurrencyValidationPackages = goanalysis.ConcurrencyPackages
	collectValidationRace         = CollectRaceWithOptions
	makeCandidateTemp             = os.MkdirTemp
	removeCandidateTemp           = os.RemoveAll
	copyCandidateRepository       = copyRepository
	writeCandidateRepositoryFile  = writeCandidate
	resolveCandidatePath          = filepath.EvalSymlinks
	walkCandidateFiles            = filepath.WalkDir
	relativeCandidatePath         = filepath.Rel
	makeCandidateDirectory        = os.MkdirAll
	openCandidateInput            = func(name string) (validationReadCloser, error) { return os.Open(name) }
	openCandidateOutput           = func(name string, flag int, mode os.FileMode) (validationWriteCloser, error) {
		return os.OpenFile(name, flag, mode)
	}
	copyCandidateContent = io.Copy
	readCandidateFile    = os.ReadFile
	writeCandidateFile   = os.WriteFile
)

func NewRepositoryValidator(options RepositoryValidatorOptions) *repositoryValidator {
	options.Environment = slices.Clone(options.Environment)
	options.MutationOperators = slices.Clone(options.MutationOperators)
	options.Packages = slices.Clone(options.Packages)
	options.BuildTags = slices.Clone(options.BuildTags)
	options.TestArgs = slices.Clone(options.TestArgs)
	return &repositoryValidator{options: options}
}

func (validator *repositoryValidator) OriginalStable(ctx context.Context, candidate provider.Candidate) error {
	return validator.withCandidate(ctx, candidate, func(ctx context.Context, root string) error {
		workspace, err := validator.open(ctx, root)
		if err != nil {
			return err
		}
		defer func() { _ = workspace.Close() }()
		return runPassing(ctx, workspace, validator.testArgv(false), "generated candidate on original code", validator.timeout())
	})
}

func (validator *repositoryValidator) Kills(ctx context.Context, finding report.Finding, candidate provider.Candidate) error {
	if finding.MutantID == "" {
		return errors.New("goatest: generated candidate finding has no mutant identity")
	}
	return validator.withCandidate(ctx, candidate, func(ctx context.Context, root string) error {
		workspace, err := validator.open(ctx, root)
		if err != nil {
			return err
		}
		defer func() { _ = workspace.Close() }()
		session, err := prepareValidationSession(ctx, workspace, mutationbridge.PrepareOptions{
			Contract: validator.options.Contract, Operators: slices.Clone(validator.options.MutationOperators),
			Packages: slices.Clone(validator.options.Packages), VerifyArgv: validator.testArgv(true),
			BuildTimeout: validator.timeout(), MutantTimeout: validator.mutantTimeout(), VerifyTimeout: validator.timeout(),
		})
		if err != nil {
			return err
		}
		var mutant *gomutants.Mutant
		for i := range session.Catalog().Mutants {
			candidateMutant := session.Catalog().Mutants[i]
			if candidateMutant.ID == finding.MutantID && candidateMutant.Accepted {
				mutant = &candidateMutant
				break
			}
		}
		if mutant == nil {
			return fmt.Errorf("goatest: target mutant %s is absent after applying candidate", finding.MutantID)
		}
		result, err := session.Exec(ctx, gomutants.ExecRequest{
			Mutant: mutant.ID, Package: mutant.Package, Args: slices.Clone(validator.options.TestArgs), Timeout: validator.mutantTimeout(),
		})
		if err != nil {
			return err
		}
		if result.Outcome != gomutants.OutcomeKilled {
			return fmt.Errorf("goatest: candidate produced mutation outcome %s", result.Outcome)
		}
		return nil
	})
}

func (validator *repositoryValidator) Suite(ctx context.Context, candidate provider.Candidate) error {
	return validator.withCandidate(ctx, candidate, func(ctx context.Context, root string) error {
		workspace, err := validator.open(ctx, root)
		if err != nil {
			return err
		}
		defer func() { _ = workspace.Close() }()
		if err := runPassing(ctx, workspace, validator.testArgv(false), "related suite", validator.timeout()); err != nil {
			return err
		}
		listArgv := []string{"go", "list", "-json"}
		if len(validator.options.BuildTags) != 0 {
			listArgv = append(listArgv, "-tags="+strings.Join(validator.options.BuildTags, ","))
		}
		listArgv = append(listArgv, validator.packages()...)
		listed, err := workspace.Exec(ctx, gomutants.Command{Argv: listArgv, Timeout: validator.timeout()})
		if err != nil || listed.ExitCode != 0 || listed.TimedOut {
			return commandError("candidate go list", listed, err)
		}
		model, err := decodeValidationPackages(bytes.NewReader(listed.Output))
		if err != nil {
			return err
		}
		concurrent, err := concurrencyValidationPackages(root, model.Packages)
		if err != nil {
			return err
		}
		raceResult, err := collectValidationRace(ctx, workspace, model, concurrent, validator.options.Contract, RaceOptions{
			Environment: slices.Clone(validator.options.Environment),
			TestArgs:    slices.Clone(validator.options.TestArgs),
			BuildTags:   slices.Clone(validator.options.BuildTags),
		})
		if err != nil {
			return err
		}
		if len(raceResult.Findings) != 0 {
			return fmt.Errorf("goatest: candidate suite reproduced a data race")
		}
		return nil
	})
}

func (validator *repositoryValidator) open(ctx context.Context, root string) (validationWorkspace, error) {
	environment := slices.Clone(validator.options.Environment)
	if len(validator.options.BuildTags) != 0 {
		environment = mutationEnvironment(environment, validator.options.BuildTags)
	}
	return openValidationWorkspace(ctx, root, mutationbridge.Options{
		GoBinary: validator.options.GoBinary, TempDirectory: validator.options.TempDirectory,
		ReportDirectory: ".goatest", Environment: environment, Trace: validator.options.Trace,
	})
}

func (validator *repositoryValidator) packages() []string {
	if len(validator.options.Packages) == 0 {
		return []string{"./..."}
	}
	return slices.Clone(validator.options.Packages)
}

func (validator *repositoryValidator) timeout() time.Duration {
	if validator.options.Timeout > 0 {
		return validator.options.Timeout
	}
	return 10 * time.Minute
}

func (validator *repositoryValidator) mutantTimeout() time.Duration {
	if validator.options.Timeout > 0 {
		return validator.options.Timeout
	}
	return 30 * time.Second
}

func (validator *repositoryValidator) testArgv(compileOnly bool) []string {
	argv := []string{"go", "test"}
	if len(validator.options.BuildTags) != 0 {
		argv = append(argv, "-tags="+strings.Join(validator.options.BuildTags, ","))
	}
	if compileOnly {
		argv = append(argv, "-run=^$")
	} else {
		argv = append(argv, "-count=1")
	}
	argv = append(argv, validator.packages()...)
	if len(validator.options.TestArgs) != 0 {
		argv = append(argv, "-args")
		argv = append(argv, validator.options.TestArgs...)
	}
	return argv
}

func runPassing(ctx context.Context, workspace CommandWorkspace, argv []string, purpose string, timeout time.Duration) error {
	result, err := workspace.Exec(ctx, gomutants.Command{Argv: argv, Timeout: timeout})
	if err != nil {
		return err
	}
	if result.TimedOut || result.ExitCode != 0 {
		return fmt.Errorf("goatest: %s failed (exit=%d timeout=%t): %s", purpose, result.ExitCode, result.TimedOut, summarize(result.Output))
	}
	return nil
}

func (validator *repositoryValidator) withCandidate(ctx context.Context, candidate provider.Candidate, action func(context.Context, string) error) error {
	root, err := makeCandidateTemp(validator.options.TempDirectory, "goatest-candidate-")
	if err != nil {
		return err
	}
	defer validator.releaseCandidate(root)
	if err := copyCandidateRepository(validator.options.Root, root); err != nil {
		return err
	}
	if err := writeCandidateRepositoryFile(root, candidate); err != nil {
		return err
	}
	return action(ctx, root)
}

func copyRepository(source, destination string) error {
	if err := validateCopyRoots(source, destination); err != nil {
		return err
	}
	return walkCandidateFiles(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := relativeCandidatePath(source, path)
		if err != nil {
			return err
		}
		if relative == "." {
			return nil
		}
		relativeSlash := filepath.ToSlash(relative)
		first, _, _ := strings.Cut(relativeSlash, "/")
		if entry.IsDir() && excludedCandidateDirectory(first) {
			return filepath.SkipDir
		}
		if entry.Type()&fs.ModeSymlink != 0 {
			return fmt.Errorf("goatest: candidate validation refuses symbolic link %s", relativeSlash)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		target := filepath.Join(destination, relative)
		if entry.IsDir() {
			return makeCandidateDirectory(target, info.Mode().Perm())
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("goatest: candidate validation refuses irregular file %s", relativeSlash)
		}
		if err := makeCandidateDirectory(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		input, err := openCandidateInput(path)
		if err != nil {
			return err
		}
		output, err := openCandidateOutput(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, info.Mode().Perm())
		if err != nil {
			_ = input.Close()
			return err
		}
		_, copyErr := copyCandidateContent(output, input)
		return errors.Join(copyErr, output.Close(), input.Close())
	})
}

func excludedCandidateDirectory(name string) bool {
	switch name {
	case ".git", ".goatest", "reports", "dist":
		return true
	default:
		return false
	}
}

func validateCopyRoots(source, destination string) error {
	canonicalSource, err := resolveCandidatePath(source)
	if err != nil {
		return fmt.Errorf("goatest: resolve candidate source: %w", err)
	}
	canonicalDestination, err := resolveCandidatePath(destination)
	if err != nil {
		return fmt.Errorf("goatest: resolve candidate destination: %w", err)
	}
	if candidatePathInside(canonicalSource, canonicalDestination) || candidatePathInside(canonicalDestination, canonicalSource) {
		return errors.New("goatest: candidate source and destination must be separate trees")
	}
	return nil
}

func candidatePathInside(root, candidate string) bool {
	relative, err := relativeCandidatePath(root, candidate)
	if err != nil {
		return false
	}
	return filepath.IsLocal(relative)
}

func writeCandidate(root string, candidate provider.Candidate) error {
	if !generationPathAllowed(candidate.Path, []string{"**/*_test.go", "**/testdata/fuzz/**"}) {
		return fmt.Errorf("goatest: candidate path %q is unsafe", candidate.Path)
	}
	target := filepath.Join(root, filepath.FromSlash(candidate.Path))
	existing, err := readCandidateFile(target)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if errors.Is(err, os.ErrNotExist) {
		if candidate.PreimageSHA256 != "" {
			return errors.New("goatest: candidate preimage expects a missing file to exist")
		}
	} else {
		sum := sha256.Sum256(existing)
		if candidate.PreimageSHA256 != hex.EncodeToString(sum[:]) {
			return errors.New("goatest: candidate preimage does not match validation copy")
		}
	}
	if err := makeCandidateDirectory(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	return writeCandidateFile(target, candidate.Content, 0o644)
}
