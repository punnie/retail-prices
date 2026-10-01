package web

import (
	"fmt"
	"html/template"
	"net/url"
)

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

func money(v *float64) string {
	if v == nil {
		return "—"
	}
	return fmt.Sprintf("€%.2f", *v)
}

func moneyF(v float64) string { return fmt.Sprintf("€%.2f", v) }

func mul100(v *float64) float64 {
	if v == nil {
		return 0
	}
	return *v * 100
}

func mul100f(v float64) float64 { return v * 100 }

// imgSrc rewrites a retailer image URL to go through the local image proxy
// (see internal/imageproxy) instead of hotlinking the retailer's CDN
// directly from the viewer's browser.
func imgSrc(raw string) string {
	if raw == "" {
		return ""
	}
	return "/img?src=" + url.QueryEscape(raw)
}

func num0(v *float64) string {
	if v == nil {
		return ""
	}
	return fmt.Sprintf("%.0f", *v)
}

func num1(v *float64) string {
	if v == nil {
		return ""
	}
	return fmt.Sprintf("%.1f", *v)
}

var funcs = template.FuncMap{
	"dict":       dict,
	"money":      money,
	"moneyF":     moneyF,
	"mul100":     mul100,
	"mul100f":    mul100f,
	"num0":       num0,
	"num1":       num1,
	"pctCompare": pctCompare,
	"imgSrc":     imgSrc,
}

