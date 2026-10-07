package lexdoc

import (
	"fmt"
	"maps"
	"regexp"
	"sort"
	"strings"
	"unicode"
)

// Annexes an amending act prints itself.
//
// "Bilag 1 affattes som bilag 1 til denne bekendtgørelse", "Som bilag 4 og bilag 5 indsættes bilag
// 1 og bilag 2 til denne lov", "Bilag 1, 2 og 3 affattes som bilagene til denne bekendtgørelse": the
// new annex is one of the amending act's own, and the change says which of its annexes becomes which
// of the law's. Both front ends find the act's annex — the XML as an element, a payload as the lines
// under "Bilag 2" — and attach it to the change; this file reads which goes where.

// AnnexPair is one of the law's annexes and the act's annex that becomes it.
type AnnexPair struct {
	Target int    // the law's annex number
	Source int    // the act's annex number; 0 with Only for "bilaget til denne lov"
	Only   bool   // the act's only annex
	Insert bool   // a new annex rather than a replacement
	Litra  string // "som bilag 2 a": the letter after the law's annex number
}

var (
	reAnnexReplace = regexp.MustCompile(`(?i)^\s*bilag(?:ene)?\s+([0-9]+(?:\s*(?:,|og)\s*(?:bilag\s+)?[0-9]+)*)\s+affattes\s+som\s+(?:i\s+)?(bilag(?:et|ene)?)\s*([0-9]+(?:\s*(?:,|og)\s*(?:bilag\s+)?[0-9]+)*)?\s+(?:til|i)\s+denne\b`)
	reAnnexInsert  = regexp.MustCompile(`(?i)^\s*(?:(?:efter|før)\s+bilag\s+[0-9]+\s+indsættes\s+)?som\s+bilag\s+([0-9]+(?:\s+[a-z]\b)?(?:\s*(?:,|og)\s*(?:bilag\s+)?[0-9]+(?:\s+[a-z]\b)?)*)\s*(?:indsættes\s+)?(bilag(?:et|ene)?)\s*([0-9]+(?:\s*(?:,|og)\s*(?:bilag\s+)?[0-9]+)*)?\s+(?:til|i)\s+denne\b`)
	reAnnexTarget  = regexp.MustCompile(`([0-9]+)(?:\s+([a-z])\b)?`)
	// reAnnexRenumber is the law's annex an insertion moves on: "Bilag 1 bliver herefter bilag 5".
	reAnnexRenumber = regexp.MustCompile(`(?i)\bbilag\s+([0-9]+)\s+bliver\s+herefter\s+(?:til\s+)?bilag\s+([0-9]+)\b`)
	reNumbers       = regexp.MustCompile(`[0-9]+`)
)

// AnnexPairs reads which of an act's annexes become which of the law's.
func AnnexPairs(definition string) []AnnexPair {
	def := Squeeze(definition)
	m := reAnnexReplace.FindStringSubmatch(def)
	insert := false
	if m == nil {
		if m = reAnnexInsert.FindStringSubmatch(def); m == nil {
			return nil
		}
		insert = true
	}
	targets := reAnnexTarget.FindAllStringSubmatch(m[1], -1)
	sources := reNumbers.FindAllString(m[3], -1)
	word := strings.ToLower(m[2])
	var out []AnnexPair
	for i, t := range targets {
		p := AnnexPair{Target: Atoi(t[1]), Litra: strings.ToLower(t[2]), Insert: insert}
		switch {
		case i < len(sources):
			p.Source = Atoi(sources[i])
		case word == "bilaget" && len(targets) == 1:
			p.Only = true
		case word == "bilagene":
			p.Source = i + 1 // "som bilagene til denne": the act's annexes in order
		default:
			return nil // more of the law's annexes than the sentence names of the act's
		}
		out = append(out, p)
	}
	return out
}

// reAnnexHeader is a line that opens an annex: "Bilag 2", "Bilag 2.", "Bilag 2 Satser …".
var reAnnexHeader = regexp.MustCompile(`(?i)^bilag\s+([0-9]+)\b\.?`)

// hasAnnex reports an annex numbered n, with litra when it has one: bilag 2 is not bilag 2 a.
func (c *Content) hasAnnex(n int, litra string) bool {
	want := itoa(n) + litra
	for _, ap := range c.Appendices {
		num := strings.ToLower(strings.ReplaceAll(ap.Number, " ", ""))
		if num == "" {
			num = strings.ToLower(strings.ReplaceAll(strings.TrimPrefix(Squeeze(ap.Label), "Bilag "), " ", ""))
		}
		if num == want {
			return true
		}
	}
	return false
}

// renumberAnnex moves the law's annex from to number to, as "Bilag 1 bliver herefter bilag 5" says
// before new annexes take its place. It reports whether it did.
func (c *Content) renumberAnnex(from, to int) bool {
	var src *Appendix
	for i := range c.Appendices {
		switch FirstNumber(c.Appendices[i].Number + c.Appendices[i].Label) {
		case itoa(to):
			return false // already moved, or the number is taken
		case itoa(from):
			if c.Appendices[i].Number == itoa(from) {
				src = &c.Appendices[i]
			}
		}
	}
	if src == nil {
		return false
	}
	src.Number, src.Label = itoa(to), "Bilag "+itoa(to)
	if len(src.Segments) > 0 && reAnnexHeader.MatchString(Squeeze(src.Segments[0].Text)) {
		src.Segments[0].Text = reAnnexHeader.ReplaceAllString(Squeeze(src.Segments[0].Text), "Bilag "+itoa(to))
	}
	sort.SliceStable(c.Appendices, func(i, j int) bool {
		return Atoi(FirstNumber(c.Appendices[i].Number+c.Appendices[i].Label)) < Atoi(FirstNumber(c.Appendices[j].Number+c.Appendices[j].Label))
	})
	return true
}

// AnnexFromLines is the act's own annex n, read from its text: the lines from the one that opens
// "Bilag n" to the next annex or the end. The act's change list cites annexes too ("affattes som
// bilag 1 til denne"), so only a header line counts, and the last one with that number is taken:
// annexes print after everything that cites them.
func AnnexFromLines(lines []string, n int, only bool) *Appendix {
	start := -1
	headers := 0
	for i, l := range lines {
		m := reAnnexHeader.FindStringSubmatch(Squeeze(l))
		if m == nil || len(Squeeze(l)) > 120 || strings.Contains(l, "»") || reChangeVerb.MatchString(l) {
			continue // "Bilag 2, del E, ændres således:" inside the act's own annex is a change
		}
		headers++
		if only || Atoi(m[1]) == n {
			start = i
		}
	}
	if start < 0 || only && headers != 1 {
		return nil
	}
	ap := &Appendix{Number: itoa(n), Label: Squeeze(lines[start])}
	depth := 0 // an annex the act's annex quotes, "»Bilag 9 …«", is no annex of the act's own
	for _, l := range lines[start+1:] {
		if depth <= 0 && reAnnexHeader.MatchString(Squeeze(l)) && len(Squeeze(l)) <= 120 && !strings.Contains(l, "»") && !reChangeVerb.MatchString(l) {
			break
		}
		depth += strings.Count(l, "»") - strings.Count(l, "«")
		if strings.EqualFold(Squeeze(l), "Officielle noter") {
			break // the act's own endnotes, printed after its last annex
		}
		ap.Segments = append(ap.Segments, Segment{Text: Squeeze(l), Break: true})
	}
	if len(ap.Segments) == 0 {
		return nil
	}
	return ap
}

// setAppendix puts an annex in place as the law's annex number n, replacing the one there or adding
// it in number order.
func (c *Content) setAppendix(n int, litra string, ap Appendix, insert bool) (replaced bool) {
	ap.Number = itoa(n) + litra
	if ap.Label == "" || FirstNumber(ap.Label) != itoa(n) || litra != "" {
		ap.Label = "Bilag " + itoa(n)
		if litra != "" {
			ap.Label += " " + litra
		}
	}
	for i := range c.Appendices {
		if strings.EqualFold(strings.ReplaceAll(c.Appendices[i].Number, " ", ""), ap.Number) || litra == "" && FirstNumber(c.Appendices[i].Number+c.Appendices[i].Label) == ap.Number && !strings.ContainsAny(c.Appendices[i].Number, "abcdefghijklmnopqrstuvwxyz") {
			if insert {
				return false
			}
			c.Appendices[i] = ap
			return true
		}
	}
	c.Appendices = append(c.Appendices, ap)
	sort.SliceStable(c.Appendices, func(i, j int) bool {
		return Atoi(FirstNumber(c.Appendices[i].Number+c.Appendices[i].Label)) < Atoi(FirstNumber(c.Appendices[j].Number+c.Appendices[j].Label))
	})
	return false
}

// Parts of an annex.
//
// "Bilag 4, nr. 2, affattes som bilag 1 til denne bekendtgørelse", "Bilag 1, punkt 2.1, affattes
// således: …", "Bilag 2, pkt. 29, ophæves", "I bilag 4, Særlige krav …, affattes nr. 17 som bilag 1",
// "Bilag 2, kapitel 6, punkt 24, affattes således". Annexes are prose and flattened tables to the
// model, so a part is found by the label it prints at the head of a line, inside whatever the change
// names around it, and it runs to the next line printing a label of the same shape. Anything that
// does not resolve to exactly one such line is refused rather than guessed: a wrong span in an
// annex is wrong law nobody would notice.

// AnnexPartChange is a change to one labelled part of one or more of the law's annexes.
type AnnexPartChange struct {
	Targets []int    // the law's annexes
	Scopes  []string // outer labels or heading words the part sits under, outermost first
	Label   string   // the part's own label: "2", "4.2", "Tabel 2", "Afsnit B"
	LabelTo string   // the last part of a run the change names together: "punkt 57 og 58"
	Also    []string // further parts given the same text: 4.5 and 5.5 in "nr. 3.5., 4.5., og 5.5."
	Delete  bool     // repealed, rather than replaced
	Insert  bool     // new text placed after Label (or at the end of the scope when Label is empty)
	Before  bool     // an insertion placed before Label rather than after it: "indsættes før nr. 1"
	Heading string   // the heading the change names beside Label: "afsnit 4. Opgørelse af …"
	Verify  bool     // the part must print Heading: it is all that names it
	Source  int      // the act's annex holding the new text, or 0 for text the change brings itself
	// Sentence is the sentence of the part a repeal names: "punkt 67, 2. pkt., ophæves". Read
	// without it, the whole of punkt 67 went.
	Sentence int
	// HeadingOnly is a change to the part's heading line alone: "regel 19-1 affattes overskriften
	// således". Read as the part, the rule's whole text went for its new heading.
	HeadingOnly bool
	// Paragraph is the ordinal of "2. afsnit": a part labelled "2." where the annex prints one, and
	// otherwise the paragraph the new text takes the place of.
	Paragraph int
	// NewLabel is the label an insertion gives its new part when it names no part to place it by:
	// "som nyt punkt 2.6 indsættes". It goes in front of the 2.6 the annex prints, which becomes
	// 2.7, or after 2.5 when there is no 2.6.
	NewLabel string
	// Anchor is the quoted text an insertion goes after (or before, with Before) inside its scope:
	// "I FELT03 Ekspeditionstype indsættes efter »… DM«: »…«". Unread, the new rows went at the
	// end of the scope.
	Anchor string
}

