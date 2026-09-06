// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package buildcache

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"io"
	"testing"
)

func TestCompleteNativeReaderReturnsEOF(t *testing.T) {
	t.Parallel()
	body := []byte("actual")
	expected := sha256.Sum256(body)
	reader := &verifiedNativeReader{
		source:   bytes.NewReader(body),
		digest:   sha256.New(),
		expected: expected[:],
	}
	buffer := make([]byte, len(body))
	read, err := reader.Read(buffer)
	if read != len(body) || err != nil || !bytes.Equal(buffer, body) {
		t.Fatalf("data read = (%d, %q, %v)", read, buffer, err)
	}
	read, err = reader.Read(buffer)
	if read != 0 || !errors.Is(err, io.EOF) || reader.invalid {
		t.Fatalf("terminal read = (%d, %v), invalid=%t", read, err, reader.invalid)
	}
}

func TestInvalidNativeReaderReturnsATerminalError(t *testing.T) {
	t.Parallel()
	body := []byte("actual")
	expected := sha256.Sum256([]byte("expected"))
	reader := &verifiedNativeReader{
		source:   bytes.NewReader(body),
		digest:   sha256.New(),
		expected: expected[:],
	}
	buffer := make([]byte, len(body))
	read, err := reader.Read(buffer)
	if read != len(body) || err != nil || !bytes.Equal(buffer, body) {
		t.Fatalf("data read = (%d, %q, %v)", read, buffer, err)
	}
	read, err = reader.Read(buffer)
	if read != 0 || err == nil || !reader.invalid {
		t.Fatalf("terminal read = (%d, %v), invalid=%t", read, err, reader.invalid)
	}
}
