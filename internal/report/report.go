// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package report owns goatest's deterministic assurance result model and
// projections.
package report

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html/template"
	"slices"
	"strings"
)

const SchemaV1 = "assurance-report-v1"

// Verdict is the primary result, deliberately not a mutation percentage.
type Verdict string

const (
	VerdictAssured      Verdict = "ASSURED"
	VerdictDefect       Verdict = "DEFECT"
	VerdictInsufficient Verdict = "INSUFFICIENT"
	VerdictError        Verdict = "ERROR"
)

type Evidence struct {
	Kind   string `json:"kind"`
	ID     string `json:"id"`
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
}

type Finding struct {
	ID       string `json:"id"`
	Kind     string `json:"kind"`
	Path     string `json:"path,omitempty"`
	Line     int    `json:"line,omitempty"`
	Summary  string `json:"summary"`
	Replay   string `json:"replay,omitempty"`
	Mutant   string `json:"mutant,omitempty"`
	MutantID string `json:"mutant_id,omitempty"`
}

type Repair struct {
	ID      string `json:"id"`
	Finding string `json:"finding"`
	Path    string `json:"path"`
	Status  string `json:"status"`
}

type Report struct {
	Schema        string     `json:"schema"`
	Verdict       Verdict    `json:"verdict"`
	Contract      string     `json:"contract,omitempty"`
	Snapshot      string     `json:"snapshot,omitempty"`
	Evidence      []Evidence `json:"evidence"`
	Findings      []Finding  `json:"findings"`
	Repairs       []Repair   `json:"repairs"`
	ResidualRisks []string   `json:"residual_risks"`
}

func canonical(input Report) Report {
	result := input
	if result.Schema == "" {
		result.Schema = SchemaV1
	}
	result.Evidence = slices.Clone(input.Evidence)
	result.Findings = slices.Clone(input.Findings)
	result.Repairs = slices.Clone(input.Repairs)
	result.ResidualRisks = slices.Clone(input.ResidualRisks)
	slices.SortFunc(result.Evidence, func(a, b Evidence) int {
		if compared := strings.Compare(a.Kind, b.Kind); compared != 0 {
			return compared
		}
		return strings.Compare(a.ID, b.ID)
	})
	slices.SortFunc(result.Findings, func(a, b Finding) int { return strings.Compare(a.ID, b.ID) })
	slices.SortFunc(result.Repairs, func(a, b Repair) int { return strings.Compare(a.ID, b.ID) })
	slices.Sort(result.ResidualRisks)
	if result.Evidence == nil {
		result.Evidence = []Evidence{}
	}
	if result.Findings == nil {
		result.Findings = []Finding{}
	}
	if result.Repairs == nil {
		result.Repairs = []Repair{}
	}
	if result.ResidualRisks == nil {
		result.ResidualRisks = []string{}
	}
	return result
}

// JSON returns canonical indented assurance-report-v1 bytes with one newline.
func JSON(input Report) ([]byte, error) {
	data, err := json.MarshalIndent(canonical(input), "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

// Lines is the deterministic renderer used by terminals, pipes, and CI.
func Lines(input Report) string {
	report := canonical(input)
	var output strings.Builder
	fmt.Fprintf(&output, "%s %s snapshot=%s\n", report.Verdict, report.Contract, report.Snapshot)
	fmt.Fprintf(&output, "evidence %d  findings %d  repairs %d  risks %d\n",
		len(report.Evidence), len(report.Findings), len(report.Repairs), len(report.ResidualRisks))
	for _, finding := range report.Findings {
		location := finding.Path
		if finding.Line > 0 {
			location += fmt.Sprintf(":%d", finding.Line)
		}
		if location != "" {
			location += ": "
		}
		fmt.Fprintf(&output, "FINDING %s %s %s%s\n", finding.ID, finding.Kind, location, finding.Summary)
		if finding.Mutant != "" {
			fmt.Fprintf(&output, "  MUTANT %s\n", finding.Mutant)
		}
		if finding.Replay != "" {
			fmt.Fprintf(&output, "  REPLAY %s\n", finding.Replay)
		}
	}
	for _, repair := range report.Repairs {
		fmt.Fprintf(&output, "REPAIR %s %s %s finding=%s\n", repair.ID, repair.Status, repair.Path, repair.Finding)
	}
	for _, risk := range report.ResidualRisks {
		fmt.Fprintf(&output, "RISK %s\n", risk)
	}
	return output.String()
}

var page = template.Must(template.New("report").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width">
<title>goatest {{.Verdict}}</title><style>
body{font:15px/1.5 system-ui,sans-serif;max-width:72rem;margin:2rem auto;padding:0 1rem;color:#17212b;background:#fff}
h1{letter-spacing:.04em}.meta{color:#52606d}table{border-collapse:collapse;width:100%}th,td{padding:.5rem;border-bottom:1px solid #d9e2ec;text-align:left}code{font-family:ui-monospace,monospace}
</style></head><body><h1>{{.Verdict}}</h1><p class="meta">Contract {{.Contract}} · snapshot <code>{{.Snapshot}}</code></p>
<h2>Findings</h2><table><thead><tr><th>ID</th><th>Kind</th><th>Location</th><th>Summary and replay</th></tr></thead><tbody>
{{range .Findings}}<tr><td><code>{{.ID}}</code></td><td>{{.Kind}}</td><td>{{.Path}}{{if .Line}}:{{.Line}}{{end}}</td><td>{{.Summary}}{{if .Mutant}}<br><code>{{.Mutant}}</code>{{end}}{{if .Replay}}<br><code>{{.Replay}}</code>{{end}}</td></tr>{{end}}
</tbody></table><h2>Evidence</h2><table><thead><tr><th>Kind</th><th>ID</th><th>Status</th><th>Detail</th></tr></thead><tbody>
{{range .Evidence}}<tr><td>{{.Kind}}</td><td><code>{{.ID}}</code></td><td>{{.Status}}</td><td>{{.Detail}}</td></tr>{{end}}
</tbody></table><h2>Repairs</h2><table><thead><tr><th>ID</th><th>Finding</th><th>Path</th><th>Status</th></tr></thead><tbody>
{{range .Repairs}}<tr><td><code>{{.ID}}</code></td><td><code>{{.Finding}}</code></td><td>{{.Path}}</td><td>{{.Status}}</td></tr>{{end}}
</tbody></table><h2>Residual risks</h2><ul>{{range .ResidualRisks}}<li>{{.}}</li>{{end}}</ul></body></html>`))

// HTML renders one self-contained offline document.
func HTML(input Report) ([]byte, error) {
	var output bytes.Buffer
	if err := page.Execute(&output, canonical(input)); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

// FindingID returns a stable 16-hex display identity over length-prefixed
// fields. The full source facts remain in the finding itself.
func FindingID(fields ...string) string {
	hash := sha256.New()
	_, _ = hash.Write([]byte("goatest-finding-v1\x00"))
	for _, field := range fields {
		var length [4]byte
		binary.BigEndian.PutUint32(length[:], uint32(len(field)))
		_, _ = hash.Write(length[:])
		_, _ = hash.Write([]byte(field))
	}
	return hex.EncodeToString(hash.Sum(nil))[:16]
}
