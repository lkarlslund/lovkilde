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
	"provenanceLabel":     provenanceLabel,
	"provenanceNoteLabel": provenanceNoteLabel,
	"statusShort":         statusShort,
	"statusClass":         statusClass,
	// coverageText states how much of the question a provision answers, which is the fact
	// that distinguishes an AND search's hits from its almost-hits.
	"coverageText": coverageText,
	// statusLabel says what the register's own status field means in the language the page
	// is written in. "Valid" on a Danish page is a field value, not a sentence, and the
	// difference matters: the reader is deciding whether this text still stands.
	"statusLabel": statusLabel,
	// The facet links are built here rather than in the templates so that a filter value is
	// URL-escaped by the one function that builds every link.
	"yearLink": func(kind, year string) string {
		return browseQueryArgs(kind, yearOf(year), "", "", "", 0)
	},
	// kindLink is used on the index page, where no other filter is active yet, so the two
	// empty filter arguments are the truth rather than a dropped choice.
	"kindLink": func(kind string) string { return browseQueryArgs(kind, 0, "", "", "", 0) },
}

func statusLabel(status string) string {
	switch strings.TrimSpace(status) {
	case "Valid":
		return "gældende"
	case "Historic":
		return "historisk, ophævet eller afløst"
	case "":
		return "status ukendt i spejlet"
	default:
		return status
	}
}

// provenanceNoteLabel puts the register's own words for a text into Danish. The machine field
// stays English on purpose — "xml" is what a client branches on and the README documents it — but
// a Danish reading page that drops an English gloss into the middle of a sentence asks the reader
// to be bilingual to find out whether the text in front of them is official.
func provenanceNoteLabel(note string) string {
	switch note {
	case "the register's own structured body, decoded from canonical ELI XML":
		return "læst fra registerets egen strukturerede XML"
	case "HTML-only document: the publisher never supplied XML, so this text is our parse of the register's page. Informal: cite from the official source.":
		return "registeret har kun en html-side for dette dokument, så teksten er vores læsning af den — citér fra den officielle kilde"
	case "no compiled text is available for this document, so it is listed but not readable here":
		return "der er ingen tekst i spejlet til dette dokument, så det kan listes men ikke læses her"
	}
	return note
}

// statusShort is the one-word Danish form, for a facet option or a table row. The document page
// spells the longer form because it also says whose claim the status is; in a list the column
// heading says that already, and a word that fits is what a row needs.
func statusShort(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "valid", "gældende", "gaeldende":
		return "gældende"
	case "historic", "historisk", "ophævet", "ophaevet":
		return "historisk"
	case "", "unknown", "ukendt":
		return "ukendt"
	}
	return s
}