var (
	reAnnexList      = regexp.MustCompile(`(?i)\bbilag(?:ene)?\s+([0-9]+(?:\s*(?:,|og|-|–)\s*(?:bilag\s+)?[0-9]+)*)`)
	reAnnexBecomes   = regexp.MustCompile(`(?i),?\s*(?:der|som)\s+bliver\s+(?:til\s+)?(?:pkt\.|punkt|nr\.)\s*[0-9]+(?:\.[0-9]+)*\s*[a-z]?\b`)
	reSentenceEnd    = regexp.MustCompile(`\.\s+[A-ZÆØÅ]`)
	reAnotherLabel   = regexp.MustCompile(`(?i)\s(?:og|,)\s*(?:[0-9]+(?:\.[0-9]+)*|[A-Z])\b`)
	reAnnexPartToken = regexp.MustCompile(`(?i)\b(?:nr\.|pkt\.|punkt|note)\s*([0-9]+(?:\.[0-9]+)*\.?(?:\s*[a-hj-z](?:[^\p{L}]|$))?|\([0-9]+\))|\b(tabel\s+[0-9]+)|\b(?:afsnit|del|kapitel|kap\.)\s+([0-9]+(?:\.[0-9]+)*|[A-Z]\b|[IVXLC]+\b)|\b([0-9]+)\.\s*afsnit`)
	reAnnexVerb      = regexp.MustCompile(`(?i)\b(affattes|ophæves|udgår|indsættes|tilføjes)\b`)
	reAnnexFromAct   = regexp.MustCompile(`(?i)som\s+(?:i\s+|nyt?\s+[^»,]{1,30}?\s+|(?:nr\.|pkt\.|afsnit)\s*[0-9][0-9.]*\s*[a-z]?(?:\s*(?:og|-|–)\s*(?:[0-9][0-9.]*\s*)?[a-z]?)?\s*,?\s*)?bilag\s+([0-9]+)\s+(?:til|i)\s+denne\b`)
)

// ParseAnnexPart reads a change to a labelled part of an annex, or reports that it is not one.
func ParseAnnexPart(definition string) (AnnexPartChange, bool) {
	// "pkt. 77, der bliver pkt. 76, ophæves": the annex still prints 77, whatever the act calls it
	// after its own repeals.
	def := reAnnexBecomes.ReplaceAllString(Squeeze(definition), "")
	def = reSplitBilag.ReplaceAllString(def, "${1}ilag")
	// "indsættes efter nr. 10. a, nr. 10.b": the entry printed "10.a", its letter typed apart.
	def = reSpacedEntryLetter.ReplaceAllString(def, "nr. $1.$2,")
	// "indsættes efter »94)«": a part's label quoted.
	def = reQuotedLabelAfter.ReplaceAllString(def, "efter nr. $1")
	// "I Bilag 1 under punktet »Talhenvisninger« affattes 13) således": the quoted heading is
	// where the part sits, and the label after the verb is the part.
	def = reUnderHeading.ReplaceAllString(def, ", $1,")
	// "punkt 1, andet afsnit": the paragraph by its ordinal in words.
	def = reOrdinalWordAfsnit.ReplaceAllStringFunc(def, func(w string) string {
		f := strings.Fields(strings.ToLower(w))
		return itoa(ordinalWords[f[0]]) + ". afsnit"
	})
	newLabel := ""
	if m := reAsNewPart.FindStringSubmatchIndex(def); m != nil && reInsertVerb.MatchString(def) {
		// "Som nyt punkt 2.6 indsættes:" names the new part, not a place; read as one, it was a
		// scope, and the new point went at the end of 2.6. "I punkt 1 tilføjes litra g)" too.
		newLabel = strings.ReplaceAll(def[m[2]:m[3]], " ", "")
		keep := " "
		if strings.Contains(strings.ToLower(def[m[0]:m[1]]), "litra") {
			newLabel = litraLabel(strings.TrimRight(newLabel, ")"))
		}
		if v := reInsertVerb.FindString(def[m[0]:m[1]]); v != "" {
			keep = " " + v + " " // "tilføjes litra g)": the verb stays, the new part's label goes
		}
		def = def[:m[0]] + keep + def[m[1]:]
	}
	labelAfter := ""
	if m := reLabelAfterVerb.FindStringSubmatch(def); m != nil {
		labelAfter = m[1]
		def = strings.Replace(def, m[0], m[0][:len(m[0])-len(m[1])-len(" således")]+"således", 1)
	}
	head := def
	if i := strings.Index(head, "»"); i >= 0 {
		head = head[:i]
	}
	// The new text follows a colon after the verb; "tabel 1: Opgørelse af …" before it is a name.
	if v := reAnnexVerb.FindStringIndex(head); v != nil {
		if j := strings.Index(head[v[1]:], ":"); j >= 0 {
			head = head[:v[1]+j]
		}
	} else if i := strings.Index(head, ":"); i >= 0 {
		head = head[:i]
	}
	verb := reAnnexVerb.FindStringIndex(head)
	if verb == nil {
		return AnnexPartChange{}, false
	}
	// The part's location ends with its own sentence: "Pkt. 30-76 bliver herefter pkt. 29-75"
	// after it is a renumbering, not a place.
	if end := reSentenceEnd.FindStringIndex(head[verb[1]:]); end != nil {
		head = head[:verb[1]+end[0]+1]
	}
	list := reAnnexList.FindStringSubmatchIndex(head)
	if list == nil || strings.Contains(head[:verb[0]], "§") {
		return AnnexPartChange{}, false
	}
	var ch AnnexPartChange
	listText := head[list[2]:list[3]]
	for _, loc := range reNumbers.FindAllStringIndex(listText, -1) {
		if after := listText[loc[1]:]; strings.HasPrefix(after, ".") || loc[1] == len(listText) && strings.HasPrefix(head[list[3]:], ".") {
			// "Bilag 1, 4. afsnit": an ordinal, not a second annex.
			list[3] = list[2] + loc[0]
			break
		}
		n := Atoi(listText[loc[0]:loc[1]])
		if k := len(ch.Targets); k > 0 && strings.ContainsAny(strings.TrimSpace(listText[:loc[0]])[max(0, len(strings.TrimSpace(listText[:loc[0]]))-3):], "-–") {
			// "bilag 1-4": every annex in the range.
			for m := ch.Targets[k-1] + 1; m < n && m-ch.Targets[k-1] < 50; m++ {
				ch.Targets = append(ch.Targets, m)
			}
		}
		ch.Targets = append(ch.Targets, n)
	}
	if len(ch.Targets) == 0 {
		return AnnexPartChange{}, false
	}
	v := strings.ToLower(head[verb[0]:verb[1]])
	ch.Delete = v == "ophæves" || v == "udgår"
	ch.HeadingOnly = reHeadingWord.MatchString(head)
	if m := rePartSentence.FindStringSubmatch(head[:verb[0]]); m != nil {
		ch.Sentence = Atoi(m[1])
		head = strings.Replace(head, m[0], "", 1) // the sentence is no label of the part
		if verb = reAnnexVerb.FindStringIndex(head); verb == nil {
			return AnnexPartChange{}, false
		}
		if list = reAnnexList.FindStringSubmatchIndex(head); list == nil {
			return AnnexPartChange{}, false
		}
	}
	if ch.Delete && strings.Contains(strings.ToLower(def), "i stedet") {
		ch.Delete = false // "ophæves, og i stedet indsættes": a replacement
	}
	ch.Insert = v == "indsættes" || v == "tilføjes" // "I punkt 4.3 tilføjes: »12. …«"
	if ch.Insert {
		// After the verb, "efter pkt. 4.01.002" is the anchor and "som nyt punkt …" names the new
		// part; only the anchor is a place. Before the verb are the scopes.
		after := head[verb[1]:]
		anchor := ""
		m := reInsertAfterLabel.FindStringSubmatch(after)
		if b := reInsertBeforeLabel.FindStringSubmatch(after); b != nil && m == nil {
			m, ch.Before = b, true
		}
		if m != nil && reLitraPiece.MatchString(m[1]) {
			// "indsættes efter litra a som nyt litra": the part "a)".
			anchor = litraLabel(reLitraPiece.FindStringSubmatch(m[1])[1])
		} else if m != nil {
			if l := reAnnexPartToken.FindStringSubmatch(m[1]); l != nil {
				anchor = firstNonEmpty(l[1], l[2], l[3], l[4])
				if !strings.HasPrefix(anchor, "(") {
					anchor = strings.TrimRight(anchor, ",;:) ")
				}
				if l[4] != "" {
					anchor += "."
				}
				anchor = strings.TrimRight(strings.ReplaceAll(anchor, " ", ""), ".")
			}
		}
		if anchor == "" && reNamedAnchor.MatchString(after) {
			return AnnexPartChange{}, false // an anchor named but not read: not placed at the end instead
		}
		into := ""
		if m := reInsertInto.FindStringSubmatch(after); m != nil && anchor == "" {
			// "indsættes i afsnit II., Beskrivelser, nr. 2, et nyt litra g": where the new part
			// goes is named after the verb, and scopes it as the places before the verb do.
			into = " , " + m[1]
		} else if anchor == "" && reInsertIntoAny.MatchString(after) {
			return AnnexPartChange{}, false // a place named after the verb but not read
		}
		head = head[:verb[0]] + into + " ,"
		end := list[3] // the list as read above, its ordinal "1. Skema …" left out
		list = reAnnexList.FindStringSubmatchIndex(head)
		if list == nil {
			return AnnexPartChange{}, false
		}
		list[3] = min(list[3], end)
		ch.Label = anchor // held aside: the scope loop below reads only what precedes the verb
	}
	insertAnchor := ch.Label
	ch.Label = ""
	if ch.Insert && insertAnchor == "" {
		ch.NewLabel = newLabel
		if m := reQuotedAnchor.FindStringSubmatch(def); m != nil && newLabel == "" {
			ch.Anchor, ch.Before = Squeeze(m[2]), !strings.EqualFold(m[1], "efter")
		}
	}
	if m := reAnnexFromAct.FindStringSubmatch(def); m != nil {
		ch.Source = Atoi(m[1])
	}
	// Everything the sentence names outside "bilag N" and "som bilag M til denne" is where the part
	// is: labels, and the heading words of a section between commas.
	rest := head[:list[0]] + " , " + head[list[3]:]
	if m := reAnnexFromAct.FindStringIndex(rest); m != nil {
		rest = rest[:m[0]]
	}
	rest = reAnnexVerb.ReplaceAllString(rest, ",")
	// "I bilag 2, I. a. Insekter, mider og nematoder på alle udviklingsstadier indsættes": section I,
	// its group a), and the group's heading, whose own commas are no separators.
	rest = reRomanLitraHeading.ReplaceAllString(rest, ", afsnit $1, litra $2 ,")
	ruleParagraph := false
	for _, piece := range splitOutsideParens(rest) {
		piece = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(piece), "I "))
		piece = strings.TrimSpace(strings.TrimPrefix(piece, "i "))
		if piece == "" || strings.EqualFold(piece, "i") || strings.EqualFold(piece, "og") || reSelfReference.MatchString(piece) || strings.EqualFold(piece, "således") || strings.Contains(strings.ToLower(piece), "i stedet") {
			continue
		}
		if m := reListedLabel.FindStringSubmatch(piece); m != nil && len(ch.Scopes) > 0 && !ch.Insert {
			ch.Also = append(ch.Also, m[1]) // "nr. 3.5., 4.5., og 5.5.": the same change again
			if m[2] != "" {
				ch.Also = append(ch.Also, m[2])
			}
			continue
		}
		if reLetterLabel.MatchString(strings.TrimRight(piece, ".")) {
			ch.Scopes = append(ch.Scopes, strings.TrimRight(piece, ".")) // "Bilag 1, A, nr. 4": section A
			continue
		}
		if ruleParagraph && reAnnexPartToken.MatchString(piece) {
			return AnnexPartChange{}, false // "stk. 1, nr. 2": a rule prints its items ".2", not read
		}
		if reStkPiece.MatchString(piece) {
			if n := len(ch.Scopes); n == 0 || !strings.HasPrefix(ch.Scopes[n-1], "Regel ") {
				return AnnexPartChange{}, false // a subsection of the part, not read
			}
			// "regel 7-2, stk. 2-4": a rule prints its paragraphs numbered on lines of their own.
			piece = "nr." + piece[len("stk."):]
			ruleParagraph = true
		}
		if m := reNamedLabel.FindStringSubmatch(piece); m != nil {
			// "afsnit A-1, regel 3-13": SOLAS chapters and rules, printed "Afsnit A-1", "Regel 3-13".
			if m[1] != "" {
				ch.Scopes = append(ch.Scopes, "Afsnit "+m[2])
			} else {
				ch.Scopes = append(ch.Scopes, "Regel "+m[4])
			}
			continue
		}
		if m := reTableHeading.FindStringSubmatch(piece); m != nil {
			// "tabel 1: Opgørelse af antal dyr …": the table, and the heading it prints.
			ch.Scopes = append(ch.Scopes, "Tabel "+m[1])
			ch.Heading = strings.TrimSpace(m[2])
			continue
		}
		if reOrdinalHeading.MatchString(piece) && !reAnnexPartToken.MatchString(piece) {
			// "1. Skema for balance i kontoform (regnskabsklasse B, C og D)": a heading the annex
			// prints whole, its number and its parenthesis with it.
			ch.Scopes = append(ch.Scopes, "~"+piece)
			continue
		}
		if m := reLitraPiece.FindStringSubmatch(piece); m != nil {
			ch.Scopes = append(ch.Scopes, litraLabel(m[1])) // "litra e": the part "e)"
			continue
		}
		tokens := reAnnexPartToken.FindAllStringSubmatch(piece, -1)
		if m := reLabelRange.FindStringSubmatch(piece); m != nil && len(tokens) == 1 {
			// "nr. 1-5 affattes således": the run of parts from the first to the last.
			if ch.LabelTo != "" || !dottedBefore(m[1], m[2]) {
				return AnnexPartChange{}, false
			}
			ch.LabelTo = m[2]
		} else if len(tokens) == 1 && reAnotherLabel.MatchString(piece) {
			// "punkt 57 og 58", "del C og D": two parts that follow each other are one stretch.
			m := reAnotherLabel.FindStringSubmatch(piece)
			first := firstNonEmpty(tokens[0][1], tokens[0][2], tokens[0][3], tokens[0][4])
			second := strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(m[0]), "og,"))
			if !follows(strings.TrimRight(first, "."), second) || ch.LabelTo != "" {
				return AnnexPartChange{}, false
			}
			ch.LabelTo = second
		}
		for _, l := range tokens {
			switch {
			case strings.HasPrefix(l[1], "("):
				ch.Scopes = append(ch.Scopes, l[1])
			case l[1] != "":
				ch.Scopes = append(ch.Scopes, strings.TrimRight(strings.ReplaceAll(l[1], " ", ""), ".,;:)"))
			case l[2] != "":
				ch.Scopes = append(ch.Scopes, strings.Title(strings.ToLower(Squeeze(l[2]))))
			case l[3] != "" && reChapterToken.MatchString(l[0]):
				ch.Scopes = append(ch.Scopes, "Kapitel "+l[3]) // printed "Kapitel 6", not "6."
			case l[3] != "":
				ch.Scopes = append(ch.Scopes, l[3])
			case l[4] != "":
				ch.Scopes = append(ch.Scopes, l[4]+".")
				ch.Paragraph = Atoi(l[4]) // "tabel 3, 2. afsnit": the paragraph, if no part prints "2."
			}
		}
		if len(tokens) > 0 || reBareSubLabel.MatchString(piece) {
			// "pkt. 3.02.200 (5), c)": the parts inside the first are labels too, and one left
			// unread would put the change on the whole of the part around it.
			residue := reAnnexPartToken.ReplaceAllString(reLabelRange.ReplaceAllString(piece, " "), " ")
			if ch.LabelTo != "" {
				residue = reAnotherLabel.ReplaceAllString(residue, " ") // "punkt 57 og 58"
			}
			for _, m := range reBareSubLabel.FindAllStringSubmatch(residue, -1) {
				if m[1] != "" {
					ch.Scopes = append(ch.Scopes, "("+m[1]+")")
				} else {
					ch.Scopes = append(ch.Scopes, litraLabel(m[2]))
				}
			}
			rest := strings.TrimSpace(strings.Trim(strings.TrimSpace(reBareSubLabel.ReplaceAllString(residue, " ")), ".:"))
			if strings.ContainsAny(rest, "0123456789") {
				return AnnexPartChange{}, false
			}
			if len([]rune(rest)) > 8 && len(tokens) == 1 {
				ch.Heading = rest // "afsnit 4. Opgørelse af de estimerede betalinger …"
			}
			continue
		}
		if r := []rune(piece); len(r) > 0 && unicode.IsLower(r[0]) && !reAnnexPartToken.MatchString(piece) {
			// "afsnit II. Planteskadegørere, der vides at forekomme …": the rest of a heading the
			// comma split, not a heading of its own.
			continue
		}
		if !reAnnexPartToken.MatchString(piece) && len([]rune(piece)) > 8 && !strings.Contains(piece, "denne") {
			ch.Scopes = append(ch.Scopes, "~"+reSectionWord.ReplaceAllString(piece, "")) // heading words the part sits under
		}
	}
	// The innermost label is the part; anything before it scopes the search.
	for i := len(ch.Scopes) - 1; i >= 0; i-- {
		if !strings.HasPrefix(ch.Scopes[i], "~") {
			ch.Label = ch.Scopes[i]
			ch.Scopes = append(ch.Scopes[:i:i], ch.Scopes[i+1:]...)
			break
		}
	}
	if ch.Insert {
		if ch.Label != "" {
			ch.Scopes = append(ch.Scopes, ch.Label) // the labels before the verb all scope an insertion
		}
		ch.Label = insertAnchor
		return ch, true
	}
	// "I bilag 2, pkt. 59, ophæves litra a": a part named after the verb is inside the one before
	// it. Read without it, the whole of pkt. 59 was repealed.
	if m := reLitraAfterVerb.FindStringSubmatch(def); m != nil && ch.Delete && ch.Label != "" {
		ch.Scopes = append(ch.Scopes, ch.Label)
		ch.Label = litraLabel(m[1])
	}
	if ch.Label == "" && labelAfter != "" && !ch.Insert {
		ch.Label = labelAfter
	} else if labelAfter != "" && ch.Label != "" {
		ch.Scopes, ch.Label = append(ch.Scopes, ch.Label), labelAfter
	}
	if ch.Label == "" {
		return AnnexPartChange{}, false
	}
	return ch, true
}

