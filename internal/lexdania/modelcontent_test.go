package lexdania

import (
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/lkarlslund/lovkilde/internal/lexdoc"
)

// The parity gate for the provision structure. The model is allowed to differ from the old
// flat section list in exactly two ways: where it holds more structure (named provision
// groups, which the flat list dropped) and where the difference is named and counted here.
// Everything else has to agree on real documents, key for key and character for character,
// because this is the point where the reader's derived semantics move into the model and a
// silent disagreement would be a consolidation bug nobody sees until a law is wrong.
//
// Both sides are ours, so this guards the move and cannot see a defect they
// share; the note on TestModelAddressParityOnCorpus names where external truth
// lives.

// unitKeys names the kinds both representations address. A named provision group is not one
// of them: the model keeps it, the flat list never had it, and the test asserts that the
// model's superset is exactly those parts rather than pretending they are equal.
// secKey names one addressable unit in the vocabulary both representations can produce.
// A stykke is named through its provision and an item through its stykke, which is what a
// Danish citation looks like ("§ 4, stk. 2, nr. 3") and what makes a key from one
// representation comparable to a key from the other.
func secKey(s *Section) string {
	switch s.Kind {
	case "paragraf":
		return "p:" + s.Number + s.Litra
	case "stk":
		if s.Parent == nil {
			return ""
		}
		return secKey(s.Parent) + fmt.Sprintf(",stk%d", s.Stk)
	case "nr":
		if s.Parent == nil {
			return ""
		}
		return secKey(s.Parent) + fmt.Sprintf(",nr%d+%s", s.Nr, s.Litra)
	case "kapitel":
		// The model numbers a chapter with its letter ("7a") and, lacking a localId, by what it
		// prints; the flat list keeps the digits of the localId. Compared on the model's rule,
		// since that is the one addresses resolve against.
		n := lexdoc.NumberWithLitra(s.Node.Attrs["localId"])
		if n == "" {
			n = lexdoc.NumberWithLitra(s.RawNumber)
		}
		return "kapitel:" + n
	case "afsnit":
		return "afsnit:" + s.Number
	case "indledning":
		return "indledning"
	case "titel":
		return "titel"
	case "bilag":
		return "bilag:" + lexdoc.FirstNumber(s.Number+s.RawNumber)
	}
	return ""
}

func sectionKeys(secs []*Section) (order []string, texts map[string][]string, force []string) {
	texts = map[string][]string{}
	for _, s := range secs {
		key := secKey(s)
		if key == "" {
			continue
		}
		if appendixChain(s) {
			// Entry-into-force material as a family. The flat list marks only the provision
			// itself, so its stykker leaked into the addressable list as keys nothing could
			// resolve — Find reaches a stykke through its provision, which is excluded. The
			// model keeps the family together, which is the difference being asserted here.
			if s.Kind == "paragraf" {
				force = append(force, "kraft:"+s.Number+s.Litra)
			}
			continue
		}
		// The preamble and the title are fields in the model rather than positions in the
		// reading order, so their place in the flat list is not comparable. They are still
		// compared, by presence and by text, below.
		if key == "indledning" || key == "titel" {
			texts[key] = append(texts[key], DisplayText(s.Node))
			continue
		}
		order = append(order, key)
		texts[key] = append(texts[key], DisplayText(s.Node))
	}
	return order, texts, force
}

// appendixChain reports whether a section belongs to entry-into-force material, itself or
// through an ancestor. The flat list flags only the provision, and that asymmetry is exactly
// what the model's separate series removes.
func appendixChain(s *Section) bool {
	for cur := s; cur != nil; cur = cur.Parent {
		if cur.Appendix {
			return true
		}
	}
	return false
}

