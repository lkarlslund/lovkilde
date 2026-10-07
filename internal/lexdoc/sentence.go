package lexdoc

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
)

// Sentences: what "3. pkt." means.
//
// Danish law numbers the sentences of a subsection, and amendments address them: "§ 7, stk. 8,
// 3. pkt., affattes således", "I § 5, stk. 2, indsættes som 2. pkt.:". The address stops at the
// subsection, and until sentences were resolved every such change replaced or repealed the whole
// subsection — 254 of them over 799 laws, each reported applied.
//
// A sentence ends at a full stop, question mark or exclamation mark followed by a space and a
// capital letter or "§", unless the word before the stop is one of the abbreviations legal prose
// uses mid-sentence ("jf. Rådets direktiv"); a list-closing "m.v." before a capital ends one (see
// closingAbbreviations). The labels a unit prints in front of its text — "§ 4.", "Stk. 2.", "3)" —
// are not sentences.

// abbreviations end with a full stop without ending the sentence. Lowercase, without the stop.
var abbreviations = map[string]bool{
	"jf": true, "jfr": true, "stk": true, "nr": true, "pkt": true, "kr": true, "pct": true,
	"ca": true, "bl.a": true, "f.eks": true, "m.v": true, "mv": true, "m.fl": true, "mfl": true,
	"dvs": true, "evt": true, "inkl": true, "ekskl": true, "vedr": true, "hhv": true, "osv": true,
	"mio": true, "mia": true, "art": true, "kap": true, "lbk": true, "bek": true, "cirk": true,
	"vejl": true, "afd": true, "ff": true, "litra": true, "tlf": true, "att": true, "pr": true,
	"min": true, "maks": true, "mht": true, "iht": true, "jan": true, "feb": true, "aug": true,
	"sept": true, "okt": true, "nov": true, "dec": true, "kl": true, "fx": true, "ifm": true,
	"d": true, "gl": true, "ang": true, "iflg": true,
}

// numberedAbbreviations stand before a number mid-sentence, so a capital after one is a new sentence.
// "art." is the article of a regulation before a number, and the word "art" at the end of one:
// "… behov for almene boliger af den pågældende art. Boligorganisationen opkræver …" is two.
var numberedAbbreviations = map[string]bool{"stk": true, "nr": true, "pkt": true, "kr": true, "pct": true, "litra": true, "art": true}

// closingAbbreviations close a list ("og lignende"), so a capitalised word after one starts a new
// sentence: "… jf. § 4 i lov om vægtafgift af motorkøretøjer m.v. Er dagsafgiften …" is two, and
// the amending act counts them as two.
var closingAbbreviations = map[string]bool{"m.v": true, "mv": true, "m.fl": true, "mfl": true, "osv": true, "m.m": true}

// rePktOpening is a sentence opening with the sentences it cites: "1. pkt. finder …", "1. og 2.
// pkt. …", "1.-3. pkt. …".
var rePktOpening = regexp.MustCompile(`^[0-9]+\.\s*(?:(?:og|-|–)\s*[0-9]+\.\s*)?(?:pkt|punktum)`)

// reLeadingLabel is a unit's own printed label, which comes before its first sentence.
var reLeadingLabel = regexp.MustCompile(`^\s*(?:§+\s*[0-9]+(?:\s*[a-zæøå]{1,2})?\.\s*|stk\.\s*[0-9]+\.\s*|[0-9]+\)\s*|[a-zæøå]\)\s*)+`)

