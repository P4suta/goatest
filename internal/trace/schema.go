// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package trace

import _ "embed"

//go:embed schema.json
var schemaDocument []byte

func JSONSchema() []byte {
	return append([]byte(nil), schemaDocument...)
}
