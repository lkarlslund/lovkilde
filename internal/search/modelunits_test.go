package search

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/lkarlslund/retsinformation/internal/lexdania"
	"github.com/lkarlslund/retsinformation/internal/lexdoc"
)

// The unitiser's contract, in two parts: a unit's address must be one that lookup resolves back to
// the text that was indexed, and a document must be cut into units that say what the thing is
// rather than into one anonymous blob per document that happens to have no numbered sections.

func sampleModel() *lexdoc.Document {
	seg := func(texts ...string) []lexdoc.Segment {
		out := make([]lexdoc.Segment, 0, len(texts))
		for i, t := range texts {
			out = append(out, lexdoc.Segment{Text: t, Break: i%2 == 1})
		}
		return out
	}
	whole := &lexdoc.Provision{
		Number: "1", Heading: "Formål",
		Segments: seg("§ 1. ", "Loven gaelder her."),
	}
	multi := &lexdoc.Provision{
		Number:   "2",
		Segments: seg("§ 2. ", "Hovedreglen.", "Stk. 2. ", "Andet stykke."),
		Stykker: []lexdoc.Stykke{
			{Nr: 1, Span: lexdoc.Span{From: 0, To: 2}},
			{Nr: 2, Span: lexdoc.Span{From: 2, To: 4}},
		},
	}
	oneStykke := &lexdoc.Provision{
		Number:   "4",
		Segments: seg("§ 4. ", "Kun ét stykke."),
		Stykker:  []lexdoc.Stykke{{Nr: 1, Span: lexdoc.Span{From: 0, To: 2}}},
	}
	// The register does print the same number twice in one document — an amending act that
	// reuses a heading, or a consolidated text with a section and its quoted original.
	duplicate := &lexdoc.Provision{Number: "2", Segments: seg("§ 2. ", "Den anden bestemmelse med samme nummer.")}
	ikraft := &lexdoc.Provision{Number: "2", Segments: seg("§ 2. ", "Loven traeder i kraft den 1. januar.")}
	numberedAnnex := &lexdoc.Appendix{Number: "1", Label: "Bilag 1", Segments: seg("Bilagets tekst.")}
	letteredAnnex := &lexdoc.Appendix{Label: "Bilag A", Segments: seg("Bilag A's tekst.")}

	return &lexdoc.Document{Content: lexdoc.Content{
		Title:     seg("Lov om noget"),
		Preamble:  seg("Vi Kongen ..."),
		Signature: seg("Givet den 1. januar."),
		Order: []lexdoc.Node{
			{Provision: whole},
			{Part: &lexdoc.Part{Kind: lexdoc.PartChapter, Number: "1", Printed: "Kapitel 1", Heading: "Særlige regler", Order: []lexdoc.Node{
				{Provision: multi},
				{Provision: oneStykke},
				{Provision: duplicate},
			}}},
			{Part: &lexdoc.Part{Kind: lexdoc.PartGroup, Number: "5", Heading: "Gruppe uden adresseform"}},
		},
		EntryIntoForce: []lexdoc.Provision{*ikraft},
		Appendices:     []lexdoc.Appendix{*numberedAnnex, *letteredAnnex},
		Unmapped:       []lexdoc.Unmapped{{Concept: "table", Text: "Tabel 1 2 3"}},
	}}
}

func unitAddrs(units []Unit) map[string]int {
	out := map[string]int{}
	for _, u := range units {
		out[u.Addr]++
	}
	return out
}