// sentenceSpans returns the byte ranges of a text's sentences, after any leading label. Each range
// includes its closing punctuation and not the space after it.
func sentenceSpans(text string) [][2]int {
	start := 0
	if loc := reLeadingLabel.FindStringIndex(strings.ToLower(text)); loc != nil {
		start = loc[1]
	}
	var out [][2]int
	from := start
	for i, r := range text {
		if i < start || (r != '.' && r != '?' && r != '!') {
			continue
		}
		rest := text[i+1:]
		if len(rest) == 0 || rest[0] != ' ' {
			continue
		}
		next := strings.TrimLeft(rest, " ")
		if next == "" {
			continue
		}
		first := []rune(next)[0]
		if unicode.IsDigit(first) && r == '.' {
			// "… højst udgøre 2 måneder. 1. og 2. pkt. finder tilsvarende anvendelse": a sentence
			// that cites sentences opens with a number, as does "13-skalaen anvendes". After an
			// abbreviation or a number of its own the stop is not an ending, unless what follows
			// names sentences.
			w := strings.ToLower(lastWord(text[from:i]))
			names := rePktOpening.MatchString(next)
			word := w != "" && unicode.IsLetter([]rune(w)[0]) && !abbreviations[w]
			if !names && !word || names && abbreviations[w] {
				continue
			}
			out = append(out, [2]int{from, i + 1})
			from = i + 1 + (len(rest) - len(next))
			continue
		}
		if !unicode.IsUpper(first) && first != '§' {
			continue
		}
		if r == '.' {
			w := strings.ToLower(lastWord(text[from:i]))
			// "stk.", "nr.", "pkt." and their kind are followed by a number in the middle of a
			// sentence; before a capitalised word they end one: "… efter 7. pkt. Ministeren kan …".
			if abbreviations[w] && !((numberedAbbreviations[w] || closingAbbreviations[w]) && unicode.IsLetter(first)) {
				continue
			}
		}
		out = append(out, [2]int{from, i + 1})
		from = i + 1 + (len(rest) - len(next))
	}
	if end := strings.TrimRight(text, " "); from < len(end) {
		out = append(out, [2]int{from, len(end)})
	}
	return out
}

// lastWord is the word in front of a full stop, abbreviation dots included: "m.v", "jf", "2".
func lastWord(s string) string {
	i := strings.LastIndexFunc(s, func(r rune) bool { return unicode.IsSpace(r) || r == '(' })
	return s[i+1:]
}

// preActSentence finds, in the unit's current text, the sentence that was number n before the act
// being applied: "I § 37 ophæves 1. pkt., og i 2. pkt. ændres …" means the old second sentence,
// which is the first once the first has gone. The old sentence's words are looked up in the text
// as it stands; when the act has already rewritten that sentence, the current number is used.
func (l Located) preActSentence(n int) (string, bool) {
	p := l.Provision
	if p == nil || p.preAct == nil {
		return "", false
	}
	old := Located{Provision: p.preAct}
	if l.Stykke != nil {
		for i := range p.preAct.Stykker {
			if p.preAct.Stykker[i].Nr == l.Stykke.Was {
				old.Stykke = &p.preAct.Stykker[i]
			}
		}
		if old.Stykke == nil {
			return "", false
		}
	}
	if l.Item != nil {
		if old.Stykke == nil {
			return "", false
		}
		for i := range old.Stykke.Items {
			if old.Stykke.Items[i].Nr == l.Item.Was && old.Stykke.Items[i].Litra == l.Item.Litra {
				old.Item = &old.Stykke.Items[i]
			}
		}
		if old.Item == nil {
			return "", false
		}
	}
	oldText, _, ok := old.unitText()
	if !ok {
		return "", false
	}
	s, ok := sentenceText(oldText, n, 0)
	if !ok {
		return "", false
	}
	cur, _, _ := l.unitText()
	if !strings.Contains(MatchText(cur), MatchText(s)) {
		return "", false
	}
	return s, true
}

// sentenceText returns sentences from..to (1-based, to 0 meaning from alone) of a unit's text, or
// false when the unit has fewer.
func sentenceText(text string, from, to int) (string, bool) {
	if to == 0 {
		to = from
	}
	spans := sentenceSpans(text)
	if from < 1 || to < from || to > len(spans) {
		return "", false
	}
	return text[spans[from-1][0]:spans[to-1][1]], true
}