// reQuotedAfterSaaledes is new text a change quotes in its own sentence: "affattes således: »…«".
var reQuotedAfterSaaledes = regexp.MustCompile(`(?i)således\s*:\s*»([^»«]+)«`)

// reUnderHeading is a part named by the heading it sits under: "under punktet »Talhenvisninger«".
var reUnderHeading = regexp.MustCompile(`(?i)\bunder\s+(?:punktet|afsnittet|overskriften)\s*»\s*([^»«]+?)\s*«`)

// reLabelAfterVerb is a part's label printed after its verb: "affattes 13) således".
var reLabelAfterVerb = regexp.MustCompile(`(?i)\baffattes\s+([0-9]+)\)\s+således`)

var (
	reQuotedLabelAfter = regexp.MustCompile(`(?i)\befter\s+»\s*([0-9]+)\)\s*«`)
)

// ParseAnnexPartRemoving reads "I bilag 9 afsnit Femårsoversigten skal endvidere indeholde følgende
// nøgletal udgår: 8. Solvensdækning", a repeal that names the part only by quoting it: its label
// and its heading, which the annex must print together for the part to be the one meant.
func ParseAnnexPartRemoving(definition string, quoted []Segment) (AnnexPartChange, bool) {
	if !reEndsRemoving.MatchString(Squeeze(definition)) || len(quoted) == 0 {
		return AnnexPartChange{}, false
	}
	m := reQuotedPart.FindStringSubmatch(Squeeze(SegmentsText(quoted)))
	if m == nil {
		return AnnexPartChange{}, false
	}
	ch, ok := ParseAnnexPart(strings.TrimRight(Squeeze(definition), ": ") + " nr. " + m[1] + ".")
	if !ok || !ch.Delete || ch.Label != m[1] {
		return AnnexPartChange{}, false
	}
	ch.Heading, ch.Verify = m[2], true
	return ch, true
}

var (
	reEndsRemoving = regexp.MustCompile(`(?i)\b(?:udgår|ophæves)\s*:$`)
	reQuotedPart   = regexp.MustCompile(`^([0-9]+)\.\s+(\S.{0,120})$`)
)

// litraLabel is the label a litra is found by: "e)" for "litra e", printed "e)" or "e."; "C" for
// "litra C", printed as the section "C.".
func litraLabel(l string) string {
	if reLetterLabel.MatchString(l) {
		return l
	}
	return strings.ToLower(l) + ")"
}

// reLabelRange is a run of numbered parts: "nr. 1-5", "pkt. 3–7".
var reLabelRange = regexp.MustCompile(`(?i)\b(?:nr\.|pkt\.|punkt|afsnit)\s*([0-9]+(?:\.[0-9]+)*)\s*[-–]\s*([0-9]+(?:\.[0-9]+)*)\b`)

// dottedBefore reports whether a label numbered "2.1" comes before "2.14", part by part.
func dottedBefore(a, b string) bool {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(pa) && i < len(pb); i++ {
		if x, y := Atoi(pa[i]), Atoi(pb[i]); x != y {
			return x < y
		}
	}
	return len(pa) < len(pb)
}

