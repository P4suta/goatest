// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package trace

import _ "embed"

//go:embed schema.json
var schemaDocument []byte

// JSONSchema returns the self-contained goatest-trace-v1 JSON Schema, the
// contract a reader of a trace stream validates one line against.
func JSONSchema() []byte {
	return append([]byte(nil), schemaDocument...)
}