const styleBlock = `
<style>
  :root { color-scheme: light; }
  body { font-family: -apple-system, Helvetica, Arial, sans-serif; max-width: 1100px; margin: 0 auto 4rem; padding: 0 1rem; color: #0b0b0b; background: #f9f9f7; }
  header { display: flex; align-items: baseline; gap: 1.5rem; padding: 1.2rem 0; border-bottom: 1px solid #e4e3de; margin-bottom: 1.5rem; }
  header a { color: #0b0b0b; text-decoration: none; font-weight: 600; }
  header nav a { color: #52514e; font-weight: 400; margin-right: 1rem; }
  header nav a:hover { color: #0b0b0b; }
  h1 { font-size: 1.4rem; margin: 0; }
  a { color: #2457c5; }
  .muted { color: #898781; }
  .grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(220px, 1fr)); gap: 1rem; margin-top: 1rem; }
  .card { border: 1px solid #e4e3de; border-radius: 8px; padding: 0.8rem; background: #fcfcfb; text-decoration: none; color: inherit; display: block; }
  .card:hover { border-color: #2457c5; }
  .card img { width: 100%; height: 120px; object-fit: contain; background: #fff; border-radius: 4px; }
  .card .name { font-weight: 600; margin: 0.4rem 0 0.1rem; font-size: 0.92rem; }
  .store-tag { display: inline-block; padding: 0.05rem 0.45rem; border-radius: 4px; background: #eef1fb; font-size: 0.74rem; color: #2457c5; }
  .label-tag { display: inline-block; padding: 0.05rem 0.45rem; border-radius: 4px; font-size: 0.74rem; }
  .label-promo, .label-pending { background: rgba(12,163,12,0.14); color: #0ca30c; }
  .label-pending { background: rgba(250,178,25,0.22); color: #93650a; }
  .label-regular { background: #eef1fb; color: #2457c5; }
  form.search { display: flex; gap: 0.5rem; margin: 1rem 0; }
  form.search input[type=text] { flex: 1; padding: 0.5rem 0.7rem; border: 1px solid #c3c2b7; border-radius: 6px; font-size: 0.95rem; }
  form.search select, form.search button { padding: 0.5rem 0.7rem; border: 1px solid #c3c2b7; border-radius: 6px; background: #fff; }
  .headline { display: flex; gap: 1.2rem; flex-wrap: wrap; margin: 1rem 0 1.5rem; }
  .stat { border: 1px solid #e4e3de; border-radius: 8px; padding: 0.8rem 1rem; min-width: 150px; background: #fcfcfb; }
  .stat .k { font-size: 0.74rem; text-transform: uppercase; color: #898781; margin-bottom: 0.2rem; }
  .stat .v { font-size: 1.25rem; font-weight: 600; }
  .stat .sub { font-size: 0.8rem; color: #52514e; margin-top: 0.1rem; }
  .badge-line { font-size: 1.05rem; margin: 0.3rem 0 1rem; }
  .signal { background: #eef1fb; border-left: 3px solid #2457c5; padding: 0.6rem 0.9rem; border-radius: 4px; margin: 1rem 0; }
  .price-chart { display: block; width: 100%; height: auto; border: 1px solid #e4e3de; border-radius: 8px; }
  .chart-legend { display: flex; flex-wrap: wrap; gap: 0.9rem; font-size: 0.78rem; color: #52514e; margin: 0.5rem 0 1.5rem; }
  .chart-legend i { display: inline-block; width: 10px; height: 10px; margin-right: 0.3rem; vertical-align: middle; border-radius: 2px; }
  .chart-legend i.dot { border-radius: 50%; }
  table { width: 100%; border-collapse: collapse; font-size: 0.88rem; margin: 0.5rem 0 1.5rem; }
  th, td { text-align: left; padding: 0.4rem 0.5rem; border-bottom: 1px solid #eee; }
  th { color: #898781; font-weight: 600; font-size: 0.76rem; text-transform: uppercase; }
  .empty { color: #898781; font-style: italic; }
  .candidate { display: flex; gap: 1rem; align-items: center; border: 1px solid #e4e3de; border-radius: 8px; padding: 0.8rem; margin-bottom: 0.8rem; background: #fcfcfb; }
  .candidate .side { flex: 1; }
  .candidate .score { width: 70px; text-align: center; font-weight: 600; color: #2457c5; }
  .candidate .actions { display: flex; flex-direction: column; gap: 0.4rem; }
  .candidate button { padding: 0.4rem 0.8rem; border-radius: 6px; border: 1px solid #c3c2b7; background: #fff; cursor: pointer; }
  .candidate button.confirm { background: #0ca30c; color: #fff; border-color: #0ca30c; }
  .candidate button.reject { background: #fff; color: #d03b3b; border-color: #d03b3b; }
  .stale { color: #ec835a; }
  .candidate .side { display: flex; gap: 0.7rem; align-items: center; }
  .thumb { width: 64px; height: 64px; object-fit: contain; background: #fff; border-radius: 4px; flex: none; }
  .manual { display: grid; grid-template-columns: 1fr 1fr; gap: 1.2rem; align-items: start; }
  .manual .col { border: 1px solid #e4e3de; border-radius: 8px; padding: 0.8rem; background: #fcfcfb; }
  .manual .col input[type=text] { width: 100%; box-sizing: border-box; padding: 0.5rem 0.7rem; border: 1px solid #c3c2b7; border-radius: 6px; margin-bottom: 0.6rem; }
  .manual label.opt { display: flex; gap: 0.6rem; align-items: center; padding: 0.4rem; border-radius: 6px; cursor: pointer; }
  .manual label.opt:hover, .manual label.opt:has(input:checked) { background: #eef1fb; }
  .manual .links { font-size: 0.78rem; }
  .manual-actions { margin: 1.2rem 0; display: flex; gap: 1rem; align-items: center; }
  .manual-actions button { padding: 0.5rem 1.2rem; border-radius: 6px; border: 1px solid #0ca30c; background: #0ca30c; color: #fff; cursor: pointer; }
  .error { background: rgba(208,59,59,0.1); border-left: 3px solid #d03b3b; padding: 0.6rem 0.9rem; border-radius: 4px; margin: 1rem 0; }
  @media (max-width: 700px) { .manual { grid-template-columns: 1fr; } }
</style>`

var tmpl = template.Must(template.New("web").Funcs(funcs).Parse(`
{{define "layout"}}<!doctype html>
<html lang="en">
<head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>{{.Title}}</title>` + styleBlock + `</head>
<body>
<header>
  <a href="/">Retail prices</a>
  <nav><a href="/products">Catalogue</a><a href="/match">Linking queue</a><a href="/match/manual">Link manually</a></nav>
</header>
{{block "content" .}}{{end}}
</body>
</html>{{end}}

{{define "content"}}{{end}}
`))