func TestModelUnitsCiteEachUnitOnce(t *testing.T) {
	units := Units(0, DocMeta{Title: "Lov om noget"}, sampleModel())
	addrs := unitAddrs(units)

	// A section that splits is cited at condition level, and its own number is claimed by
	// those conditions rather than cited a second time over them. A section with one
	// condition is cited as the section, because that is what the document prints.
	for _, want := range []string{"titel", "indledningen", "§ 1", "§ 2, stk. 1", "§ 2, stk. 2", "§ 4", "kapitel 1", "§ 2, ikrafttrædelsesbestemmelse", "bilag 1"} {
		if addrs[want] != 1 {
			t.Errorf("address %q cited %d times, want exactly 1", want, addrs[want])
		}
	}

	if addrs["§ 2"] != 0 {
		t.Errorf("the split section is cited %d times as well as by its conditions, so its words are indexed twice", addrs["§ 2"])
	}
	// One copy of every word: each condition's words are in its unit and nowhere else. A
	// word that sits in two units is a hit the reader sees twice and a posting the index
	// pays for twice.
	copies := map[string]int{}
	for _, u := range units {
		for _, w := range strings.Fields(u.Text) {
			copies[w]++
		}
	}
	for _, word := range []string{"Hovedreglen.", "Andet", "gaelder"} {
		if copies[word] != 1 {
			t.Errorf("%q is indexed %d times, want exactly once", word, copies[word])
		}
	}
	// The second provision printed as "§ 2" keeps its words and loses the location: citing two
	// units as "§ 2" would make half the hits point at the wrong provision.
	if addrs[""] < 1 {
		t.Error("the duplicated provision's text was dropped instead of being indexed unaddressed")
	}
	var duplicated string
	for _, u := range units {
		if u.Addr == "" && strings.Contains(u.Text, "samme nummer") {
			duplicated = u.Text
		}
	}
	if duplicated == "" {
		t.Error("the duplicated provision's words are not in the index at all")
	}
}

// The coverage contract, in fixture form. The section list's units reached 53% of the text a
// reader is served in a measured sample of the mirror, because content with no section of its
// own — a table, a bill's running prose, a chapter's loose lines — was simply not indexed. The
// model's units must hold every word the document contains, so each named kind of content gets
// a word here that only it could supply.
func TestModelUnitsCoverEveryKindOfContent(t *testing.T) {
	var joined strings.Builder
	for _, u := range Units(0, DocMeta{Title: "Lov om noget"}, sampleModel()) {
		joined.WriteString(u.Text)
		joined.WriteString("\n")
	}
	indexed := squashAll(joined.String())
	for _, want := range []string{
		"lov om noget",                // the title, which the index otherwise never sees
		"vi kongen",                   // the preamble
		"loven gaelder her",           // a section's own text
		"hovedreglen", "andet stykke", // both conditions of a split section
		"kun ét stykke",                      // a section with a single condition
		"den anden bestemmelse med samme nu", // the second section printed with a taken number
		"særlige regler",                     // a printed chapter's heading
		"gruppe uden adresseform",            // a division the mirror has no citation word for
		"loven traeder i kraft",              // an entry-into-force provision
		"bilagets tekst", "bilag a",          // numbered and lettered annexes
		"givet den 1. januar", // the signature block
		"tabel 1 2 3",         // content the model has no concept for
	} {
		if !strings.Contains(indexed, want) {
			t.Errorf("no unit holds %q, so a search cannot find it", want)
		}
	}
}

// A word with no number after it is the shape that produced 179 of the 250 addresses lookup could
// not open, so the unitiser must not emit one even when the document prints its number only beside
// the element.
func TestModelUnitsNeverCiteABareWord(t *testing.T) {
	m := sampleModel()
	m.Content.Order = append(m.Content.Order,
		lexdoc.Node{Part: &lexdoc.Part{Kind: lexdoc.PartChapter, Heading: "Kapitel uden nummer"}},
		lexdoc.Node{Provision: &lexdoc.Provision{Heading: "Rubrik uden nummer", Segments: []lexdoc.Segment{{Text: "Brødtekst uden nummer."}}}},
	)
	m.Content.Appendices = append(m.Content.Appendices, lexdoc.Appendix{Label: "Bilag C", Segments: []lexdoc.Segment{{Text: "Bilag C's tekst."}}})

	for _, u := range Units(0, DocMeta{}, m) {
		if u.Text == "" {
			t.Errorf("an empty unit was emitted for address %q", u.Addr)
			continue
		}
		if strings.HasSuffix(u.Addr, " ") || strings.HasSuffix(u.Addr, ".") {
			t.Errorf("address %q is a word with no number after it: %q", u.Addr, u.Text)
		}
		if strings.Contains(u.Addr, "kapitel") || strings.Contains(u.Addr, "bilag") {
			// Every chapter and annex in the fixture is numbered except the ones added above; the
			// unnumbered ones must be cited by nothing.
			if u.Addr != "kapitel 1" && u.Addr != "bilag 1" {
				t.Errorf("unnumbered division cited as %q", u.Addr)
			}
		}
	}
}