// unitText is the text an address resolved to, rendered the way replaceIn reads it.
func (l Located) unitText() (string, Span, bool) {
	p := l.Provision
	if p == nil {
		return "", Span{}, false
	}
	sp := Span{From: 0, To: len(p.Segments)}
	switch {
	case l.Item != nil:
		sp = l.Item.Span
	case l.Stykke != nil:
		sp = l.Stykke.Span
	case len(p.Stykker) > 1:
		return "", Span{}, false // a provision of several subsections has no sentence numbering of its own
	case len(p.Stykker) == 1:
		sp = p.Stykker[0].Span
	}
	return Squeeze(ownText(p.Segments, sp)), sp, true
}

// ownText is a span's text without the footnotes set inside it: the sentences a change counts are
// the provision's own.
func ownText(segs []Segment, sp Span) string {
	if sp.To > len(segs) || sp.To < sp.From {
		sp.To = len(segs)
	}
	if sp.From >= sp.To {
		return ""
	}
	own := make([]Segment, 0, sp.To-sp.From)
	for _, s := range segs[sp.From:sp.To] {
		if s.Note == "" {
			own = append(own, s)
		}
	}
	return SegmentsText(own)
}

// applySentence carries out a change addressed to sentences: replacing, repealing, or inserting
// one. It reports false when the address names no sentence, and refuses rather than guess when the
// unit has fewer sentences than the change counts.
func (c *Content) applySentence(e Edit, loc Located, reject func(string), apply func(where, note string)) bool {
	a := e.Address
	if a.Sentence == 0 || a.Heading {
		return false
	}
	var replacement string
	switch e.Kind {
	case EditReplaceProvision:
		replacement = e.blockString()
	case EditDeleteProvision:
	case EditInsertProvision:
		replacement = e.blockString()
	default:
		return false // a quoted substitution inside a sentence needs no sentence to find its words
	}
	where := loc.String() + ", " + itoa(a.Sentence) + ". pkt."
	text, sp, ok := loc.unitText()
	if !ok {
		reject("the change names a sentence of " + loc.String() + ", which has no sentence numbering of its own")
		return true
	}
	if e.Kind == EditInsertProvision {
		if m := reAfterSentence.FindStringSubmatch(e.Definition); m != nil {
			// "indsættes efter 1. pkt. som nye punktummer" names the sentence the new ones follow;
			// read as the number they take, the new sentences went in front of it.
			a.Sentence = Atoi(m[1]) + 1
			where = loc.String() + ", " + itoa(a.Sentence) + ". pkt."
		}
		// "indsættes som 3. pkt." goes after the second sentence, or in front of everything as 1.
		if a.Sentence == 1 {
			first, ok := sentenceText(text, 1, 0)
			if !ok || !replaceAt(loc.Provision, sp, first, strings.TrimSpace(replacement)+" "+reSpaceBeforeMark.ReplaceAllString(first, "$1")) {
				reject("could not place the new first sentence of " + loc.String())
				return true
			}
			apply(where, "")
			return true
		}
		prev, ok := sentenceText(text, a.Sentence-1, 0)
		if !ok {
			reject(where + " follows a sentence " + loc.String() + " does not have")
			return true
		}
		// Written back, the sentence read around a footnote is tidied: "36-42 , om" was
		// "36-42" and ", om" either side of the note.
		if !replaceAt(loc.Provision, sp, prev, reSpaceBeforeMark.ReplaceAllString(prev, "$1")+" "+strings.TrimSpace(replacement)) {
			reject("could not place " + where)
			return true
		}
		apply(where, "")
		return true
	}
	if a.SentenceList {
		// "1. og 4. pkt." are two sentences, not the run between them. Repealing goes from the
		// last so the first keeps its number; a non-adjacent rewrite has no single place to go.
		if e.Kind != EditDeleteProvision {
			reject(where + " names two separate sentences for one new text")
			return true
		}
		for _, n := range []int{a.SentenceTo, a.Sentence} {
			cur, _, _ := loc.unitText()
			s, ok := sentenceText(cur, n, 0)
			if !ok || !replaceAt(loc.Provision, sp, s, "") {
				reject(fmt.Sprintf("%s has no sentence %d", loc, n))
				return true
			}
		}
		apply(where, "")
		return true
	}
	old, ok := sentenceText(text, a.Sentence, a.SentenceTo)
	if pre, has := loc.preActSentence(a.Sentence); has && a.SentenceTo == 0 && !a.SentenceBecomes {
		old, ok = pre, true
	}
	if !ok && e.Kind == EditDeleteProvision && c.carried {
		// "§ 16 B, stk. 2, nr. 4, 2. pkt., ophæves" by an act the base may carry, whose nr. 4
		// prints one sentence: the base made the repeal already.
		apply(where, "the text already reads as the change has it; the sentence is not printed")
		return true
	}
	if !ok {
		reject(where + " is not in the current text: " + loc.String() + " has fewer sentences")
		return true
	}
	if e.Kind == EditReplaceProvision && strings.TrimSpace(replacement) == "" {
		reject("sentence replacement carries no new text")
		return true
	}
	if !replaceAt(loc.Provision, sp, old, strings.TrimSpace(replacement)) {
		reject("could not replace " + where)
		return true
	}
	apply(where, "")
	return true
}

