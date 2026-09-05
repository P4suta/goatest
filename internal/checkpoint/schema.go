// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package checkpoint

import "encoding/json"

// JSONSchema returns the self-contained assurance-checkpoint-v1 JSON Schema.
func JSONSchema() []byte {
	stringType := map[string]any{"type": "string"}
	nonEmpty := map[string]any{"type": "string", "minLength": 1}
	nonNegative := map[string]any{"type": "integer", "minimum": 0}
	digest := map[string]any{"type": "string", "pattern": "^[0-9a-f]{64}$"}
	array := func(ref string) map[string]any {
		return map[string]any{"type": "array", "items": map[string]any{"$ref": ref}}
	}
	document := map[string]any{
		"$schema": "https://json-schema.org/draft/2020-12/schema",
		"$id":     SchemaV1,
		"title":   "goatest interrupted assurance checkpoint v1",
		"type":    "object", "additionalProperties": false,
		"required": []string{"schema", "input_digest", "attempts", "baseline"},
		"properties": map[string]any{
			"schema": map[string]any{"const": SchemaV1}, "input_digest": digest,
			"attempts": map[string]any{"type": "integer", "minimum": 1},
			"baseline": map[string]any{"$ref": "#/$defs/baseline"},
			"race":     map[string]any{"$ref": "#/$defs/race"},
			"mutation": map[string]any{"$ref": "#/$defs/mutation"},
		},
		"$defs": map[string]any{
			"evidence": checkpointObject([]string{"kind", "id", "status"}, map[string]any{
				"kind": stringType, "id": stringType, "status": stringType, "detail": stringType,
			}),
			"finding": checkpointObject([]string{"id", "kind", "summary"}, map[string]any{
				"id": stringType, "kind": stringType, "path": stringType, "line": nonNegative,
				"summary": stringType, "replay": stringType, "mutant": stringType, "mutant_id": stringType,
			}),
			"repair": checkpointObject([]string{"id", "finding", "path", "status"}, map[string]any{
				"id": stringType, "finding": stringType, "path": stringType, "status": stringType,
				"diff": stringType, "validation": stringType, "reason": stringType, "provenance": stringType,
			}),
			"targetInventory": checkpointObject([]string{"id", "name", "kind", "package", "path", "line", "status", "duration_ms"}, map[string]any{
				"id": nonEmpty, "name": nonEmpty, "kind": nonEmpty, "package": nonEmpty, "path": stringType,
				"line": nonNegative, "status": nonEmpty, "duration_ms": nonNegative, "detail": stringType,
			}),
			"target": checkpointObject([]string{"id", "name", "kind", "package", "relative_dir", "path", "line", "capability", "capabilities", "dependencies"}, map[string]any{
				"id": nonEmpty, "name": nonEmpty, "kind": nonEmpty, "package": nonEmpty, "relative_dir": stringType,
				"path": stringType, "line": nonNegative, "capability": stringType,
				"capabilities": map[string]any{"type": "array", "items": stringType},
				"dependencies": map[string]any{"type": "array", "items": stringType},
			}),
			"targetEvidence": checkpointObject([]string{"target", "covered_files", "environment", "duration_ns"}, map[string]any{
				"target":        map[string]any{"$ref": "#/$defs/target"},
				"covered_files": map[string]any{"type": "array", "items": stringType},
				"environment":   map[string]any{"type": "array", "items": stringType}, "duration_ns": nonNegative,
				"whole_tree": map[string]any{"type": "boolean"}, "repository_observed": map[string]any{"type": "boolean"},
			}),
			"baselineTarget": checkpointObject([]string{"id", "executed", "skipped", "evidence", "findings", "inventory"}, map[string]any{
				"id": nonEmpty, "executed": map[string]any{"type": "boolean"}, "skipped": map[string]any{"type": "boolean"},
				"evidence": array("#/$defs/evidence"), "findings": array("#/$defs/finding"),
				"inventory": map[string]any{"$ref": "#/$defs/targetInventory"},
				"target":    map[string]any{"$ref": "#/$defs/targetEvidence"},
			}),
			"baseline": checkpointObject([]string{"build_vet_complete", "complete", "evidence", "findings", "targets"}, map[string]any{
				"build_vet_complete": map[string]any{"type": "boolean"}, "complete": map[string]any{"type": "boolean"},
				"evidence": array("#/$defs/evidence"), "findings": array("#/$defs/finding"), "targets": array("#/$defs/baselineTarget"),
			}),
			"race": checkpointObject([]string{"complete", "packages", "evidence", "findings"}, map[string]any{
				"complete": map[string]any{"type": "boolean"}, "packages": map[string]any{"type": "array", "items": stringType},
				"evidence": array("#/$defs/evidence"), "findings": array("#/$defs/finding"),
			}),
			"mutationResult": checkpointObject([]string{"id", "evidence", "findings", "repairs", "applied"}, map[string]any{
				"id": nonEmpty, "evidence": array("#/$defs/evidence"), "findings": array("#/$defs/finding"),
				"repairs": array("#/$defs/repair"), "applied": map[string]any{"type": "boolean"},
				"provenance": nonEmpty,
			}),
			"mutation": checkpointObject([]string{"catalog_fingerprint", "complete", "results"}, map[string]any{
				"catalog_fingerprint": digest, "complete": map[string]any{"type": "boolean"}, "results": array("#/$defs/mutationResult"),
			}),
		},
	}
	data, _ := json.MarshalIndent(document, "", "  ")
	return append(data, '\n')
}

func checkpointObject(required []string, properties map[string]any) map[string]any {
	return map[string]any{"type": "object", "additionalProperties": false, "required": required, "properties": properties}
}
