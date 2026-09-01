// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package report owns goatest's deterministic assurance result model and
// projections.
package report

import (
	"bytes"
	"cmp"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html/template"
	"slices"
	"strings"
	"unicode"
)

// SchemaV1 is the first public report contract. Pre-release report shapes are
// intentionally replaced in place instead of consuming a public version.
const SchemaV1 = "assurance-report-v1"

// Verdict is the primary result, deliberately not a mutation percentage.
type Verdict string

const (
	VerdictAssured       Verdict = "ASSURED"
	VerdictChangeAssured Verdict = "CHANGE_ASSURED"
	VerdictScopeAssured  Verdict = "SCOPE_ASSURED"
	VerdictDefect        Verdict = "DEFECT"
	VerdictInsufficient  Verdict = "INSUFFICIENT"
	VerdictError         Verdict = "ERROR"
	VerdictReproduced    Verdict = "REPRODUCED"
	VerdictResolved      Verdict = "RESOLVED"
	VerdictCompleted     Verdict = "COMPLETED"
)

// RunKind records what the operator requested. Scope.Resolved separately
// records what was actually verified (for example, a changeset run may safely
// broaden to the full project when impact information is unavailable).
type RunKind string

const (
	RunFull      RunKind = "full"
	RunChangeset RunKind = "changeset"
	RunPackage   RunKind = "package"
	RunReplay    RunKind = "replay"
	RunOperation RunKind = "operation"
)

type ScopeSpec struct {
	Kind     string   `json:"kind"`
	Project  string   `json:"project"`
	Modules  []string `json:"modules"`
	Packages []string `json:"packages"`
	Files    []string `json:"files"`
	Ref      string   `json:"ref,omitempty"`
}

type Scope struct {
	Requested ScopeSpec `json:"requested"`
	Resolved  ScopeSpec `json:"resolved"`
}

type Git struct {
	Available    bool     `json:"available"`
	Commit       string   `json:"commit"`
	Dirty        bool     `json:"dirty"`
	MergeBase    string   `json:"merge_base"`
	ChangedFiles []string `json:"changed_files"`
}

type Repository struct {
	Module   string   `json:"module"`
	Packages []string `json:"packages"`
	Git      Git      `json:"git"`
}

type Configuration struct {
	Digest string `json:"digest"`
}

type Toolchain struct {
	Go        string `json:"go"`
	Goatest   string `json:"goatest"`
	GoMutants string `json:"go_mutants"`
	OS        string `json:"os"`
	Arch      string `json:"arch"`
}

type Timing struct {
	StartedAt  string `json:"started_at"`
	FinishedAt string `json:"finished_at"`
	DurationMS int64  `json:"duration_ms"`
}

type Cache struct {
	Derived     bool   `json:"derived"`
	SourceRunID string `json:"source_run_id,omitempty"`
}

// CountAccounting describes discovery and execution for targets and race
// package checks. Discovered = Selected + Excluded and Selected = Executed +
// Skipped are the complete-accounting invariants when the counts are known.
type CountAccounting struct {
	Discovered int `json:"discovered"`
	Selected   int `json:"selected"`
	Executed   int `json:"executed"`
	Skipped    int `json:"skipped"`
	Excluded   int `json:"excluded"`
}

// MutantAccounting is deliberately redundant so a reader can audit both the
// disposition of every discovered mutant and the outcome of every execution.
// Discovered must equal Executed + CompileRejected + Accepted + OutOfScope +
// Unknown;
// Executed must equal Killed + Survived + Inconclusive.
type MutantAccounting struct {
	Discovered      int `json:"discovered"`
	Selected        int `json:"selected"`
	Executed        int `json:"executed"`
	Killed          int `json:"killed"`
	Survived        int `json:"survived"`
	Inconclusive    int `json:"inconclusive"`
	CompileRejected int `json:"compile_rejected"`
	Accepted        int `json:"accepted"`
	OutOfScope      int `json:"out_of_scope"`
	Unknown         int `json:"unknown"`
}