// reSpaceBeforeMark is a space left before a closing mark where two pieces of text were joined.
var reSpaceBeforeMark = regexp.MustCompile(`\s+([,.;:])`)

// replaceAt replaces one passage inside a span with new text, and reports whether it was there.
func replaceAt(p *Provision, sp Span, old, new string) bool {
	found, _, n := replaceIn(p.Segments, sp, old, new, false)
	return found && n > 0
}

// NewWording is the wording a change brings, its provisions' text included, reduced for matching
// and without the label it opens with; "" when the change brings none.
func (e Edit) NewWording() string {
	text := e.blockString()
	if text == "" {
		var parts []string
		for _, p := range e.NewProvisions {
			parts = append(parts, p.Text())
		}
		text = strings.Join(parts, " ")
	}
	return MatchText(stripLeadLabel(Squeeze(text)))
}

// BroughtText is the new text a change brings, as printed: for a reader shown a change the engine
// could not place. Empty for a change whose new words are its quotation (Edit.New).
func (e Edit) BroughtText() string {
	text := e.blockString()
	if text == "" {
		var parts []string
		for _, p := range e.NewProvisions {
			parts = append(parts, p.Text())
		}
		text = strings.Join(parts, " ")
	}
	return Squeeze(text)
}

// blockString is the new text a change brings, as one string without its quotation marks.
func (e Edit) blockString() string {
	var parts []string
	for _, s := range e.NewText {
		parts = append(parts, s.Text)
	}
	text := strings.Join(parts, " ")
	if text == "" {
		text = strings.Join(e.NewProse, " ")
	}
	if text == "" {
		text = e.New
	}
	text = strings.TrimSpace(Squeeze(text))
	text = strings.TrimSuffix(strings.TrimPrefix(text, "»"), "«")
	return strings.TrimSpace(text)
}

