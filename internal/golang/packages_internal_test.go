// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package golang

import (
	"errors"
	"strings"
	"testing"
)

func TestDecodePackagesRejectsRelativePathFailure(t *testing.T) {
	original := relativePackagePath
	t.Cleanup(func() { relativePackagePath = original })
	relativePackagePath = func(string, string) (string, error) {
		return "", errors.New("relative path failed")
	}

	stream := strings.NewReader(`{"ImportPath":"example.com/sample","Dir":"module","Module":{"Path":"example.com/sample","Dir":"module"}}`)
	_, err := DecodePackages(stream)
	const want = "goatest: package example.com/sample is outside module directory"
	if err == nil || err.Error() != want {
		t.Fatalf("DecodePackages error = %v, want %q", err, want)
	}
}
