// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package mutationbridge is goatest's one-way adapter to go-mutants.
package mutationbridge

import (
	"context"
	"errors"
	"fmt"
	"time"

	gomutants "github.com/P4suta/go-mutants"
)

type Options struct {
	GoBinary        string
	TempDirectory   string
	ReportDirectory string
	Environment     []string
}

type PrepareOptions struct {
	Contract      string
	Operators     []string
	Include       []string
	Exclude       []string
	Changed       bool
	ChangedRef    string
	Packages      []string
	Jobs          int
	BuildTimeout  time.Duration
	MutantTimeout time.Duration
	VerifyArgv    []string
	VerifyEnv     []string
	VerifyTimeout time.Duration
}

type mutationWorkspace interface {
	Exec(context.Context, gomutants.Command) (gomutants.CommandResult, error)
	Prepare(context.Context, gomutants.PrepareOptions) (*gomutants.Session, error)
	Close() error
}

type Workspace struct{ inner mutationWorkspace }

var openMutationWorkspace = func(ctx context.Context, root string, options gomutants.OpenOptions) (mutationWorkspace, error) {
	return gomutants.Open(ctx, root, options)
}

func Profile(contract string) (string, error) {
	switch contract {
	case "standard-v1":
		return "strong", nil
	case "deep-v1":
		return "all", nil
	default:
		return "", fmt.Errorf("goatest: mutation contract %q is unknown", contract)
	}
}

func Open(ctx context.Context, root string, options Options) (*Workspace, error) {
	inner, err := openMutationWorkspace(ctx, root, gomutants.OpenOptions{
		GoBinary:        options.GoBinary,
		TempDirectory:   options.TempDirectory,
		ReportDirectory: options.ReportDirectory,
		Env:             append([]string(nil), options.Environment...),
	})
	if err != nil {
		return nil, fmt.Errorf("goatest: open mutation workspace: %w", err)
	}
	return &Workspace{inner: inner}, nil
}

func (workspace *Workspace) Exec(ctx context.Context, command gomutants.Command) (gomutants.CommandResult, error) {
	if workspace == nil || workspace.inner == nil {
		return gomutants.CommandResult{}, errors.New("goatest: nil mutation workspace")
	}
	return workspace.inner.Exec(ctx, command)
}

func (workspace *Workspace) Prepare(ctx context.Context, options PrepareOptions) (*gomutants.Session, error) {
	if workspace == nil || workspace.inner == nil {
		return nil, errors.New("goatest: nil mutation workspace")
	}
	profile, err := Profile(options.Contract)
	if err != nil {
		return nil, err
	}
	session, err := workspace.inner.Prepare(ctx, gomutants.PrepareOptions{
		Profile:       profile,
		Operators:     append([]string(nil), options.Operators...),
		Include:       append([]string(nil), options.Include...),
		Exclude:       append([]string(nil), options.Exclude...),
		Packages:      append([]string(nil), options.Packages...),
		Jobs:          options.Jobs,
		BuildTimeout:  options.BuildTimeout,
		MutantTimeout: options.MutantTimeout,
		Verify: gomutants.Command{
			Argv:    append([]string(nil), options.VerifyArgv...),
			Env:     append([]string(nil), options.VerifyEnv...),
			Timeout: options.VerifyTimeout,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("goatest: prepare mutation session: %w", err)
	}
	return session, nil
}

func (workspace *Workspace) Close() error {
	if workspace == nil || workspace.inner == nil {
		return nil
	}
	err := workspace.inner.Close()
	workspace.inner = nil
	return err
}