type MutantStatus string

const (
	MutantKilled          MutantStatus = "killed"
	MutantSurvived        MutantStatus = "survived"
	MutantInconclusive    MutantStatus = "inconclusive"
	MutantCompileRejected MutantStatus = "compile-rejected"
	MutantAccepted        MutantStatus = "accepted"
	MutantOutOfScope      MutantStatus = "out-of-scope"
	MutantUnknown         MutantStatus = "unknown"
)

// MutantDisposition is the one-and-only terminal classification for a
// discovered mutant. The redundant Accounting aggregate is validated against
// this inventory before a report can be persisted or reused from cache.
type MutantDisposition struct {
	ID      string       `json:"id"`
	Status  MutantStatus `json:"status"`
	Path    string       `json:"path"`
	Line    int          `json:"line"`
	Package string       `json:"package"`
	Rule    string       `json:"rule"`
	Detail  string       `json:"detail,omitempty"`
}

type Accounting struct {
	Targets CountAccounting  `json:"targets"`
	Mutants MutantAccounting `json:"mutants"`
	Race    CountAccounting  `json:"race"`
}

// TargetDisposition is the durable inventory entry for one selected baseline
// target. DurationMS is the measured baseline runtime and is zero when the
// target was not executed.
type TargetDisposition struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Kind       string `json:"kind"`
	Package    string `json:"package"`
	Path       string `json:"path"`
	Line       int    `json:"line"`
	Status     string `json:"status"`
	DurationMS int64  `json:"duration_ms"`
	Detail     string `json:"detail,omitempty"`
}

// Resume records how much exact-input checkpoint work contributed to a
// completed report. Attempts includes the current attempt.
type Resume struct {
	Attempts           int `json:"attempts"`
	ReusedTargets      int `json:"reused_targets"`
	ReusedRacePackages int `json:"reused_race_packages"`
	ReusedMutants      int `json:"reused_mutants"`
}

type Limitation struct {
	Code      string `json:"code"`
	Summary   string `json:"summary"`
	Estimated bool   `json:"estimated,omitempty"`
}

// Acceptance records the human authorization behind an accepted finding.
// Expires is an RFC3339 timestamp evaluated before the acceptance is used.
type Acceptance struct {
	ID      string `json:"id"`
	Reason  string `json:"reason"`
	Expires string `json:"expires"`
	Owner   string `json:"owner,omitempty"`
	Ticket  string `json:"ticket,omitempty"`
}

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
	ID         string `json:"id"`
	Finding    string `json:"finding"`
	Path       string `json:"path"`
	Status     string `json:"status"`
	Diff       string `json:"diff,omitempty"`
	Validation string `json:"validation,omitempty"`
	Reason     string `json:"reason,omitempty"`
	Provenance string `json:"provenance,omitempty"`
}

type Report struct {
	Schema        string              `json:"schema"`
	RunID         string              `json:"run_id"`
	RunKind       RunKind             `json:"run_kind"`
	Verdict       Verdict             `json:"verdict"`
	Contract      string              `json:"contract"`
	Snapshot      string              `json:"snapshot"`
	Scope         Scope               `json:"scope"`
	Repository    Repository          `json:"repository"`
	Configuration Configuration       `json:"configuration"`
	Toolchain     Toolchain           `json:"toolchain"`
	Timing        Timing              `json:"timing"`
	Cache         Cache               `json:"cache"`
	Accounting    Accounting          `json:"accounting"`
	Targets       []TargetDisposition `json:"targets"`
	Mutants       []MutantDisposition `json:"mutants"`
	Resume        *Resume             `json:"resume,omitempty"`
	Acceptances   []Acceptance        `json:"acceptances"`
	Evidence      []Evidence          `json:"evidence"`
	Findings      []Finding           `json:"findings"`
	Repairs       []Repair            `json:"repairs"`
	Limitations   []Limitation        `json:"limitations"`
}