// replaceInSentence edits a quoted passage inside the sentence a change names, or, naming none,
// anywhere in the unit. "I § 30, stk. 4, 1. pkt., der bliver 2. pkt., indsættes efter »kan«" means
// the "kan" of that sentence: searching the whole subsection found it in the sentence the same act
// had just put in front, and inserted the words there.
//
// When the passage is not in the named sentence, the change's numbering and ours disagree; the
// passage itself is then the better witness, but only if it occurs once in the unit.
func (l Located) replaceInSentence(c *Content, a Address, old, new string, all bool) (found, changed bool) {
	if a.Sentence > 0 && l.Provision != nil {
		if text, sp, ok := l.unitText(); ok {
			sentences := []int{a.Sentence}
			switch {
			case a.SentenceList:
				sentences = append(sentences, a.SentenceTo)
			case a.SentenceTo > a.Sentence:
				sentences = nil
				for n := a.Sentence; n <= a.SentenceTo; n++ {
					sentences = append(sentences, n)
				}
			}
			// Each sentence is read from the text as it stands, after the ones before it changed,
			// and edited where it stands: the occurrence inside the sentence is replaced in place.
			// Rewriting the sentence as one string emptied every segment it spanned, and a
			// sentence that runs into a list of conditions took the list's items with it.
			for _, n := range sentences {
				text, _, _ := l.unitText()
				s, ok := sentenceText(text, n, 0)
				if pre, has := l.preActSentence(n); has && !a.SentenceBecomes {
					s, ok = pre, true // the act cites its sentences by their numbers before it
				}
				if !ok {
					continue
				}
				start := strings.Index(text, s)
				inSentence := countMatches(s, old)
				if start < 0 || inSentence == 0 {
					continue
				}
				before := countMatches(text[:start], old)
				picks := []int{before}
				if all {
					picks = nil
					for k := inSentence - 1; k >= 0; k-- {
						picks = append(picks, before+k)
					}
				}
				for _, k := range picks {
					if f, ch, m := replaceSelected(l.Provision.Segments, sp, old, new, k); f && m > 0 {
						found = true
						changed = changed || ch
					}
				}
			}
			if found {
				return found, changed
			}
			if strings.Count(MatchText(text), MatchText(old)) != 1 {
				return false, false
			}
		}
	}
	if all {
		return l.replaceSpanAll(c, old, new)
	}
	return l.replaceSpan(c, old, new)
}

// replaceSpanAll replaces every occurrence in the unit: "to steder i § 24, stk. 2, ændres …".
func (l Located) replaceSpanAll(c *Content, old, new string) (found, changed bool) {
	p := l.Provision
	if p == nil && l.Part != nil && l.Appendix == nil {
		return replaceInPart(l.Part, old, new, true)
	}
	if p == nil {
		return l.replaceSpan(c, old, new)
	}
	sp := Span{From: 0, To: len(p.Segments)}
	switch {
	case l.Item != nil:
		sp = l.Item.Span
	case l.Stykke != nil:
		sp = l.Stykke.Span
	}
	f, ch, n := replaceIn(p.Segments, sp, old, new, true)
	return f && n > 0, ch
}

// reSeveralPlaces is "to steder", "begge steder", "3 steder": the passage is replaced wherever it
// occurs in the place named, not only the first time.
var reSeveralPlaces = regexp.MustCompile(`(?i)\b(?:to|tre|fire|fem|seks|syv|otte|ni|ti|begge|alle|[0-9]+)\s+steder\b|^\s*overalt\s+i\s+(?:kapitel|afsnit|afdeling)\b`)

// SentencesOf returns a unit text's sentences, for diagnostics and tools.
func SentencesOf(text string) []string {
	var out []string
	for _, sp := range sentenceSpans(text) {
		out = append(out, text[sp[0]:sp[1]])
	}
	return out
}

// UnitText is the text an address resolved to, for diagnostics and tools.
func (l Located) UnitText() string {
	t, _, _ := l.unitText()
	return t
}

// alreadyReads reports that a unit already holds a change's new wording where the change puts it:
// in the named sentence, or, naming none, anywhere in the unit when the wording is long enough not
// to be there by coincidence.
func (l Located) alreadyReads(a Address, wording string) bool {
	w := MatchText(QuoteStrip(wording))
	if w == "" || l.Provision == nil {
		return false
	}
	text, _, ok := l.unitText()
	if !ok {
		return false
	}
	if a.Sentence > 0 {
		s, ok := sentenceText(text, a.Sentence, a.SentenceTo)
		return ok && strings.Contains(MatchText(s), w)
	}
	return len([]rune(w)) >= 12 && strings.Contains(MatchText(text), w)
}

// OwnText is the provision's text without the footnotes set inside it: its own words.
func (p *Provision) OwnText() string {
	return ownText(p.Segments, Span{From: 0, To: len(p.Segments)})
}

// reAfterSentence is an insertion naming the sentence it follows: "indsættes efter 2. pkt.".
var reAfterSentence = regexp.MustCompile(`(?i)\bindsættes\s+efter\s+([0-9]+)\.\s*(?:pkt|punktum)`)
