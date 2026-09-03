// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package evidence

import "encoding/json"

// MutationJSONSchema returns the self-contained mutation-evidence-v1 JSON
// Schema. It is the structural contract a consumer outside this module can
// hold the store to: every object is closed, every key is a sha256 digest, and
// every outcome is one of the four a later run can reuse.
//
// The per-outcome shape rules stay in validate rather than in the schema. A
// schema that also encoded them would state the same rule twice, in two
// languages, and the copy that drifted would be the one nobody ran.
func MutationJSONSchema() []byte {
	nonEmpty := map[string]any{"type": "string", "minLength": 1}
	digest := map[string]any{"type": "string", "pattern": "^[0-9a-f]{64}$"}
	document := map[string]any{
		"$schema": "https://json-schema.org/draft/2020-12/schema",
		"$id":     MutationSchemaV1,
		"title":   "goatest mutation evidence v1",
		"type":    "object", "additionalProperties": false,
		"required": []string{"schema", "module_path", "records"},
		"properties": map[string]any{
			"schema": map[string]any{"const": MutationSchemaV1}, "module_path": nonEmpty,
			"records": map[string]any{"type": "array", "items": map[string]any{"$ref": "#/$defs/record"}},
		},
		"$defs": map[string]any{
			"targetKey": mutationObject([]string{"package", "name", "kind", "key"}, map[string]any{
				"package": nonEmpty, "name": nonEmpty, "kind": nonEmpty, "key": digest,
			}),
			"suiteKey": mutationObject([]string{"package", "key"}, map[string]any{
				"package": nonEmpty, "key": digest,
			}),
			"findingSeed": mutationObject([]string{"kind", "summary"}, map[string]any{
				"kind": nonEmpty, "summary": nonEmpty,
			}),
			"record": mutationObject([]string{"mutant_id", "path", "package", "outcome", "provenance"}, map[string]any{
				"mutant_id": digest, "path": nonEmpty, "package": nonEmpty,
				"outcome": map[string]any{"enum": []string{
					MutationOutcomeKilled, MutationOutcomeSurvived,
					MutationOutcomeUnreached, MutationOutcomeTimedOut,
				}},
				"provenance": map[string]any{"type": "string", "pattern": "^snapshot=[0-9a-f]{64}$"},
				"killed_by":  map[string]any{"$ref": "#/$defs/targetKey"},
				"exhausted": map[string]any{
					"type": "array", "items": map[string]any{"$ref": "#/$defs/targetKey"}, "uniqueItems": true,
				},
				"suite": map[string]any{"$ref": "#/$defs/suiteKey"}, "finding": map[string]any{"$ref": "#/$defs/findingSeed"},
			}),
		},
	}
	data, _ := json.MarshalIndent(document, "", "  ")
	return append(data, '\n')
}

func mutationObject(required []string, properties map[string]any) map[string]any {
	return map[string]any{"type": "object", "additionalProperties": false, "required": required, "properties": properties}
}