// reBareSubLabel is a part named without a word before it: "(5)", "c)".
var reBareSubLabel = regexp.MustCompile(`(?:^|\s)(?:\(([0-9]+)\)|([a-zæøåA-ZÆØÅ])\))`)

// reSelfReference is the part a sentence has already named, named again after its verb:
// "affattes afsnittet således".
var reSelfReference = regexp.MustCompile(`(?i)^(?:(?:afsnittet|punktet|nummeret|bilaget|teksten|skemaet|tabellen)(?:\s+således)?|til\s+(?:bekendtgørelsen|loven))$`)

// reSectionWord is the word a change puts before a section's heading: "afsnit Femårsoversigten
// skal …", "afsnit om beregning af nøgletal".
var reSectionWord = regexp.MustCompile(`(?i)^(?:afsnit(?:tet)?|punkt(?:et)?)\s+(?:om\s+)?`)

// reChapterToken is a chapter of an annex as a change names it: "kapitel 6", "kap. 2".
var reChapterToken = regexp.MustCompile(`(?i)^(?:kapitel|kap\.)\s`)

// reListedLabel is a further dotted label in a list of parts: "4.5.", "og 5.5. således".
var reListedLabel = regexp.MustCompile(`(?i)^(?:og\s+)?([0-9]+(?:\.[0-9]+)+)\.?(?:\s+og\s+([0-9]+(?:\.[0-9]+)+)\.?)?(?:\s+således)?$`)

// reLitraPiece is a lettered part named before the verb: "punkt 22, litra e, ophæves".
var reLitraPiece = regexp.MustCompile(`(?i)^litra\s+([a-zæøå])$`)

// reLitraLabel is a lettered part as an annex prints it before its ")".
var reLitraLabel = regexp.MustCompile(`^[a-zæøå]$`)

// reLitraAfterVerb is a lettered part named after the repealing verb: "ophæves litra a".
var reLitraAfterVerb = regexp.MustCompile(`(?i)\b(?:ophæves|udgår)\s+litra\s+([a-zæøå])\b`)

// reInsertBeforeLabel is an insertion's anchor it goes before: "før nr. 1".
var reInsertBeforeLabel = regexp.MustCompile(`(?i)\bfør\s+((?:nr\.|pkt\.|punkt|tabel|afsnit|litra)\s*[^\s,]+)`)

// reInsertAfterLabel is an insertion's anchor: "efter pkt. 4.01.002", "efter nr. 5".
var reInsertAfterLabel = regexp.MustCompile(`(?i)\befter\s+((?:nr\.|pkt\.|punkt|tabel|afsnit|litra)\s*[^\s,]+)`)

// wordsScope is the part of an annex a change names by its heading words, "i Kriterium 4
// Bedømmelse af modulet": from the line they begin on, the words read across up to three lines.
// A heading that opens with a word and a number, "Kriterium 4.", ends at the next such heading,
// "Kriterium 5."; any other ends where within does.
func wordsScope(segs []Segment, within Span, words string) (Span, bool) {
	want := alnumKey(words)
	if want == "" {
		return Span{}, false
	}
	for i := within.From; i < within.To; i++ {
		joined := ""
		for j := i; j < within.To && j < i+3; j++ {
			joined += alnumKey(segs[j].Text)
			if strings.HasPrefix(joined, want) || j == i && strings.Contains(joined, want) {
				out := Span{From: i, To: within.To}
				if m := wordNumber(Squeeze(segs[i].Text)); m != nil {
					// "Afsnit 3" ends at "Afsnit 4", and "FELT08 Identifikation af receptudsteder"
					// at "FELT09 Lægens CPR-nummer".
					for k := j + 1; k < within.To; k++ {
						if n := wordNumber(Squeeze(segs[k].Text)); n != nil && strings.EqualFold(n[0], m[0]) && n[1] != m[1] ||
							reCodeHeading.MatchString(Squeeze(segs[i].Text)) && reDottedNumber.MatchString(strings.TrimRight(Squeeze(segs[k].Text), ".")) && strings.Contains(Squeeze(segs[k].Text), ".") {
							// "FELT03 …", a field's code, ends at "3.4", the heading of the next
							// section, too.
							out.To = k
							break
						}
					}
				} else if shape := headingShape(Squeeze(segs[i].Text)); shape != "" && j == i {
					// "1. Skema for balance …" ends at "2. Skema …", "EGENKAPITAL" at the next
					// heading in capitals, "IV. Andre reserver" at "V. Overført overskud".
					for k := i + 1; k < within.To; k++ {
						if headingShape(Squeeze(segs[k].Text)) == shape {
							out.To = k
							break
						}
					}
				}
				return out, true
			}
		}
	}
	return Span{}, false
}

