// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package evidence

import "encoding/json"

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
			"targetKey": mutationObject([]string{"package", "name", "kind", "key", "whole_tree"}, map[string]any{
				"package": nonEmpty, "name": nonEmpty, "kind": nonEmpty, "key": digest,
				"whole_tree": map[string]any{"type": "boolean"},
			}),
			"suiteKey": mutationObject([]string{"package", "key", "whole_tree"}, map[string]any{
				"package": nonEmpty, "key": digest, "whole_tree": map[string]any{"type": "boolean"},
			}),
			"findingSeed": mutationObject([]string{"kind", "summary"}, map[string]any{
				"kind": nonEmpty, "summary": nonEmpty,
			}),
			"record": mutationRecordSchema(nonEmpty, digest),
		},
	}
	data, _ := json.MarshalIndent(document, "", "  ")
	return append(data, '\n')
}

func mutationRecordSchema(nonEmpty, digest map[string]any) map[string]any {
	record := mutationObject([]string{"mutant_id", "path", "package", "outcome", "provenance"}, map[string]any{
		"mutant_id": digest, "path": nonEmpty, "package": nonEmpty,
		"outcome": map[string]any{"enum": []string{
			MutationOutcomeKilled, MutationOutcomeSurvived,
			MutationOutcomeUnreached,
		}},
		"provenance": map[string]any{"type": "string", "pattern": "^snapshot=[0-9a-f]{64}$"},
		"killed_by": map[string]any{
			"type": "array", "minItems": 1, "items": map[string]any{"$ref": "#/$defs/targetKey"}, "uniqueItems": true,
		},
		"exhausted": map[string]any{
			"type": "array", "minItems": 1, "items": map[string]any{"$ref": "#/$defs/targetKey"}, "uniqueItems": true,
		},
		"suite": map[string]any{"$ref": "#/$defs/suiteKey"}, "finding": map[string]any{"$ref": "#/$defs/findingSeed"},
	})
	record["oneOf"] = []any{
		mutationOutcomeShape(MutationOutcomeKilled, []string{"killed_by"}, []string{"exhausted", "suite", "finding"}),
		mutationOutcomeShape(MutationOutcomeSurvived, []string{"exhausted", "finding"}, []string{"killed_by", "suite"}),
		mutationOutcomeShape(MutationOutcomeUnreached, []string{"suite", "finding"}, []string{"killed_by", "exhausted"}),
	}
	return record
}

func mutationOutcomeShape(outcome string, required, forbidden []string) map[string]any {
	prohibited := make([]any, len(forbidden))
	for index, name := range forbidden {
		prohibited[index] = map[string]any{"required": []string{name}}
	}
	return map[string]any{
		"properties": map[string]any{"outcome": map[string]any{"const": outcome}},
		"required":   required,
		"not":        map[string]any{"anyOf": prohibited},
	}
}

func mutationObject(required []string, properties map[string]any) map[string]any {
	return map[string]any{"type": "object", "additionalProperties": false, "required": required, "properties": properties}
}