// The acceptance test in fixture form: every address a unit cites parses, resolves through the
// model, and resolves to text that contains the words that were indexed. Over the mirror the same
// claim is TestModelUnitsOverMirrorResolve.
func TestModelUnitAddressesResolveToTheirOwnText(t *testing.T) {
	m := sampleModel()
	for _, u := range Units(0, DocMeta{Title: "Lov om noget"}, m) {
		if u.Addr == "" || u.Addr == "titel" {
			continue
		}
		a := lexdoc.ParseAddress(u.Addr)
		if !a.Recognised {
			t.Errorf("address %q does not parse", u.Addr)
			continue
		}
		loc, ok := m.Content.Find(a)
		if !ok {
			t.Errorf("address %q resolves to nothing", u.Addr)
			continue
		}
		if !loc.Found() {
			t.Errorf("address %q resolved to an empty location", u.Addr)
			continue
		}
		rendered, ok := m.Content.PlainTextProvisions(a)
		if !ok {
			t.Errorf("address %q resolves but does not render", u.Addr)
			continue
		}
		if !strings.Contains(squashAll(rendered), squashAll(u.Text)) {
			t.Errorf("address %q opens text that does not contain the indexed words:\nunit: %s\nopened: %s", u.Addr, u.Text, rendered)
		}
	}
}

func squashAll(s string) string {
	return strings.Join(strings.Fields(strings.ToLower(s)), " ")
}

