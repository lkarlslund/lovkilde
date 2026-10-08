// The page templates. One layout, one content block per page, one stylesheet.
//
// Everything here is server-rendered: no build step, no asset pipeline, and one small inline
// script that only helps (toggles apply themselves, long lists load as the reader scrolls). A page
// is a rendered document and works without the script, so the interface is the document — which
// is also the only way to show Danish law text honestly, since the text itself is the thing
// people came to read.
//
// html/template is what makes the escaping claim in the package comment hold for the parts this
// file writes: a query echoed back, an address, a title from the register. The document body is
// the one value inserted unescaped, and it arrives escaped from lexdoc.
package webserver

import (
	"fmt"
	"html/template"
	"regexp"
	"strconv"
	"strings"

	"github.com/lkarlslund/retsinformation/internal/disclaimer"
)

var templateFuncs = template.FuncMap{
	// The notices every page carries, and the stronger one a consolidated text carries: the
	// mirror is not the official register, and a text it consolidated itself can be wrong.
	"disclaimer":             func() string { return disclaimer.ShortDanish },
	"consolidatedDisclaimer": func() string { return disclaimer.ConsolidatedDanish },
	// provenanceLabel says how trustworthy the text on screen is, in the words the README
	// uses. A page that showed "xml" would be showing a filename instead of a warning.
	"provenanceLabel":     provenanceLabel,
	"provenanceExplained": provenanceExplained,
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
	"kindName": kindName,
	"num":      num,
}

// num writes a count the Danish way, with a point between thousands: 12.345.
func num(n int) string {
	s := strconv.Itoa(n)
	if n < 0 {
		return "-" + num(-n)
	}
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "." + s[i:]
	}
	return s
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
		return "genskabt fra websiden"
	case "none", "":
		return "uden tekst"
	default:
		return source
	}
}

// provenanceExplained is what a source label means, for the reader who stops at it: the
// difference decides whether the text on screen can be quoted as it stands.
func provenanceExplained(source string) string {
	switch strings.ToLower(strings.TrimSpace(source)) {
	case "xml":
		return "Retsinformation leverer selv teksten, opmærket med §, stk. og nr. Den kan citeres, som den står."
	case "html":
		return "Retsinformation leverer kun en webside. Teksten er læst ud af den, og opdelingen i §, stk. og nr. er genskabt herfra — kontrollér mod Retsinformation, før du citerer."
	case "none", "":
		return "Registeret har kun dokumentets oplysninger, ikke dets tekst. Kun titlen kan findes."
	default:
		return ""
	}
}

// refusalReasons are the engine's reasons for leaving a change out, by class, in Danish. The
// engine words its reasons for the agents and logs that read them; a Danish page names the class
// and the place, and anything it has no class for gets a plain Danish sentence rather than the
// engine's English.
var refusalReasons = []struct {
	re *regexp.Regexp
	da string // %s is the place the reason names, when it names one
}{
	{regexp.MustCompile(`^quoted passage not present in (.+)$`), "den citerede tekst står ikke i %s"},
	{regexp.MustCompile(`^passage to delete not present in (.+)$`), "teksten, der skal udgå, står ikke i %s"},
	{regexp.MustCompile(`^the passage the insertion is placed by is not present in (.+)$`), "teksten, indsættelsen placeres efter, står ikke i %s"},
	{regexp.MustCompile(`^address "(.+)" not found in the current text$`), "%s findes ikke i teksten"},
	{regexp.MustCompile(`^no heading found for (.+)$`), "der er ingen overskrift ved %s"},
	{regexp.MustCompile(`^bilag ([0-9]+) does not print`), "bilag %s viser ikke det punkt, ændringen placeres ved"},
	{regexp.MustCompile(`^the change names a part of bilag ([0-9]+)`), "ændringen angår en del af bilag %s, som modellen ikke kan adressere"},
	{regexp.MustCompile(`^the change names no place in bilag ([0-9]+)`), "ændringen angiver ikke, hvor i bilag %s den hører til"},
	{regexp.MustCompile(`table's rows and values|table or list in the amending act's own annex`), "ændringen retter i en tabel celle for celle, og modellen holder tabellen som tekst"},
	{regexp.MustCompile(`^the title has no footnote$`), "titlen har ingen fodnote"},
	{regexp.MustCompile(`^the title already has a footnote`), "titlen har allerede en fodnote, og ændringen tilføjer en"},
	{regexp.MustCompile(`^change wording not recognised`), "ændringens formulering genkendes ikke"},
	{regexp.MustCompile(`^insertion carries no content$|^the new text .* is empty$`), "ændringen bringer ingen ny tekst"},
	{regexp.MustCompile(`^renumbering names no provision`), "omnummereringen nævner ingen bestemmelse"},
	{regexp.MustCompile(`^inserted text addresses`), "indsættelsen angiver intet sted i teksten"},
	{regexp.MustCompile(`names a sentence or a table inside the provision`), "ændringen angår et punktum eller en tabel inde i bestemmelsen, som modellen ikke kan adressere"},
}

