// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package app

import (
	"errors"
	"testing"

	"github.com/P4suta/goatest/internal/cli"
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