// catalogueTmpl, productTmpl and matchTmpl are parsed separately (rather than
// one shared associated-template tree) so each page's "content" block can't
// collide with another's — html/template has one flat namespace per *Template.
var catalogueTmpl = template.Must(template.Must(tmpl.Clone()).Parse(`
{{define "content"}}
<h1>Catalogue</h1>
<form class="search" method="get" action="/products">
  <input type="text" name="q" placeholder="Search products..." value="{{.Query}}">
  <select name="store">
    <option value="">All stores</option>
    {{range .Stores}}<option value="{{.}}" {{if eq . $.Store}}selected{{end}}>{{.}}</option>{{end}}
  </select>
  <button type="submit">Search</button>
</form>
{{if .Products}}
<div class="grid">
  {{range .Products}}
  <a class="card" href="/products/{{.ID}}">
    {{if .ImageURL}}<img src="{{imgSrc .ImageURL}}" alt="" loading="lazy">{{else}}<div style="height:120px;background:#f3f3f1;border-radius:4px"></div>{{end}}
    <div><span class="store-tag">{{.Store}}</span>{{if .HasSnapshot}} <span class="label-tag label-{{.CurrentLabel}}">{{.CurrentLabel}}</span>{{end}}</div>
    <p class="name">{{.Name}}</p>
    {{if .Brand}}<p class="muted" style="margin:0;font-size:0.82rem">{{.Brand}}</p>{{end}}
    {{if .HasSnapshot}}<p style="margin:0.3rem 0 0;font-weight:600">{{moneyF .CurrentPrice}}</p>{{end}}
  </a>
  {{end}}
</div>
{{else}}
<p class="empty">No products match.</p>
{{end}}
{{end}}
`))

