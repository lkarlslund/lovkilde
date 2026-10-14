package lexdania

import (
	"strings"
	"testing"
)

// An annex that prints its provisions as text keeps that text once, in the annex, with its own
// label out of it, its bold and italic, and its list entries: the page of a 7-annex circular had
// printed "Bilag 2" twice and then the whole agreement again as content with no concept.
func TestAnnexProseIsKeptOnceWithItsLayout(t *testing.T) {
	doc := mustParse(t, `<Dokument><Meta><AccessionNumber>C20260000001</AccessionNumber></Meta>
<Bilag localId="2"><Explicatus>Bilag 2</Explicatus><Rubrica><Linea><Char formaChar="Bold">Protokollat</Char></Linea></Rubrica>
<BilagIndhold><TekstGruppe><Rubrica><Linea><Char>-</Char></Linea></Rubrica>
<Exitus><Linea><Char formaChar="Bold">§ 1.</Char></Linea><Linea><Char formaChar="Bold">Område</Char></Linea></Exitus>
<Exitus><Linea><Char formaChar="Italic">Stk. 2</Char><Char>.</Char></Linea><Linea><Char>Omfatter ikke:</Char></Linea></Exitus>
<Exitus><Index><Indentatio formaInd="Nummer"><Explicatus>1)</Explicatus><Exitus><Linea><Char>tjenestemænd</Char></Linea></Exitus></Indentatio></Index></Exitus>
</TekstGruppe></BilagIndhold></Bilag></Dokument>`)
	c := doc.Model().Content
	if len(c.Appendices) != 1 || len(c.Unmapped) > 1 {
		t.Fatalf("%d annexes and %d pieces of other text, want the annex alone", len(c.Appendices), len(c.Unmapped))
	}
	ap := c.Appendices[0]
	if ap.Label != "Bilag 2" || ap.Heading != "Protokollat" {
		t.Errorf("annex %q headed %q", ap.Label, ap.Heading)
	}
	var marker, bold, italic bool
	for _, sg := range ap.Segments {
		if strings.Contains(sg.Text, "Bilag 2") {
			t.Error("the annex's label is printed again as its first line")
		}
		marker = marker || sg.Marker && sg.Text == "1)" && sg.Indent == 1
		bold = bold || sg.Text == "Område" && sg.Style == "b"
		italic = italic || sg.Text == "Stk. 2" && sg.Style == "i"
	}
	if !marker || !bold || !italic {
		t.Errorf("layout lost: list marker %v, bold %v, italic %v\n%+v", marker, bold, italic, ap.Segments)
	}
	html := c.HTML()
	for _, want := range []string{
		`<p class="annex-title">Protokollat</p>`,
		`<p class="prose-head">§ 1. Område</p>`,
		`<p><em>Stk. 2</em>. Omfatter ikke:</p>`,
		`<p class="pitem d1"><span class="lbl">1)</span>tjenestemænd</p>`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("the page lacks %s:\n%s", want, html)
		}
	}
}

// A ruling has no provisions: its text is one piece kept with its paragraphs, not a piece per run
// of words, and it prints as the document's text rather than under a heading about the model.
func TestARulingsTextIsOnePieceThatPrintsAsText(t *testing.T) {
	doc := mustParse(t, `<Dokument><Meta><AccessionNumber>W20260000001</AccessionNumber></Meta><DokumentIndhold>
<TekstGruppe><Exitus><Linea><Char>Klageren klagede.</Char></Linea></Exitus>
<Exitus><Linea><Char formaChar="Bold">1 Sagsfremstilling</Char></Linea></Exitus>
<Exitus><Linea><Char>Mediet bragte en artikel.</Char></Linea></Exitus></TekstGruppe></DokumentIndhold></Dokument>`)
	c := doc.Model().Content
	pieces := 0
	for _, u := range c.Unmapped {
		if u.Words() != "" {
			pieces++
		}
	}
	if pieces != 1 {
		t.Errorf("the ruling's text is %d pieces, want 1: %+v", pieces, c.Unmapped)
	}
	md := c.Markdown()
	if !strings.Contains(md, "Klageren klagede.\n\n#### 1 Sagsfremstilling\n\nMediet bragte en artikel.") || strings.Contains(md, "concept") {
		t.Errorf("the ruling reads:\n%s", md)
	}
}