// contentKeys produces the same vocabulary from the model.
func contentKeys(c *lexdoc.Content) (order []string, texts map[string][]string, force []string, groups int) {
	texts = map[string][]string{}
	var walk func(nodes []lexdoc.Node)
	walk = func(nodes []lexdoc.Node) {
		for _, n := range nodes {
			switch {
			case n.Part != nil:
				switch n.Part.Kind {
				case lexdoc.PartChapter:
					key := "kapitel:" + n.Part.Number
					order = append(order, key)
					texts[key] = append(texts[key], n.Part.Heading)
				case lexdoc.PartAfsnit:
					key := "afsnit:" + n.Part.Number
					order = append(order, key)
					texts[key] = append(texts[key], n.Part.Heading)
				default:
					groups++
				}
				walk(n.Part.Order)
			case n.Provision != nil:
				addProvision(&order, texts, n.Provision)
			}
		}
	}
	walk(c.Order)
	for i := range c.Appendices {
		ap := &c.Appendices[i]
		key := "bilag:" + ap.Number
		order = append(order, key)
		texts[key] = append(texts[key], ap.Heading)
		walk(ap.Order)
	}
	if len(c.Preamble) > 0 {
		texts["indledning"] = append(texts["indledning"], lexdoc.SegmentsText(c.Preamble))
	}
	if len(c.Title) > 0 {
		texts["titel"] = append(texts["titel"], lexdoc.SegmentsText(c.Title))
	}
	for i := range c.EntryIntoForce {
		p := &c.EntryIntoForce[i]
		force = append(force, "kraft:"+p.Address())
	}
	return order, texts, force, groups
}

// addProvision appends a provision and its subsections in the flat list's own order: the
// subsections before their parent, because that is how the section list emits them.
func addProvision(order *[]string, texts map[string][]string, p *lexdoc.Provision) {
	key := "p:" + p.Address()
	for i := range p.Stykker {
		st := &p.Stykker[i]
		skey := fmt.Sprintf("%s,stk%d", key, st.Nr)
		for _, it := range st.Items {
			ikey := fmt.Sprintf("%s,nr%d+%s", skey, it.Nr, it.Litra)
			*order = append(*order, ikey)
			texts[ikey] = append(texts[ikey], p.SpanText(it.Span))
		}
		*order = append(*order, skey)
		texts[skey] = append(texts[skey], p.SpanText(st.Span))
	}
	*order = append(*order, key)
	texts[key] = append(texts[key], p.Text())
}

// comparableText reports whether a key's text means the same thing in both representations.
func comparableText(key string) bool {
	return strings.HasPrefix(key, "p:") || key == "indledning" || key == "titel"
}