var productTmpl = template.Must(template.Must(tmpl.Clone()).Parse(`
{{define "content"}}
<h1>{{.Product.Name}}</h1>
<p><span class="store-tag">{{.Product.Store}}</span> {{if .Product.Brand}}&middot; {{.Product.Brand}}{{end}}
  {{if .Product.PackValue}}&middot; {{num0 .Product.PackValue}}{{.Product.PackUnit}}{{end}}
  &middot; <a href="{{.Product.URL}}" target="_blank" rel="noopener">view on {{.Product.Store}} &rarr;</a>
  &middot; <a href="/match/manual?a={{.Product.ID}}">link to another retailer&hellip;</a></p>

{{if .HasSnapshot}}
<p class="badge-line">{{.StateBadge}}</p>

<div class="headline">
  <div class="stat">
    <div class="k">Current price</div>
    <div class="v">{{moneyF .Snapshot.CurrentPrice}}</div>
  </div>
  {{if .Snapshot.HasEnoughHistory}}
  <div class="stat">
    <div class="k">Regular price</div>
    <div class="v">{{money .Snapshot.RegularPrice}}</div>
    {{with pctCompare .Snapshot.RegularPrice .Snapshot.RegularPrice1y}}<div class="sub">{{.}} vs a year ago</div>{{end}}
  </div>
  <div class="stat">
    <div class="k">What people typically pay</div>
    <div class="v">{{money .Snapshot.AvgPaidPrice}}</div>
    {{with pctCompare .Snapshot.AvgPaidPrice .Snapshot.AvgPaidPrice1y}}<div class="sub">{{.}} vs a year ago</div>{{end}}
    <div class="sub muted">averaged over ~12 weeks, so occasional promos don't swing it</div>
  </div>
  {{else}}
  <div class="stat"><div class="k">Regular &amp; average-paid price</div><div class="v muted" style="font-size:0.95rem">Not enough history yet</div></div>
  {{end}}
</div>

<div class="signal">{{.BuyNowSignal}}</div>

{{.Chart}}

{{if .Snapshot.HasEnoughHistory}}
<h2>Promo pattern</h2>
{{if or .Snapshot.PromosPerYear .Snapshot.TypicalPromoDepth .Snapshot.TypicalPromoDays}}
<p>
  {{if .Snapshot.PromosPerYear}}About {{num1 .Snapshot.PromosPerYear}} promos/year{{end}}
  {{if .Snapshot.TypicalPromoDepth}}, typically {{printf "%.0f" (mul100 .Snapshot.TypicalPromoDepth)}}% off{{end}}
  {{if .Snapshot.TypicalPromoDays}}, usually lasting about {{num0 .Snapshot.TypicalPromoDays}} days{{end}}.
</p>
{{else}}
<p class="muted">No completed promo episodes yet in this item's history.</p>
{{end}}
{{if .Episodes}}
<table>
<thead><tr><th>From</th><th>To</th><th>Days</th><th>Lowest price</th><th>Depth</th><th>Status</th></tr></thead>
<tbody>
{{range .Episodes}}
<tr>
  <td>{{.Start.Format "2006-01-02"}}</td>
  <td>{{.End.Format "2006-01-02"}}</td>
  <td>{{.Days}}</td>
  <td>{{moneyF .Price}}</td>
  <td>{{printf "%.0f" (mul100f .Depth)}}%</td>
  <td>{{if .Pending}}<span class="label-tag label-pending">pending</span>{{else}}ended{{end}}</td>
</tr>
{{end}}
</tbody>
</table>
{{end}}
{{end}}

{{if .Changes}}
<h2>Regular price changes</h2>
<table>
<thead><tr><th>Date</th><th>From</th><th>To</th><th>Change</th></tr></thead>
<tbody>
{{range .Changes}}
<tr><td>{{.ChangeDate.Format "2006-01-02"}}</td><td>{{moneyF .OldPrice}}</td><td>{{moneyF .NewPrice}}</td>
<td>{{printf "%+.1f%%" (mul100f .PctChange)}} ({{.Direction}})</td></tr>
{{end}}
</tbody>
</table>
{{end}}

{{if .Siblings}}
<h2>Linked listings</h2>
<table>
<thead><tr><th>Store</th><th>Listing</th><th>Price</th><th>Last observed</th></tr></thead>
<tbody>
{{range .Siblings}}
<tr>
  <td>{{.Store}}{{if .SameStore}} <span class="label-tag label-pending">other listing here</span>{{end}}</td>
  <td><a href="/products/{{.ID}}">{{.Name}}</a> <span class="muted">SKU {{.SKU}}</span></td>
  <td>{{if .HasSnapshot}}{{moneyF .CurrentPrice}}{{else}}&mdash;{{end}}</td>
  <td>{{.LastSeen.Format "2006-01-02"}}</td>
</tr>
{{end}}
</tbody>
</table>
{{end}}

{{else}}
<p class="muted">No pricing analysis yet for this product (run <code>retail refresh</code>).</p>
{{end}}

<p class="muted">{{.ObservationFooter}}</p>
{{end}}
`))

