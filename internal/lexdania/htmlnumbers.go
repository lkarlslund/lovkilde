package lexdania

import (
	"regexp"
	"strings"
	"unicode"
)

// promotePrintedParagraphNumbers moves a provision's printed section number out of its prose and
// into the Explicatus marker every other body shape carries.
//
// The register's own XML puts "§ 267." in <Explicatus> beside the text. The site's HTML bodies —
// the payload the crawler recovers for documents whose XML carries no body at all — print it as
// the first words of the paragraph instead, usually inside a bold inline run:
//
//	<DIV CLASS=PARAGRAF ID="P6"><P class="Paragraftekst"><B>§ 6.</B> Arbejdsformidlingen …</P>
//
// Nothing downstream can recover the number from that shape, because both Sections() and the model
// read the marker. Over a 2,016-document sample of the mirror, 236 of the 750 documents that have
// provisions gave *no* provision a number at all — 4,587 of 11,402 provisions, 37% of the love
// shelf — so every one of those hits has a text but no address, and an amendment naming "§ 6"
// finds nothing in a law that prints § 6 in its own body.
//
// Moving the number to where the XML puts it is what makes the two bodies one shape rather than
// two, which is the whole claim of reading HTML as the same model as XML.
func promotePrintedParagraphNumbers(n *Node) {
	if n == nil {
		return
	}
	if printedNumberedProvision[n.Name] && !hasSectionMarker(n) {
		if printed := takeLeadingParagraphNumber(n); printed != "" {
			// A Node interleaves text and children -- len(Runs) is len(Elems)+1 -- so the marker
			// goes in with an empty text run beside it, exactly as attachBody does.
			n.Elems = append([]*Node{{Name: "Explicatus", Runs: []string{printed}}}, n.Elems...)
			n.Runs = append([]string{""}, n.Runs...)
		}
	}
	for _, c := range n.Elems {
		promotePrintedParagraphNumbers(c)
	}
}

// printedNumberedProvision lists the provision elements whose number is at stake. A
// TilParagraf is deliberately absent: it is prose attached to a paragraph rather than a numbered
// provision, and inventing a number for it would name a provision that is not citable.
var printedNumberedProvision = map[string]bool{
	"Paragraf":                true,
	"IkraftCentreretParagraf": true,
}

// hasSectionMarker reports whether the element already carries an Explicatus that is its section
// number. It has to be the marker's first word: a numbered item inside the provision is emitted as
// an Explicatus too, and its text quotes the provisions it refers to ("… jf. § 2, nr. 5 …"), which
// would otherwise count as this provision having a number of its own and leave the model reading
// its number out of the item -- over a 1,000-document sample that gave five provisions in one
// bekendtgørelse the number 1, including its § 3, § 4, § 6 and § 11.
func hasSectionMarker(n *Node) bool {
	for _, e := range n.ElemsNamed("Explicatus") {
		if strings.HasPrefix(strings.TrimSpace(strings.ReplaceAll(e.Text(), "\u00a0", " ")), "§") {
			return true
		}
	}
	return false
}

// takeLeadingParagraphNumber cuts a printed "§ 6." off the front of a provision's text, wherever
// in its subtree that text starts, and returns what was cut. A Node interleaves its own text runs
// with its children -- Runs[i] sits before Elems[i] -- so the walk has to visit them in that order
// rather than reading all of a node's runs first: the number lives in the child and the prose
// begins in the run after it.
//
// It stops at the first text that is not a section number: a provision whose prose does not begin
// with a section sign has no printed number, and searching deeper would harvest a citation from
// the middle of the text and call it the provision's name.
func takeLeadingParagraphNumber(n *Node) string {
	if n == nil {
		return ""
	}
	for i, run := range n.Runs {
		if strings.TrimSpace(run) != "" {
			printed, rest, ok := cutParagraphNumber(run)
			if !ok {
				return ""
			}
			n.Runs[i] = rest
			return printed
		}
		if i >= len(n.Elems) {
			continue
		}
		if printed := takeLeadingParagraphNumber(n.Elems[i]); printed != "" {
			return printed
		}
		if strings.TrimSpace(n.Elems[i].PlainText()) != "" {
			return ""
		}
	}
	return ""
}

// rePrintedParagraphNumber matches the section number an HTML body prints as the first words of
// its provision: a section sign, the number, and an optional litra. The corpus separates sign from
// number with a non-breaking space, which is why the classes are not just \s. Whether what follows
// makes this the provision's own number is cutParagraphNumber's decision, because the two shapes
// that matter differ in what comes next rather than in what comes first.
var rePrintedParagraphNumber = regexp.MustCompile(`^[\s\x{00a0}]*§[\s\x{00a0}]*(\d{1,4})([\s\x{00a0}]?[a-zæøåA-ZÆØÅ]?)`)

// cutParagraphNumber splits a leading printed number from the text that follows it. Two shapes
// prove the number is the provision's own:
//
//	§ 6. Arbejdsformidlingen kan benyttes …   the printed number, closed by its period
//	§ 5                                       a number that is the whole of its element
//
// Anything else is a citation rather than a name: "§ 3 finder ikke anvendelse" and "§ 3, stk. 2
// finder …" begin with a reference to another provision, and naming this one by that reference
// would aim an amendment at the wrong text. Legacy bodies do print both shapes, which is why the
// period is required when prose follows in the same run.
func cutParagraphNumber(s string) (printed, rest string, ok bool) {
	m := rePrintedParagraphNumber.FindString(s)
	if m == "" {
		return "", s, false
	}
	after := strings.TrimLeft(s[len(m):], " \u00a0")
	if !strings.HasPrefix(after, ".") && strings.TrimFunc(s[len(m):], unicode.IsSpace) != "" {
		return "", s, false
	}
	if strings.HasPrefix(after, ".") {
		m = m + "."
		after = after[1:]
	}
	return strings.TrimSpace(m), after, true
}