func canonical(input Report) Report {
	result := input
	if result.Schema == "" {
		result.Schema = SchemaV1
	}
	result.Scope.Requested = canonicalScope(result.Scope.Requested)
	result.Scope.Resolved = canonicalScope(result.Scope.Resolved)
	result.Repository.Packages = canonicalStrings(result.Repository.Packages)
	result.Repository.Git.ChangedFiles = canonicalStrings(result.Repository.Git.ChangedFiles)
	result.Targets = slices.Clone(input.Targets)
	result.Mutants = slices.Clone(input.Mutants)
	if input.Resume != nil {
		resume := *input.Resume
		result.Resume = &resume
	}
	result.Acceptances = slices.Clone(input.Acceptances)
	result.Evidence = slices.Clone(input.Evidence)
	result.Findings = slices.Clone(input.Findings)
	result.Repairs = slices.Clone(input.Repairs)
	result.Limitations = slices.Clone(input.Limitations)
	slices.SortFunc(result.Evidence, func(a, b Evidence) int {
		if compared := strings.Compare(a.Kind, b.Kind); compared != 0 {
			return compared
		}
		return strings.Compare(a.ID, b.ID)
	})
	slices.SortFunc(result.Acceptances, func(a, b Acceptance) int { return strings.Compare(a.ID, b.ID) })
	slices.SortFunc(result.Targets, func(a, b TargetDisposition) int {
		if compared := cmp.Compare(b.DurationMS, a.DurationMS); compared != 0 {
			return compared
		}
		return strings.Compare(a.ID, b.ID)
	})
	slices.SortFunc(result.Mutants, func(a, b MutantDisposition) int { return strings.Compare(a.ID, b.ID) })
	slices.SortFunc(result.Findings, func(a, b Finding) int { return strings.Compare(a.ID, b.ID) })
	slices.SortFunc(result.Repairs, func(a, b Repair) int { return strings.Compare(a.ID, b.ID) })
	slices.SortFunc(result.Limitations, func(a, b Limitation) int {
		if compared := strings.Compare(a.Code, b.Code); compared != 0 {
			return compared
		}
		return strings.Compare(a.Summary, b.Summary)
	})
	if result.Evidence == nil {
		result.Evidence = []Evidence{}
	}
	if result.Acceptances == nil {
		result.Acceptances = []Acceptance{}
	}
	if result.Targets == nil {
		result.Targets = []TargetDisposition{}
	}
	if result.Mutants == nil {
		result.Mutants = []MutantDisposition{}
	}
	if result.Findings == nil {
		result.Findings = []Finding{}
	}
	if result.Repairs == nil {
		result.Repairs = []Repair{}
	}
	if result.Limitations == nil {
		result.Limitations = []Limitation{}
	}
	return result
}

func canonicalScope(input ScopeSpec) ScopeSpec {
	result := input
	result.Modules = canonicalStrings(input.Modules)
	result.Packages = canonicalStrings(input.Packages)
	result.Files = canonicalStrings(input.Files)
	return result
}

func canonicalStrings(input []string) []string {
	result := slices.Clone(input)
	slices.Sort(result)
	result = slices.Compact(result)
	if result == nil {
		return []string{}
	}
	return result
}

// JSON returns canonical indented assurance-report-v1 bytes with one newline.
// Report contains only JSON-native fields, so encoding cannot fail.
func JSON(input Report) []byte {
	data, _ := json.MarshalIndent(canonical(input), "", "  ")
	return append(data, '\n')
}