// TestModelContentParityOnCorpus compares the two representations on real documents of every
// kind: the same units in the same order, and the same text behind each one.
func TestModelContentParityOnCorpus(t *testing.T) {
	dir := corpusDir(t)
	files := sampleFiles(t, dir, 400)

	var parsed, provisions, stykker, items, groups, unmappedDocs int
	var unmappedWords = map[string]int{}
	derived := 0
	for _, f := range files {
		doc, err := Load(f)
		if err != nil {
			t.Logf("parse failed (the crawl may be mid-write): %v", err)
			continue
		}
		parsed++
		m := doc.Model()
		oldOrder, oldTexts, oldForce := sectionKeys(doc.Sections())
		newOrder, newTexts, newForce, newGroups := contentKeys(&m.Content)
		groups += newGroups

		// Every rubric the document prints must survive somewhere in the model: a division
		// heading, a provision's own rubric, or text inside a provision. Losing them is what made
		// "Overskriften før § 3 ophæves" unanswerable, and the unit-by-unit comparison below
		// cannot see it, because the flat list gives a paragraph group no entry of its own.
		have := lexdoc.MatchText(modelText(&m.Content))
		for _, r := range allRubrics(doc.Root) {
			if !strings.Contains(have, lexdoc.MatchText(r)) {
				t.Errorf("%s: the model has no trace of the rubric %q", f, r)
			}
		}

		// Entry-into-force material: the flag the flat list carried and the field the model
		// has must name the same provisions.
		if strings.Join(oldForce, "\n") != strings.Join(newForce, "\n") {
			t.Errorf("%s: entry-into-force differs\nold:\n%s\nmodel:\n%s",
				f, strings.Join(oldForce, "\n"), strings.Join(newForce, "\n"))
		}

		// Same units, same order: every unit the flat list has is in the model, in its place. The
		// model may add units the flat list never made — named provision groups, and subsections
		// and items read from printed labels where the markup gave none (Provision.Normalise) —
		// and they are counted rather than hidden.
		j := 0
		for _, k := range oldOrder {
			for j < len(newOrder) && newOrder[j] != k {
				j++
			}
			if j == len(newOrder) {
				t.Errorf("%s: the model lost the unit %q, or has it out of order\nold:\n%s\nmodel:\n%s",
					f, k, strings.Join(oldOrder, "\n"), strings.Join(newOrder, "\n"))
				break
			}
			j++
		}
		derived += len(newOrder) - len(oldOrder)

		// Same characters behind each unit. This is the assertion that matters for
		// consolidation: an amendment is located by matching text, so text that differs by
		// one space is an amendment that will not apply. Parts and annexes are compared by
		// key only: the flat list stored their whole subtree as their "text", where the model
		// stores a heading plus the provisions inside, so the two are different by design.
		for _, k := range oldOrder {
			if !comparableText(k) {
				continue
			}
			o := oldTexts[k]
			n := newTexts[k]
			if len(o) != len(n) {
				t.Errorf("%s %s: %d instances in the flat list, %d in the model", f, k, len(o), len(n))
				continue
			}
			for i := range o {
				if o[i] != n[i] && !rubricOnly(&m.Content, k, o[i], n[i]) {
					t.Errorf("%s %s: text differs\nflat list: %q\nmodel:     %q",
						f, k, cut(o[i], 160), cut(n[i], 160))
					break
				}
			}
		}

		for _, ref := range m.Content.Provisions() {
			provisions++
			stykker += len(ref.Provision.Stykker)
			for _, st := range ref.Provision.Stykker {
				items += len(st.Items)
			}
		}
		if u := m.Content.Unmapped; len(u) > 0 {
			unmappedDocs++
			for _, x := range u {
				unmappedWords[x.Concept]++
			}
		}
	}

	if parsed == 0 {
		t.Fatal("no documents parsed, so this test proved nothing")
	}
	if provisions == 0 || stykker == 0 {
		t.Fatalf("vacuous: %d provisions, %d stykker across %d documents", provisions, stykker, parsed)
	}
	if groups == 0 {
		t.Errorf("no named provision groups seen in %d documents, so the structure claim is untested", parsed)
	}
	if unmappedDocs == 0 {
		t.Errorf("no document produced unmapped content in %d documents, which means content is being dropped somewhere", parsed)
	}
	words := make([]string, 0, len(unmappedWords))
	for w := range unmappedWords {
		words = append(words, w)
	}
	sort.Strings(words)
	t.Logf("%d units the model read from printed labels that the flat list does not have", derived)
	t.Logf("%d documents: %d provisions, %d stykker, %d items, %d named groups; %d documents carried unmapped content: %v",
		parsed, provisions, stykker, items, groups, unmappedDocs, words)
}

