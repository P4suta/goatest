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
)

type RepositoryValidatorOptions struct {
	Root              string
	Contract          string
	GoBinary          string
	TempDirectory     string
	Environment       []string
	MutationOperators []string
}

type repositoryValidator struct{ options RepositoryValidatorOptions }

func NewRepositoryValidator(options RepositoryValidatorOptions) *repositoryValidator {
	return &repositoryValidator{options: options}
}

func (validator *repositoryValidator) OriginalStable(ctx context.Context, candidate provider.Candidate) error {
	return validator.withCandidate(ctx, candidate, func(ctx context.Context, root string) error {
		workspace, err := validator.open(ctx, root)
		if err != nil {
			return err
		}
		defer func() { _ = workspace.Close() }()
		return runPassing(ctx, workspace, []string{"go", "test", "-count=1", "./..."}, "generated candidate on original code")
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
		session, err := workspace.Prepare(ctx, mutationbridge.PrepareOptions{
			Contract: validator.options.Contract, Operators: slices.Clone(validator.options.MutationOperators),
			VerifyArgv: []string{"go", "test", "-run=^$", "./..."},
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
			Mutant: mutant.ID, Package: mutant.Package, Timeout: 30 * time.Second,
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
		if err := runPassing(ctx, workspace, []string{"go", "test", "-count=1", "./..."}, "related suite"); err != nil {
			return err
		}
		listed, err := workspace.Exec(ctx, gomutants.Command{Argv: []string{"go", "list", "-json", "./..."}, Timeout: 5 * time.Minute})
		if err != nil || listed.ExitCode != 0 || listed.TimedOut {
			return commandError("candidate go list", listed, err)
		}
		model, err := goanalysis.DecodePackages(bytes.NewReader(listed.Output))
		if err != nil {
			return err
		}
		concurrent, err := goanalysis.ConcurrencyPackages(root, model.Packages)
		if err != nil {
			return err
		}
		raceResult, err := CollectRace(ctx, workspace, model, concurrent, validator.options.Contract, nil)
		if err != nil {
			return err
		}
		if len(raceResult.Findings) != 0 {
			return fmt.Errorf("goatest: candidate suite reproduced a data race")
		}
		return nil
	})
}

func (validator *repositoryValidator) open(ctx context.Context, root string) (*mutationbridge.Workspace, error) {
	return mutationbridge.Open(ctx, root, mutationbridge.Options{
		GoBinary: validator.options.GoBinary, TempDirectory: validator.options.TempDirectory,
		ReportDirectory: ".goatest", Environment: slices.Clone(validator.options.Environment),
	})
}

func runPassing(ctx context.Context, workspace *mutationbridge.Workspace, argv []string, purpose string) error {
	result, err := workspace.Exec(ctx, gomutants.Command{Argv: argv, Timeout: 10 * time.Minute})
	if err != nil {
		return err
	}
	if result.TimedOut || result.ExitCode != 0 {
		return fmt.Errorf("goatest: %s failed (exit=%d timeout=%t): %s", purpose, result.ExitCode, result.TimedOut, summarize(result.Output))
	}
	return nil
}

func (validator *repositoryValidator) withCandidate(ctx context.Context, candidate provider.Candidate, action func(context.Context, string) error) error {
	root, err := os.MkdirTemp(validator.options.TempDirectory, "goatest-candidate-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(root) }()
	if err := copyRepository(validator.options.Root, root); err != nil {
		return err
	}
	if err := writeCandidate(root, candidate); err != nil {
		return err
	}
	return action(ctx, root)
}

func copyRepository(source, destination string) error {
	if err := validateCopyRoots(source, destination); err != nil {
		return err
	}
	return filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		if relative == "." {
			return nil
		}
		relativeSlash := filepath.ToSlash(relative)
		first, _, _ := strings.Cut(relativeSlash, "/")
		if entry.IsDir() && (first == ".git" || first == ".goatest" || first == "reports" || first == "dist") {
			return filepath.SkipDir
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if entry.Type()&fs.ModeSymlink != 0 {
			return fmt.Errorf("goatest: candidate validation refuses symbolic link %s", relativeSlash)
		}
		target := filepath.Join(destination, relative)
		if entry.IsDir() {
			return os.MkdirAll(target, info.Mode().Perm())
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("goatest: candidate validation refuses irregular file %s", relativeSlash)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		input, err := os.Open(path)
		if err != nil {
			return err
		}
		output, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, info.Mode().Perm())
		if err != nil {
			_ = input.Close()
			return err
		}
		_, copyErr := io.Copy(output, input)
		return errors.Join(copyErr, output.Close(), input.Close())
	})
}

func validateCopyRoots(source, destination string) error {
	canonicalSource, err := filepath.EvalSymlinks(source)
	if err != nil {
		return fmt.Errorf("goatest: resolve candidate source: %w", err)
	}
	canonicalDestination, err := filepath.EvalSymlinks(destination)
	if err != nil {
		return fmt.Errorf("goatest: resolve candidate destination: %w", err)
	}
	inside := func(root, path string) bool {
		relative, relErr := filepath.Rel(root, path)
		return relErr == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
	}
	if inside(canonicalSource, canonicalDestination) || inside(canonicalDestination, canonicalSource) {
		return errors.New("goatest: candidate source and destination must be separate trees")
	}
	return nil
}

func writeCandidate(root string, candidate provider.Candidate) error {
	if !generationPathAllowed(candidate.Path, []string{"**/*_test.go", "**/testdata/fuzz/**"}) {
		return fmt.Errorf("goatest: candidate path %q is unsafe", candidate.Path)
	}
	target := filepath.Join(root, filepath.FromSlash(candidate.Path))
	existing, err := os.ReadFile(target)
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
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	return os.WriteFile(target, candidate.Content, 0o644)
}
