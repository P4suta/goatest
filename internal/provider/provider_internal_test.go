// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package provider

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"slices"
	"testing"
	"time"

	"github.com/P4suta/goatest/internal/report"
)

func TestGenerateUsesOnlyExplicitEnvironmentWhenProvided(t *testing.T) {
	sentinel := errors.New("captured environment")
	original := startGenerationProcess
	t.Cleanup(func() { startGenerationProcess = original })
	var captured []string
	startGenerationProcess = func(command *exec.Cmd) (generationProcessTree, error) {
		captured = slices.Clone(command.Env)
		return nil, sentinel
	}
	environment := []string{"PATH=provider-path", "EXPLICIT=value"}
	client := Client{Command: []string{os.Args[0]}, Environment: environment}
	_, err := client.Generate(t.Context(), Request{Version: 1, Finding: report.Finding{ID: "finding-a"}})
	if !errors.Is(err, sentinel) || !slices.Equal(captured, environment) {
		t.Fatalf("Generate = (%v), environment=%v", err, captured)
	}
	captured[0] = "MUTATED=yes"
	if environment[0] != "PATH=provider-path" {
		t.Fatal("provider command environment aliases caller input")
	}
}

func TestLimitedBufferHonoursEveryBoundary(t *testing.T) {
	t.Run("multiple writes", func(t *testing.T) {
		buffer := &limitedBuffer{remaining: 5}
		if written, err := buffer.Write([]byte("ab")); err != nil || written != 2 {
			t.Fatalf("first Write = (%d, %v)", written, err)
		}
		if written, err := buffer.Write([]byte("cd")); err != nil || written != 2 {
			t.Fatalf("second Write = (%d, %v)", written, err)
		}
		if buffer.remaining != 1 || string(buffer.Bytes()) != "abcd" || buffer.String() != "abcd" {
			t.Fatalf("buffer = %q, remaining = %d", buffer.String(), buffer.remaining)
		}
	})
	t.Run("exact limit", func(t *testing.T) {
		buffer := &limitedBuffer{remaining: 3}
		written, err := buffer.Write([]byte("abc"))
		if err != nil || written != 3 || buffer.remaining != 0 || buffer.String() != "abc" {
			t.Fatalf("Write = (%d, %v), buffer = %q, remaining = %d", written, err, buffer.String(), buffer.remaining)
		}
	})
	t.Run("overflow preserves prefix", func(t *testing.T) {
		buffer := &limitedBuffer{remaining: 3}
		written, err := buffer.Write([]byte("abcd"))
		const want = "goatest: provider output exceeded limit"
		if err == nil || err.Error() != want || written != 0 || buffer.remaining != 0 || buffer.String() != "abc" {
			t.Fatalf("Write = (%d, %v), buffer = %q, remaining = %d", written, err, buffer.String(), buffer.remaining)
		}
	})
	t.Run("zero remaining", func(t *testing.T) {
		buffer := &limitedBuffer{}
		if written, err := buffer.Write(nil); err != nil || written != 0 {
			t.Fatalf("empty Write = (%d, %v)", written, err)
		}
		written, err := buffer.Write([]byte("x"))
		const want = "goatest: provider output exceeded limit"
		if err == nil || err.Error() != want || written != 0 || buffer.String() != "" {
			t.Fatalf("overflow Write = (%d, %v), buffer = %q", written, err, buffer.String())
		}
	})
}

func TestGenerateReturnsRequestMarshalFailure(t *testing.T) {
	sentinel := errors.New("marshal request failed")
	original := marshalGenerationRequest
	t.Cleanup(func() { marshalGenerationRequest = original })
	marshalGenerationRequest = func(any) ([]byte, error) { return nil, sentinel }

	client := Client{Command: []string{"unused"}, Timeout: time.Second}
	_, err := client.Generate(context.Background(), Request{Version: 1, Finding: report.Finding{ID: "finding-a"}})
	if !errors.Is(err, sentinel) {
		t.Fatalf("Generate error = %v", err)
	}
}

func TestGenerateReturnsProcessTreeCloseFailure(t *testing.T) {
	sentinel := errors.New("close process tree failed")
	original := startGenerationProcess
	t.Cleanup(func() { startGenerationProcess = original })
	startGenerationProcess = func(command *exec.Cmd) (generationProcessTree, error) {
		tree, err := original(command)
		if err != nil {
			return nil, err
		}
		return closeFailureTree{generationProcessTree: tree, err: sentinel}, nil
	}
	t.Setenv("GOATEST_GENERATION_HELPER", "1")
	client := Client{Command: []string{os.Args[0], "-test.run=^TestGenerationProviderHelper$"}, Timeout: 5 * time.Second}
	_, err := client.Generate(context.Background(), Request{Version: 1, Finding: report.Finding{ID: "finding-a"}})
	if !errors.Is(err, sentinel) {
		t.Fatalf("Generate error = %v", err)
	}
}

type closeFailureTree struct {
	generationProcessTree
	err error
}

func (tree closeFailureTree) Close() error { return tree.err }
