// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package app

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

const appFixtureProcessID = 4242

func appTestDigest(character string) string {
	return strings.Repeat(character, hex.EncodedLen(sha256.Size))
}
