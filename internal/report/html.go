package report

import (
	"html/template"
	"io"
)

// WriteHTML renders the report as one self-contained HTML file: no scripts,
// no external stylesheets, fonts or images.
func (r *Report) WriteHTML(w io.Writer) error {
	return page.Execute(w, r)
}

var page = template.Must(template.New("report").Funcs(template.FuncMap{
	"short":    ShortHash,
	"severity": severity,
	"limit": func(items []string) []string {
		if len(items) > maxListed {
			return items[:maxListed]
		}
		return items
	},
	"more": func(items []string) int {
		if len(items) > maxListed {
			return len(items) - maxListed
		}
		return 0
	},
}).Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>TrailSource report</title>
<style>
:root{--bg:#fbfbfa;--fg:#1d1f21;--muted:#5d6166;--line:#d9dadc;--card:#fff;--ok:#1f6f3f;--bad:#a8261b;--note:#7a5a00;--code:#f1f1ef}
@media (prefers-color-scheme:dark){:root{--bg:#16181b;--fg:#e6e6e3;--muted:#a0a4aa;--line:#33363b;--card:#1d2024;--ok:#5cc489;--bad:#ff7b6e;--note:#e3b341;--code:#25282d}}
*{box-sizing:border-box}
body{margin:0;background:var(--bg);color:var(--fg);font:15px/1.5 system-ui,-apple-system,"Segoe UI",Roboto,sans-serif}
main{max-width:960px;margin:0 auto;padding:16px}
h1{font-size:1.4rem;margin:.2rem 0 .6rem}
h2{font-size:1.15rem;margin:1.6rem 0 .4rem;word-break:break-all}
h3{font-size:1rem;margin:1.2rem 0 .3rem;color:var(--muted)}
section{background:var(--card);border:1px solid var(--line);border-radius:8px;padding:12px 14px;margin:12px 0}
code,.mono{font-family:ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;font-size:.86em}
code{background:var(--code);padding:1px 4px;border-radius:4px;word-break:break-all}
.ok{color:var(--ok);font-weight:600}.fail{color:var(--bad);font-weight:600}.notice{color:var(--note)}.error{color:var(--bad)}
.muted{color:var(--muted)}
dl{display:grid;grid-template-columns:max-content 1fr;gap:2px 12px;margin:0}
dt{color:var(--muted)}dd{margin:0;word-break:break-word}
.scroll{overflow-x:auto;-webkit-overflow-scrolling:touch}
table{border-collapse:collapse;width:100%;font-size:.92em}
th,td{text-align:left;padding:4px 8px;border-bottom:1px solid var(--line);vertical-align:top}
th{color:var(--muted);font-weight:500;white-space:nowrap}
td.num{text-align:right;font-variant-numeric:tabular-nums}
ul.diff{margin:.2rem 0 .4rem;padding-left:1.2rem}
ul.diff li{word-break:break-all}
@media (max-width:560px){body{font-size:14px}dl{grid-template-columns:1fr}dt{margin-top:4px}}
</style>
</head>
<body>
<main>
<h1>TrailSource report</h1>
<section>
<dl>
<dt>Journal</dt><dd><code>{{.File}}</code></dd>
<dt>Verify</dt><dd class="{{if .OK}}ok{{else}}fail{{end}}">{{.VerifySummary}}</dd>
</dl>
</section>
{{range .Streams}}
<h2>Stream <code>{{.StreamID}}</code></h2>
<section>
<dl>
<dt>Producer</dt><dd>{{.Producer}}</dd>
{{if .Endpoint}}<dt>Endpoint</dt><dd><code>{{.Endpoint}}</code></dd>{{end}}
<dt>Period</dt><dd>{{.First}} to {{.Last}} ({{.Span}})</dd>
<dt>At end</dt><dd><code>{{.LastEvent}}</code>, last seen {{if .LastSeen}}{{.LastSeen}}{{else}}never{{end}}</dd>
<dt>Records</dt><dd>{{.Records}}</dd>
{{with .Values}}{{if or .Good .Uncertain .Bad}}<dt>Values</dt><dd>{{.Nodes}} nodes; quality good {{.Good}}, uncertain {{.Uncertain}}, bad {{.Bad}}; without source timestamp {{.NoSourceTimestamp}}</dd>{{end}}{{end}}
</dl>
<div class="scroll"><table>
<tr><th>Event type</th><th>Records</th></tr>
{{range .Events}}<tr><td><code>{{.EventType}}</code></td><td class="num">{{.Count}}</td></tr>{{end}}
</table></div>
</section>

<h3>Address space generations</h3>
<section>
{{if not .Generations}}<p class="muted">None in this journal.</p>{{end}}
{{range .Generations}}
<p><strong>Generation {{.Generation}}</strong> <span class="muted">seq {{.Seq}}, {{.RecordedAt}}</span><br>
<code>{{.ReasonCode}}</code>, {{.NodeCount}} nodes, fingerprint <code>{{short .Fingerprint}}</code>
{{if ne .ReasonCode "source.inventory.initial"}}<br>added {{len .Added}}, removed {{len .Removed}}, changed {{len .Changed}}{{end}}
{{if .SameAs}}<br>Same fingerprint as generation {{.SameAs}}.{{end}}
{{if .Truncated}}<br><span class="notice">Truncated at max_nodes: the inventory covers a prefix of the address space.</span>{{end}}</p>
{{if or .Added .Removed .Changed}}<ul class="diff mono">
{{range limit .Added}}<li>+ {{.}}</li>{{end}}{{with more .Added}}<li class="muted">+ {{.}} more</li>{{end}}
{{range limit .Removed}}<li>- {{.}}</li>{{end}}{{with more .Removed}}<li class="muted">- {{.}} more</li>{{end}}
{{range limit .Changed}}<li>~ {{.}}</li>{{end}}{{with more .Changed}}<li class="muted">~ {{.}} more</li>{{end}}
</ul>{{end}}
{{end}}
</section>

<h3>Gaps</h3>
<section>
{{if not .Gaps}}<p class="muted">None.</p>{{else}}
<div class="scroll"><table>
<tr><th>Kind</th><th>Begin</th><th>End</th><th>Duration</th><th>Reason</th></tr>
{{range .Gaps}}<tr><td>{{.Kind}}</td><td class="mono">{{.Begin}}</td><td class="mono">{{if .Open}}open{{else}}{{.End}}{{end}}</td><td>{{.Duration}}</td>
<td>{{if .ReasonCode}}<code>{{.ReasonCode}}</code> {{end}}{{.Reason}}{{if .LastDataBefore}}<br><span class="muted">last value before: {{.LastDataBefore}}</span>{{end}}</td></tr>{{end}}
</table></div>{{end}}
</section>

<h3>Restarts</h3>
<section>
<div class="scroll"><table>
<tr><th>Seq</th><th>Recorded</th><th>Anchor</th><th>After previous record</th></tr>
{{range .Restarts}}<tr><td class="num">{{.Seq}}</td><td class="mono">{{.RecordedAt}}</td><td>{{if .AnchorGenesis}}genesis{{else}}<code>{{short .Anchor}}</code>{{end}}</td><td>{{.AfterPrevious}}</td></tr>{{end}}
</table></div>
</section>
{{end}}

<h2>Findings</h2>
<section>
{{if not .Verify.Issues}}<p class="muted">None.</p>{{else}}
<div class="scroll"><table>
<tr><th>Severity</th><th>Class</th><th>Line</th><th>Seq</th><th>Detail</th></tr>
{{range .Verify.Issues}}<tr><td class="{{severity .}}">{{severity .}}</td><td><code>{{.Kind}}</code></td><td class="num">{{.Line}}</td><td class="num">{{.Seq}}</td><td class="mono">{{.Detail}}</td></tr>{{end}}
</table></div>{{end}}
</section>
<p class="muted">A verified journal is internally consistent and unchanged since sealing. It says nothing about whether the values were true.</p>
</main>
</body>
</html>
`))
