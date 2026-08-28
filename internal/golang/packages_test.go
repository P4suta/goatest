// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package golang_test

import (
	"strings"
	"testing"

	gotest "github.com/P4suta/goatest/internal/golang"
)

func TestDecodePackageStreamComputesRelativeDirectories(t *testing.T) {
	stream := strings.NewReader(`{"ImportPath":"example.com/sample","Dir":"C:/snap","Module":{"Path":"example.com/sample","Dir":"C:/snap"},"Deps":["fmt"]}
{"ImportPath":"example.com/sample/sub","Dir":"C:/snap/sub","Module":{"Path":"example.com/sample","Dir":"C:/snap"},"Deps":["example.com/sample","fmt"]}
`)
	model, err := gotest.DecodePackages(stream)
	if err != nil {
		t.Fatal(err)
	}
	if model.ModulePath != "example.com/sample" || len(model.Packages) != 2 || model.Packages[0].RelativeDir != "." || model.Packages[1].RelativeDir != "sub" {
		t.Errorf("model = %+v", model)
	}
}