// cut shortens a string for an error message on a rune boundary, so a failure report cannot
// corrupt the very text it is showing.
func cut(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// TestModelContentStructure pins the structure the flat list could not express, on a document
// whose contents are known rather than sampled.
func TestModelContentStructure(t *testing.T) {
	doc := mustParse(t, fixtureStructured)
	c := doc.Model().Content

	if len(c.Order) == 0 {
		t.Fatal("the fixture produced no content")
	}
	parts := c.Parts()
	if len(parts) != 2 {
		t.Fatalf("expected an afsnit holding a chapter, got %d parts", len(parts))
	}
	if parts[0].Kind != lexdoc.PartAfsnit || parts[0].Number != "2" {
		t.Errorf("first part = %s %q, want afsnit 2 (printed as Afsnit II)", parts[0].Kind, parts[0].Number)
	}
	if parts[1].Kind != lexdoc.PartChapter || parts[1].Number != "3" {
		t.Errorf("nested part = %s %q, want chapter 3", parts[1].Kind, parts[1].Number)
	}

	refs := c.Provisions()
	if len(refs) != 2 {
		t.Fatalf("expected two provisions in the law's own series, got %d", len(refs))
	}
	if refs[0].Part == nil || refs[0].Part.Kind != lexdoc.PartChapter {
		t.Errorf("§ 1 lost its chapter: %v", refs[0].Part)
	}

	p := refs[0].Provision
	if p.Number != "1" || p.Litra != "b" {
		t.Errorf("first provision = %q/%q, want 1/b", p.Number, p.Litra)
	}
	if len(p.Stykker) != 2 {
		t.Fatalf("expected two stykker, got %d", len(p.Stykker))
	}
	st := p.Stykke(2)
	if st == nil {
		t.Fatal("stk. 2 not found by number")
	}
	stekst := p.SpanText(st.Span)
	if !strings.Contains(stekst, "Fast sæt") || strings.Contains(stekst, "Hovedtekst") {
		t.Errorf("stk. 2 span is the wrong text: %q", stekst)
	}
	if len(st.Items) != 2 {
		t.Fatalf("expected two numbered items under stk. 2, got %d", len(st.Items))
	}
	if got := p.SpanText(st.Items[1].Span); !strings.Contains(got, "Andet nummer") {
		t.Errorf("item 2 span = %q", got)
	}
	// The spans must nest: an item's text inside its stykke's text inside the provision.
	if !strings.Contains(p.Text(), stekst) || !strings.Contains(stekst, p.SpanText(st.Items[1].Span)) {
		t.Error("spans do not nest, so the same text is stored more than once or spans are wrong")
	}

	// Entry-into-force material is separate and unaddressable as the law's own numbering.
	if len(c.EntryIntoForce) != 1 || c.EntryIntoForce[0].Number != "1" {
		t.Fatalf("entry-into-force provisions = %d, want one numbered 1", len(c.EntryIntoForce))
	}
	if _, ok := c.Find(ParseAddress("§ 1")); !ok {
		t.Error("the law's own § 1 stopped resolving")
	}
	if _, ok := c.Find(ParseAddress("§ 1, stk. 2, nr. 1")); !ok {
		t.Error("a full item address stopped resolving")
	}
	if loc, ok := c.Find(ParseAddress("afsnit II")); !ok || loc.Part != parts[0] {
		t.Error("afsnit II does not resolve to the part it names")
	}
	if _, ok := c.Find(ParseAddress("bilag 1")); !ok {
		t.Error("bilag 1 does not resolve")
	}
	if _, ok := c.Find(ParseAddress("§ 99")); ok {
		t.Error("an address for a provision that does not exist resolved anyway")
	}

	// The entry-into-force quotation prints "§ 1" too. Resolving "§ 1" must reach the law's
	// own provision, and the entry-into-force series must not be reachable by number.
	loc, ok := c.Find(ParseAddress("§ 1"))
	if !ok || loc.Provision != refs[0].Provision {
		t.Errorf("§ 1 resolved to the wrong provision: %s", loc)
	}
	if strings.Contains(loc.String(), "kraft") {
		t.Error("§ 1 resolved into entry-into-force material")
	}
}

// fixtureStructured is a small document that carries each structural concept at least once:
// an afsnit, a chapter inside it, a litra provision with stykker and numbered items, an
// entry-into-force quotation restarting the numbering, and an annex.
const fixtureStructured = `<?xml version="1.0" encoding="utf-8"?>
<Dokument>
  <Meta>
    <DocumentType>Bekendtgørelse</DocumentType>
    <Rank>A</Rank>
    <AccessionNumber>A20050000129</AccessionNumber>
    <DocumentTitle>Bekendtgørelse med strukturer</DocumentTitle>
    <Year>2005</Year>
    <Number>129</Number>
    <Status>Valid</Status>
    <Ministry>Justitsministeriet</Ministry>
    <AnnouncedIn>Lovtidende A</AnnouncedIn>
  </Meta>
  <DokumentIndhold>
    <Indledning>Loven gælder ikke for Færøerne.</Indledning>
    <Afsnit><Explicatus>Afsnit II</Explicatus><Rubrica>Den store del</Rubrica>
      <Kapitel localId="L3"><Explicatus>Kapitel 3</Explicatus><Rubrica>Om ting</Rubrica>
        <Paragraf>
          <Explicatus>§ 1 b.</Explicatus>
          <Rubrica>Bestemmelse overskrift</Rubrica>
          <Exitus><Linea><Char>Hovedtekst</Char></Linea></Exitus>
          <Stk>
            <Exitus><Linea>Første stykke.</Linea></Exitus>
          </Stk>
          <Stk>
            <Exitus><Linea>Fast sæt:</Linea></Exitus>
            <Exitus><Indentatio formaInd="Nummer"><Explicatus>1)</Explicatus>For det første.</Indentatio></Exitus>
            <Exitus><Indentatio formaInd="Nummer"><Explicatus>2)</Explicatus>Andet nummer.</Indentatio></Exitus>
          </Stk>
        </Paragraf>
      </Kapitel>
    </Afsnit>
    <Paragraf>
      <Explicatus>§ 2.</Explicatus>
      <Exitus><Linea>Anden bestemmelse uden for kapitlet.</Linea></Exitus>
    </Paragraf>
    <Ikraft>
      <Paragraf><Explicatus>§ 1.</Explicatus><Exitus><Linea>Loven træder i kraft den 1. januar.</Linea></Exitus></Paragraf>
    </Ikraft>
    <Bilag><Explicatus>Bilag 1</Explicatus><Rubrica>Skema</Rubrica>
      <Table><Tr><Td>Indhold</Td></Tr></Table>
    </Bilag>
  </DokumentIndhold>
</Dokument>`

// TestModelKeepsPrintedStykkeNumber pins that the number a document prints for a stykke
// survives into the model, including where it contradicts the stykke's position.
//
// This is the shape a chain leaves behind when a provision loses a stykke and the publisher
// does not renumber the rest: the second stykke still prints "Stk. 3.". The model numbered
// such a stykke 2 and threw the label away, so nothing downstream could tell a provision that
// had been renumbered from one whose numbers had simply fallen behind — the premise behind
// consolidated BR18 printing item numbers that cannot exist in 14 provisions and stykke
// numbers in 7 more. Nr stays positional here on purpose, because honouring the printed number
// before deletions renumber costs five real edits on the parity corpus; the label is what had
// to survive, since that is what an amendment renumbering subsections edits.
func TestModelKeepsPrintedStykkeNumber(t *testing.T) {
	doc := mustParse(t, fixtureRenumberedStykker)
	c := doc.Model().Content
	loc, ok := c.Find(ParseAddress("§ 5"))
	if !ok {
		t.Fatal("§ 5 does not resolve")
	}
	p := loc.Provision
	if len(p.Stykker) != 3 {
		t.Fatalf("expected three stykker, got %d", len(p.Stykker))
	}
	want := []string{"", "Stk. 3.", "Stk. 4."}
	for i, st := range p.Stykker {
		if st.Label != want[i] {
			t.Errorf("stykke %d label = %q, want %q", i+1, st.Label, want[i])
		}
		if st.Nr != i+1 {
			t.Errorf("stykke %d Nr = %d, want the positional %d while renumbering stays unexecuted", i+1, st.Nr, i+1)
		}
		if want[i] == "" {
			continue
		}
		if !strings.Contains(p.SpanText(st.Span), want[i]) {
			t.Errorf("stykke %d no longer prints its label %q: %q", i+1, want[i], p.SpanText(st.Span))
		}
	}

	// Items keep their printed marker the same way, and it is the marker the reader sees:
	// "3)" rather than a position-derived 3.
	st := p.Stykke(3)
	if st == nil || len(st.Items) == 0 {
		t.Fatal("the stykke printed as Stk. 4. lost its items")
	}
	if got := st.Items[0].Label; got != "3)" {
		t.Errorf("item marker = %q, want %q", got, "3)")
	}
}

// fixtureRenumberedStykker is a provision whose printed stykke numbers have fallen behind the
// count because a stykke was repealed without the rest being renumbered: the second stykke
// prints "Stk. 3." and the third "Stk. 4.".
const fixtureRenumberedStykker = `<?xml version="1.0" encoding="utf-8"?>
<Dokument>
  <Meta>
    <DocumentType>Bekendtgørelse</DocumentType>
    <Rank>A</Rank>
    <AccessionNumber>A20050000130</AccessionNumber>
    <DocumentTitle>Bekendtgørelse med omnumrerede stykker</DocumentTitle>
    <Year>2005</Year>
    <Number>130</Number>
    <Status>Valid</Status>
    <Ministry>Justitsministeriet</Ministry>
    <AnnouncedIn>Lovtidende A</AnnouncedIn>
  </Meta>
  <DokumentIndhold>
    <Paragraf>
      <Explicatus>§ 5.</Explicatus>
      <Exitus><Linea>Grundtekst.</Linea></Exitus>
      <Stk>
        <Exitus><Linea>Første stykke, som ingen nummer er trykt på.</Linea></Exitus>
      </Stk>
      <Stk>
        <Explicatus>Stk. 3.</Explicatus>
        <Exitus><Linea>Tredje stykke, anden Plads.</Linea></Exitus>
      </Stk>
      <Stk>
        <Explicatus>Stk. 4.</Explicatus>
        <Exitus><Linea>Fjerde stykke:</Linea></Exitus>
        <Exitus><Indentatio formaInd="Nummer"><Explicatus>3)</Explicatus>Tredje nummer.</Indentatio></Exitus>
      </Stk>
    </Paragraf>
  </DokumentIndhold>
</Dokument>`

// allRubrics returns every rubric the document prints, recognised by rubric element name rather
// than by container name: the corpus nests divisions under more container names than any list
// holds, while a rubric is recognisable by what it is called.
func allRubrics(n *Node) []string {
	var out []string
	n.Descendants(func(c *Node) {
		switch c.Name {
		case "Rubrica", "ParagrafOverskrift", "KapitelOverskrift":
			if h := strings.TrimSpace(DisplayText(c)); h != "" {
				out = append(out, h)
			}
		}
	})
	return out
}

// modelText renders everything the model holds, in reading order. It exists for this comparison:
// the front-end renderer is a later task, and what is checked here is only that no words the
// document prints went missing on the way in.
func modelText(c *lexdoc.Content) string {
	var b strings.Builder
	var walk func(nodes []lexdoc.Node)
	walk = func(nodes []lexdoc.Node) {
		for _, n := range nodes {
			switch {
			case n.Part != nil:
				b.WriteString(n.Part.Printed)
				b.WriteString(" ")
				b.WriteString(n.Part.Heading)
				b.WriteString("\n")
				walk(n.Part.Order)
			case n.Provision != nil:
				b.WriteString(n.Provision.Heading)
				b.WriteString("\n")
				b.WriteString(n.Provision.Text())
				b.WriteString("\n")
			}
		}
	}
	walk(c.Order)
	b.WriteString(lexdoc.SegmentsText(c.Preamble))
	b.WriteString(lexdoc.SegmentsText(c.Title))
	b.WriteString(lexdoc.SegmentsText(c.Signature))
	for i := range c.EntryIntoForce {
		b.WriteString(c.EntryIntoForce[i].Heading)
		b.WriteString("\n")
		b.WriteString(c.EntryIntoForce[i].Text())
		b.WriteString("\n")
	}
	for i := range c.Appendices {
		b.WriteString(c.Appendices[i].Label)
		b.WriteString(" ")
		b.WriteString(c.Appendices[i].Heading)
		b.WriteString("\n")
		b.WriteString(lexdoc.SegmentsText(c.Appendices[i].Segments))
		b.WriteString("\n")
		walk(c.Appendices[i].Order)
	}
	// Unmapped is part of the answer: content the model has no concept for keeps its text here
	// so that an incomplete model is visible per document rather than inferred from an average.
	for _, u := range c.Unmapped {
		b.WriteString(u.Words())
		b.WriteString("\n")
	}
	return b.String()
}

// TestRubricInsideParagraphGroupIsCarried guards a reader gap found through consolidation: the
// register writes a group's rubric as Rubrica>Linea>Char, so the element's own text is only
// whitespace. Reading direct text made every rubric empty, which cost the model the words and left
// "Overskriften før § 3 ophæves" with nothing to remove.
func TestRubricInsideParagraphGroupIsCarried(t *testing.T) {
	const src = "<" + "?xml version=\"1.0\"?><Dokument><Meta>" +
		"<DocumentTitle>Bekendtgørelse om zoner</DocumentTitle>" +
		"<AccessionNumber>A20260000001</AccessionNumber></Meta>" +
		"<ParagrafGruppe><Rubrica><Linea><Char>Definitioner</Char></Linea></Rubrica>" +
		"<Paragraf><Explicatus>\u00a7 1.</Explicatus>" +
		"<Stk><Exitus><Linea><Char>Der defineres her.</Char></Linea></Exitus></Stk></Paragraf>" +
		"</ParagrafGruppe></Dokument>"
	m := mustParse(t, src).Model()
	var heading string
	for _, n := range m.Content.Order {
		if n.Part != nil && n.Part.Kind == lexdoc.PartGroup {
			heading = n.Part.Heading
		}
	}
	if heading != "Definitioner" {
		t.Errorf("group rubric = %q, want %q", heading, "Definitioner")
	}
}

// rubricOnly reports texts that differ only by the provision's rubric, which the flat list prints
// inline and the model carries as the provision's heading: the same words in their own field.
func rubricOnly(c *lexdoc.Content, key, flat, model string) bool {
	if !strings.HasPrefix(key, "p:") || strings.Contains(key, ",") {
		return false
	}
	loc, ok := c.Find(lexdoc.ParseAddress("§ " + strings.TrimPrefix(key, "p:")))
	if !ok || loc.Provision == nil || loc.Provision.Heading == "" {
		return false
	}
	return strings.Join(strings.Fields(strings.Replace(flat, loc.Provision.Heading, "", 1)), " ") ==
		strings.Join(strings.Fields(model), " ")
}

// Books and afsnit printed as loose lines, beside the chapters or inside the last provision of the
// chapter before, hold the chapters after them.
func TestLooseDivisionsGroupTheChapters(t *testing.T) {
	ch := func(n string) lexdoc.Node {
		return lexdoc.Node{Part: &lexdoc.Part{Kind: lexdoc.PartChapter, Number: n}}
	}
	divs := []division{
		{at: 0, book: true, part: lexdoc.Part{Number: "1", Printed: "Første bog"}},
		{at: 0, part: lexdoc.Part{Number: "1", Printed: "Første afsnit"}},
		{at: 2, part: lexdoc.Part{Number: "2", Printed: "Andet afsnit"}},
		{at: 3, book: true, part: lexdoc.Part{Number: "2", Printed: "Anden bog"}},
	}
	out := regroup([]lexdoc.Node{ch("1"), ch("2"), ch("3"), ch("4")}, divs)
	if len(out) != 2 || out[0].Part.Kind != lexdoc.PartBook || len(out[0].Part.Order) != 2 || len(out[1].Part.Order) != 1 {
		t.Fatalf("regrouped %+v", out)
	}
	if first := out[0].Part.Order[0].Part; len(first.Order) != 2 || first.Order[1].Part.Number != "2" {
		t.Errorf("første afsnit = %+v", first)
	}
}