// Lines is the deterministic renderer used by terminals, pipes, and CI.
func Lines(input Report) string {
	report := canonical(input)
	var output strings.Builder
	fmt.Fprintf(&output, "%s %s snapshot=%s", LineText(string(report.Verdict)), LineText(report.Contract), LineText(report.Snapshot))
	if report.RunID != "" {
		fmt.Fprintf(&output, " run=%s kind=%s", LineText(report.RunID), LineText(string(report.RunKind)))
	}
	output.WriteByte('\n')
	fmt.Fprintf(&output, "evidence %d  findings %d  repairs %d  acceptances %d  limitations %d\n",
		len(report.Evidence), len(report.Findings), len(report.Repairs), len(report.Acceptances), len(report.Limitations))
	for _, finding := range report.Findings {
		location := finding.Path
		location = LineText(location)
		if finding.Line > 0 {
			location += fmt.Sprintf(":%d", finding.Line)
		}
		if location != "" {
			location += ": "
		}
		fmt.Fprintf(&output, "FINDING %s %s %s%s\n", LineText(finding.ID), LineText(finding.Kind), location, LineText(finding.Summary))
		if finding.Mutant != "" {
			fmt.Fprintf(&output, "  MUTANT %s\n", LineText(finding.Mutant))
		}
		if finding.Replay != "" {
			fmt.Fprintf(&output, "  REPLAY %s\n", LineText(finding.Replay))
		}
	}
	if report.RunKind == RunOperation {
		for _, item := range report.Evidence {
			fmt.Fprintf(&output, "EVIDENCE %s %s %s %s\n", LineText(item.Kind), LineText(item.ID), LineText(item.Status), LineText(item.Detail))
		}
	}
	for _, repair := range report.Repairs {
		fmt.Fprintf(&output, "REPAIR %s %s %s finding=%s\n", LineText(repair.ID), LineText(repair.Status), LineText(repair.Path), LineText(repair.Finding))
		if repair.Reason != "" {
			fmt.Fprintf(&output, "  REASON %s\n", LineText(repair.Reason))
		}
	}
	for _, acceptance := range report.Acceptances {
		fmt.Fprintf(&output, "ACCEPTANCE %s expires=%s reason=%s", LineText(acceptance.ID), LineText(acceptance.Expires), LineText(acceptance.Reason))
		if acceptance.Owner != "" {
			fmt.Fprintf(&output, " owner=%s", LineText(acceptance.Owner))
		}
		if acceptance.Ticket != "" {
			fmt.Fprintf(&output, " ticket=%s", LineText(acceptance.Ticket))
		}
		output.WriteByte('\n')
	}
	for _, limitation := range report.Limitations {
		fmt.Fprintf(&output, "LIMITATION %s %s\n", LineText(limitation.Code), LineText(limitation.Summary))
	}
	return output.String()
}

// LineText escapes terminal control characters without changing printable
// Unicode, keeping every diagnostic or report field on one physical line.
func LineText(input string) string {
	var output strings.Builder
	for _, character := range input {
		switch character {
		case '\n':
			output.WriteString(`\n`)
		case '\r':
			output.WriteString(`\r`)
		case '\t':
			output.WriteString(`\t`)
		default:
			if unicode.IsControl(character) {
				_, _ = fmt.Fprintf(&output, `\u%04x`, character)
			} else {
				output.WriteRune(character)
			}
		}
	}
	return output.String()
}

