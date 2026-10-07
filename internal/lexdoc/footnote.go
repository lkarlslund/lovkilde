package lexdoc

import (
	"fmt"
	"regexp"
	"strings"
)

// The footnote under a law's title.
//
// Danish law records which EU directives it implements in a note on its title, and amending acts
// keep that note current: "Fodnoten til lovens titel affattes således", "I fodnoten til lovens titel
// indsættes efter »X«: »Y«", "Som fodnote til lovens titel indsættes". Over the 799-law census these
// were the largest class of changes with no § in their address. The note is the title's text from
// the segment tagged with a note id to the end of the title.

// titleNote is the range of Title the footnote occupies, or false when the title has none.
func (c *Content) titleNote() (Span, bool) {
	for i, s := range c.Title {
		if s.Note != "" {
			return Span{From: i, To: len(c.Title)}, true
		}
	}
	// An older payload prints the note's marker, "1)", as a line of the title with the note's
	// text after it, and nothing tags either.
	for i := 1; i+1 < len(c.Title); i++ {
		if reTitleNoteMarker.MatchString(c.Title[i].Text) {
			return Span{From: i, To: len(c.Title)}, true
		}
	}
	return Span{}, false
}

// reTitleNoteMarker is a footnote's marker printed as a line of its own: "1)".
var reTitleNoteMarker = regexp.MustCompile(`^\s*[0-9]{1,2}\)\s*$`)

// reNewTitleNote is a change bringing a title its footnote: "Som fodnote til lovens titel indsættes".
var reNewTitleNote = regexp.MustCompile(`(?i)^\s*som\s+(?:ny\s+)?fodnote\s+til\s+[^»]*titel\s+indsættes`)

