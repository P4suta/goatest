// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package provider invokes external generation providers through versioned
// one-shot JSON without giving goatest core a network or LLM dependency.
package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"slices"
	"strings"
	"time"

	"github.com/P4suta/goatest/internal/processtree"
	"github.com/P4suta/goatest/internal/report"
)

const (
	ProtocolVersion   = 1
	defaultTimeout    = 5 * time.Minute
	outputLimit       = 4 << 20
	maximumCandidates = 64
)

type Request struct {
	Version      int            `json:"version"`
	Finding      report.Finding `json:"finding"`
	AllowedPaths []string       `json:"allowed_paths"`
	Snapshot     string         `json:"snapshot"`
}

type Candidate struct {
	Kind           string `json:"kind"`
	Path           string `json:"path"`
	PreimageSHA256 string `json:"preimage_sha256,omitempty"`
	Content        []byte `json:"content"`
	Replay         string `json:"replay,omitempty"`
}

type Response struct {
	Version    int         `json:"version"`
	FindingID  string      `json:"finding_id"`
	Candidates []Candidate `json:"candidates"`
}

type Client struct {
	Command []string
	Timeout time.Duration
}

type generationProcessTree interface {
	Kill() error
	Close() error
}

var (
	marshalGenerationRequest = json.Marshal
	startGenerationProcess   = func(command *exec.Cmd) (generationProcessTree, error) {
		return processtree.Start(command)
	}
)

func (client Client) Generate(parent context.Context, request Request) (Response, error) {
	if len(client.Command) == 0 || strings.TrimSpace(client.Command[0]) == "" {
		return Response{}, errors.New("goatest: generation provider has no command")
	}
	if request.Version != ProtocolVersion || request.Finding.ID == "" {
		return Response{}, errors.New("goatest: generation request requires protocol v1 and a finding ID")
	}
	timeout := client.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	input, err := marshalGenerationRequest(request)
	if err != nil {
		return Response{}, err
	}
	cmd := exec.Command(client.Command[0], client.Command[1:]...)
	cmd.Stdin = bytes.NewReader(append(input, '\n'))
	stdout := &limitedBuffer{remaining: outputLimit}
	stderr := &limitedBuffer{remaining: outputLimit}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	tree, err := startGenerationProcess(cmd)
	if err != nil {
		return Response{}, fmt.Errorf("goatest: generation provider start: %w", err)
	}
	wait := make(chan error, 1)
	go func() { wait <- cmd.Wait() }()
	select {
	case runErr := <-wait:
		closeErr := tree.Close()
		if runErr != nil || closeErr != nil {
			return Response{}, fmt.Errorf("goatest: generation provider: %w: %s", errors.Join(runErr, closeErr), strings.TrimSpace(stderr.String()))
		}
	case <-ctx.Done():
		killErr := tree.Kill()
		runErr := <-wait
		closeErr := tree.Close()
		return Response{}, fmt.Errorf("goatest: generation provider timeout: %w", errors.Join(ctx.Err(), killErr, runErr, closeErr))
	}
	decoder := json.NewDecoder(bytes.NewReader(stdout.Bytes()))
	decoder.DisallowUnknownFields()
	var response Response
	if err := decoder.Decode(&response); err != nil {
		return Response{}, fmt.Errorf("goatest: generation response: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Response{}, errors.New("goatest: generation response has trailing data")
	}
	if response.Version != ProtocolVersion || response.FindingID != request.Finding.ID {
		return Response{}, fmt.Errorf("goatest: generation response identity mismatch: version=%d finding=%q", response.Version, response.FindingID)
	}
	if len(response.Candidates) > maximumCandidates {
		return Response{}, fmt.Errorf("goatest: generation response has %d candidates, maximum is %d", len(response.Candidates), maximumCandidates)
	}
	for i := range response.Candidates {
		response.Candidates[i].Content = slices.Clone(response.Candidates[i].Content)
	}
	return response, nil
}

type limitedBuffer struct {
	buffer    bytes.Buffer
	remaining int
}

func (buffer *limitedBuffer) Write(data []byte) (int, error) {
	if len(data) > buffer.remaining {
		_, _ = buffer.buffer.Write(data[:buffer.remaining])
		buffer.remaining = 0
		return 0, errors.New("goatest: provider output exceeded limit")
	}
	buffer.remaining -= len(data)
	return buffer.buffer.Write(data)
}

func (buffer *limitedBuffer) Bytes() []byte  { return buffer.buffer.Bytes() }
func (buffer *limitedBuffer) String() string { return buffer.buffer.String() }
