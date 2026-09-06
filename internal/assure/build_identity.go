// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package assure

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"sync"
)

var goatestBuildIdentity = sync.OnceValues(resolveGoatestBuildIdentity)

func resolveGoatestBuildIdentity() (string, error) {
	path, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("goatest: locate running executable: %w", err)
	}
	digest, err := digestGoatestExecutable(path)
	if err != nil {
		return "", fmt.Errorf("goatest: identify running executable: %w", err)
	}
	return digest, nil
}

func digestGoatestExecutable(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = file.Close() }()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