// applyFootnote carries out a change to the title's footnote.
func (c *Content) applyFootnote(e Edit, reject func(string), apply func(where, note string)) {
	const where = "fodnoten til titlen"
	sp, has := c.titleNote()
	if e.Kind == EditInsertText && !has && reNewTitleNote.MatchString(e.Definition) {
		// "Som fodnote til bekendtgørelsens titel indsættes: »…«" read as words to insert: there is
		// nothing to insert them into, and the change brings the note itself.
		e.Kind = EditInsertProvision
	}
	switch e.Kind {
	case EditInsertProvision:
		if has {
			reject("the title already has a footnote, and the change adds one")
			return
		}
		text := e.blockString()
		if text == "" {
			reject("the new footnote carries no text")
			return
		}
		c.Title = append(c.Title, Segment{Text: text, Note: "title-note", Break: true})
		apply(where, "")
		return
	}
	if !has && e.Kind == EditReplaceProvision {
		// "Fodnoten til lovens titel affattes således" says what the note reads from now on,
		// whatever the mirror held: a title printed without its note gets the new one.
		if text := e.blockString(); text != "" {
			c.Title = append(c.Title, Segment{Text: text, Note: "title-note", Break: true})
			apply(where, "the mirror's title carried no footnote; the note the change gives it is set")
			return
		}
	}
	if !has {
		reject("the title has no footnote")
		return
	}
	switch e.Kind {
	case EditReplaceProvision:
		text := e.blockString()
		if text == "" {
			reject("the new footnote carries no text")
			return
		}
		note := c.Title[sp.From].Note
		c.Title = append(c.Title[:sp.From:sp.From], Segment{Text: text, Note: note, Break: true})
		apply(where, "")
	case EditDeleteProvision:
		c.Title = c.Title[:sp.From:sp.From]
		apply(where, "")
	case EditReplaceText, EditDeleteText:
		old := QuoteStrip(e.Old)
		found, _, n := replaceIn(c.Title, sp, old, e.New, false)
		note := ""
		if !found || n == 0 {
			// A note quoting directives is long, and the act's quotation of it rarely spaces it
			// the way the mirror does.
			if f, _ := replaceLoose(c.Title, sp, old, e.New); f {
				found, n, note = true, 1, "the note prints the passage with different spacing; it occurs once"
			} else if printed, ok := fuzzyPrinted(SegmentsText(c.Title[sp.From:sp.To]), old); ok && countMatches(SegmentsText(c.Title[sp.From:sp.To]), printed) == 1 {
				if f, _, k := replaceSelected(c.Title, sp, printed, e.New, pickFirst); f && k > 0 {
					found, n, note = true, 1, "the note prints the passage with other punctuation or hyphenation; it occurs once"
				}
			} else if printed, ok := typoPrinted(SegmentsText(c.Title[sp.From:sp.To]), old); ok && countMatches(SegmentsText(c.Title[sp.From:sp.To]), printed) == 1 {
				// »Kommissionens direktiv 2004/102/EF« quoted where the note prints "Kommissions
				// direktiv": the act's copy of a long note is not always letter for letter.
				if f, _, k := replaceSelected(c.Title, sp, printed, e.New, pickFirst); f && k > 0 {
					found, n, note = true, 1, "the note prints the passage with a word spelled differently; it occurs once"
				}
			}
		}
		if !found || n == 0 {
			reject(fmt.Sprintf("quoted passage not present in %s", where))
			return
		}
		apply(where, note)
	case EditInsertText:
		anchor, before := insertionAnchorText(e)
		if anchor == "" {
			if reAsLastPart.MatchString(e.Definition) {
				// "I fodnoten til lovens titel indsættes som sidste led: », dele af …«": the note's
				// last words, before its closing stop.
				for k := sp.To - 1; k >= sp.From && k >= 0 && k < len(c.Title); k-- {
					text := strings.TrimRight(c.Title[k].Text, " \u00a0\n\t")
					if text == "" {
						continue
					}
					add := strings.TrimSpace(QuoteStrip(e.New))
					// "… (direktiv om forsikringsformidling). Se endvidere bilag 9": the remark
					// after the list is no part of it.
					tail := ""
					if m := reSeeAlsoTail.FindStringIndex(text); m != nil && m[0] > 0 {
						text, tail = strings.TrimRight(text[:m[0]], " \u00a0\n\t"), text[m[0]:]
					}
					if strings.HasPrefix(add, ",") && strings.HasSuffix(text, ".") && !strings.HasSuffix(add, ".") {
						c.Title[k].Text = strings.TrimSuffix(text, ".") + add + "." + tail
					} else {
						c.Title[k].Text = joinInsertion(text, add, false) + tail
					}
					apply(where, "added as the note's last part")
					return
				}
			}
			c.Title = append(c.Title, Segment{Text: " " + e.New})
			apply(where, "insertion point within the note is approximate; the source states it in prose")
			return
		}
		if found, _, n := replaceIn(c.Title, sp, anchor, joinInsertion(anchor, e.New, before), false); !found || n == 0 {
			text := SegmentsText(c.Title[sp.From:sp.To])
			printed, ok := looseMatch(text, anchor)
			if f := false; ok {
				f, _ = replaceLoose(c.Title, sp, anchor, joinInsertion(printed, e.New, before))
				if f {
					apply(where, "the note prints the passage with different spacing; it occurs once")
					return
				}
			}
			// "De Cock& Man in't Veld" quoted where the note prints "De Cock & Man in´t Veld".
			if printed, ok = fuzzyPrinted(text, anchor); !ok {
				printed, ok = typoPrinted(text, anchor)
			}
			if ok && countMatches(text, printed) == 1 {
				if f, _, k := replaceSelected(c.Title, sp, printed, joinInsertion(printed, e.New, before), pickFirst); f && k > 0 {
					apply(where, "the note prints the passage with other punctuation or spelling; it occurs once")
					return
				}
			}
			reject(fmt.Sprintf("the passage the insertion is placed by is not present in %s", where))
			return
		}
		apply(where, "")
	default:
		reject("change wording not recognised by the pattern set")
	}
}