// refusalReasonDA is an engine reason as the Danish page states it.
func refusalReasonDA(reason string) string {
	for _, r := range refusalReasons {
		if m := r.re.FindStringSubmatch(reason); m != nil {
			if strings.Contains(r.da, "%s") && len(m) > 1 {
				return fmt.Sprintf(r.da, m[1])
			}
			return r.da
		}
	}
	return "ændringen kunne ikke placeres automatisk i teksten"
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
<title>{{.PageTitle}} · Retsinformation-MCP</title>
<link rel="icon" href="data:image/svg+xml,%3Csvg%20viewBox%3D%220%200%2032%2032%22%20xmlns%3D%22http%3A//www.w3.org/2000/svg%22%3E%3Ccircle%20cx%3D%2216%22%20cy%3D%2216%22%20r%3D%2215.5%22%20fill%3D%22%2314532d%22/%3E%3Ccircle%20cx%3D%2216%22%20cy%3D%2216%22%20r%3D%2212.6%22%20fill%3D%22none%22%20stroke%3D%22%23f6f5f2%22%20stroke-width%3D%22.9%22%20stroke-dasharray%3D%221.6%201.1%22/%3E%3Ctext%20x%3D%2216%22%20y%3D%2222.6%22%20text-anchor%3D%22middle%22%20font-family%3D%22Georgia%2CTimes%20New%20Roman%2Cserif%22%20font-size%3D%2219%22%20font-weight%3D%22700%22%20fill%3D%22%23f6f5f2%22%3E%C2%A7%3C/text%3E%3C/svg%3E">
<style>
:root { color-scheme: light dark; --ink: #1b1b1b; --muted: #66635d; --line: #d8d5cf; --accent: #14532d; --accent-ink: #fff; --wash: #f6f5f2; --panel: #fffefb; --band: #efece6; --amber: #b0763a; --red: #a12a2a;
  --ui: ui-sans-serif, system-ui, -apple-system, "Segoe UI", Roboto, sans-serif; --text: Georgia, "Times New Roman", serif; }
@media (prefers-color-scheme: dark) { :root { --ink: #e7e5e1; --muted: #9d9a94; --line: #3a3833; --accent: #7cc796; --accent-ink: #10220f; --wash: #141311; --panel: #1b1a17; --band: #201f1b; --amber: #d29a5c; --red: #e07a7a; } }
* { box-sizing: border-box; }
/* Two voices: the interface speaks in a plain sans-serif, the law in a book face. A reader can
   tell at a glance which words are the register's and which are this page's. */
body { margin: 0; color: var(--ink); background: var(--wash); font: 16px/1.55 var(--ui); }
.wrap { max-width: 50rem; margin: 0 auto; padding: 1rem 1.1rem 3rem; }
/* A page with a contents rail needs the room for two columns; :has keeps that fact in the
   one place that knows it, instead of a class on every caller of the layout. */
.wrap:has(.doc-grid) { max-width: 78rem; }
a { color: var(--accent); }
h1 { font: 700 1.55rem/1.25 var(--text); margin: .4rem 0 .4rem; overflow-wrap: anywhere; }
h2 { font-size: 1.05rem; margin: 1.6rem 0 .5rem; }
.hint, .note, .mirror { color: var(--muted); font-size: .86rem; }
.warn, .bad { color: var(--red); font-size: .9rem; }
.mirror { border-left: 3px solid var(--line); padding-left: .6rem; overflow-wrap: anywhere; }
.note { border-left: 3px solid var(--line); padding-left: .7rem; }

/* Header: the brand, a search box on every page that has no larger one, and the sections. */
header.site { border-bottom: 1px solid var(--line); background: var(--wash); position: sticky; top: 0; z-index: 3; }
header.site .wrap { display: flex; flex-wrap: wrap; gap: .5rem 1.2rem; align-items: center; padding-top: .6rem; padding-bottom: .6rem; max-width: 78rem; }
/* The mark: a paragraph sign in a seal, the way a law is stamped. */
.brand { display: inline-flex; align-items: center; gap: .5rem; font: 700 1.1rem var(--text); text-decoration: none; color: var(--ink); white-space: nowrap; }
.brand .seal { width: 1.9rem; height: 1.9rem; flex: none; }
.brand .mcp { color: var(--accent); }
header.site form { flex: 1 1 14rem; display: flex; max-width: 30rem; }
header.site form input { flex: 1; }
nav.site { display: flex; gap: 1rem; margin-left: auto; font-size: .9rem; }
nav.site a { color: var(--muted); text-decoration: none; }
nav.site a:hover, nav.site a.on { color: var(--ink); text-decoration: underline; }

/* Controls: one height, one radius, one border everywhere. */
input[type=search], input[type=number], input[type=date], select { font: inherit; font-size: .95rem; padding: .45rem .6rem; border: 1px solid var(--line); border-radius: 6px; background: var(--panel); color: inherit; min-width: 0; }
input[type=number] { width: 6.5rem; }
input:focus-visible, button:focus-visible, .chip input:focus-visible + span { outline: 2px solid var(--accent); outline-offset: 1px; }
button { font: inherit; font-size: .95rem; padding: .45rem .95rem; border: 1px solid var(--accent); background: var(--accent); color: var(--accent-ink); border-radius: 6px; cursor: pointer; }
button.quiet { background: transparent; color: var(--accent); }
.joined { display: flex; }
.joined input { border-radius: 6px 0 0 6px; }
.joined button { border-radius: 0 6px 6px 0; }

/* The search form: the words, then the filters as switches, grouped by what they narrow. */
form.finder .joined { margin: .8rem 0 .6rem; }
form.finder .joined input { flex: 1; font-size: 1.05rem; padding: .6rem .75rem; }
.filters { border: 1px solid var(--line); border-radius: 8px; background: var(--panel); padding: .3rem .9rem .6rem; margin: 0 0 1rem; }
.filters fieldset { border: 0; padding: 0; margin: .5rem 0 0; display: flex; flex-wrap: wrap; gap: .4rem; align-items: center; }
.filters legend { float: left; width: 5.2rem; font-size: .78rem; text-transform: uppercase; letter-spacing: .04em; color: var(--muted); padding-top: .3rem; }
.filters .row { display: flex; flex-wrap: wrap; gap: .4rem; align-items: center; flex: 1; }
.filters .actions { display: flex; gap: .8rem; align-items: center; justify-content: flex-end; margin-top: .6rem; font-size: .88rem; }
.chip { position: relative; display: inline-flex; }
.chip input { position: absolute; opacity: 0; width: 1px; height: 1px; }
.chip span { display: inline-flex; gap: .35rem; align-items: baseline; border: 1px solid var(--line); border-radius: 999px; padding: .22rem .7rem; font-size: .86rem; cursor: pointer; background: var(--wash); user-select: none; }
.chip span small { color: var(--muted); font-size: .75rem; font-variant-numeric: tabular-nums; }
.chip span:hover { border-color: var(--accent); }
.chip input:checked + span { background: var(--accent); border-color: var(--accent); color: var(--accent-ink); }
.chip input:checked + span small { color: inherit; opacity: .8; }
.chip input:checked + span::before { content: "✓"; font-size: .8rem; }
.within { display: flex; gap: .5rem; align-items: center; flex-wrap: wrap; font-size: .9rem; margin: 0 0 .6rem; padding: .4rem .7rem; border-radius: 6px; background: var(--band); }

.cited { border: 1px solid var(--accent); border-radius: 8px; background: var(--panel); padding: .5rem .9rem; margin: 0 0 1rem; font-size: .92rem; }
.cited ul { margin: .3rem 0 0; padding-left: 1.1rem; }
.cited li { margin: .2rem 0; }
/* The count line above a list: the total is the fact, the rest is how it was asked. */
.resbar { display: flex; flex-wrap: wrap; gap: .3rem 1rem; align-items: baseline; margin: .4rem 0 .6rem; }
.resbar .count { font-size: 1.05rem; font-weight: 700; }
.resbar .hint { flex: 1 1 20rem; }

/* A hit: the address loud, since it is what the reader clicks and quotes back; the law it is
   in beside it; the passage in the law's own voice. */
.hit { border: 1px solid var(--line); border-radius: 8px; padding: .7rem .95rem; margin: .6rem 0; background: var(--panel); }
.hit .head { display: flex; flex-wrap: wrap; gap: .2rem .6rem; align-items: baseline; }
.hit .n { color: var(--muted); font-size: .8rem; font-variant-numeric: tabular-nums; min-width: 1.6rem; }
.hit a.addr { font-weight: 700; color: var(--accent); text-decoration: none; }
.hit a.addr:hover { text-decoration: underline; }
.hit .cit { font-size: .88rem; overflow-wrap: anywhere; }
.hit .cit a { color: var(--ink); }
.hit p.snip { margin: .4rem 0 .35rem; font: .98rem/1.55 var(--text); }
.hit .meta { display: flex; flex-wrap: wrap; gap: .2rem .8rem; font-size: .8rem; color: var(--muted); }
.prov, .pill { display: inline-block; font-size: .74rem; color: var(--muted); border: 1px solid var(--line); border-radius: 999px; padding: 0 .5rem; white-space: nowrap; }
.prov.xml, .pill.ok { border-color: var(--accent); color: var(--accent); }
.prov.html, .prov.none { border-color: var(--red); color: var(--red); }
.pill.warn { border-color: var(--amber); color: var(--amber); }
a.more { display: block; text-align: center; margin: 1rem 0; padding: .6rem; border: 1px dashed var(--line); border-radius: 8px; text-decoration: none; font-size: .9rem; }
.end { text-align: center; color: var(--muted); font-size: .85rem; margin: 1.2rem 0; }

/* Lists of documents, one line each. */
.rows { margin: .4rem 0; }
.rows .r { display: grid; grid-template-columns: 6.5rem minmax(0, 1fr); gap: .8rem; padding: .45rem .3rem; border-top: 1px solid var(--line); }
.rows .r:nth-child(even) { background: var(--band); }
.rows .r .n { color: var(--muted); font-size: .82rem; font-variant-numeric: tabular-nums; white-space: nowrap; padding-top: .1rem; }
.rows .r a { color: var(--ink); text-decoration-color: var(--line); }
.rows .r a:hover { color: var(--accent); }
.rows .r .meta { display: block; font-size: .8rem; color: var(--muted); margin-top: .1rem; }

/* The front page: the box first, then ways in. */
.hero { margin: 1.4rem 0 .4rem; }
.hero h1 { font-size: 1.9rem; }
.cards { display: grid; grid-template-columns: repeat(auto-fill, minmax(14rem, 1fr)); gap: .6rem; margin: .6rem 0; padding: 0; list-style: none; }
.cards a { display: block; height: 100%; border: 1px solid var(--line); border-radius: 8px; background: var(--panel); padding: .6rem .8rem; text-decoration: none; color: var(--ink); }
.cards a:hover { border-color: var(--accent); }
.cards b { display: block; }
.cards small { color: var(--muted); }
.examples { display: flex; flex-wrap: wrap; gap: .4rem; font-size: .9rem; }
.examples a { border: 1px solid var(--line); border-radius: 999px; padding: .15rem .7rem; text-decoration: none; background: var(--panel); }
.examples code { font-size: .85rem; }

/* The document page: its facts in one line, its tools in one bar. */
.kv { display: flex; flex-wrap: wrap; gap: .25rem .5rem; align-items: center; font-size: .86rem; color: var(--muted); margin: .2rem 0 .6rem; }
.kv b { color: var(--ink); }
.toolbar { display: flex; flex-wrap: wrap; gap: .6rem 1.2rem; align-items: center; border: 1px solid var(--line); border-radius: 8px; background: var(--panel); padding: .5rem .7rem; margin: .6rem 0 1rem; font-size: .9rem; }
.toolbar form { display: flex; gap: .4rem; align-items: center; flex-wrap: wrap; }
.toolbar form.joined { flex: 1 1 16rem; flex-wrap: nowrap; gap: 0; }
.toolbar form.joined input { flex: 1; }
.toolbar label { color: var(--muted); }

/* Contents as a rail that scrolls on its own, so the law stays where the reader left it.
   Without the media query the details element is a plain collapsible on a narrow screen. */
.doc-grid { display: grid; grid-template-columns: minmax(15rem, 19rem) minmax(0, 1fr); gap: 0 2.2rem; align-items: start; }
nav.toc { position: sticky; top: 4.2rem; max-height: calc(100vh - 5rem); overflow: auto; border: 1px solid var(--line); border-radius: 8px; background: var(--panel); padding: .6rem .8rem; font-size: .84rem; }
nav.toc summary { font-weight: 700; cursor: pointer; color: var(--ink); }
nav.toc ol { list-style: none; margin: .35rem 0 0; padding: 0; }
nav.toc li { margin: 0; }
nav.toc a { display: block; padding: .1rem 0; color: var(--ink); text-decoration: none; overflow-wrap: anywhere; }
nav.toc a:hover { color: var(--accent); text-decoration: underline; }
nav.toc .lvl2 { padding-left: .9rem; }
nav.toc .lvl3 { padding-left: 1.8rem; }
nav.toc .count { color: var(--muted); font-weight: 400; }
@media (max-width: 60rem) { .doc-grid { display: block; } nav.toc { position: static; max-height: none; margin-bottom: 1.2rem; } }
article.law { min-width: 0; font: 17px/1.6 var(--text); }
article.law h2 { font-size: 1.06rem; margin: 1.3rem 0 .2rem; }
article.law p { margin: .45rem 0; max-width: 78ch; }
article.law blockquote { margin: .3rem 0 .6rem 1rem; padding-left: .8rem; border-left: 3px solid var(--line); color: var(--muted); }
article.law section { margin: 1.1rem 0; }
article.law h3 { font-size: 1rem; font-style: italic; font-weight: 600; margin: 1.4rem 0 .3rem; }
article.law p.stk { margin: .5rem 0; padding-left: 4.6rem; text-indent: -4.6rem; }
article.law p.cont { margin: .3rem 0 .3rem 4.6rem; }
article.law p.item { margin: .2rem 0 .2rem 4.6rem; padding-left: 2.2rem; text-indent: -2.2rem; }
article.law p.item.d2 { margin-left: 6.8rem; }
article.law p.item.d3 { margin-left: 9rem; }
article.law .lbl { font-variant-numeric: tabular-nums; display: inline-block; text-indent: 0; }
article.law p.stk .lbl { min-width: 4.6rem; margin-right: -.28em; }
article.law .lbl.par { font-weight: 700; }
article.law .lbl.stk { font-style: italic; }
article.law p.item .lbl.nr { min-width: 2.2rem; margin-right: -.28em; }
details.refused { border: 1px solid var(--line); border-left: 4px solid var(--amber); border-radius: 0 8px 8px 0; padding: .4rem .9rem; margin: 1rem 0; background: var(--band); }
details.refused summary { font-weight: 700; cursor: pointer; }
details.refused li { margin: .7rem 0; }
details.refused blockquote { margin: .3rem 0 0 0; font: .95rem/1.5 var(--text); }
article.law p.refused-mark { border-left: 4px solid var(--amber); padding: .2rem .6rem; font: .88rem/1.5 var(--ui); background: var(--band); }
article.law :target { background: color-mix(in srgb, var(--accent) 18%, transparent); scroll-margin-top: 4.5rem; }
aside.related { margin-top: 2.5rem; border-top: 2px solid var(--line); padding-top: .6rem; }
aside.related ul { list-style: none; padding: 0; }
aside.related li { margin: .3rem 0; font-size: .92rem; }
aside.related .unheld { color: var(--muted); }
pre.plain { white-space: pre-wrap; word-break: break-word; font-size: .9rem; }
footer { color: var(--muted); font-size: .8rem; border-top: 1px solid var(--line); margin-top: 2.5rem; }
.disclaimer { border: 2px solid var(--amber); border-radius: 8px; background: var(--band); padding: .6rem .9rem; margin: 1rem 0; font: .9rem/1.5 var(--ui); }
.disclaimer b { color: var(--red); }
.disclaimer.consolidated { border-color: var(--red); }
@media (max-width: 36rem) { .filters legend { float: none; width: auto; } </style>
</head>
<body>
<header class="site"><div class="wrap">
<a class="brand" href="/"><svg class="seal" aria-hidden="true" viewBox="0 0 32 32" xmlns="http://www.w3.org/2000/svg"><circle cx="16" cy="16" r="15.5" fill="var(--accent)"/><circle cx="16" cy="16" r="12.6" fill="none" stroke="var(--wash)" stroke-width=".9" stroke-dasharray="1.6 1.1"/><text x="16" y="22.6" text-anchor="middle" font-family="Georgia,Times New Roman,serif" font-size="19" font-weight="700" fill="var(--wash)">§</text></svg><span>Retsinformation<span class="mcp">-MCP</span></span></a>
{{block "headsearch" .}}<form class="joined" method="get" action="/search" role="search"><input type="search" name="q" placeholder="Søg i lovgivningen" aria-label="Søg" autocomplete="off"><button type="submit">Søg</button></form>{{end}}
<nav class="site"><a href="/search">Søg</a><a href="/browse">Gennemse</a><a href="/status">Status</a></nav>
</div></header>
<main class="wrap"><p class="disclaimer" role="note"><b>Ansvarsfraskrivelse:</b> {{disclaimer}}</p>
{{template "content" .}}</main>
<footer><div class="wrap">Uofficiel kopi af Retsinformation, uden tilknytning til Civilstyrelsen. Teksten er læst fra
retsinformation.dk; kilder og usikkerhed står ved hvert svar. Leveres som den er, uden garanti og uden ansvar
for fejl eller tab. Ikke juridisk rådgivning. Samme svar til sprogmodeller på <a href="/mcp">/mcp</a>.</div></footer>
<script>
// Progressive help only: every page works without this. It does four things — a long contents
// list folds on a phone, a ticked toggle applies itself, an empty field stays out of the
// address, and the next screenful of a list arrives when the reader scrolls near its end (the
// "Vis flere" link is the fallback).
(function () {
  document.querySelectorAll("form[data-auto]").forEach(function (f) {
    f.addEventListener("change", function (e) {
      if (e.target.matches("input[type=checkbox], input[type=radio]")) f.requestSubmit ? f.requestSubmit() : f.submit();
    });
  });
  document.querySelectorAll("form").forEach(function (f) {
    f.addEventListener("submit", function () {
      f.querySelectorAll("input").forEach(function (i) { if (i.name && i.value === "" && i.type !== "checkbox" && i.type !== "radio") i.disabled = true; });
    });
  });
  // On a narrow screen the contents list stands above the law, so it starts folded there.
  if (window.matchMedia && matchMedia("(max-width: 60rem)").matches) document.querySelectorAll("nav.toc details").forEach(function (d) { d.open = false; });
  if (!("IntersectionObserver" in window) || !window.fetch) return;
  function hook() {
    var a = document.querySelector("a[data-more]");
    if (!a) return;
    var io = new IntersectionObserver(function (es) {
      if (!es[0].isIntersecting) return;
      io.disconnect();
      a.textContent = "Henter flere …";
      fetch(a.href + "&del=1").then(function (r) {
        if (!r.ok) throw new Error(r.status);
        return r.text();
      }).then(function (html) {
        var t = document.createElement("template");
        t.innerHTML = html;
        a.replaceWith(t.content);
        hook();
      }).catch(function () { a.textContent = "Vis flere"; });
    }, { rootMargin: "800px 0px" });
    io.observe(a);
  }
  hook();
})();
</script>
</body>
</html>`

const indexBody = `{{define "headsearch"}}{{/* the page has its own, larger box */}}{{""}}{{end}}{{define "content"}}
<div class="hero">
<h1 id="top">Søg i dansk lovgivning</h1>
<form class="finder" method="get" action="/search" role="search">
<div class="joined"><input type="search" name="q" placeholder="F.eks. forældremyndighed, råderet, opsigelse af lejemål" aria-label="Søg" autofocus autocomplete="off"><button type="submit">Søg</button></div>
</form>
<p class="hint">Søgningen finder bestemmelser, ikke kun dokumenter. <b>a AND b</b> kræver begge ord,
<b>"flere ord"</b> kræver dem samlet, og <b>-ord</b> udelukker.</p>
<div class="examples">Prøv: {{range .Queries}}<a href="/search?q={{.Query}}" title="{{.Label}}"><code>{{.Query}}</code></a>{{end}}</div>
</div>
{{if .SearchPending}}<p class="note">Søgeindekset bygges fra den kompilerede fil og er klar om et minut
eller to. Dokumenter kan allerede læses og gennemses.</p>{{else if not .Search}}<p class="warn">Søgeindeks mangler — siden
kan vise dokumenter og læse bestemmelser, men ikke søge i dem. Serveren bygger selv indekset fra den
kompilerede fil: byg den med <b>retsin-update artifact build</b>, og genstart.</p>{{end}}
<h2>Gennemse efter type</h2>
<ul class="cards">{{range .Kinds}}<li><a href="{{kindLink .Value}}"><b>{{.Label}}</b><small>{{if .Hint}}{{.Hint}} · {{end}}{{num .Count}} dokumenter</small></a></li>{{end}}</ul>
<p class="hint">{{num .Total}} dokumenter i alt. <a href="/browse">Alle dokumenter</a> kan filtreres på titel, år og status.</p>
<p class="mirror">{{.Status}}</p>
{{end}}`

const searchBody = `{{define "headsearch"}}{{/* the page has its own, larger box */}}{{""}}{{end}}{{define "content"}}
<form class="finder" method="get" action="/search" role="search" data-auto>
<div class="joined"><input type="search" name="q" value="{{.Query}}" placeholder="{{if .Within}}Søg i dette dokument{{else}}Søg i bestemmelsers tekst{{end}}" aria-label="Søg" autocomplete="off" {{if not .Query}}autofocus{{end}}><button type="submit">Søg</button></div>
{{if .Within}}<div class="within"><input type="hidden" name="in" value="{{.Within}}">Søger kun i <b>{{.WithinTitle}}</b> <a href="{{.AllLink}}">søg i alt</a></div>{{end}}
<div class="filters">
<fieldset><legend>Type</legend><div class="row">{{range .Kinds}}<label class="chip" title="{{.Hint}}"><input type="checkbox" name="kind" value="{{.Value}}" {{if .On}}checked{{end}}><span>{{.Label}} <small>{{num .Count}}</small></span></label>{{end}}</div></fieldset>
<fieldset><legend>Status</legend><div class="row">{{range .Statuses}}<label class="chip"><input type="checkbox" name="status" value="{{.Value}}" {{if .On}}checked{{end}}><span>{{.Label}} <small>{{num .Count}}</small></span></label>{{end}}</div></fieldset>
<fieldset><legend>Periode</legend><div class="row"><input type="number" name="since" value="{{.Since}}" placeholder="fra år" min="1800" max="2100" aria-label="fra år"> – <input type="number" name="until" value="{{.Until}}" placeholder="til år" min="1800" max="2100" aria-label="til år"></div></fieldset>
<fieldset><legend>Kilde</legend><div class="row">{{range .Sources}}<label class="chip" title="{{.Hint}}"><input type="checkbox" name="kilde" value="{{.Value}}" {{if .On}}checked{{end}}><span>{{.Label}} <small>{{num .Count}}</small></span></label>{{end}}</div></fieldset>
<div class="actions">{{if .Filtered}}<a href="/search?q={{.Query}}">Nulstil filtre</a>{{end}}<button type="submit" class="quiet">Anvend</button></div>
</div>
</form>
{{range .Warn}}<p class="warn">{{.}}</p>{{end}}
{{if .NoIndex}}<p class="hint">Uden indeks kan du stadig læse og gennemse:
<a href="/browse">alle dokumenter</a>.</p>{{end}}
{{if .HasQuery}}{{if not .NoIndex}}
{{if .Cited}}<div class="cited"><b>Dokumenter med det nummer</b><ul>{{range .Cited}}<li><a href="{{.Link}}">{{.Title}}</a> <span class="hint">{{.Label}}</span> <span class="pill {{statusClass .Status}}">{{statusShort .Status}}</span></li>{{end}}</ul></div>{{end}}
<div class="resbar"><span class="count">{{num .Total}} {{if eq .Total 1}}bestemmelse{{else}}bestemmelser{{end}}</span>
<span class="hint">{{if .Hits}}bedste først{{if .More}} · flere hentes, når du ruller ned{{end}}{{end}}</span></div>
{{if not .Hits}}<p>Intet fandtes. Ret stavemåden, slå filtre fra, eller slap kravene: <b>AND</b> og
anførselstegn kræver, at ordene står sammen.</p>{{end}}
<div id="hits">{{template "hits" .}}</div>
<p class="mirror">{{.Mirror}}</p>
{{end}}{{else}}<p class="hint">Skriv ord i feltet, og vælg eventuelt, hvilke typer der skal søges i.</p>{{end}}
{{end}}
{{define "hits"}}{{range .Hits}}
<article class="hit">
<div class="head"><span class="n">{{.N}}.</span><a class="addr" href="{{.Link}}">{{if .Address}}{{.Address}}{{else}}hele dokumentet{{end}}</a><span class="cit"><a href="{{.DocLink}}">{{.Title}}</a></span></div>
<p class="snip">{{.Snippet}}</p>
<div class="meta"><span class="prov {{.Provenance}}" title="{{provenanceExplained .Provenance}}">{{provenanceLabel .Provenance}}</span>{{with coverageText .Covered .Requested}}<span>{{.}}</span>{{end}}{{if .Gap}}<span>{{.Gap}}</span>{{end}}{{if .ELI}}<a href="{{.ELI}}">Retsinformation ↗</a>{{end}}</div>
</article>{{end}}
{{if .More}}<a class="more" href="{{.More}}" data-more>Vis flere</a>{{else if gt .Total 0}}<p class="end">Det var alle {{num .Total}}.</p>{{end}}
{{end}}`

const documentBody = `{{define "content"}}
<h1 id="top">{{.Citation}}</h1>
<div class="kv">{{if .Kind}}<b>{{kindName .Kind}}</b>{{end}}{{if .Year}}<span>· {{.Year}}</span>{{end}}<span class="pill {{statusClass .Status}}">{{statusLabel .Status}}</span><span class="prov {{.Provenance}}" title="{{provenanceExplained .Provenance}}">{{provenanceLabel .Provenance}}</span>{{if .Accession}}<span>{{.Accession}}</span>{{end}}{{if .ProvenanceNote}}<span>{{provenanceNoteLabel .ProvenanceNote}}</span>{{end}}</div>
<div class="toolbar">
{{if .Accession}}<form class="joined" method="get" action="/search" role="search"><input type="hidden" name="in" value="{{.Accession}}"><input type="search" name="q" placeholder="Søg i dette dokument" aria-label="Søg i dette dokument" autocomplete="off"><button type="submit">Søg</button></form>{{end}}
{{if .DateTool}}<form method="get" action="/dok"><input type="hidden" name="ref" value="{{.Ref}}">{{if .Addr}}<input type="hidden" name="addr" value="{{.Addr}}">{{end}}<label for="asof">Teksten pr.</label><input id="asof" type="date" name="as_of" value="{{.AsOf}}"><button type="submit" class="quiet">Vis</button>{{if .AsOf}} <a href="{{.OriginalLink}}">som udstedt</a>{{end}}</form>{{end}}
{{if .ELI}}<a href="{{.ELI}}">Retsinformation ↗</a>{{end}}
</div>
{{if .AsOf}}<p class="disclaimer consolidated" role="note"><b>Advarsel:</b> {{consolidatedDisclaimer}}</p>{{end}}
{{if .AmendmentsNote}}<p class="note">{{.AmendmentsNote}}{{if .ConsolidateLink}} <a href="{{.ConsolidateLink}}">Se teksten, som den gælder {{.ConsolidateOn}}</a>.{{end}}</p>{{end}}
{{range .Warnings}}<p class="warn">{{.}}</p>{{end}}
{{if .Refused}}<details class="refused" open><summary>Ændringer, der ikke er indarbejdet ({{len .Refused}})</summary>
<p class="hint">Teksten nedenfor er loven pr. {{.AsOf}} med alle andre ændringer indarbejdet. Disse kunne ikke placeres, og hver er gengivet, som ændringsloven skriver den.</p>
<ol>{{range .Refused}}<li id="{{.ID}}"><b>{{if .ActLink}}<a href="{{.ActLink}}">{{.Act}}</a>{{else}}{{.Act}}{{end}}</b>, nr. {{.Number}}{{if .Target}} → {{.Target}}{{end}}{{if .Concerns}} — <b>angår den viste bestemmelse</b>{{end}}
<br><span class="hint">{{if .InForce}}I kraft {{.InForce}} · {{end}}Årsag: {{.Reason}}</span>
<blockquote>{{.Wording}}</blockquote></li>{{end}}</ol></details>{{end}}
{{if .Addr}}<p class="note">Kun <b>{{.Addr}}</b> er vist.
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
<p class="hint">“Ikke i dette spejl” betyder, at loven nævnes andre steder, men at dens egen
tekst ikke er hentet endnu. Spejlet er en cache, ikke hele registeret.</p>
</aside>{{end}}
<p class="mirror">{{.Mirror}}</p>
{{end}}`

const browseBody = `{{define "content"}}
<h1 id="top">{{.PageTitle}}</h1>
<form class="finder" method="get" action="/browse" data-auto>
<div class="joined"><input type="search" name="title" value="{{.Title}}" placeholder="Søg i titler, f.eks. forældrepension" aria-label="Titel" autocomplete="off"><button type="submit">Vis</button></div>
<div class="filters">
<fieldset><legend>Type</legend><div class="row"><label class="chip"><input type="radio" name="kind" value="" {{if not .Kind}}checked{{end}}><span>alle</span></label>{{range .Kinds}}<label class="chip" title="{{.Hint}}"><input type="radio" name="kind" value="{{.Value}}" {{if .On}}checked{{end}}><span>{{.Label}} <small>{{num .Count}}</small></span></label>{{end}}</div></fieldset>
<fieldset><legend>Status</legend><div class="row"><label class="chip"><input type="radio" name="status" value="" {{if not .Status}}checked{{end}}><span>alle</span></label>{{range .Statuses}}<label class="chip"><input type="radio" name="status" value="{{.Value}}" {{if .On}}checked{{end}}><span>{{.Label}} <small>{{num .Count}}</small></span></label>{{end}}</div></fieldset>
<fieldset><legend>År og kode</legend><div class="row"><input type="number" name="year" value="{{.Year}}" placeholder="år" min="1800" max="2100" aria-label="år"><input type="search" name="type" value="{{.TypeCode}}" placeholder="typekode, f.eks. LOKDOK" aria-label="typekode" autocomplete="off"></div></fieldset>
<div class="actions">{{if or .Kind .Status .Year .Title .TypeCode}}<a href="/browse">Nulstil filtre</a>{{end}}<button type="submit" class="quiet">Anvend</button></div>
</div>
</form>
<div class="resbar"><span class="count">{{num .Total}} {{if eq .Total 1}}dokument{{else}}dokumenter{{end}}</span><span class="hint">nyeste først{{if .More}} · flere hentes, når du ruller ned{{end}}</span></div>
{{if not .Items}}<p>Ingen dokumenter matcher. Prøv uden årstal, eller slå et filter fra.</p>{{end}}
<div class="rows">{{template "rows" .}}</div>
{{end}}
{{define "rows"}}{{range .Items}}<div class="r"><span class="n">{{.Year}} nr. {{.Number}}</span><div><a href="{{.Link}}">{{.Title}}</a><span class="meta">{{kindName .Kind}}{{if .Type}} · {{.Type}}{{end}} · <span class="pill {{statusClass .Status}}">{{statusShort .Status}}</span></span></div></div>
{{end}}{{if .More}}<a class="more" href="{{.More}}" data-more>Vis flere</a>{{else if gt .Total 0}}<p class="end">Det var alle {{num .Total}}.</p>{{end}}{{end}}`