// alnumKey is text reduced to its letters and digits, lower-cased, for words a change quotes
// across the line breaks and marks an annex prints.
func alnumKey(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// reWordNumber is a heading that opens with a word and a number: "Kriterium 4.", "Modul 2".
var reWordNumber = regexp.MustCompile(`^(\p{L}+)\s+([0-9]+)\.?(?:\s|$)|^(\p{Lu}{2,})([0-9]+)(?:\s|$)`)

// findPart is partSpan for a label a change names, which an annex may print with the word for its
// kind in front: section B as "AFSNIT B" or "Del B" rather than "B.".
func findPart(segs []Segment, within Span, label string) (Span, bool) {
	direct, found := partSpan(segs, within, label)
	if found && (strings.Count(label, ".") < 2 || holdsProse(segs, direct.From, direct.To, labelShape(label))) {
		return direct, true
	}
	if k := strings.LastIndex(strings.TrimRight(label, "."), "."); k > 0 && reDottedNumber.MatchString(strings.TrimRight(label, ".")) {
		// "pkt. 1.03.060" in an annex printing "060 Motorredskab" under "1.03 Køretøjsarter",
		// where the full number is printed by its table of contents alone.
		if parent, ok := findPart(segs, within, label[:k]); ok {
			if sp, ok := partSpan(segs, Span{From: parent.From + 1, To: parent.To}, label[k+1:]); ok {
				return sp, true
			}
		}
	}
	if found {
		return direct, true
	}
	switch partShape(label) {
	case "kapitel":
		// An annex printing its chapters as "6." alone.
		return partSpan(segs, within, strings.TrimPrefix(label, "Kapitel "))
	case "letter", "roman", "n0":
		for _, word := range []string{"Afsnit ", "Del "} {
			if sp, ok := partSpan(segs, within, word+strings.TrimRight(label, ".")); ok {
				return sp, true
			}
		}
	}
	return Span{}, false
}

// labelMark is the mark a line's label is printed with: "." for "3.", ")" for "3)", "" for none.
func labelMark(text string) string {
	f := strings.Fields(Squeeze(text))
	if len(f) == 0 {
		return ""
	}
	switch t := f[0]; {
	case strings.HasSuffix(t, "."):
		return "."
	case strings.HasSuffix(t, ")"):
		return ")"
	}
	return ""
}

// holdsProse reports a labelled line followed, before the next label of its shape, by a line of
// running text rather than only the short entries of a table of contents.
func holdsProse(segs []Segment, at, to int, shape string) bool {
	for i := at + 1; i < to; i++ {
		if opensWithShape(segs[i].Text, shape) {
			return false
		}
		if len([]rune(Squeeze(segs[i].Text))) > 100 {
			return true
		}
	}
	return false
}

// partSpan finds the lines of an annex part inside from..to: the one line that opens with label, and
// the lines up to the next that opens with a label of the same shape.
func partSpan(segs []Segment, within Span, label string) (Span, bool) {
	shape := partShape(label)
	var hits []int
	for i := within.From; i < within.To; i++ {
		if opensWith(segs[i].Text, label) {
			hits = append(hits, i)
		}
	}
	if len(hits) > 1 {
		// A table of contents printed as a table, "Regel 19-1 | Øvelser i havarikontrol …", names
		// the part the text outside it holds.
		var outside []int
		for _, h := range hits {
			if segs[h].Table == 0 {
				outside = append(outside, h)
			}
		}
		if len(outside) > 0 {
			hits = outside
		}
	}
	if len(hits) > 1 {
		// Annexes restart their numbering in each list, so "2." can be printed many times. The
		// one meant is the one continuing its list: the label of the same shape before it is "1."
		// (or there is none, for the first). If that does not single one out, it cannot be told.
		var inSequence []int
		for _, h := range hits {
			if previousLabel(segs, within.From, h, shape) == predecessor(label) {
				inSequence = append(inSequence, h)
			}
		}
		if len(inSequence) != 1 {
			// A table of contents prints the labels again, each on a line of its own with the
			// next label straight after it. The part itself is the one followed by its text.
			var withText []int
			for _, h := range hits {
				if h+1 < within.To && !opensWithShape(segs[h+1].Text, shape) {
					withText = append(withText, h)
				}
			}
			if len(withText) == 1 {
				inSequence = withText
			}
		}
		if len(inSequence) != 1 {
			// "Afsnit B" heads the section on a line of its own; "Afsnit B indeholder en
			// henvisning …" is a sentence about it.
			var alone []int
			for _, h := range hits {
				if strings.EqualFold(strings.TrimRight(Squeeze(segs[h].Text), ".:"), strings.TrimRight(label, ".")) {
					alone = append(alone, h)
				}
			}
			if len(alone) == 1 {
				inSequence = alone
			}
		}
		if len(inSequence) != 1 {
			// A table of contents whose entries carry sub-entries prints only short lines under
			// each label; the part itself holds the text.
			var long []int
			for _, h := range hits {
				if holdsProse(segs, h, within.To, shape) {
					long = append(long, h)
				}
			}
			if len(long) == 1 {
				inSequence = long
			}
		}
		hits = inSequence
	}
	if len(hits) != 1 {
		return Span{}, false
	}
	at := hits[0]
	end := within.To
	mark := labelMark(segs[at].Text)
	// "16.5" ends where "17." begins as well as at "16.6": a part of a section ends with it.
	parentNext, listBefore, rowEnd := "", false, false
	if l := strings.TrimRight(label, "."); strings.Contains(l, ".") && reDottedNumber.MatchString(l) {
		parentNext = itoa(Atoi(l[:strings.Index(l, ".")]) + 1)
	}
	for i := at + 1; i < within.To; i++ {
		if parentNext != "" && opensWith(segs[i].Text, parentNext) && !listBefore {
			end = i
			break
		}
		if f := strings.Fields(Squeeze(segs[i].Text)); len(f) > 0 && labelShape(f[0]) == "n0" {
			listBefore = true // "1.", "2.", … inside the part: a later "4." is the list's, not the section's
		}
		if first := strings.Fields(Squeeze(segs[i].Text)); shape == "n0" && len(first) > 0 && segs[at].Table != 0 &&
			segs[i].Table == segs[at].Table && segs[i].Row != segs[at].Row && segs[i].Col == segs[at].Col &&
			reDottedNumber.MatchString(strings.TrimRight(first[0], ".")) {
			// A table of numbered entries, bilag 4 of the order on imports of plants: "11." wood
			// from China, then "11.1" oak from the USA, each a row with requirements of its own.
			// "udgår nr. 11" takes the one row, and a later order places a row after "11.2".
			end, rowEnd = i, true
			break
		}
		if first := strings.Fields(Squeeze(segs[i].Text)); shape == "litra" && len(first) > 0 && labelShape(first[0]) == "litra" && !nextLitra(label, first) {
			continue // "f. sp. platani" inside c) Svampe: an abbreviation, not the part after c)
		}
		if first := strings.Fields(Squeeze(segs[i].Text)); len(first) > 0 && shape != "" &&
			(labelShape(first[0]) == shape && (shape != "n0" || mark == "" || labelMark(first[0]) == mark) ||
				shape == "letter" && labelShape(first[0]) == "roman" ||
				len(first) > 1 && labelShape(first[0]+" "+first[1]) == shape) {
			end = i
			break
		}
	}
	if shape == "n0" && end < within.To && !rowEnd {
		if next := Atoi(strings.TrimRight(strings.Fields(Squeeze(segs[end].Text))[0], ".)")); next <= Atoi(label) {
			// "3." followed by "1." starts a list, inside the part or after it, and which one
			// cannot be told from the numbers. A section with a heading runs to the next number
			// with a heading of its own; anything else is not known to end anywhere.
			return headedSection(segs, within, label, nil)
		}
	}
	return Span{From: at, To: end}, true
}

// opensWithShape reports a line that opens with a label of the given shape.
func opensWithShape(text, shape string) bool {
	first := strings.Fields(Squeeze(text))
	return len(first) > 0 && shape != "" && (labelShape(first[0]) == shape || len(first) > 1 && labelShape(first[0]+" "+first[1]) == shape)
}

// opensWith reports a line printing label at its head: "2.", "2)", "4.2 …", "Tabel 2 …".
func opensWith(text, label string) bool {
	switch partShape(label) {
	case "letter", "roman", "litra":
		// "A." or "IV." heads a section and "d)" or "d." a litra; "a)" is no section A, and "I
		// denne lov" is a word.
		t := Squeeze(text)
		l := strings.TrimRight(label, ".)")
		return strings.HasPrefix(t, l) && len(t) > len(l) && (t[len(l)] == '.' || t[len(l)] == ')')
	}
	t := strings.ToLower(Squeeze(text))
	l := strings.ToLower(label)
	if !strings.HasPrefix(t, l) {
		return false
	}
	rest := t[len(l):]
	if len(rest) > 1 && rest[0] == '.' && rest[1] >= '0' && rest[1] <= '9' {
		return false // "2.1" is a part of "2", not "2" itself
	}
	return rest == "" || strings.IndexAny(rest[:1], " .):") == 0
}

// reLetterLabel is a section labelled by a capital letter, "A", and reRomanLabel one labelled by a
// roman numeral, "IV". "I", "V" and "X" are read as numerals.
var (
	reLetterLabel = regexp.MustCompile(`^[A-ZÆØÅ]$`)
	reRomanLabel  = regexp.MustCompile(`^(?:[IVXLC]{2,}|[IVX])$`)
)

// markShape is the shape of a label without the mark an annex prints after it: "A", "IV", "d".
func markShape(core string) string {
	switch {
	case reRomanLabel.MatchString(core):
		return "roman"
	case reLetterLabel.MatchString(core):
		return "letter"
	case reLitraLabel.MatchString(core):
		return "litra"
	}
	return ""
}

// partShape is labelShape for a label a change names, "A", "V" or "d)", without the mark the
// annex prints after it.
func partShape(label string) string {
	t := strings.TrimRight(Squeeze(label), ".)")
	if sh := markShape(t); sh != "" && (sh != "litra" || strings.HasSuffix(label, ")")) {
		return sh
	}
	return labelShape(label)
}

// labelShape names what kind of label a token is: digits with as many dots, a table, a section.
// Two labels of the same shape are siblings.
func labelShape(label string) string {
	if t := Squeeze(label); strings.HasSuffix(t, ".") || strings.HasSuffix(t, ")") {
		if sh := markShape(t[:len(t)-1]); sh != "" {
			return sh // "A.", "IV.", "d)" as the annex prints them; a bare "I" is a word
		}
	}
	l := strings.ToLower(strings.TrimRight(Squeeze(label), ".):"))
	switch {
	case strings.HasPrefix(l, "tabel"):
		return "tabel"
	case strings.HasPrefix(l, "afsnit"), strings.HasPrefix(l, "del "):
		return "afsnit"
	case strings.HasPrefix(l, "kapitel "):
		return "kapitel"
	case strings.HasPrefix(l, "regel "):
		return "regel"
	case strings.HasPrefix(l, "(") && reDottedNumber.MatchString(strings.TrimPrefix(l, "(")):
		return "paren" // "(8)", "(9)"
	}
	if reDottedNumber.MatchString(l) {
		return "n" + itoa(strings.Count(l, "."))
	}
	return ""
}

// reSpacedEntryLetter is an entry's number and letter typed apart before a comma: "nr. 10. a,".
var reSpacedEntryLetter = regexp.MustCompile(`\bnr\.\s*([0-9]+)\.\s+([a-z]),`)

// reQuotedAnchor is the quoted text an insertion is placed by: "indsættes efter »…«:".
var reQuotedAnchor = regexp.MustCompile(`(?i)\b(?:indsættes|tilføjes)\s+(efter|før|foran)\s*»([^«]+)«`)

// reAsNewPart is the label an insertion gives its new part: "som nyt punkt 2.6", "som ny nr. 4",
// "tilføjes litra g)".
var reAsNewPart = regexp.MustCompile(`(?i)(?:\bsom\s+(?:et\s+)?nyt?\s+(?:punkt|pkt\.|nr\.)|\btilføjes\s+(?:som\s+(?:ny\s+)?)?litra)\s*([0-9]+(?:\.[0-9]+)*(?:\s?[a-z]\b)?|[a-zæøå]\)?)\.?`)

// reOrdinalWordAfsnit is a paragraph named by its ordinal in words: "andet afsnit".
var reOrdinalWordAfsnit = regexp.MustCompile(`(?i)\b(første|andet|tredje|fjerde|femte|sjette|syvende|ottende|niende|tiende)\s+afsnit\b`)

// ordinalWords are the ordinals a paragraph is named by.
var ordinalWords = map[string]int{"første": 1, "andet": 2, "tredje": 3, "fjerde": 4, "femte": 5, "sjette": 6, "syvende": 7, "ottende": 8, "niende": 9, "tiende": 10}

// reInsertVerb is the verb of an insertion.
var reInsertVerb = regexp.MustCompile(`(?i)\b(?:indsættes|tilføjes)\b`)

var reDottedNumber = regexp.MustCompile(`^[0-9]+(?:\.[0-9]+)*[a-z]?$`)

// anchorRun is the one run of lines whose words, read together, are the passage: a quotation of
// table rows, "Forhandlere af lægemidler til produktionsdyr … DM", printed a cell to a line.
func anchorRun(segs []Segment, within Span, passage string) (Span, bool) {
	want := alnumKey(passage)
	if want == "" {
		return Span{}, false
	}
	var hits []Span
	for i := within.From; i < within.To; i++ {
		if alnumKey(segs[i].Text) == "" {
			continue // "-" before "Prøvetagning, …": the run starts at words
		}
		got := ""
		for j := i; j < within.To; j++ {
			got += alnumKey(segs[j].Text)
			if got == want {
				hits = append(hits, Span{From: i, To: j + 1})
				break
			}
			if !strings.HasPrefix(want, got) {
				break
			}
		}
	}
	if len(hits) == 1 {
		return hits[0], true
	}
	if len(hits) > 1 {
		return Span{}, false
	}
	// A table printed a column at a time: the quotation reads its rows, "… magistrelle lægemidler
	// DI … DM", and the lines give the names and then the codes. The run is the one whose words
	// are the quotation's, every one as often.
	words := wordCount(passage)
	total := 0
	for _, n := range words {
		total += n
	}
	for i := within.From; i < within.To; i++ {
		if alnumKey(segs[i].Text) == "" {
			continue
		}
		have, n := map[string]int{}, 0
		for j := i; j < within.To && n < total; j++ {
			for w, k := range wordCount(segs[j].Text) {
				have[w] += k
				n += k
			}
			if n == total && maps.Equal(have, words) {
				hits = append(hits, Span{From: i, To: j + 1})
			}
		}
	}
	if len(hits) != 1 {
		return Span{}, false
	}
	return hits[0], true
}

// wordCount is how often each word of a text occurs in it, compared without case or punctuation.
func wordCount(text string) map[string]int {
	out := map[string]int{}
	for _, f := range strings.Fields(text) {
		if k := alnumKey(f); k != "" {
			out[k]++
		}
	}
	return out
}

// placeNewPart is where a new part labelled label goes: in front of the part the annex prints with
// that label, whose run of labels after it each move up by one ("2.6", "2.7", "2.8" become "2.7",
// "2.8", "2.9"), or after the part before it. The segments come back relabelled.
func placeNewPart(segs []Segment, within Span, label string) (int, []Segment, bool) {
	if sp, found := findPart(segs, within, label); found {
		out := cloneSegments(segs)
		for cur, part := label, sp; ; {
			next := successorLabel(cur)
			if next == "" {
				break
			}
			// The run goes on only where the next label opens the line the part ends at.
			nsp, more := findPart(out, Span{From: part.From + 1, To: within.To}, next)
			out[part.From].Text = relabelLine(out[part.From].Text, cur, next)
			if !more || nsp.From != part.To {
				break
			}
			cur, part = next, nsp
		}
		return sp.From, out, true
	}
	if p := predecessor(label); p != "" {
		if sp, found := findPart(segs, within, p); found {
			if strings.HasSuffix(p, ")") && sp.To > sp.From+1 && strings.ContainsAny(lastRune(Squeeze(segs[sp.From].Text)), ".;") {
				// "f) Hvis væv og celler … mærkes med »BIOLOGISK FARE«." on a line of its own, the
				// point's closing paragraph after it: the list ends with f)'s line.
				return sp.From + 1, segs, true
			}
			return sp.To, segs, true
		}
	}
	return 0, segs, false
}

// lastRune is a text's last character, or "".
func lastRune(s string) string {
	r := []rune(s)
	if len(r) == 0 {
		return ""
	}
	return string(r[len(r)-1])
}

// successorLabel is the label after this one: "2.6" → "2.7".
func successorLabel(label string) string {
	l := strings.TrimRight(label, ".")
	i := strings.LastIndex(l, ".")
	last := l[i+1:]
	if n := Atoi(last); n > 0 && strings.TrimLeft(last, "0") == itoa(n) {
		return l[:i+1] + fmt.Sprintf("%0*d", len(last), n+1)
	}
	return ""
}

// relabelLine gives a line opening with one label the next.
func relabelLine(text, from, to string) string {
	lead := len(text) - len(strings.TrimLeft(text, " \u00a0\n\t"))
	if strings.HasPrefix(text[lead:], from) {
		return text[:lead] + to + text[lead+len(from):]
	}
	return text
}

// headingLine reports a line that is a part's label and its heading, "4.4 Feltudfyldelse": a few
// words, no sentence.
func headingLine(text, label string) bool {
	t := Squeeze(text)
	if !opensWith(t, label) || strings.HasSuffix(t, ".") {
		return false
	}
	return len(strings.Fields(t)) <= 4
}

// reCodeHeading is a line opening with a code, capitals and digits run together: "FELT03".
var reCodeHeading = regexp.MustCompile(`^\p{Lu}{2,}[0-9]+(?:\s|$)`)

// wordNumber is a line's opening word and the number with it, "Afsnit 3" or "FELT08", or nil.
func wordNumber(line string) []string {
	m := reWordNumber.FindStringSubmatch(line)
	switch {
	case m == nil:
		return nil
	case m[1] != "":
		return []string{m[1], m[2]}
	}
	return []string{m[3], m[4]}
}

// printsAtHead reports a line an annex prints among its first lines: its title and table header.
func printsAtHead(segs []Segment, line string) bool {
	line = Squeeze(line)
	for i := 0; i < len(segs) && i < 12; i++ {
		if line != "" && Squeeze(segs[i].Text) == line {
			return true
		}
	}
	return false
}

// applyAnnexPart carries out a change to labelled parts of annexes.
func (c *Content) applyAnnexPart(ch AnnexPartChange, e Edit, reject func(string), apply func(where, note string)) {
	var repl []Segment
	if !ch.Delete {
		switch {
		case ch.Source != 0 && (e.NewAppendix == nil || len(e.NewAppendix.Segments) == 0):
			reject("the act's own bilag " + itoa(ch.Source) + ", which holds the new text, is not in the mirror")
			return
		case ch.Source != 0:
			repl = cloneSegments(e.NewAppendix.Segments)
			if len(repl) > 0 && reAnnexHeader.MatchString(Squeeze(repl[0].Text)) {
				repl = repl[1:] // the act's own "Bilag 1" title is not part of the text it brings
			}
			repl = withoutOuterQuotes(repl)
		default:
			repl = e.blockText()
			if m := reQuotedAfterSaaledes.FindStringSubmatch(e.Definition); len(repl) == 0 && m != nil {
				repl = []Segment{{Text: Squeeze(m[1]), Break: true}} // "affattes 13) således: »13) …«"
			}
		}
		if len(repl) == 0 {
			reject("the new text for the annex part is empty")
			return
		}
	}
	for _, n := range ch.Targets {
		ap := c.annexPartHolder(n, ch)
		if ap == nil {
			reject("the law has no bilag " + itoa(n) + " in the mirror")
			continue
		}
		if len(ap.Order) > 0 {
			reject("bilag " + itoa(n) + " holds provisions, and its parts are addressed through them")
			continue
		}
		within := Span{From: 0, To: len(ap.Segments)}
		ok := true
		for _, sc := range ch.Scopes {
			if strings.HasPrefix(sc, "~") {
				sp, found := wordsScope(ap.Segments, within, strings.TrimPrefix(sc, "~"))
				ok = ok && found
				within = sp
				continue
			}
			sp, found := findPart(ap.Segments, within, sc)
			ok = ok && found
			within = sp
		}
		if ch.Insert {
			at := within.To
			if ch.Label != "" {
				sp, found := findPart(ap.Segments, within, ch.Label)
				if !ok || !found {
					reject("bilag " + itoa(n) + " does not print \"" + ch.Label + "\" at the head of exactly one line where the change places it")
					continue
				}
				at = sp.To
				if ch.Before {
					at = sp.From
				}
			} else if ch.Anchor != "" {
				run, found := anchorRun(ap.Segments, within, ch.Anchor)
				if !ok || !found {
					reject("bilag " + itoa(n) + " does not print the passage the insertion is placed by, once, where the change places it")
					continue
				}
				at = run.To
				if ch.Before {
					at = run.From
				}
			} else if ch.NewLabel != "" {
				where, relabelled, placed := placeNewPart(ap.Segments, within, ch.NewLabel)
				if !ok || !placed {
					reject("bilag " + itoa(n) + " prints neither \"" + ch.NewLabel + "\" nor the part before it, where the new part goes")
					continue
				}
				at = where
				ap.Segments = relabelled
			} else if !ok {
				reject("bilag " + itoa(n) + " does not print the part the insertion is placed in")
				continue
			}
			add := repl
			if ch.Source != 0 {
				// The act's annex prints the table's header over the entry it brings, "Planter
				// m.m. | Særlige krav": the law's table has it already.
				for len(add) > 1 && printsAtHead(ap.Segments, add[0].Text) {
					add = add[1:]
				}
			}
			segs := make([]Segment, 0, len(ap.Segments)+len(add))
			segs = append(segs, ap.Segments[:at]...)
			segs = append(segs, cloneSegments(add)...)
			segs = append(segs, ap.Segments[at:]...)
			ap.Segments = segs
			apply("bilag "+itoa(n), "inserted where the labels the annex prints place it")
			continue
		}
		for _, label := range append([]string{ch.Label}, ch.Also...) {
			if !c.replaceAnnexPart(ap, n, within, ok, ch, label, repl, reject, apply) {
				break
			}
		}
	}
}

// quotedPart is the one part printing label followed by heading, from its line to the next label
// of its shape.
func quotedPart(segs []Segment, within Span, label, heading string) (Span, bool) {
	want := alnumKey(label + heading)
	at := -1
	for i := within.From; i < within.To; i++ {
		if !opensWith(segs[i].Text, label) || !strings.HasPrefix(alnumKey(SegmentsText(segs[i:min(i+3, within.To)])), want) {
			continue
		}
		if at >= 0 {
			return Span{}, false
		}
		at = i
	}
	if at < 0 {
		return Span{}, false
	}
	// The part is exactly the lines the change quotes; what follows them is not its to repeal.
	for i := at + 1; i <= within.To && i <= at+3; i++ {
		if alnumKey(SegmentsText(segs[at:i])) == want {
			return Span{From: at, To: i}, true
		}
	}
	return Span{}, false
}

// withoutOuterQuotes drops the act's quotation marks around new text it prints as lines of their own:
// "»" before the first line and "«" after the last.
func withoutOuterQuotes(segs []Segment) []Segment {
	for len(segs) > 0 && strings.TrimSpace(segs[0].Text) == "»" {
		segs = segs[1:]
	}
	for len(segs) > 0 && strings.TrimSpace(segs[len(segs)-1].Text) == "«" {
		segs = segs[:len(segs)-1]
	}
	if len(segs) > 0 {
		segs[0].Text = strings.TrimPrefix(strings.TrimSpace(segs[0].Text), "»")
		last := &segs[len(segs)-1]
		last.Text = strings.TrimSuffix(strings.TrimSpace(last.Text), "«")
	}
	return segs
}

// printsLabel reports new text printing the label anywhere at the head of a line.
func printsLabel(segs []Segment, label string) bool {
	for _, sg := range segs {
		if opensWith(sg.Text, label) {
			return true
		}
	}
	return false
}

// replaceAnnexPart replaces or repeals one labelled part of an annex, inside within.
func (c *Content) replaceAnnexPart(ap *Appendix, n int, within Span, ok bool, ch AnnexPartChange, label string, repl []Segment, reject func(string), apply func(where, note string)) bool {
	sp, found := findPart(ap.Segments, within, label)
	note := "the part's extent is read from the labels the annex prints"
	if !found && len(ch.Scopes) > 0 && !strings.HasPrefix(ch.Scopes[len(ch.Scopes)-1], "~") {
		// "Bilag 3, afsnit B-4, regel 19-1": the table of contents prints "Afsnit B-4" alone and
		// was taken for the section; the part is the one under the section's own heading.
		if sp2, ok2 := underScope(ap.Segments, ch.Scopes[len(ch.Scopes)-1], label); ok2 {
			sp, found, ok = sp2, true, true
		}
	}

	if found && ch.LabelTo != "" {
		last, ok2 := findPart(ap.Segments, within, ch.LabelTo)
		if !ok2 || last.From < sp.To {
			// the run ends in the same part its first label is found in
			last, ok2 = partSpan(ap.Segments, Span{From: sp.From, To: len(ap.Segments)}, ch.LabelTo)
		}
		if !ok2 || last.From < sp.To {
			reject("bilag " + itoa(n) + " does not print \"" + ch.LabelTo + "\" after \"" + label + "\" where the change places it")
			return false
		}
		sp.To = last.To
	}
	if ok && ch.Verify {
		sp, found = quotedPart(ap.Segments, within, label, ch.Heading)
	}
	if ok && !found {
		// "I bilag 12 affattes afsnit 2, således: 2. Uddannelsens varighed …": the label is
		// printed by every numbered list too, and the section is the one headed as the new
		// text is headed.
		if sp, found = headedSection(ap.Segments, within, label, repl); found {
			note = "the section is the one printing the heading the new text brings"
		}
	}
	if ok && !found && ch.Paragraph > 0 && !ch.Delete && len(repl) > 0 {
		// "Bilag 2, tabel 3, 2. afsnit, affattes således: Korrektionsformlerne for malkekøer …": the
		// annex prints its paragraphs a sentence to a line, so the paragraph is not counted. It is
		// the one run of lines opening as the new text's sentences open, one line to a sentence.
		if run, aligned := alignedRun(ap.Segments, within, repl); aligned {
			segs := append([]Segment(nil), ap.Segments[:run.From]...)
			segs = append(segs, cloneSegments(withoutOuterQuotes(repl))...)
			ap.Segments = append(segs, ap.Segments[run.To:]...)
			apply("bilag "+itoa(n), "the paragraph is the one whose sentences the new text rewrites, one by one")
			return true
		}
	}
	if !ok || !found {
		reject("bilag " + itoa(n) + " does not print \"" + label + "\" at the head of exactly one line where the change places it")
		return false
	}
	if ch.HeadingOnly {
		if ch.Delete || len(repl) == 0 {
			reject("bilag " + itoa(n) + ", " + label + ": the change repeals a heading, which this model does not place")
			return false
		}
		line := Squeeze(SegmentsText(withoutOuterQuotes(repl)))
		segs := append([]Segment(nil), ap.Segments...)
		segs[sp.From].Text = line
		ap.Segments = segs
		apply("bilag "+itoa(n)+", "+label+" (overskrift)", "")
		return true
	}
	if ch.Sentence > 0 && !ch.Delete {
		reject("bilag " + itoa(n) + ", " + label + ": the change rewrites one sentence of the part, which this model does not place")
		return false
	}
	if ch.Delete && ch.Sentence > 0 {
		segs, why := deleteSentence(ap.Segments, sp, ch.Sentence)
		if why != "" {
			reject("bilag " + itoa(n) + ", " + label + ": " + why)
			return false
		}
		ap.Segments = segs
		apply("bilag "+itoa(n)+", "+label+", "+itoa(ch.Sentence)+". pkt.", "")
		return true
	}
	if ch.Verify && !strings.HasPrefix(alnumKey(SegmentsText(ap.Segments[sp.From:sp.To])), alnumKey(label+ch.Heading)) {
		reject("bilag " + itoa(n) + " does not print \"" + label + ". " + ch.Heading + "\", which the change quotes as the part it repeals")
		return false
	}
	if len(repl) > 0 && !printsLabel(repl, label) && (Squeeze(strings.TrimRight(ap.Segments[sp.From].Text, ". ")) == strings.TrimRight(label, ".") ||
		headingLine(ap.Segments[sp.From].Text, label)) {
		// "I bilag 2 affattes nr. 3.5. således: Ved opgaverne …" brings the part's text without
		// its label, which the annex prints on a line of its own: the label stays.
		sp.From++
		if ch.Heading != "" && sp.From < sp.To && alnumKey(ap.Segments[sp.From].Text) == alnumKey(ch.Heading) && !strings.Contains(alnumKey(SegmentsText(repl)), alnumKey(ch.Heading)) {
			sp.From++ // and so does the heading the change named it by
		}
	}
	segs := make([]Segment, 0, len(ap.Segments)-(sp.To-sp.From)+len(repl))
	segs = append(segs, ap.Segments[:sp.From]...)
	segs = append(segs, cloneSegments(repl)...)
	segs = append(segs, ap.Segments[sp.To:]...)
	ap.Segments = segs
	apply("bilag "+itoa(n)+", "+label, note)
	return true
}

// headedSection finds a numbered section of an annex by its heading: the one line printing label
// whose next line is a heading reading as the new text's does. It runs to the next section of the
// same level, the following number with a heading of its own; numbered list items in between, which
// print the same labels, are the section's content.
func headedSection(segs []Segment, within Span, label string, repl []Segment) (Span, bool) {
	l := strings.TrimRight(strings.TrimSpace(label), ".")
	if Atoi(l) == 0 || itoa(Atoi(l)) != l {
		return Span{}, false
	}
	heading := func(lines []Segment, i int, l string) string {
		// The heading is printed after the label, on the same line or on the next.
		rest := strings.TrimSpace(strings.TrimPrefix(Squeeze(lines[i].Text), l+"."))
		if rest == "" && i+1 < len(lines) {
			rest = Squeeze(lines[i+1].Text)
		}
		if k := reSubLabelInLine.FindStringIndex(rest); k != nil {
			rest = strings.TrimSpace(rest[:k[0]]) // new text run together: "… m.v. 2.1. Uddannelsens …"
		}
		if rest == "" || len([]rune(rest)) > 120 || strings.HasSuffix(rest, ":") || unicode.IsDigit([]rune(rest)[0]) {
			return ""
		}
		return MatchText(rest)
	}
	// The section is the one headed as the new text is headed; a section that is repealed brings
	// no text, and is the one numbered label that carries a heading at all.
	want := ""
	if len(repl) > 0 {
		if want = heading(repl, 0, l); want == "" {
			return Span{}, false
		}
	}
	var hits []int
	for i := within.From; i < within.To; i++ {
		if h := heading(segs[:within.To], i, l); opensWith(segs[i].Text, l) && h != "" && (want == "" || h == want) {
			hits = append(hits, i)
		}
	}
	if len(hits) > 1 {
		// "3. Fisk og vildt, niveau 4 …" in a list of specialisations reads like a heading too. The
		// section is the one its first subsection, "3.1.", follows.
		var opened []int
		for _, i := range hits {
			if subsectionFollows(segs, i, within.To, l) {
				opened = append(opened, i)
			}
		}
		hits = opened
	}
	if len(hits) != 1 {
		return Span{}, false
	}
	hit := hits[0]
	next := itoa(Atoi(l) + 1)
	end := -1
	for i := hit + 1; i < within.To; i++ {
		if opensWith(segs[i].Text, next) && heading(segs[:within.To], i, next) != "" {
			if end < 0 {
				end = i
			}
			if subsectionFollows(segs, i, within.To, next) {
				end = i // the next section itself, past list items that only look like it
				break
			}
		}
	}
	if end < 0 {
		// Without the next section's heading the extent is not known, and replacing to the end
		// of the annex would repeal every section after this one.
		if !lastSection(segs, hit, within.To, l) {
			return Span{}, false
		}
		end = within.To
	}
	return Span{From: hit, To: end}, true
}

// subsectionFollows reports a section label at line i whose first subsection, "3.1." under "3.",
// follows within the next few lines.
func subsectionFollows(segs []Segment, i, to int, label string) bool {
	for j := i + 1; j < to && j <= i+3; j++ {
		if opensWith(segs[j].Text, label+".1") {
			return true
		}
	}
	return false
}

// previousLabel is the nearest label of the given shape before line at, or "" when there is none.
func previousLabel(segs []Segment, from, at int, shape string) string {
	for i := at - 1; i >= from; i-- {
		if first := strings.Fields(Squeeze(segs[i].Text)); len(first) > 0 && labelShape(first[0]) == shape {
			return strings.ToLower(strings.TrimRight(first[0], ".):"))
		}
	}
	return ""
}

// predecessor is the label before a numbered label in its list: "2" before "3", "4.1" before
// "4.2", and "" for the first of a list.
func predecessor(label string) string {
	if r := []rune(strings.ToLower(label)); len(r) == 2 && r[1] == ')' && r[0] > 'a' && r[0] <= 'z' {
		return string(r[0]-1) + ")" // "g)" follows "f)"
	}
	l := strings.ToLower(strings.TrimRight(label, ".):"))
	i := strings.LastIndex(l, ".")
	last := l[i+1:]
	n := Atoi(last)
	if n <= 1 || strings.TrimLeft(last, "0") != itoa(n) {
		return ""
	}
	// "4.01.023" follows "4.01.022": the number keeps its width.
	return l[:i+1] + fmt.Sprintf("%0*d", len(last), n-1)
}

// reSubLabelInLine is a dotted sublabel printed inside a line: " 2.1. ", " 4.2 ".
var reSubLabelInLine = regexp.MustCompile(`\s[0-9]+\.[0-9]+\.?\s`)

// lastSection reports that no section of a later number follows the one at hit: its own numbered
// lists aside, the annex prints no higher label with a heading after it.
func lastSection(segs []Segment, hit, to int, l string) bool {
	for i := hit + 1; i < to; i++ {
		f := strings.Fields(Squeeze(segs[i].Text))
		if len(f) == 0 {
			continue
		}
		if n := Atoi(strings.TrimRight(f[0], ".")); n > Atoi(l) && strings.HasSuffix(f[0], ".") && !strings.Contains(strings.TrimSuffix(f[0], "."), ".") {
			if i+1 < to && len([]rune(Squeeze(segs[i+1].Text))) < 120 && !strings.HasSuffix(Squeeze(segs[i+1].Text), ".") {
				return false
			}
		}
	}
	return true
}

// follows reports a label that comes straight after another: "58" after "57", "D" after "C".
func follows(a, b string) bool {
	if Atoi(a) > 0 && Atoi(b) == Atoi(a)+1 {
		return true
	}
	ra, rb := []rune(a), []rune(b)
	return len(ra) == 1 && len(rb) == 1 && unicode.IsLetter(ra[0]) && rb[0] == ra[0]+1
}

// splitOutsideParens splits at the commas that are not inside parentheses: "1. Skema for balance i
// kontoform (regnskabsklasse B, C og D), PASSIVER" is two pieces, not four.
func splitOutsideParens(s string) []string {
	var out []string
	depth, from := 0, 0
	for i, r := range s {
		switch r {
		case '(':
			depth++
		case ')':
			if depth > 0 {
				depth--
			}
		case ',':
			if depth == 0 {
				out = append(out, s[from:i])
				from = i + 1
			}
		}
	}
	return append(out, s[from:])
}

// reOrdinalHeading is a heading opening with its own number: "1. Skema for balance i kontoform".
var reOrdinalHeading = regexp.MustCompile(`^[0-9]+\.\s+\p{Lu}\p{Ll}+\s+\p{L}`)

var (
	reCapsHeading    = regexp.MustCompile(`^[\p{Lu}][\p{Lu}\s,\-–]{3,}$`)
	reRomanHeading   = regexp.MustCompile(`^[IVXL]+\.\s+\p{Lu}`)
	reOrdinalSection = regexp.MustCompile(`^[0-9]+\.\s+\p{Lu}\p{Ll}+\s+\p{L}`)
)

// headingShape names the shape of a heading line an annex scopes its parts by: a heading in
// capitals, one numbered in roman numerals, one opening with its ordinal and words. A part's label
// alone, "2.", is none of them.
func headingShape(line string) string {
	switch {
	case reCapsHeading.MatchString(line) && len([]rune(line)) >= 4:
		return "caps"
	case reRomanHeading.MatchString(line):
		return "roman"
	case reOrdinalSection.MatchString(line):
		return "ordinal"
	}
	return ""
}

// rePartSentence is a sentence of an annex's part, named after the part: ", 2. pkt.".
var rePartSentence = regexp.MustCompile(`(?i),\s*([0-9]+)\.\s*(?:pkt\.?|punktum)\s*,?`)

// deleteSentence removes the sentence numbered k from the lines of a part, its label line aside.
// It reports why it cannot: a sentence that runs over more than one line is not cut.
func deleteSentence(segs []Segment, sp Span, k int) ([]Segment, string) {
	from := sp.From
	if from < sp.To && reLabelHead.MatchString(segs[from].Text) && len(strings.TrimSpace(reLabelHead.ReplaceAllString(segs[from].Text, ""))) == 0 {
		from++ // the label printed alone
	}
	count := 0
	for i := from; i < sp.To; i++ {
		text := Squeeze(segs[i].Text)
		spans := sentenceSpans(text)
		if count+len(spans) < k {
			count += len(spans)
			continue
		}
		at := spans[k-count-1]
		rest := strings.TrimSpace(text[:at[0]] + " " + text[at[1]:])
		out := append([]Segment(nil), segs[:i]...)
		if rest != "" {
			cp := segs[i]
			cp.Text = rest
			out = append(out, cp)
		}
		return append(out, segs[i+1:]...), ""
	}
	return nil, fmt.Sprintf("the part has fewer than %d sentences", k)
}

// reNamedAnchor is an insertion naming the part it goes by: "efter nr. 5", "før litra a".
var reNamedAnchor = regexp.MustCompile(`(?i)\b(?:efter|før)\s+(?:nr\.|pkt\.|punkt|tabel|afsnit|litra)\s*\S`)

// alignedRun is the one run of lines within whose lines open with the same first words as the new
// text's sentences, in order, one line to a sentence.
func alignedRun(segs []Segment, within Span, repl []Segment) (Span, bool) {
	var want []string
	for _, sentence := range SentencesOf(Squeeze(SegmentsText(withoutOuterQuotes(repl)))) {
		want = append(want, openingWords(sentence))
	}
	if len(want) < 2 {
		return Span{}, false // one sentence is too little to place a paragraph by
	}
	var hits []Span
	for i := within.From; i+len(want) <= within.To; i++ {
		ok := true
		for k, w := range want {
			if w == "" || openingWords(segs[i+k].Text) != w {
				ok = false
				break
			}
		}
		if ok {
			hits = append(hits, Span{From: i, To: i + len(want)})
		}
	}
	if len(hits) != 1 {
		return Span{}, false
	}
	return hits[0], true
}

// openingWords is a sentence's first four words, letters and digits only.
func openingWords(s string) string {
	words := strings.Fields(strings.ToLower(Squeeze(s)))
	if len(words) < 4 {
		return ""
	}
	return alnumKey(strings.Join(words[:4], " "))
}

// reRomanLitraHeading is a section, its lettered group and the group's heading words: "I. a.
// Insekter, mider og nematoder på alle udviklingsstadier".
var reRomanLitraHeading = regexp.MustCompile(`,\s*([IVXL]+)\.\s*([a-zæøå])[.)]\s+\p{Lu}[^»]*$`)

// nextLitra reports a line opening with a lettered part after label's: a later letter, alone on
// its line or followed by a word with a capital.
func nextLitra(label string, words []string) bool {
	cur := []rune(strings.ToLower(strings.TrimRight(Squeeze(label), ".)")))
	next := []rune(strings.ToLower(strings.TrimRight(words[0], ".)")))
	if len(cur) != 1 || len(next) != 1 || next[0] <= cur[0] {
		return false
	}
	return len(words) == 1 || unicode.IsUpper([]rune(words[1])[0])
}

// reInsertInto is the place a new part goes, named after the verb: "indsættes i afsnit II.,
// Beskrivelser, nr. 2, et nyt litra g".
var reInsertInto = regexp.MustCompile(`(?i)^\s*i\s+(.+?)\s*,?\s*(?:et|en)\s+ny[t]?\s+(?:litra|nr\.|nummer|punkt|pkt\.)`)

// reInsertIntoAny is any place named after the verb of an insertion: "indsættes i afsnit II".
var reInsertIntoAny = regexp.MustCompile(`(?i)^\s*i\s+(?:afsnit|nr\.|pkt\.|punkt|del|kapitel|tabel)\b`)

var (
	// reNamedLabel is a part named by a word and a hyphenated number: "afsnit A-1", "regel 3-13".
	reNamedLabel = regexp.MustCompile(`(?i)^(?:(afsnit)\s+([A-Z]-[0-9]+)|(regel)\s+([0-9]+(?:-[0-9]+)?))$`)
	// reTableHeading is a table named with its heading: "tabel 1: Opgørelse af antal dyr".
	reStkPiece     = regexp.MustCompile(`(?i)^stk\.\s*[0-9]`)
	reTableHeading = regexp.MustCompile(`(?i)^tabel\s+([0-9]+)\s*:\s*(\p{L}.+)$`)
)

// underScope is the one part labelled label whose nearest heading of scope's shape above it is the
// scope itself.
func underScope(segs []Segment, scope, label string) (Span, bool) {
	shape := labelShape(scope)
	if shape == "" {
		return Span{}, false
	}
	var hits []Span
	for i := range segs {
		if !opensWith(segs[i].Text, label) {
			continue
		}
		for k := i - 1; k >= 0; k-- {
			if opensWithShape(segs[k].Text, shape) || labelShape(firstWords(segs[k].Text, 2)) == shape {
				if opensWith(segs[k].Text, scope) {
					end := len(segs)
					for j := k + 1; j < len(segs); j++ {
						if opensWithShape(segs[j].Text, shape) || labelShape(firstWords(segs[j].Text, 2)) == shape ||
							reStructureHeading.MatchString(Squeeze(segs[j].Text)) && !strings.EqualFold(firstWords(segs[j].Text, 1), firstWords(label, 1)) {
							end = j // "Afdeling III – Lastskibe" after regel 30 is no part of it
							break
						}
					}
					if sp, ok := partSpan(segs, Span{From: k + 1, To: end}, label); ok && sp.From == i {
						hits = append(hits, sp)
					}
				}
				break
			}
		}
	}
	if len(hits) != 1 {
		return Span{}, false
	}
	return hits[0], true
}

// firstWords is a line's first n words.
func firstWords(s string, n int) string {
	f := strings.Fields(Squeeze(s))
	if len(f) > n {
		f = f[:n]
	}
	return strings.Join(f, " ")
}

// reHeadingWord is a change naming a heading: "affattes overskriften", "overskriften til punkt 4".
var reHeadingWord = regexp.MustCompile(`(?i)\boverskrift(?:en)?\b`)

// reStructureHeading is a heading of an annex's own divisions: "Afdeling III – Lastskibe".
var reStructureHeading = regexp.MustCompile(`(?i)^(afsnit|afdeling|kapitel|del|regel)\s+[A-Z0-9IVX]`)

// reAnnexSentenceAfter is a sentence put into an annex's numbered point: "I bilag 2, pkt. 27,
// indsættes efter 2. pkt.:". A point runs from its label to the next; an annex's "afsnit" is a
// paragraph, and the segments the mirror gives an annex are its lines, which do not say where a
// paragraph ends.
var reAnnexSentenceAfter = regexp.MustCompile(`(?i)^\s*I\s+bilag\s+[0-9]+\s*[a-zæøå]?\s*,\s*(?:pkt\.|punkt)\s*([0-9]+)\s*,?\s*indsættes\s+efter\s+([0-9]+)\.\s*(?:pkt\.?|punktum)\s*:`)

// reAnnexPointLabel is a numbered point's label on a line of its own: "27.".
var reAnnexPointLabel = regexp.MustCompile(`^[0-9]+\.$`)

// insertAnnexSentence puts a new sentence after the one a change names in an annex's numbered
// point. It went to the end of the annex.
func insertAnnexSentence(ap *Appendix, def, add string) (string, bool) {
	m := reAnnexSentenceAfter.FindStringSubmatch(Squeeze(def))
	if m == nil || strings.TrimSpace(add) == "" {
		return "", false
	}
	label, at := m[1]+".", -1
	for i, sg := range ap.Segments {
		if sg.Table == 0 && strings.TrimSpace(sg.Text) == label {
			if at >= 0 {
				return "", false // the point is printed twice
			}
			at = i
		}
	}
	if at < 0 {
		return "", false
	}
	var unit []int
	for i := at + 1; i < len(ap.Segments) && ap.Segments[i].Table == 0 && !reAnnexPointLabel.MatchString(strings.TrimSpace(ap.Segments[i].Text)); i++ {
		unit = append(unit, i)
	}
	named := Atoi(m[2])
	after := named
	for _, i := range unit {
		text := ap.Segments[i].Text
		spans := sentenceSpans(text)
		if after <= len(spans) {
			end := spans[after-1][1]
			ap.Segments[i].Text = text[:end] + " " + strings.TrimSpace(add) + text[end:]
			return "pkt. " + m[1] + ", " + itoa(named+1) + ". pkt.", true
		}
		after -= len(spans)
	}
	return "", false
}

// reAnnexLetterBy is a lettered entry put into an annex's numbered point: "I bilag 2, punkt 50,
// indsættes før litra a som nyt litra: »a. …«".
var reAnnexLetterBy = regexp.MustCompile(`(?i)^\s*I\s+bilag\s+[0-9]+\s*[a-zæøå]?\s*,\s*(?:pkt\.|punkt)\s*([0-9]+)\s*,?\s*indsættes\s+(før|efter)\s+litra\s+([a-z])\s+som\s+nyt\s+litra\s*:`)

// reAnnexLetter is a point's letter on a line of its own, "a.", or opening the new words, "a. En …".
var reAnnexLetter = regexp.MustCompile(`^([a-z])\.(?:\s+|$)`)

// insertAnnexLetter puts a lettered entry before or after the one named in an annex's numbered
// point, the letter on a line of its own as the point prints them, and letters the entries again.
func insertAnnexLetter(ap *Appendix, def, add string) (string, bool) {
	m := reAnnexLetterBy.FindStringSubmatch(Squeeze(def))
	nm := reAnnexLetter.FindStringSubmatch(strings.TrimSpace(add))
	if m == nil || nm == nil {
		return "", false
	}
	label, at := m[1]+".", -1
	for i, sg := range ap.Segments {
		if sg.Table == 0 && strings.TrimSpace(sg.Text) == label {
			if at >= 0 {
				return "", false
			}
			at = i
		}
	}
	if at < 0 {
		return "", false
	}
	end := at + 1
	for end < len(ap.Segments) && ap.Segments[end].Table == 0 && !reAnnexPointLabel.MatchString(strings.TrimSpace(ap.Segments[end].Text)) {
		end++
	}
	var letters []int // the letters' own lines in the point
	named := -1
	for i := at + 1; i < end; i++ {
		if lm := reAnnexLetter.FindStringSubmatch(strings.TrimSpace(ap.Segments[i].Text)); lm != nil && strings.TrimSpace(ap.Segments[i].Text) == lm[1]+"." {
			if lm[1] == strings.ToLower(m[3]) {
				named = len(letters)
			}
			letters = append(letters, i)
		}
	}
	if named < 0 {
		return "", false
	}
	pos := letters[named]
	if strings.EqualFold(m[2], "efter") {
		pos = end
		if named+1 < len(letters) {
			pos = letters[named+1]
		}
	}
	body := strings.TrimSpace(strings.TrimSpace(add)[len(nm[0]):])
	ins := []Segment{{Text: nm[1] + ".", Break: true}, {Text: body, Break: true}}
	segs := append(append(append([]Segment{}, ap.Segments[:pos]...), ins...), ap.Segments[pos:]...)
	n := 0
	for i := at + 1; i < end+len(ins); i++ {
		t := strings.TrimSpace(segs[i].Text)
		if lm := reAnnexLetter.FindStringSubmatch(t); lm != nil && t == lm[1]+"." && n < 26 {
			segs[i].Text = string(rune('a'+n)) + "."
			n++
		}
	}
	ap.Segments = segs
	return "pkt. " + m[1] + ", litra " + nm[1], true
}

// annexPartHolder is the annex numbered n a change to a labelled part of it is made in. An annex
// the mirror prints in parts, "Bilag 1" and "Bilag 1 (fortsat)" sixty times over, is the part that
// prints the label the change names, when exactly one does; otherwise the last, as before.
func (c *Content) annexPartHolder(n int, ch AnnexPartChange) *Appendix {
	var all []*Appendix
	for i := range c.Appendices {
		if FirstNumber(c.Appendices[i].Number+c.Appendices[i].Label) == itoa(n) {
			all = append(all, &c.Appendices[i])
		}
	}
	if len(all) <= 1 {
		if len(all) == 1 {
			return all[0]
		}
		return nil
	}
	label := ch.Label
	for _, sc := range ch.Scopes {
		if !strings.HasPrefix(sc, "~") {
			label = sc
			break
		}
	}
	if label == "" {
		return all[len(all)-1]
	}
	var holding []*Appendix
	for _, ap := range all {
		if _, ok := findPart(ap.Segments, Span{From: 0, To: len(ap.Segments)}, label); ok {
			holding = append(holding, ap)
		}
	}
	if len(holding) == 1 {
		return holding[0]
	}
	return all[len(all)-1]
}
