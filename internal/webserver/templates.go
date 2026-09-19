// The page templates. One layout, one content block per page, one stylesheet.
//
// Everything here is static: no JavaScript, no build step, no asset pipeline. A page is a
// rendered document, so the interface is the document — which is also the only way to show
// Danish law text honestly, since the text itself is the thing people came to read.
//
// html/template is what makes the escaping claim in the package comment hold for the parts this
// file writes: a query echoed back, an address, a title from the register. The document body is
// the one value inserted unescaped, and it arrives escaped from lexdoc.
package webserver

import (
	"fmt"
	"html/template"
	"strings"
)

var templateFuncs = template.FuncMap{
	// provenanceLabel says how trustworthy the text on screen is, in the words the README
	// uses. A page that showed "xml" would be showing a filename instead of a warning.
	"provenanceLabel": provenanceLabel,
	// coverageText states how much of the question a provision answers, which is the fact
	// that distinguishes an AND search's hits from its almost-hits.
	"coverageText": coverageText,
	// The facet links are built here rather than in the templates so that a filter value is
	// URL-escaped by the one function that builds every link.
	"yearLink": func(kind, year string) string {
		return browseQueryArgs(kind, yearOf(year), "", 0)
	},
	"kindLink": func(kind string) string { return browseQueryArgs(kind, 0, "", 0) },
}

func provenanceLabel(source string) string {
	switch strings.ToLower(strings.TrimSpace(source)) {
	case "xml":
		return "registerets egen tekst"
	case "html":
		return "læst fra www — rekonstrueret, ikke ordret"
	case "none", "":
		return "ingen tekst i spejlet"
	default:
		return source
	}
}

func coverageText(covered, requested int) string {
	if requested <= 0 || covered >= requested {
		return ""
	}
	return fmt.Sprintf("dækker %d af %d ord", covered, requested)
}

func parseTemplates() map[string]*template.Template {
	pages := map[string]string{
		"index":    indexBody,
		"search":   searchBody,
		"document": documentBody,
		"browse":   browseBody,
	}
	out := make(map[string]*template.Template, len(pages))
	for name, body := range pages {
		t := template.Must(template.New("layout").Funcs(templateFuncs).Parse(layoutTmpl))
		out[name] = template.Must(t.New("content").Parse(body))
	}
	return out
}

const layoutTmpl = `<!doctype html>
<html lang="da">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Title}}</title>
<style>
:root { color-scheme: light dark; --ink: #1b1b1b; --muted: #6a6a6a; --line: #d8d5cf; --accent: #14532d; --wash: #f6f5f2; }
@media (prefers-color-scheme: dark) { :root { --ink: #e6e4e0; --muted: #9d9a94; --line: #3a3833; --accent: #7cc796; --wash: #171614; } }
* { box-sizing: border-box; }
body { margin: 0; color: var(--ink); background: var(--wash); font: 16px/1.55 Georgia, "Times New Roman", serif; }
.wrap { max-width: 46rem; margin: 0 auto; padding: 1rem 1.1rem 3rem; }
header.site { border-bottom: 1px solid var(--line); background: transparent; }
header.site .wrap { display: flex; gap: 1rem; align-items: baseline; padding-bottom: .6rem; padding-top: .8rem; }
.brand { font-weight: 700; font-size: 1.15rem; text-decoration: none; color: var(--ink); }
nav.site a { margin-left: .8rem; color: var(--muted); font-size: .9rem; }
a { color: var(--accent); }
h1 { font-size: 1.5rem; line-height: 1.25; margin: 1.2rem 0 .3rem; }
h2 { font-size: 1.15rem; margin: 1.4rem 0 .4rem; }
form.search { display: flex; flex-wrap: wrap; gap: .5rem; margin: 1rem 0 .4rem; }
input[type=search] { flex: 1 1 18rem; padding: .55rem .7rem; font: inherit; border: 1px solid var(--line); border-radius: 3px; background: transparent; color: inherit; }
input[type=number], select { padding: .5rem; font: inherit; border: 1px solid var(--line); border-radius: 3px; background: transparent; color: inherit; }
button { padding: .55rem .9rem; font: inherit; border: 1px solid var(--accent); background: var(--accent); color: #fff; border-radius: 3px; cursor: pointer; }
.hint, .mirror, .bad, .note { color: var(--muted); font-size: .88rem; }
.bad, .warn { color: #a12a2a; }
ul.kinds { list-style: none; display: flex; flex-wrap: wrap; gap: .3rem .9rem; padding: 0; margin: .8rem 0; font-size: .92rem; }
.hit { border-top: 1px solid var(--line); padding: .8rem 0 .9rem; }
.hit .cit { font-size: .84rem; color: var(--muted); font-family: ui-monospace, Menlo, monospace; }
.hit a.addr { font-size: .95rem; font-weight: 600; text-decoration: none; }
.hit p.snip { margin: .35rem 0 .3rem; }
.prov { display: inline-block; font-size: .78rem; color: var(--muted); border: 1px solid var(--line); border-radius: 999px; padding: 0 .5rem; }
.prov.xml { border-color: var(--accent); color: var(--accent); }
.meta { font-size: .9rem; color: var(--muted); }
.meta b { color: var(--ink); }
nav.toc { border: 1px solid var(--line); border-radius: 3px; padding: .6rem .9rem; margin: 1rem 0; font-size: .9rem; max-height: 18rem; overflow: auto; background: transparent; }
nav.toc ol { margin: 0; padding-left: 1.1rem; list-style: none; }
nav.toc li { margin: .1rem 0; }
nav.toc .lvl2 { margin-left: 1rem; }
article.law { background: transparent; }
article.law section { margin: .2rem 0; }
article.law :target { background: color-mix(in srgb, var(--accent) 14%, transparent); }
aside.related { margin-top: 2.5rem; border-top: 2px solid var(--line); padding-top: .6rem; }
aside.related ul { list-style: none; padding: 0; }
aside.related li { margin: .3rem 0; font-size: .94rem; }
aside.related .unheld { color: var(--muted); }
table.browse { width: 100%; border-collapse: collapse; font-size: .95rem; }
table.browse td { border-top: 1px solid var(--line); padding: .35rem .4rem .35rem 0; vertical-align: top; }
table.browse td.n { white-space: nowrap; color: var(--muted); font-family: ui-monospace, Menlo, monospace; font-size: .85rem; }
.pages { display: flex; flex-wrap: wrap; gap: .5rem; font-size: .9rem; margin: .8rem 0; }
.pages .now { font-weight: 700; }
pre.plain { white-space: pre-wrap; word-break: break-word; font-size: .9rem; }
footer { color: var(--muted); font-size: .82rem; border-top: 1px solid var(--line); margin-top: 2.5rem; }
</style>
</head>
<body>
<header class="site"><div class="wrap">
<a class="brand" href="/">Retsinformation</a>
<nav class="site"><a href="/browse">Alle dokumenter</a><a href="/status">Status</a><a href="/mcp">MCP</a></nav>
</div></header>
<div class="wrap">{{template "content" .}}</div>
<footer><div class="wrap">Frit og offentligt indhold. Teksten er læst fra Retsinformation;
kilder og usikkerhed står ved hver svar. <!-- /mcp serves the same answers to agents. --></div></footer>
</body>
</html>`

