// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/P4suta/goatest/internal/cli"
	"github.com/P4suta/goatest/internal/report"
)

const signalHelperEnvironment = "GOATEST_SIGNAL_HELPER"

type processSignalCase struct {
	name   string
	signal os.Signal
	want   int
}

type signalBlockingService struct{ ready string }

func (service signalBlockingService) Execute(ctx context.Context, _ cli.Command, _ cli.Request, _ string) (report.Report, error) {
	if err := os.WriteFile(service.ready, []byte("ready"), 0o600); err != nil {
		return report.Report{}, err
	}
	<-ctx.Done()
	return report.Report{}, ctx.Err()
}

func TestSignalProcessHelper(t *testing.T) {
	if os.Getenv(signalHelperEnvironment) != "1" {
		return
	}
	os.Exit(runWithService(nil, signalBlockingService{ready: os.Getenv("GOATEST_SIGNAL_READY")}))
}

func TestProcessSignalsProduceDocumentedExitCodes(t *testing.T) {
	for _, testCase := range processSignalCases() {
		t.Run(testCase.name, func(t *testing.T) {
			ready := filepath.Join(t.TempDir(), "ready")
			command := exec.Command(os.Args[0], "-test.run=^TestSignalProcessHelper$")
			command.Env = append(os.Environ(), signalHelperEnvironment+"=1", "GOATEST_SIGNAL_READY="+ready)
			configureSignalProcess(command)
			var stdout, stderr bytes.Buffer
			command.Stdout, command.Stderr = &stdout, &stderr
			if err := command.Start(); err != nil {
				t.Fatal(err)
			}
			if err := waitForReady(ready, 20*time.Second); err != nil {
				_ = command.Process.Kill()
				_ = command.Wait()
				t.Fatalf("helper did not become ready: %v\nstdout=%s\nstderr=%s", err, stdout.String(), stderr.String())
			}
			if err := sendProcessSignal(command.Process.Pid, testCase.signal); err != nil {
				_ = command.Process.Kill()
				_ = command.Wait()
				t.Fatal(err)
			}
			wait := make(chan error, 1)
			go func() { wait <- command.Wait() }()
			select {
			case err := <-wait:
				var exit *exec.ExitError
				if !errors.As(err, &exit) || exit.ExitCode() != testCase.want {
					t.Fatalf("exit = %v, want %d\nstdout=%s\nstderr=%s", err, testCase.want, stdout.String(), stderr.String())
				}
			case <-time.After(20 * time.Second):
				_ = command.Process.Kill()
				t.Fatal("helper did not stop after signal")
			}
		})
	}
}

func waitForReady(path string, timeout time.Duration) error {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err := os.Stat(path); err == nil {
			return nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		select {
		case <-deadline.C:
			return errors.New("timed out")
		case <-ticker.C:
		}
	}
}