// statusClass keeps the colour scale honest: green is the register saying "in force", amber a
// document that no longer governs, and no colour at all the case where the register says nothing
// — which is not a bad result, it is an absent one.
func statusClass(s string) string {
	switch statusShort(s) {
	case "gældende":
		return "ok"
	case "historisk":
		return "warn"
	}
	return ""
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
<title>{{.PageTitle}}</title>
<style>
:root { color-scheme: light dark; --ink: #1b1b1b; --muted: #6a6a6a; --line: #d8d5cf; --accent: #14532d; --wash: #f6f5f2; --panel: #fffefb; --band: #efece6; }
@media (prefers-color-scheme: dark) { :root { --ink: #e7e5e1; --muted: #9d9a94; --line: #3a3833; --accent: #7cc796; --wash: #141311; --panel: #1b1a17; --band: #201f1b; } }
* { box-sizing: border-box; }
body { margin: 0; color: var(--ink); background: var(--wash); font: 17px/1.6 Georgia, "Times New Roman", serif; }
.wrap { max-width: 46rem; margin: 0 auto; padding: 1rem 1.1rem 3rem; }
/* A page with a contents rail needs the room for two columns; :has keeps that fact in the
   one place that knows it, instead of a class on every caller of the layout. */
.wrap:has(.doc-grid) { max-width: 78rem; }
header.site { border-bottom: 1px solid var(--line); background: var(--wash); position: sticky; top: 0; z-index: 3; }
header.site .wrap { display: flex; gap: 1rem; align-items: baseline; padding-bottom: .6rem; padding-top: .8rem; max-width: 78rem; }
.brand { font-weight: 700; font-size: 1.15rem; text-decoration: none; color: var(--ink); }
nav.site a { margin-left: .8rem; color: var(--muted); font-size: .9rem; }
a { color: var(--accent); }
h1 { font-size: 1.6rem; line-height: 1.25; margin: .4rem 0 .3rem; overflow-wrap: anywhere; }
h2 { font-size: 1.15rem; margin: 1.4rem 0 .4rem; }
form.search, form.filters { display: flex; flex-wrap: wrap; gap: .45rem; margin: 1rem 0 .5rem; align-items: center; }
label.facet { display: inline-flex; align-items: center; gap: .3rem; font-size: .85rem; color: var(--muted); }
input[type=search] { flex: 1 1 16rem; padding: .55rem .7rem; font: inherit; border: 1px solid var(--line); border-radius: 4px; background: var(--panel); color: inherit; }
input[type=number], select { padding: .45rem .5rem; font: inherit; font-size: .92rem; border: 1px solid var(--line); border-radius: 4px; background: var(--panel); color: inherit; }
button { padding: .5rem .9rem; font: inherit; border: 1px solid var(--accent); background: var(--accent); color: #fff; border-radius: 4px; cursor: pointer; }
.hint, .mirror, .note { color: var(--muted); font-size: .88rem; }
.bad, .warn { color: #a12a2a; }
.bad, .warn { font-size: .9rem; }
.mirror { border-left: 3px solid var(--line); padding-left: .6rem; overflow-wrap: anywhere; }
ul.kinds { list-style: none; display: flex; flex-wrap: wrap; gap: .3rem .9rem; padding: 0; margin: .8rem 0; font-size: .92rem; }
/* The result: one panel per provision, with the citation quiet and the address loud, since
   the address is what the reader clicks and what they quote back. */
.hit { border: 1px solid var(--line); border-radius: 6px; padding: .7rem .9rem; margin: .7rem 0; background: var(--panel); }
.hit .cit { font-size: .85rem; color: var(--muted); overflow-wrap: anywhere; }
.hit a.addr { display: inline-block; margin-top: .15rem; font-size: 1rem; font-weight: 700; color: var(--accent); text-decoration: none; }
.hit a.addr:hover { text-decoration: underline; }
.hit p.snip { margin: .4rem 0 .3rem; font-size: .97rem; }
.hit .meta { font-size: .84rem; }
.prov { display: inline-block; font-size: .76rem; color: var(--muted); border: 1px solid var(--line); border-radius: 999px; padding: 0 .5rem; white-space: nowrap; }
.pill { border: 1px solid var(--line); border-radius: 999px; padding: .05rem .45rem; font-size: .78rem; color: var(--muted); white-space: nowrap; }
.pill.ok { border-color: var(--accent); color: var(--accent); }
.pill.warn { border-color: #b0763a; color: #b0763a; }
.note { font-size: .88rem; color: var(--muted); border-left: 3px solid var(--line); padding-left: .7rem; }
.prov.xml { border-color: var(--accent); color: var(--accent); }
.prov.html, .prov.none { border-color: #a12a2a; color: #a12a2a; }
.meta { font-size: .9rem; color: var(--muted); }
.meta b { color: var(--ink); }
.kv { font-size: .88rem; color: var(--muted); margin: .2rem 0 1rem; }
.kv span { white-space: nowrap; }
/* Contents as a rail that scrolls on its own, so the law stays where the reader left it.
   Without the media query the details element is a plain collapsible on a narrow screen. */
.doc-grid { display: grid; grid-template-columns: minmax(15rem, 19rem) minmax(0, 1fr); gap: 0 2.2rem; align-items: start; }
nav.toc { position: sticky; top: 4.2rem; max-height: calc(100vh - 5rem); overflow: auto; border: 1px solid var(--line); border-radius: 6px; background: var(--panel); padding: .6rem .8rem; font-size: .86rem; font-family: ui-sans-serif, system-ui, sans-serif; }
nav.toc summary { font-weight: 700; cursor: pointer; color: var(--ink); }
nav.toc ol { list-style: none; margin: .35rem 0 0; padding: 0; }
nav.toc li { margin: 0; }
nav.toc a { display: block; padding: .1rem 0; color: var(--ink); text-decoration: none; overflow-wrap: anywhere; }
nav.toc a:hover { color: var(--accent); text-decoration: underline; }
nav.toc .lvl2 { padding-left: .9rem; }
nav.toc .lvl3 { padding-left: 1.8rem; }
nav.toc .count { color: var(--muted); font-weight: 400; }
@media (max-width: 60rem) { .doc-grid { display: block; } nav.toc { position: static; max-height: none; margin-bottom: 1.2rem; } }
article.law { min-width: 0; }
article.law section { margin: .1rem 0; }
article.law h2 { font-size: 1.06rem; margin: 1.3rem 0 .2rem; }
article.law p { margin: .45rem 0; max-width: 78ch; }
article.law blockquote { margin: .3rem 0 .6rem 1rem; padding-left: .8rem; border-left: 3px solid var(--line); color: var(--muted); }
article.law :target { background: color-mix(in srgb, var(--accent) 18%, transparent); scroll-margin-top: 4.5rem; }
aside.related { margin-top: 2.5rem; border-top: 2px solid var(--line); padding-top: .6rem; }
aside.related ul { list-style: none; padding: 0; }
aside.related li { margin: .3rem 0; font-size: .94rem; }
aside.related .unheld { color: var(--muted); }
table.browse { width: 100%; border-collapse: collapse; font-size: .95rem; }
table.browse td { border-top: 1px solid var(--line); padding: .35rem .4rem .35rem 0; vertical-align: top; }
table.browse td.n { white-space: nowrap; color: var(--muted); font-family: ui-monospace, Menlo, monospace; font-size: .85rem; }
table.browse tr:nth-child(even) { background: var(--band); }
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
<h1 id="top">{{.Citation}}</h1>
<p class="kv">{{if .Kind}}<span><b>{{.Kind}}</b></span>{{end}}{{if .Year}} · <span>{{.Year}}</span>{{end}} · <span>{{statusLabel .Status}}</span>{{if .Accession}} · <span>{{.Accession}}</span>{{end}}{{if .ELI}} · <span><a href="{{.ELI}}">Retsinformation</a></span>{{end}}</p>
<p class="kv">Kilde: <span class="prov {{.Provenance}}">{{provenanceLabel .Provenance}}</span>{{if .ProvenanceNote}} <span class="hint">{{provenanceNoteLabel .ProvenanceNote}}</span>{{end}}</p>
<p class="mirror">{{.Mirror}}</p>
{{if .AmendmentsNote}}<p class="note">{{.AmendmentsNote}}</p>{{end}}
{{range .Warnings}}<p class="warn">{{.}}</p>{{end}}
{{if .Addr}}<p class="note">Adressen <b>{{.Addr}}</b> er vist alene.
{{if .Anchor}}Det faste anker er <a href="{{.FullLink}}#{{.Anchor}}">#{{.Anchor}}</a>.{{end}}
<a href="{{.FullLink}}">Se hele dokumentet</a>.</p>{{end}}
{{if .Contents}}<div class="doc-grid">
<nav class="toc" aria-label="Indhold"><details open><summary>Indhold <span class="count">({{len .Contents}})</span></summary>
<ol>{{range .Contents}}<li class="lvl{{.Level}}"><a href="{{.Href}}">{{.Label}}</a></li>{{end}}</ol></details></nav>{{end}}
{{if .Body}}<article class="law">{{.Body}}</article>{{end}}
{{if .PlainText}}<pre class="plain">{{.PlainText}}</pre>{{end}}
{{if .Contents}}</div>{{end}}
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
