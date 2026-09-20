package report

import (
	"fmt"
	"html/template"
)

// dict lets the template pass multiple named values into the "section"
// sub-template, e.g. {{template "section" (dict "Title" "x" "Events" .Foo)}}.
func dict(pairs ...any) (map[string]any, error) {
	if len(pairs)%2 != 0 {
		return nil, fmt.Errorf("dict: odd number of arguments")
	}
	m := make(map[string]any, len(pairs)/2)
	for i := 0; i < len(pairs); i += 2 {
		key, ok := pairs[i].(string)
		if !ok {
			return nil, fmt.Errorf("dict: key %v is not a string", pairs[i])
		}
		m[key] = pairs[i+1]
	}
	return m, nil
}

var tmpl = template.Must(template.New("report").Funcs(template.FuncMap{"dict": dict}).Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<title>Retail price changes</title>
<style>
  body { font-family: -apple-system, Helvetica, Arial, sans-serif; max-width: 1100px; margin: 2rem auto; padding: 0 1rem; color: #1a1a1a; }
  h1 { margin-bottom: 0.2rem; }
  .meta { color: #666; margin-bottom: 1.5rem; }
  h2 { margin-top: 2.5rem; border-bottom: 2px solid #eee; padding-bottom: 0.3rem; }
  table { width: 100%; border-collapse: collapse; font-size: 0.92rem; }
  th, td { text-align: left; padding: 0.4rem 0.6rem; border-bottom: 1px solid #eee; vertical-align: middle; }
  th { color: #888; font-weight: 600; font-size: 0.8rem; text-transform: uppercase; }
  tr:hover { background: #fafafa; }
  a { color: #0b5fff; text-decoration: none; }
  a:hover { text-decoration: underline; }
  .store { display: inline-block; padding: 0.1rem 0.5rem; border-radius: 4px; background: #eef; font-size: 0.78rem; color: #334; }
  .pct-down { color: #1a7f37; font-weight: 600; }
  .pct-up { color: #b02a37; font-weight: 600; }
  .pct-suspect { color: #8a6d00; font-weight: 600; }
  .empty { color: #999; font-style: italic; }
  .spark { display: block; }
  details summary { cursor: pointer; color: #666; margin-top: 2.5rem; }
</style>
</head>
<body>

<h1>Retail price changes</h1>
<p class="meta">generated {{.GeneratedAt}} &middot; window: last {{.Since}} &middot;
  {{.TotalEventsInWindow}} price events in window ({{printf "%.1f" .SuspectShareOfWindow}}% flagged suspect)</p>

{{template "section" (dict "Title" "Promotions running now" "Events" .PromosActive "PctClass" "pct-down")}}
{{template "section" (dict "Title" "Promotions that started and have since lapsed" "Events" .PromosLapsed "PctClass" "pct-down")}}
{{template "section" (dict "Title" "Promotions ended (back to normal price)" "Events" .PromoEnds "PctClass" "pct-up")}}
{{template "section" (dict "Title" "Price decreases" "Events" .Decreases "PctClass" "pct-down")}}
{{template "section" (dict "Title" "Price increases" "Events" .Increases "PctClass" "pct-up")}}

{{if .IncludeSuspect}}
{{template "section" (dict "Title" "Suspected data glitches (excluded from the sections above)" "Events" .Suspects "PctClass" "pct-suspect")}}
{{else}}
<p class="meta">Suspected glitches are hidden by default. Re-run with <code>--include-suspect</code> to inspect them.</p>
{{end}}

</body>
</html>

{{define "section"}}
<h2>{{.Title}} <small style="color:#999; font-weight:normal;">({{len .Events}})</small></h2>
{{if .Events}}
<table>
<thead><tr><th>Store</th><th>Product</th><th>Change</th><th>Now</th><th>Trend</th><th>When</th></tr></thead>
<tbody>
{{range .Events}}
<tr>
  <td><span class="store">{{.Store}}</span></td>
  <td><a href="{{.URL}}" target="_blank" rel="noopener">{{.Name}}</a>{{if .Brand}} <span style="color:#999">&middot; {{.Brand}}</span>{{end}}</td>
  <td>
    &euro;{{printf "%.2f" .OldPrice}} &rarr; &euro;{{printf "%.2f" .NewPrice}}
    <span class="{{$.PctClass}}">({{printf "%+.0f" .PctChange}}%)</span>
  </td>
  <td>&euro;{{printf "%.2f" .CurrentPrice}}{{if not .StillActive}} <span style="color:#999">(changed since)</span>{{end}}</td>
  <td>{{.Sparkline}}</td>
  <td>{{.ChangedAt.Format "2006-01-02"}}</td>
</tr>
{{end}}
</tbody>
</table>
{{else}}
<p class="empty">none in this window</p>
{{end}}
{{end}}
`))
