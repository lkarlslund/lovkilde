package lexdoc

import (
	"strings"
	"testing"
)

// threeStykke builds a provision whose text is one flat slice with three subsections
// pointing into it, which is the shape consolidation edits against.
func threeStykke() *Provision {
	return &Provision{
		Number: "5",
		Segments: []Segment{
			{Text: "§ 5. "},
			{Text: "Pligt til oplysning.", Break: true},
			{Text: "Kravet gælder virksomheder.", Break: true},
			{Text: "Særlige tilfælde fastsættes i bekendtgørelsen.", Break: true},
		},
		Stykker: []Stykke{
			{Nr: 1, Span: Span{From: 1, To: 2}},
			{Nr: 2, Span: Span{From: 2, To: 3}},
			{Nr: 3, Span: Span{From: 3, To: 4}},
		},
	}
}

func TestReplaceEditsOnceAndEveryEnclosingTextFollows(t *testing.T) {
	p := threeStykke()
	if !p.Replace("Pligt til oplysning.", "Pligt til at rapportere.") {
		t.Fatal("Replace did not find a passage that is plainly present")
	}
	if got := p.SpanText(p.Stykker[0].Span); got != "Pligt til at rapportere." {
		t.Errorf("stk. 1 text = %q", got)
	}
	// The point of the flat slice: the provision's own text carries the edit without being
	// recomposed, and the sibling spans are untouched.
	if got := p.Text(); got != "§ 5. Pligt til at rapportere. Kravet gælder virksomheder. Særlige tilfælde fastsættes i bekendtgørelsen." {
		t.Errorf("provision text did not follow the edit: %q", got)
	}
	if got := p.SpanText(p.Stykker[1].Span); got != "Kravet gælder virksomheder." {
		t.Errorf("sibling span changed: %q", got)
	}
	if len(p.Segments) != 4 {
		t.Errorf("segments = %d, want the same four: replacement must not add or remove segments", len(p.Segments))
	}
}

func TestReplaceReplacesOnlyTheFirstOccurrence(t *testing.T) {
	p := &Provision{
		Number: "1",
		Segments: []Segment{
			{Text: "Pligt til oplysning. Pligt til oplysning."},
		},
	}
	if !p.Replace("Pligt til oplysning.", "Krav om rapportering.") {
		t.Fatal("no match")
	}
	// The register's edits are one passage at a time. Replacing every match would silently
	// widen the change beyond what the amending act said.
	if got := p.Text(); got != "Krav om rapportering. Pligt til oplysning." {
		t.Errorf("text = %q, want the second occurrence left alone", got)
	}
}

func TestReplaceIsBoundedByTheSpanItWasGiven(t *testing.T) {
	p := threeStykke()
	// "virksomheder" appears in stk. 2. An edit addressed at stk. 1 must fail rather than
	// reach into its neighbour and report success.
	if p.ReplaceSpan(p.Stykker[0].Span, "virksomheder", "enheder") {
		t.Errorf("an edit on stk. 1 changed stk. 2: %q", p.SpanText(p.Stykker[1].Span))
	}
	if !p.ReplaceSpan(p.Stykker[1].Span, "virksomheder", "enheder") {
		t.Fatal("the edit addressed at the right subsection failed")
	}
	if got := p.SpanText(p.Stykker[1].Span); got != "Kravet gælder enheder." {
		t.Errorf("stk. 2 text = %q", got)
	}
}

func TestReplaceKeepsBreaksAndFootnotesItEmpties(t *testing.T) {
	p := &Provision{
		Number: "1",
		Segments: []Segment{
			{Text: "Tekst.", Break: true},
			{Text: "Fodnote om EU-forordning.", Note: "id42", Break: true},
			{Text: "Slut."},
		},
	}
	// An edit over the provision's own text leaves the note alone; one aimed at the note, by a
	// span that starts in it, empties it.
	if p.Replace("Fodnote om EU-forordning.", "") {
		t.Fatal("an edit of the provision's text reached into its footnote")
	}
	if !p.ReplaceSpan(Span{From: 1, To: 3}, "Fodnote om EU-forordning.", "") {
		t.Fatal("deleting a whole segment's text failed")
	}
	if p.Segments[1].Note != "id42" || !p.Segments[1].Break {
		t.Errorf("emptying a segment lost its identity: %+v, want note id42 and its break kept", p.Segments[1])
	}
	if got := p.Text(); got != "Tekst. Slut." {
		t.Errorf("text = %q, want the deleted passage gone and nothing else disturbed", got)
	}
}