const indexBody = `{{define "content"}}
<h1 id="top">Søg i dansk lovgivning</h1>
<p class="mirror">{{.Status}}</p>
<form class="search" method="get" action="/search">
<input type="search" name="q" placeholder="Søg i bestemmelsers tekst" autofocus autocomplete="off">
<button type="submit">Søg</button>
</form>
<p class="hint">Ord finder bestemmelsen, men ikke nødvendigvis hele meningen. Brug
<b>AND</b> for at kræve begge ord, <b>"flere ord"</b> for netop den rækkefølge, og
<b>-ord</b> for at udelukke. Tryk <b>Enter</b> for at søge.</p>
<p>Prøv: {{range .Queries}}<a href="/search?q={{.Query}}">{{.Label}}</a> &nbsp;{{end}}</p>
<h2>Find efter type</h2>
<ul class="kinds">{{range .Kinds}}<li><a href="{{kindLink .Name}}">{{.Name}}</a> <span class="hint">{{.Count}}</span></li>{{end}}</ul>
<p class="hint">{{.Total}} dokumenter i alt. År og titel kan filtreres på
<a href="/browse">listen over alle dokumenter</a>.</p>
{{if not .Search}}<p class="warn">Søgeindeks er ikke læst — siden kan vise dokumenter og læse
bestemmelser, men ikke søge i dem. Start serveren med <b>--index</b>.</p>{{end}}
{{end}}`

const searchBody = `{{define "content"}}
<h1 id="top">Søgning</h1>
<form class="search" method="get" action="/search">
<input type="search" name="q" value="{{.Query}}" placeholder="Søg i bestemmelsers tekst" autocomplete="off">
<select name="kind"><option value="">alle typer</option>{{range .Kinds}}<option value="{{.Name}}" {{if eq .Name $.Kind}}selected{{end}}>{{.Name}} ({{.Count}})</option>{{end}}</select>
<input type="number" name="since" value="{{.Since}}" placeholder="fra år" min="1800" max="2100">
<input type="number" name="until" value="{{.Until}}" placeholder="til år" min="1800" max="2100">
<input type="number" name="limit" placeholder="antal" min="1" max="200">
<button type="submit">Søg</button>
</form>
{{range .Warn}}<p class="warn">{{.}}</p>{{end}}
{{if .NoIndex}}<p class="hint">Uden indeks kan du stadig læse og browse:
<a href="/browse">alle dokumenter</a>.</p>{{end}}
{{if .HasQuery}}
<p class="mirror">{{.Total}} resultater. {{.Mirror}}</p>
{{if not .Hits}}<p>Intet fandtes. Ret staveformen, eller slap kravene: <b>AND</b> og
citerede gåser kræver at ordene står sammen.</p>{{end}}
{{range .Hits}}
<div class="hit">
<div class="cit">{{.Title}}{{if .ELI}} · <a href="{{.ELI}}">eli</a>{{end}}</div>
<a class="addr" href="{{.Link}}">{{if .Address}}{{.Address}}{{else}}hele dokumentet{{end}}</a>
<span class="prov {{.Provenance}}">{{provenanceLabel .Provenance}}</span>
<p class="snip">{{.Snippet}}</p>
<p class="hint">{{coverageText .Covered .Requested}}{{if .Gap}} · {{.Gap}}{{end}}</p>
</div>
{{end}}
{{else}}<p class="hint">Skriv noget i feltet for at søge.</p>{{end}}
{{end}}`

