// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package evidence

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

func evidenceTestDigest(character string) string {
	return strings.Repeat(character, hex.EncodedLen(sha256.Size))
}