var matchTmpl = template.Must(template.Must(tmpl.Clone()).Parse(`
{{define "content"}}
<h1>Cross-retailer linking queue</h1>
<p class="muted">Suggested pairs from name/brand/pack-size matching, ranked by score. Nothing links automatically &mdash; confirm or reject each one. Missing a pair? <a href="/match/manual">Link products manually</a>.</p>
{{if .Candidates}}
{{range .Candidates}}
<div class="candidate">
  <div class="side">
    {{if .Left.ImageURL}}<img class="thumb" src="{{imgSrc .Left.ImageURL}}" alt="" loading="lazy">{{end}}
    <div>
    <span class="store-tag">{{.Left.Store}}</span><br>
    <strong>{{.Left.Name}}</strong>{{if .Left.Brand}} <span class="muted">&middot; {{.Left.Brand}}</span>{{end}}<br>
    {{if .Left.HasPrice}}{{moneyF .Left.Price}} &middot; {{end}}<a href="/products/{{.Left.ID}}" target="_blank">product page</a> &middot; <a href="{{.Left.URL}}" target="_blank" rel="noopener">on {{.Left.Store}} &nearr;</a>
    </div>
  </div>
  <div class="score">{{printf "%.2f" .Score}}</div>
  <div class="side">
    {{if .Right.ImageURL}}<img class="thumb" src="{{imgSrc .Right.ImageURL}}" alt="" loading="lazy">{{end}}
    <div>
    <span class="store-tag">{{.Right.Store}}</span><br>
    <strong>{{.Right.Name}}</strong>{{if .Right.Brand}} <span class="muted">&middot; {{.Right.Brand}}</span>{{end}}<br>
    {{if .Right.HasPrice}}{{moneyF .Right.Price}} &middot; {{end}}<a href="/products/{{.Right.ID}}" target="_blank">product page</a> &middot; <a href="{{.Right.URL}}" target="_blank" rel="noopener">on {{.Right.Store}} &nearr;</a>
    </div>
  </div>
  <div class="actions">
    <form method="post" action="/match/decide">
      <input type="hidden" name="left" value="{{.Left.ID}}">
      <input type="hidden" name="right" value="{{.Right.ID}}">
      <input type="hidden" name="decision" value="matched">
      <button class="confirm" type="submit">Same product</button>
    </form>
    <form method="post" action="/match/decide">
      <input type="hidden" name="left" value="{{.Left.ID}}">
      <input type="hidden" name="right" value="{{.Right.ID}}">
      <input type="hidden" name="decision" value="rejected">
      <button class="reject" type="submit">Not a match</button>
    </form>
  </div>
</div>
{{end}}
{{else}}
<p class="empty">No pending candidates &mdash; run <code>retail match</code> to generate more.</p>
{{end}}
{{end}}
`))

var manualTmpl = template.Must(template.Must(tmpl.Clone()).Parse(`
{{define "option"}}
<label class="opt">
  <input type="radio" name="{{.Field}}" value="{{.O.ID}}" {{if and .Sel (eq .Sel.ID .O.ID)}}checked{{end}}>
  {{if .O.ImageURL}}<img class="thumb" src="{{imgSrc .O.ImageURL}}" alt="" loading="lazy">{{end}}
  <span>
    <span class="store-tag">{{.O.Store}}</span> <strong>{{.O.Name}}</strong>{{if .O.Brand}} <span class="muted">&middot; {{.O.Brand}}</span>{{end}}<br>
    <span class="muted">{{if .O.HasPrice}}{{moneyF .O.Price}}{{end}}{{if .O.Pack}} &middot; {{.O.Pack}}{{end}}{{if .O.Grouped}} &middot; already in a group{{end}}</span>
    <span class="links"><a href="/products/{{.O.ID}}" target="_blank">product page</a> &middot; <a href="{{.O.URL}}" target="_blank" rel="noopener">on {{.O.Store}} &nearr;</a></span>
  </span>
</label>
{{end}}

{{define "side"}}
<div class="col">
  <h3 style="margin-top:0">{{.Title}}</h3>
  <input type="text" name="q{{.S.Field}}" placeholder="Search by name..." value="{{.S.Query}}">
  {{if .S.List}}
    {{range .S.List}}{{template "option" (dict "Field" $.S.Field "O" . "Sel" $.S.Selected)}}{{end}}
  {{else if .S.Searched}}<p class="empty">No products found.</p>
  {{else}}<p class="muted">Search, then pick the product.</p>{{end}}
</div>
{{end}}

{{define "content"}}
<h1>Link products manually</h1>
<p class="muted">For pairs the suggestion queue missed. Search each side, pick one product from each, and confirm. Both can be from the same retailer &mdash; for an item relisted under a new SKU.</p>
{{if .Error}}<div class="error">{{.Error}}</div>{{end}}
<form method="get" action="/match/manual">
  <div class="manual">
    {{template "side" (dict "Title" "Product" "S" .A)}}
    {{template "side" (dict "Title" "Same product (another retailer, or a relisting)" "S" .B)}}
  </div>
  <div class="manual-actions">
    <button type="submit">Search</button>
    <button type="submit" formmethod="post" formaction="/match/manual">Same product &mdash; link them</button>
  </div>
</form>
{{end}}
`))