// reInsertAnchor is "indsættes efter »X«" or "før »X«": the insertion names the words it goes by.
var reInsertAnchor = regexp.MustCompile(`(?i)(efter|før|foran)\s*»([^«]+)«[^»]*indsættes|indsættes\s*:?\s+(?:i\s+afsnit(?:tet)?\s+»[^«]+«\s+)?(?:[^»]{0,40}?\s)?(efter|før|foran)\s*:?\s*»([^«]+)«`)

// reAsLastPart is an insertion at the end of a unit: "indsættes som sidste led".
var reAsLastPart = regexp.MustCompile(`(?i)\bindsættes\s+som\s+(?:nyt\s+)?sidste\s+led\b`)

// reSeeAlsoTail is the remark closing a note after its list: "Se endvidere bilag 9".
var reSeeAlsoTail = regexp.MustCompile(`(?s)\s*\n[\s\n]*Se\s+(?:endvidere|også)\b.*$`)

// reSignOutside is "efter § »3, stk. 1-3,«": the section sign of the passage typed outside its
// quotation.
var reSignOutside = regexp.MustCompile(`(?i)\b(efter|før|foran)\s+§\s*»`)

// reBareAnchor is "indsættes efter m.v.: »,«": a short unquoted passage the new words follow.
var reBareAnchor = regexp.MustCompile(`(?i)\bindsættes\s+efter\s+([^»:§0-9]{2,20}?)\s*:\s*»[^«]*«\s*\.?\s*$`)

// reUnitWord is a word naming a unit of text rather than words in it: "efter litra a:" places by
// the unit.
var reUnitWord = regexp.MustCompile(`(?i)\b(?:pkt|punkt|punktum|stk|nr|litra|afsnit|bilag|kapitel|note|tabel|række|kolonne|overskrift)\b`)

// reAnchorAsQuote is "indsættes »efter bemyndigelse«: »…«": the passage the new words follow
// quoted with the word "efter" inside it.
var reAnchorAsQuote = regexp.MustCompile(`(?i)\bindsættes\s+»efter\s+([^«]+)«\s*:\s*»[^«]*«\s*\.?\s*$`)

// InsertionAnchor is the quoted passage an insertion is placed by, or "" when it names none.
func InsertionAnchor(e Edit) string {
	a, _ := insertionAnchorText(e)
	return a
}

// insertionAnchorText is the quoted passage an insertion is placed by, and whether it goes in
// front of it. Empty when the change names no passage.
func insertionAnchorText(e Edit) (anchor string, before bool) {
	if e.Anchor != "" {
		return e.Anchor, e.AnchorBefore
	}
	def := reSignOutside.ReplaceAllString(e.Definition, "$1 »§ ")
	m := reInsertAnchor.FindStringSubmatch(def)
	if m == nil {
		if m := reBareAnchor.FindStringSubmatch(def); m != nil && !reUnitWord.MatchString(m[1]) {
			// "I § 1, stk. 1, nr. 13, indsættes efter m.v.: »,«": the words it goes by unquoted.
			return Squeeze(m[1]), false
		}
		if m := reAnchorAsQuote.FindStringSubmatch(def); m != nil {
			// "I indledningen indsættes »efter bemyndigelse«: »i henhold til …«": the word
			// "efter" typed inside the quotation of the words the new ones follow.
			return Squeeze(m[1]), false
		}
		return "", false
	}
	word, anchor := m[1], m[2]
	if word == "" {
		word, anchor = m[3], m[4]
	}
	w := strings.ToLower(word)
	return Squeeze(anchor), w == "før" || w == "foran"
}

// joinInsertion places new words next to their anchor the way the act writes them: an insertion
// that brings its own leading comma or space attaches to the word before it as given, and one that
// does not gets a space.
func joinInsertion(anchor, add string, before bool) string {
	if before {
		if strings.HasSuffix(add, " ") {
			return add + anchor
		}
		return add + " " + anchor
	}
	if add == "" {
		return anchor
	}
	switch add[0] {
	case ',', '.', ';', ':', ')', ' ':
		return anchor + add
	}
	return anchor + " " + add
}