// TestModelUnitsOverMirrorResolve is the same claim over real documents, where the shapes the unit
// iser cites are the ones nobody chose. It also reports the number T010 exists to move: how many
// units carry no address at all.
//
//	RETSIN_SEARCH_UNITS=1 go test ./internal/search -run TestModelUnitsOverMirrorResolve -v
func TestModelUnitsOverMirrorResolve(t *testing.T) {
	if os.Getenv("RETSIN_SEARCH_UNITS") == "" {
		t.Skip("set RETSIN_SEARCH_UNITS=1 to unitise the mirror and check every cited address")
	}
	root := os.Getenv("RETSIN_MIRROR")
	if root == "" {
		for _, cand := range []string{"data/xml", "../../data/xml"} {
			if fi, err := os.Stat(cand); err == nil && fi.IsDir() {
				root = cand
				break
			}
		}
	}
	if root == "" {
		t.Skip("no mirror found: set RETSIN_MIRROR to the archive directory")
	}
	docs, units, err := Sample(context.Background(), root, 25)
	if err != nil {
		t.Fatal(err)
	}

	archive, err := lexdania.Scan(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	var unaddressed, addressed, failed int
	type counts struct{ addressed, unaddressed int }
	bySource := map[string]counts{}
	// The two document-level labels are not locations: a reader cites "lovens titel", and
	// ParseAddress wants that phrase rather than the bare word the index uses to label a
	// unit. They are checked for text, not for resolution.
	isLabel := map[string]bool{"titel": true, "indledningen": true}
	for _, u := range units {
		meta := docs[u.Doc]
		c := bySource[meta.Source]
		if u.Addr == "" {
			unaddressed++
			c.unaddressed++
			bySource[meta.Source] = c
			continue
		}
		if isLabel[u.Addr] {
			addressed++
			c.addressed++
			bySource[meta.Source] = c
			continue
		}
		addressed++
		c.addressed++
		bySource[meta.Source] = c
		doc, _, ok := archive.Open(meta.Accession)
		if !ok {
			t.Errorf("%s: indexed but could not be read", meta.Accession)
			continue
		}
		m := doc.Model()
		a := lexdoc.ParseAddress(u.Addr)
		if !a.Recognised {
			t.Errorf("%s cites %q, which does not parse as an address", meta.Accession, u.Addr)
			failed++
			continue
		}
		if _, ok := m.Content.Find(a); !ok {
			t.Errorf("%s cites %q, which resolves to nothing in its own document", meta.Accession, u.Addr)
			failed++
		}
	}
	t.Logf("%d units over %d documents: %d addressed, %d unaddressed (%.1f%%)",
		len(units), len(docs), addressed, unaddressed, 100*float64(unaddressed)/float64(len(units)))
	for src, c := range bySource {
		t.Logf("  source %-5s %5d addressed, %5d unaddressed", src, c.addressed, c.unaddressed)
	}
	if failed > 0 {
		t.Errorf("%d units cite an address that does not open", failed)
	}

	// A citation the reader can follow, counted the same way on both sides: opening the
	// address must show the words the hit quoted. Resolution alone is too generous, because
	// an ambiguous number still resolves — "§ 2" in an act amending ten laws opens the first
	// law's § 2 — and a hit that opens the wrong provision is a wrong answer wearing a
	// citation. Counting addresses that merely parse is how the shipped index came to print
	// 179 addresses in 250 that led nowhere.
	secUnits := sectionsUnits(t, docs)
	// Which units carry a citation a reader can follow: opening the address must show the
	// words the hit quoted. Resolution alone is too generous — an ambiguous number still
	// resolves, because "§ 2" in an act amending ten laws opens the first law's § 2 — and a
	// hit that opens the wrong provision is a wrong answer wearing a citation. Counting
	// addresses that merely parse is how the shipped index came to print 179 of 250 addresses
	// that led nowhere.
	//
	// Counted two ways: as units carrying a usable citation, and as distinct places those
	// citations reach. The first rewards cutting one document into many units over the same
	// ground; the second says what a reader can actually get to.
	opensOwnWords := func(u Unit) bool {
		if u.Addr == "" || isLabel[u.Addr] {
			return false
		}
		a := lexdoc.ParseAddress(u.Addr)
		if !a.Recognised {
			return false
		}
		doc, _, ok := archive.Open(docs[u.Doc].Accession)
		if !ok {
			return false
		}
		rendered, ok := doc.Model().Content.PlainTextProvisions(a)
		if !ok {
			return false
		}
		return strings.Contains(squashAll(rendered), squashAll(u.Text))
	}
	followable := func(us []Unit) (open, cited int, places map[string]bool) {
		places = map[string]bool{}
		for _, u := range us {
			if u.Addr == "" || isLabel[u.Addr] {
				continue
			}
			cited++
			if opensOwnWords(u) {
				open++
				places[docs[u.Doc].Accession+"|"+u.Addr] = true
			}
		}
		return open, cited, places
	}
	modelOpen, modelCited, modelPlaces := followable(units)
	sectionOpen, sectionCited, sectionPlaces := followable(secUnits)
	perThousand := func(n int) float64 { return 1000 * float64(n) / float64(len(docs)) }
	t.Logf("citations a reader can follow: model units %d of %d cited, %.0f per 1,000 documents over %d distinct places; section units %d of %d, %.0f per 1,000 over %d distinct places",
		modelOpen, modelCited, perThousand(modelOpen), len(modelPlaces), sectionOpen, sectionCited, perThousand(sectionOpen), len(sectionPlaces))

	// Two kinds of place the section list names and the model's units deliberately do not,
	// counted rather than waved away:
	//
	//   - Item-level addresses ("§ 5, stk. 2, nr. 3"). The index stores no positions, so a hit
	//     on one numbered item cannot be scored or highlighted any better than a hit on the
	//     condition holding it; the condition's unit carries the item's words and is cited,
	//     and lookup opens the item address directly when a reader asks for it.
	//   - Addresses resting on a number the document prints more than once. An act amending
	//     ten laws prints "§ 2" ten times, and lookup answers "§ 2, stk. 2" with the first of
	//     them; citing the other nine by it points a reader at the wrong provision, so the
	//     unitiser keeps their words and cites them as the document.
	numbers := map[string]map[string]int{}
	perDocStykker := map[string]map[string]int{}
	for _, meta := range docs {
		doc, _, ok := archive.Open(meta.Accession)
		if !ok {
			continue
		}
		counts := map[string]int{}
		stykker := map[string]int{}
		count := func(pr lexdoc.Provision) {
			key := strings.ToLower(strings.TrimSpace(pr.Number + pr.Litra))
			counts[key]++
			n := len(pr.Stykker)
			if n == 0 {
				n = 1
			}
			stykker[key] += n
		}
		for _, ref := range doc.Model().Content.Provisions() {
			count(*ref.Provision)
		}
		for i := range doc.Model().Content.EntryIntoForce {
			count(doc.Model().Content.EntryIntoForce[i])
		}
		numbers[meta.Accession] = counts
		perDocStykker[meta.Accession] = stykker
	}
	// Every place the section list reaches and the model's citations do not has to belong to a
	// named class, and the class has to be checked on the place rather than counted in the
	// aggregate: a subtraction someone typed by hand is how a gate quietly stops meaning
	// anything. The classes, and the evidence each one is accepted on:
	//
	//   - item ("§ 5, stk. 2, nr. 3"): the index stores no positions, so a hit on a numbered
	//     item cannot be scored or highlighted better than a hit on the condition holding it,
	//     and the condition's own place has to be one the model reaches.
	//   - one unnumbered condition ("§ 5, stk. 1" where § 5 has a single stykke the register
	//     prints without a number): the model cites "§ 5", which holds the same words, and has
	//     to reach that place.
	//   - entry-into-force: the series is cited under its own locator; the model has to reach
	//     the series place it uses for that document.
	//   - ambiguous number: the document prints that number more than once, and lookup answers
	//     with the first, so citing the rest by it is a wrong pointer.
	//   - glued run: the model cited the place, and only the self-opening text check refused —
	//     that is M012T013's run boundary, counted by its own bound below.
	citedInstead := map[string]bool{}
	for _, u := range units {
		if u.Addr != "" && !isLabel[u.Addr] {
			citedInstead[docs[u.Doc].Accession+"|"+u.Addr] = true
		}
	}
	// The address the model cites instead when it refuses to invent a number: the same locator
	// with the condition part dropped.
	parentOf := func(addr string) string {
		a := lexdoc.ParseAddress(addr)
		if a.Paragraph == "" {
			return ""
		}
		return "§ " + a.Paragraph
	}
	// placeOpens asks lookup, through the model's own Find, whether an address resolves in the
	// document the section list cited it in. The cache keeps the sampled documents open rather
	// than re-parsing one per missing place.
	models := map[string]*lexdoc.Document{}
	placeOpens := func(acc string, a lexdoc.Address) bool {
		m := models[acc]
		if m == nil {
			doc, _, ok := archive.Open(acc)
			if !ok {
				return false
			}
			m = doc.Model()
			models[acc] = m
		}
		loc, ok := m.Content.Find(a)
		return ok && (loc.Provision != nil || loc.Stykke != nil || loc.Item != nil ||
			loc.Part != nil || loc.Appendix != nil || loc.Preamble || loc.Title)
	}

	docHasIkraftPlace := map[string]bool{}
	for _, u := range units {
		if strings.Contains(u.Addr, "ikrafttrædelsesbestemmelse") {
			docHasIkraftPlace[docs[u.Doc].Accession] = true
		}
	}
	var classes = map[string]int{}
	var unexplained []string
	for place := range sectionPlaces {
		if modelPlaces[place] {
			continue
		}
		acc, addr, _ := strings.Cut(place, "|")
		a := lexdoc.ParseAddress(addr)
		parent := parentOf(addr)
		parentReached := parent != "" && modelPlaces[acc+"|"+parent]
		// The parent's words are indexed and cited even when the self-opening text check
		// refused them, which is M012T013's glued run rather than a place the model lacks.
		parentCitedButRefused := parent != "" && !parentReached && citedInstead[acc+"|"+parent]
		class := ""
		if a.Nr != 0 && (parentReached || parentCitedButRefused) {
			class = "item, condition cited instead"
		} else if strings.Contains(addr, "ikrafttrædelsesbestemmelse") && docHasIkraftPlace[acc] {
			class = "entry-into-force series"
		} else if a.Stk != 0 && parentReached {
			// The condition carries no number the document prints, so the model cites "§ N",
			// which holds exactly the same words; lookup still opens "§ N, stk. 1".
			class = "condition not numbered as printed, section cited instead"
		} else if citedInstead[place] || parentCitedButRefused {
			class = "cited, text check refused (M012T013)"
		} else if a.Paragraph != "" && numbers[acc][strings.ToLower(a.Paragraph)] > 1 {
			class = "ambiguous number"
		} else if !placeOpens(acc, a) {
			// The section list cited a place lookup cannot open at all. M012T012 measured 250
			// of 440 such citations in the shipped index; refusing them is the point.
			class = "lookup cannot open the section's address"
		}
		if class == "" {
			if len(unexplained) < 15 {
				unexplained = append(unexplained, place)
			}
			class = "UNEXPLAINED"
		}
		classes[class]++
	}
	var names []string
	for k, n := range classes {
		names = append(names, fmt.Sprintf("%s=%d", k, n))
	}
	sort.Strings(names)
	excluded := 0
	for k, n := range classes {
		if k != "UNEXPLAINED" {
			excluded += n
		}
	}
	t.Logf("places the section list reaches and the model does not cite: %s", strings.Join(names, ", "))
	if classes["UNEXPLAINED"] > 0 {
		t.Errorf("%d section places are not reachable and not explained by any class: %s",
			classes["UNEXPLAINED"], strings.Join(unexplained, ", "))
	}
	if len(modelPlaces) < len(sectionPlaces)-excluded {
		t.Errorf("the model's units reach %d distinct citable places, below the section units' %d minus the %d places in named classes",
			len(modelPlaces), len(sectionPlaces), excluded)
	}

	// The residue: citations whose quoted text is not quite the text that opens. Almost all of
	// it is the run-join difference M012T013 tracks — the model glues a run across an element
	// boundary where the tree kept a space, so the index holds "afgift.stk." where the opened
	// provision reads "afgift. stk." — and the bound exists so that fixing the walker shows up
	// here as room to tighten it, and a regression shows up as a failure.
	if modelCited > 0 {
		bad := 100 * float64(modelCited-modelOpen) / float64(modelCited)
		secBad := 0.0
		if sectionCited > 0 {
			secBad = 100 * float64(sectionCited-sectionOpen) / float64(sectionCited)
		}
		// Both sides from the same run: the comparison is the point, and the section figure
		// quoted in the README came from a diagnostic that no longer exists.
		t.Logf("cited units whose opened text does not contain their own words: model %.1f%%, section units %.1f%% (mostly the run-join difference M012T013 tracks)", bad, secBad)
		if bad > citationTextBound {
			t.Errorf("%.1f%% of cited addresses do not open the words they quote, above the recorded bound of %.1f%%", bad, citationTextBound)
		}
	}

	// Coverage, measured against the units this replaces rather than against an assumption.
	// An index that reaches fewer of a document's words than the one it supersedes is a
	// regression no ranking number excuses, and the two are produced by different code, so
	// the comparison is made here in the same run over the same sample.
	modelCov := wordCoverage(t, docs, units, len(docs))
	sectionCov := wordCoverage(t, docs, secUnits, len(docs))
	t.Logf("served words reached: model units %.3f, section units %.3f", modelCov, sectionCov)
	if modelCov < sectionCov {
		t.Errorf("the model's units reach %.3f of served words, below the section units' %.3f", modelCov, sectionCov)
	}
	if modelCov < coverageBound {
		t.Errorf("the model's units reach %.3f of served words, below the recorded bound of %.2f", modelCov, coverageBound)
	}
}

// coverageBound is a floor, not an estimate: measured at 0.995 of served words over 1,597
// mirror documents, with the section units the milestone replaces at 0.456 on the same
// documents. It may only be raised. The remaining 0.5% is the renderer's own scaffolding —
// the heading it prints above content the model has no concept for, and the per-concept
// counts beside it — which is not the document's text and is deliberately not indexed.
const coverageBound = 0.99

// citationTextBound is the measured share of cited units whose opened text does not contain
// the unit's own words: 4.2% over 1,597 mirror documents, nearly all of it the glued run
// boundary M012T013 is about. Lower is better; the number may only fall.
const citationTextBound = 4.5

// placeShape labels an address by the section-list form that produced it, so a count of
// missing places says something about which shape the model does not cite.
func placeShape(addr string) string {
	switch {
	case strings.HasPrefix(addr, "overskriften"):
		return "rubric heading"
	case strings.HasPrefix(addr, "kapitel"):
		return "kapitel"
	case strings.HasPrefix(addr, "afsnit"):
		return "afsnit"
	case strings.HasPrefix(addr, "bilag"):
		return "bilag"
	case strings.Contains(addr, "nr. "):
		return "item"
	case strings.Contains(addr, "stk. "):
		return "section + condition"
	case strings.HasPrefix(addr, "§ "):
		return "section"
	default:
		return "other: " + addr
	}
}