var page = template.Must(template.New("report").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width">
<title>goatest {{.Verdict}}</title><style>
:root{color-scheme:light dark;font-family:system-ui,sans-serif;line-height:1.5}body{max-width:80rem;margin:2rem auto;padding:0 1rem;color:#17212b;background:#fff}h1{margin-bottom:.25rem;letter-spacing:.04em}h2{margin-top:2rem}.meta{margin-top:0;color:#52606d}.badge{display:inline-block;padding:.2rem .55rem;border:1px solid currentColor;border-radius:999px;font-weight:700}.audit,.warning{padding:1rem;border:1px solid #bcccdc;border-radius:.5rem}.warning{border-color:#d97706;background:#fffbeb}.controls{display:flex;gap:.75rem;flex-wrap:wrap;margin:2rem 0 1rem}.controls label{display:grid;gap:.25rem}.controls input,.controls select{min-width:16rem;padding:.45rem;border:1px solid #9fb3c8;border-radius:.25rem;background:inherit;color:inherit}table{border-collapse:collapse;width:100%;overflow-wrap:anywhere}th,td{padding:.5rem;border-bottom:1px solid #d9e2ec;text-align:left;vertical-align:top}th{font-weight:650}code,pre{font-family:ui-monospace,monospace}pre{white-space:pre-wrap;max-height:32rem;overflow:auto}.scope-grid{display:grid;grid-template-columns:repeat(auto-fit,minmax(18rem,1fr));gap:1rem}.scope-card{border:1px solid #d9e2ec;border-radius:.5rem;padding:.75rem}.scope-card h3{margin-top:0}.scope-card ul{margin:.25rem 0;padding-left:1.25rem}.empty{color:#7b8794}[hidden]{display:none!important}@media(prefers-color-scheme:dark){body{color:#e5e7eb;background:#111827}.meta,.empty{color:#9ca3af}.audit,.scope-card,th,td{border-color:#374151}.warning{background:#422006;border-color:#f59e0b}.controls input,.controls select{border-color:#4b5563}}
</style></head><body>
<header><h1><span class="badge">{{.Verdict}}</span></h1><p class="meta">Contract <code>{{.Contract}}</code> · snapshot <code>{{.Snapshot}}</code>{{if .RunID}} · run <code>{{.RunID}}</code> (<code>{{.RunKind}}</code>){{end}}</p></header>
{{if .Limitations}}<section class="warning" data-filterable data-section="limitations"><h2>Limitations</h2><ul>{{range .Limitations}}<li data-row><code>{{.Code}}</code>: {{.Summary}}{{if .Estimated}} (estimated){{end}}</li>{{end}}</ul><p class="empty" data-empty hidden>No matching limitations.</p></section>{{end}}
{{if .RunID}}<section><h2>Audit identity</h2><div class="audit"><table><tbody>
<tr><th>Repository module</th><td><code>{{.Repository.Module}}</code></td></tr><tr><th>Packages</th><td>{{range .Repository.Packages}}<code>{{.}}</code><br>{{end}}</td></tr>
<tr><th>Git</th><td>available={{.Repository.Git.Available}} · commit <code>{{.Repository.Git.Commit}}</code> · dirty={{.Repository.Git.Dirty}} · merge-base <code>{{.Repository.Git.MergeBase}}</code>{{range .Repository.Git.ChangedFiles}}<br><code>{{.}}</code>{{end}}</td></tr>
<tr><th>Configuration</th><td><code>{{.Configuration.Digest}}</code></td></tr><tr><th>Toolchain</th><td>{{.Toolchain.Go}} · goatest {{.Toolchain.Goatest}} · go-mutants {{.Toolchain.GoMutants}} · {{.Toolchain.OS}}/{{.Toolchain.Arch}}</td></tr>
<tr><th>Timing</th><td>{{.Timing.StartedAt}} → {{.Timing.FinishedAt}} · {{.Timing.DurationMS}} ms</td></tr><tr><th>Cache</th><td>derived={{.Cache.Derived}}{{if .Cache.SourceRunID}} · source <code>{{.Cache.SourceRunID}}</code>{{end}}</td></tr>
{{if .Resume}}<tr><th>Resume</th><td>attempt {{.Resume.Attempts}} · reused {{.Resume.ReusedTargets}} targets, {{.Resume.ReusedRacePackages}} race packages, {{.Resume.ReusedMutants}} mutants</td></tr>{{end}}
</tbody></table></div></section>
<section><h2>Scope</h2><div class="scope-grid"><article class="scope-card"><h3>Requested</h3><p><code>{{.Scope.Requested.Kind}}</code> · project <code>{{.Scope.Requested.Project}}</code>{{if .Scope.Requested.Ref}} · ref <code>{{.Scope.Requested.Ref}}</code>{{end}}</p><strong>Modules</strong><ul>{{range .Scope.Requested.Modules}}<li><code>{{.}}</code></li>{{else}}<li class="empty">none</li>{{end}}</ul><strong>Packages</strong><ul>{{range .Scope.Requested.Packages}}<li><code>{{.}}</code></li>{{else}}<li class="empty">none</li>{{end}}</ul><strong>Files</strong><ul>{{range .Scope.Requested.Files}}<li><code>{{.}}</code></li>{{else}}<li class="empty">none</li>{{end}}</ul></article>
<article class="scope-card"><h3>Resolved</h3><p><code>{{.Scope.Resolved.Kind}}</code> · project <code>{{.Scope.Resolved.Project}}</code>{{if .Scope.Resolved.Ref}} · ref <code>{{.Scope.Resolved.Ref}}</code>{{end}}</p><strong>Modules</strong><ul>{{range .Scope.Resolved.Modules}}<li><code>{{.}}</code></li>{{else}}<li class="empty">none</li>{{end}}</ul><strong>Packages</strong><ul>{{range .Scope.Resolved.Packages}}<li><code>{{.}}</code></li>{{else}}<li class="empty">none</li>{{end}}</ul><strong>Files</strong><ul>{{range .Scope.Resolved.Files}}<li><code>{{.}}</code></li>{{else}}<li class="empty">none</li>{{end}}</ul></article></div></section>
<section><h2>Accounting</h2><table><thead><tr><th>Subject</th><th>Discovered</th><th>Selected</th><th>Executed</th><th>Skipped / rejected</th><th>Excluded</th></tr></thead><tbody>
<tr><td>Targets</td><td>{{.Accounting.Targets.Discovered}}</td><td>{{.Accounting.Targets.Selected}}</td><td>{{.Accounting.Targets.Executed}}</td><td>{{.Accounting.Targets.Skipped}}</td><td>{{.Accounting.Targets.Excluded}}</td></tr>
<tr><td>Mutants</td><td>{{.Accounting.Mutants.Discovered}}</td><td>{{.Accounting.Mutants.Selected}}</td><td>{{.Accounting.Mutants.Executed}} (killed {{.Accounting.Mutants.Killed}}, survived {{.Accounting.Mutants.Survived}}, inconclusive {{.Accounting.Mutants.Inconclusive}})</td><td>compile-rejected {{.Accounting.Mutants.CompileRejected}}, accepted {{.Accounting.Mutants.Accepted}}, unknown {{.Accounting.Mutants.Unknown}}</td><td>{{.Accounting.Mutants.OutOfScope}}</td></tr>
<tr><td>Race packages</td><td>{{.Accounting.Race.Discovered}}</td><td>{{.Accounting.Race.Selected}}</td><td>{{.Accounting.Race.Executed}}</td><td>{{.Accounting.Race.Skipped}}</td><td>{{.Accounting.Race.Excluded}}</td></tr></tbody></table></section>{{end}}
<div class="controls"><label>Search report<input id="report-search" type="search" placeholder="ID, path, status, summary…"></label><label>Section<select id="report-section"><option value="all">All</option><option value="targets">Targets</option><option value="mutants">Mutants</option><option value="findings">Findings</option><option value="evidence">Evidence</option><option value="repairs">Repairs</option><option value="acceptances">Acceptances</option><option value="limitations">Limitations</option></select></label></div>
<section data-filterable data-section="targets"><h2>Targets (slowest first)</h2><table><thead><tr><th>ID</th><th>Target</th><th>Status</th><th>Location</th><th>Duration</th></tr></thead><tbody>{{range .Targets}}<tr data-row><td><code>{{.ID}}</code></td><td>{{.Kind}} <code>{{.Package}}/{{.Name}}</code></td><td>{{.Status}}{{if .Detail}}<br>{{.Detail}}{{end}}</td><td>{{.Path}}{{if .Line}}:{{.Line}}{{end}}</td><td>{{.DurationMS}} ms</td></tr>{{end}}</tbody></table><p class="empty" data-empty{{if .Targets}} hidden{{end}}>No matching targets.</p></section>
<section data-filterable data-section="mutants"><h2>Mutants</h2><table><thead><tr><th>ID</th><th>Status</th><th>Location</th><th>Package / rule</th><th>Detail</th></tr></thead><tbody>{{range .Mutants}}<tr data-row><td><code>{{.ID}}</code></td><td>{{.Status}}</td><td>{{.Path}}{{if .Line}}:{{.Line}}{{end}}</td><td><code>{{.Package}}</code><br>{{.Rule}}</td><td>{{.Detail}}</td></tr>{{end}}</tbody></table><p class="empty" data-empty{{if .Mutants}} hidden{{end}}>No matching mutants.</p></section>
<section data-filterable data-section="findings"><h2>Findings</h2><table><thead><tr><th>ID</th><th>Kind</th><th>Location</th><th>Summary and replay</th></tr></thead><tbody>{{range .Findings}}<tr data-row><td><code>{{.ID}}</code></td><td>{{.Kind}}</td><td>{{.Path}}{{if .Line}}:{{.Line}}{{end}}</td><td>{{.Summary}}{{if .Mutant}}<br><code>{{.Mutant}}</code>{{end}}{{if .Replay}}<br><code>{{.Replay}}</code>{{end}}</td></tr>{{end}}</tbody></table><p class="empty" data-empty{{if .Findings}} hidden{{end}}>No matching findings.</p></section>
<section data-filterable data-section="evidence"><h2>Evidence</h2><table><thead><tr><th>Kind</th><th>ID</th><th>Status</th><th>Detail</th></tr></thead><tbody>{{range .Evidence}}<tr data-row><td>{{.Kind}}</td><td><code>{{.ID}}</code></td><td>{{.Status}}</td><td>{{.Detail}}</td></tr>{{end}}</tbody></table><p class="empty" data-empty{{if .Evidence}} hidden{{end}}>No matching evidence.</p></section>
<section data-filterable data-section="repairs"><h2>Repairs</h2><table><thead><tr><th>ID</th><th>Finding</th><th>Path and diff</th><th>Status</th></tr></thead><tbody>{{range .Repairs}}<tr data-row><td><code>{{.ID}}</code></td><td><code>{{.Finding}}</code></td><td>{{.Path}}{{if .Diff}}<details><summary>diff</summary><pre>{{.Diff}}</pre></details>{{end}}</td><td>{{.Status}}{{if .Validation}}<br>{{.Validation}}{{end}}{{if .Reason}}<br>{{.Reason}}{{end}}{{if .Provenance}}<br><code>{{.Provenance}}</code>{{end}}</td></tr>{{end}}</tbody></table><p class="empty" data-empty{{if .Repairs}} hidden{{end}}>No matching repairs.</p></section>
<section data-filterable data-section="acceptances"><h2>Acceptances</h2><table><thead><tr><th>Finding</th><th>Reason</th><th>Expires</th><th>Owner / ticket</th></tr></thead><tbody>{{range .Acceptances}}<tr data-row><td><code>{{.ID}}</code></td><td>{{.Reason}}</td><td>{{.Expires}}</td><td>{{.Owner}}{{if .Ticket}} · <code>{{.Ticket}}</code>{{end}}</td></tr>{{end}}</tbody></table><p class="empty" data-empty{{if .Acceptances}} hidden{{end}}>No matching acceptances.</p></section>
<script>(()=>{const q=document.getElementById('report-search'),s=document.getElementById('report-section'),sections=[...document.querySelectorAll('[data-filterable]')];function apply(){const term=q.value.toLocaleLowerCase(),chosen=s.value;for(const section of sections){const sectionMatch=chosen==='all'||section.dataset.section===chosen;let shown=0;for(const row of section.querySelectorAll('[data-row]')){const match=sectionMatch&&row.textContent.toLocaleLowerCase().includes(term);row.hidden=!match;if(match)shown++}section.hidden=!sectionMatch;const empty=section.querySelector('[data-empty]');if(empty)empty.hidden=shown!==0}}q.addEventListener('input',apply);s.addEventListener('change',apply)})();</script>
</body></html>`))

// HTML renders one self-contained offline document. The template is parsed at
// initialization and bytes.Buffer writes cannot fail.
func HTML(input Report) []byte {
	var output bytes.Buffer
	_ = page.Execute(&output, canonical(input))
	return output.Bytes()
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