const documentBody = `{{define "content"}}
<div class="cit"><b>{{.Citation}}</b></div>
<h1 id="top">{{.Citation}}</h1>
<p class="meta">
{{if .Kind}}<b>{{.Kind}}</b>{{end}}
{{if .Year}} · {{.Year}}{{end}}
{{if .Status}} · {{.Status}}{{end}}
{{if .Accession}} · accession {{.Accession}}{{end}}
{{if .ELI}} · <a href="{{.ELI}}">Retsinformation</a>{{end}}
</p>
<p class="meta">Kilde: <span class="prov {{.Provenance}}">{{provenanceLabel .Provenance}}</span>
{{if .ProvenanceNote}} — {{.ProvenanceNote}}{{end}}</p>
<p class="mirror">{{.Mirror}}</p>
{{range .Warnings}}<p class="warn">{{.}}</p>{{end}}
{{if .Addr}}<p class="note">Adressen <b>{{.Addr}}</b> er vist alene.
{{if .Anchor}}Det faste anker er <a href="{{.FullLink}}#{{.Anchor}}">#{{.Anchor}}</a>.{{end}}
<a href="{{.FullLink}}">Se hele dokumentet</a>.</p>{{end}}
{{if .Contents}}<nav class="toc" aria-label="Indhold">
<b>Inhold</b>
<ol>{{range .Contents}}<li class="lvl{{.Level}}"><a href="{{.Href}}">{{.Label}}</a></li>{{end}}</ol>
</nav>{{end}}
{{if .Body}}<article class="law">{{.Body}}</article>{{end}}
{{if .PlainText}}<pre class="plain">{{.PlainText}}</pre>{{end}}
{{if .Truncated}}<p class="warn">Svaret er afkortet.</p>{{end}}
{{if .Groups}}<aside class="related">
{{range .Groups}}
<h2>{{.Name}}</h2>
<ul>{{range .Items}}<li>{{if .Held}}<a href="{{.Href}}">{{.Label}}</a>{{else}}<span class="unheld">{{.Label}}</span>{{end}}{{if .Date}} · virker {{.Date}}{{end}}{{if not .Held}} <span class="hint">(ikke i dette spejl)</span>{{end}}</li>{{end}}</ul>
{{end}}
<p class="hint">“Ikke i dette spejl” betyder at loven nævnes andre steder, men at dens egen
tekst ikke er hentet endnu. Spejlet er en cache, ikke hele registeret.</p>
</aside>{{end}}
{{end}}`

const browseBody = `{{define "content"}}
<h1 id="top">Alle dokumenter</h1>
<form class="search" method="get" action="/browse">
<input type="search" name="title" value="{{.Title}}" placeholder="Titel, f.eks. 'forældrepension'" autocomplete="off">
<select name="kind"><option value="">alle typer</option>{{range .Kinds}}<option value="{{.Name}}" {{if eq .Name $.Kind}}selected{{end}}>{{.Name}} ({{.Count}})</option>{{end}}</select>
<input type="number" name="year" value="{{.Year}}" placeholder="år" min="1800" max="2100">
<button type="submit">Vis</button>
</form>
<p class="mirror">{{.Total}} dokumenter matcher. Viser {{.From}}–{{.To}}, nyeste først.</p>
{{if .Pages}}<div class="pages">{{range .Pages}}{{if .Current}}<span class="now">{{.Label}}</span>{{else}}<a href="{{.Href}}">{{.Label}}</a>{{end}}{{end}}</div>{{end}}
<table class="browse">
{{range .Items}}<tr>
<td class="n">{{.Year}} {{.Number}}</td>
<td><a href="{{.Link}}">{{.Title}}</a> <span class="hint">{{.Kind}}{{if .Type}} · {{.Type}}{{end}}</span></td>
</tr>{{end}}
</table>
{{if not .Items}}<p>Ingen dokumenter matcher. Prøv uden årstal.</p>{{end}}
{{if .Pages}}<div class="pages">{{range .Pages}}{{if .Current}}<span class="now">{{.Label}}</span>{{else}}<a href="{{.Href}}">{{.Label}}</a>{{end}}{{end}}</div>{{end}}
{{end}}`