func TestReplaceFindsWhatTheAmendmentQuotesNotTheTargetSpelling(t *testing.T) {
	// The target stores guillemets, a non-breaking space and a soft hyphen from justified
	// typesetting; a drafter quoting the passage reproduces none of that. The variants the
	// match covers are the ones the normaliser actually claims — an ASCII straight quote is
	// deliberately not one of them, because folding it would merge characters Danish legal
	// text uses distinctly, so that variant is asserted to fail rather than quietly dropped.
	p := &Provision{
		Number: "2",
		Segments: []Segment{
			{Text: "Der ydes erstatning for »tab af tabt­ arbejdsfortjeneste« ved ekspropriering."},
		},
	}
	if p.Replace(`for "tab af tabt arbejdsfortjeneste"`, "for tab af driftsfortjeneste") {
		t.Errorf("an ASCII straight quote matched guillemets, which MatchRunes does not claim: text is now %q", p.Text())
	}
	// The quotation marks around the passage are kept out of the needle's ends on purpose:
	// QuoteStrip removes them the way the register's own delimiters, so a needle that *is* a
	// quotation would arrive stripped and stop short of its closing mark. That is how the
	// amendment path feeds a passage in, and a model that behaved differently here would not
	// be byte-identical to it.
	if !p.Replace("for »tab af tabt arbejdsfortjeneste« ved", "for tab af driftsfortjeneste i") {
		t.Fatalf("normalised match failed; provision reads %q", p.Text())
	}
	if got := p.Text(); got != "Der ydes erstatning for tab af driftsfortjeneste i ekspropriering." {
		t.Errorf("text = %q, want the soft hyphen and non-breaking space gone with the replaced passage", got)
	}

	// The low-9 double quote the register's own typesetting uses stands for » in the
	// comparison form, so a passage quoted that way reaches the same target.
	curly := &Provision{Number: "3", Segments: []Segment{{Text: "Krav om »erstatning«."}}}
	if !curly.Replace("Krav om „erstatning”.", "Krav om godtgørelse.") {
		t.Errorf("a curly-quote variant did not reach its target: %q", curly.Text())
	}
}

func TestReplaceRefusesWhatIsNotThere(t *testing.T) {
	p := threeStykke()
	before := p.Text()
	for _, old := range []string{"", " ", "Passage der ikke findes", "pligt til oplysning"} {
		if p.Replace(old, "udskiftet") {
			t.Errorf("Replace(%q) reported success", old)
		}
	}
	if p.Text() != before {
		t.Errorf("a failed edit changed the text: %q", p.Text())
	}
	// An empty span addresses no text, so it must fail rather than match at the front of the
	// provision the caller never pointed at.
	if p.ReplaceSpan(Span{From: 2, To: 2}, "Kravet", "Det") {
		t.Error("an empty span accepted an edit")
	}
}

func TestReplaceSpansSeveralSegmentsInOneGo(t *testing.T) {
	// The passage is quoted the way it prints, with the paragraph break between the two
	// subsections standing for a space. Matching only the raw segment texts would not find
	// this, which is why the match runs over rendered text.
	p := threeStykke()
	if !p.Replace("Pligt til oplysning. Kravet gælder", "Én samlet bestemmelse") {
		t.Fatalf("a passage crossing a segment boundary was not found: %q", p.Text())
	}
	if got := p.Segments[1].Text; got != "Én samlet bestemmelse" {
		t.Errorf("first touched segment = %q, want the whole replacement placed there", got)
	}
	if got := p.Segments[2].Text; got != " virksomheder." {
		t.Errorf("second segment = %q, want only the covered words removed", got)
	}
	if !p.Segments[1].Break {
		t.Error("the paragraph break moved off the segment that still ends the paragraph")
	}
	if got := p.Text(); got != "§ 5. Én samlet bestemmelse virksomheder. Særlige tilfælde fastsættes i bekendtgørelsen." {
		t.Errorf("text = %q", got)
	}
}

func TestAppendAddsTextWithoutDisturbingExistingSpans(t *testing.T) {
	p := threeStykke()
	if !p.Append("Loven træder i kraft den 1. januar 2027.") {
		t.Fatal("Append refused a non-empty passage")
	}
	if got := p.SpanText(p.Stykker[2].Span); got != "Særlige tilfælde fastsættes i bekendtgørelsen." {
		t.Errorf("existing span text changed: %q", got)
	}
	if got := p.Text(); !strings.HasSuffix(got, "1. januar 2027.") {
		t.Errorf("appended text did not reach the provision text: %q", got)
	}
	if p.Append("") {
		t.Error("Append accepted empty text, which would add a segment meaning nothing")
	}
}
