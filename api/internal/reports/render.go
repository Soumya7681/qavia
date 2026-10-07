package reports

import (
	"bytes"
	"fmt"
	"html/template"
	"time"
)

// Rendering (BE-5.9.2).
//
// One template, and `html/template` rather than string concatenation, because
// everything in a report came from somewhere else: a test name a model wrote, a
// failure message a target returned, a root cause an agent produced. Any of those can
// contain a `<script>` tag, and a report is a file people open in a browser and email
// to each other.
//
// The CSS is inline and print-first. A report is read on paper or as a PDF as often
// as on a screen, and a document that needs a network fetch to be legible is a
// document that is illegible in an email client.

// Render turns gathered contents into a self-contained HTML document.
func Render(contents Contents) ([]byte, error) {
	var out bytes.Buffer
	if err := reportTemplate.Execute(&out, contents); err != nil {
		return nil, fmt.Errorf("render the report: %w", err)
	}
	return out.Bytes(), nil
}

var reportTemplate = template.Must(template.New("report").Funcs(template.FuncMap{
	"date":     func(at time.Time) string { return at.Format("2 January 2006, 15:04") },
	"day":      func(at time.Time) string { return at.Format("2 Jan 15:04") },
	"percent":  func(rate float64) string { return fmt.Sprintf("%.1f%%", rate*100) },
	"duration": func(d time.Duration) string { return d.Round(time.Millisecond).String() },
	"score": func(value *float64) string {
		if value == nil {
			// Stated rather than left blank, because a blank cell reads as zero and the
			// honest answer is that the platform cannot tell yet (BE-5.4.2).
			return "not measurable"
		}
		return fmt.Sprintf("%.3f", *value)
	},
	"coveragePercent": func(coverage Coverage) string {
		if coverage.Requirements == 0 {
			return "—"
		}
		return fmt.Sprintf("%.0f%%", float64(coverage.Covered)/float64(coverage.Requirements)*100)
	},
}).Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<title>{{.ProjectName}} — test report</title>
<style>
  :root { color-scheme: light; }
  body {
    font: 14px/1.55 -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, Helvetica, Arial, sans-serif;
    color: #16181d; margin: 0; padding: 40px; max-width: 940px;
  }
  h1 { font-size: 24px; margin: 0 0 4px; }
  h2 { font-size: 17px; margin: 36px 0 10px; padding-bottom: 6px; border-bottom: 1px solid #e3e5e9; }
  .meta { color: #5b6070; font-size: 13px; margin-bottom: 28px; }
  table { border-collapse: collapse; width: 100%; font-size: 13px; }
  th, td { text-align: left; padding: 7px 10px; border-bottom: 1px solid #eceef1; vertical-align: top; }
  th { font-weight: 600; color: #40454f; background: #f7f8fa; }
  .cards { display: flex; flex-wrap: wrap; gap: 12px; margin: 0 0 8px; }
  .card { border: 1px solid #e3e5e9; border-radius: 8px; padding: 12px 16px; min-width: 118px; }
  .card .n { font-size: 22px; font-weight: 600; }
  .card .l { font-size: 12px; color: #5b6070; text-transform: uppercase; letter-spacing: .04em; }
  .pill { display: inline-block; padding: 1px 8px; border-radius: 999px; font-size: 12px; font-weight: 600; }
  .passed, .fixed { background: #e6f6ec; color: #16653a; }
  .failed, .errored, .critical { background: #fdeaea; color: #8a1c1c; }
  .flaky, .high { background: #fff4e0; color: #8a5300; }
  .skipped, .open, .medium { background: #eef1f5; color: #414651; }
  .failure { border: 1px solid #e3e5e9; border-radius: 8px; padding: 14px 16px; margin-bottom: 14px; }
  .failure h3 { font-size: 14px; margin: 0 0 6px; }
  .failure dl { display: grid; grid-template-columns: 130px 1fr; gap: 4px 14px; margin: 10px 0 0; }
  .failure dt { color: #5b6070; font-size: 12px; }
  .failure dd { margin: 0; }
  pre { background: #f7f8fa; border-radius: 6px; padding: 10px; overflow-x: auto; font-size: 12px; margin: 6px 0 0; }
  ul.evidence { margin: 6px 0 0; padding-left: 18px; font-size: 13px; }
  .empty { color: #5b6070; font-style: italic; }
  footer { margin-top: 40px; color: #5b6070; font-size: 12px; border-top: 1px solid #e3e5e9; padding-top: 12px; }
  @media print {
    body { padding: 0; max-width: none; }
    h2 { break-after: avoid; }
    .failure, tr { break-inside: avoid; }
  }
</style>
</head>
<body>

<h1>{{.ProjectName}}</h1>
<div class="meta">
  Test report generated {{date .GeneratedAt}} · covering the last {{.WindowDays}} day(s)
</div>

<div class="cards">
  <div class="card"><div class="n">{{.Summary.Runs}}</div><div class="l">Runs</div></div>
  <div class="card"><div class="n">{{percent .Summary.PassRate}}</div><div class="l">Pass rate</div></div>
  <div class="card"><div class="n">{{.Summary.Failed}}</div><div class="l">Failed</div></div>
  <div class="card"><div class="n">{{.Summary.Flaky}}</div><div class="l">Flaky</div></div>
  <div class="card"><div class="n">{{.Summary.OpenDefect}}</div><div class="l">Open defects</div></div>
</div>
{{if .Summary.LastRunAt}}<div class="meta">Most recent run {{date .Summary.LastRunAt}}.</div>{{end}}

<h2>Requirement coverage</h2>
<table>
  <tr><th>Requirements</th><th>With a test case</th><th>Coverage</th><th>Approved cases</th><th>Generated files</th></tr>
  <tr>
    <td>{{.Coverage.Requirements}}</td>
    <td>{{.Coverage.Covered}}</td>
    <td>{{coveragePercent .Coverage}}</td>
    <td>{{.Coverage.ApprovedCases}} of {{.Coverage.TotalCases}}</td>
    <td>{{.Coverage.TestFiles}}</td>
  </tr>
</table>

<h2>Runs</h2>
{{if .Runs}}
<table>
  <tr><th>When</th><th>Status</th><th>Tests</th><th>Passed</th><th>Failed</th><th>Flaky</th><th>Duration</th><th>Target</th></tr>
  {{range .Runs}}
  <tr>
    <td>{{day .At}}</td>
    <td><span class="pill {{.Status}}">{{.Status}}</span></td>
    <td>{{.Total}}</td><td>{{.Passed}}</td><td>{{.Failed}}</td><td>{{.Flaky}}</td>
    <td>{{duration .Duration}}</td>
    <td>{{.Target}}</td>
  </tr>
  {{end}}
</table>
{{else}}<p class="empty">No runs in this window.</p>{{end}}

<h2>Failures</h2>
{{if .Failures}}
{{range .Failures}}
<div class="failure">
  <h3>{{.TestName}} <span class="pill {{.Status}}">{{.Status}}</span></h3>
  {{if .Analysed}}
    <div>{{.Reason}}</div>
    <dl>
      <dt>Root cause</dt><dd>{{.RootCause}}</dd>
      {{if .SuggestedFix}}<dt>Suggested fix</dt><dd>{{.SuggestedFix}} <em>(not applied)</em></dd>{{end}}
      <dt>Stability</dt><dd>{{score .Stability}}</dd>
    </dl>
    {{if .Evidence}}
    <ul class="evidence">{{range .Evidence}}<li>{{.}}</li>{{end}}</ul>
    {{end}}
  {{else}}
    <div class="empty">Not analysed. The failure message is below as reported by the framework.</div>
  {{end}}
  {{if .Message}}<pre>{{.Message}}</pre>{{end}}
</div>
{{end}}
{{else}}<p class="empty">No failures in this window.</p>{{end}}

<h2>Defects</h2>
{{if .Defects}}
<table>
  <tr><th>Title</th><th>Severity</th><th>Status</th><th>Occurrences</th><th>Filed</th></tr>
  {{range .Defects}}
  <tr>
    <td>{{.Title}}</td>
    <td><span class="pill {{.Severity}}">{{.Severity}}</span></td>
    <td><span class="pill {{.Status}}">{{.Status}}</span></td>
    <td>{{.Occurrences}}</td>
    <td>{{day .CreatedAt}}</td>
  </tr>
  {{end}}
</table>
{{else}}<p class="empty">No defects filed.</p>{{end}}

<footer>
  Generated by Qavia. Every figure here is computed from stored results: the pass rate
  counts a flaky test as neither a pass nor a failure, and a stability score reads
  "not measurable" when the platform has no repeat history rather than showing a
  number nothing supports.
</footer>

</body>
</html>
`))
