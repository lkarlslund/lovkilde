package lexdoc

import (
	"regexp"
	"strings"
	"time"
)

// The wording of a change, read the same way whatever format it arrived in.
//
// An amending act says what it changes in Danish legislative prose: "I § 6, stk. 4, ændres »stk. 3«
// til: »stk. 1«", "§ 7 affattes således:", "Efter § 18 indsættes:". The register's XML wraps that
// prose in markup and the site's HTML prints it as paragraphs, but the sentence is the same, and so
// is what it means. Reading it is therefore the model's business, not a front end's: a front end
// finds the sentence and whatever new text follows it, and everything here decides what the change
// is, which law it is aimed at, and what it brings. One reader for both formats is what keeps an act
// from meaning one thing in XML and another in HTML.

// Target identifies the law a change list is aimed at, as its preamble names it: "I bekendtgørelse
// nr. 1399 af 12. december 2019 …".
//
// It is a hint rather than an accession because the preamble quotes the Danish way (number plus
// signing date), not the register identifier. Only 1 of 70 sampled amending acts declared a
// Concerns anchor, so the target cannot be read from metadata.
type Target struct {
	Raw    string
	Number int
	Date   time.Time
	Title  string
	// Kind is the instrument a preamble names when it gives no number to recognise it by: "I
	// forordning af 2. december 1825 ang. Strandings-Commissionaires Sallarium …". Such a change
	// list is not aimed at a law, whatever else the act amends.
	Kind string
	// Earlier are the acts the preamble says amended the instrument before this one: "jf.
	// lovbekendtgørelse nr. 1114 af 18. september 2013, som ændret ved § 4 i lov nr. 202 af 27.
	// februar 2015 og senest ved § 15 i lov nr. 688 af 8. juni 2017". Section is the provision
	// of that act which does it, when the preamble names one.
	Earlier []Target
	Section string
}

// Empty reports a preamble that named no instrument this reader could recognise.
func (t Target) Empty() bool { return t.Number == 0 && t.Date.IsZero() }

// reTarget matches the preamble form "bekendtgørelse nr. 1399 af 12. december 2019". Two things
// cost more than they look:
//
//   - The month is a NAME in Danish practice, not a number: matching only a numeric month silently
//     yields an empty hint, and an empty hint means "target unknown", which makes the engine apply a
//     change aimed at a different law to the law it is reconstructing.
//   - The instrument is often a compound: "jf. lovbekendtgørelse nr. 295 af 7. marts 2022".
//     Requiring a word boundary in front of "bekendtgørelse" fails to match inside
//     "lovbekendtgørelse", and an amending act that changes several laws then has its election-law
//     changes applied to the municipal government act. Measured: of 117 changes kept for lov om
//     kommunernes styrelse, the ones naming § 96 and § 100 — paragraphs that act does not have —
//     came from exactly this.
//
// Letters are therefore allowed on both sides of the instrument word, and the run up to "nr." may
// not cross a comma; "lovbekendtgørelse 377 af 18. april 2017" with no "nr." at all is the same
// citation (lov nr. 710 of 2017's change to registreringsafgiftsloven, applied to the law on
// borgerservicecentre because its target read as unknown), so the reference to the instrument being amended is the one that is read
// rather than an earlier citation in the same sentence.
var reTarget = regexp.MustCompile(`(?i)(?:^|[^\p{L}])[a-zæøå]*(?:lov|bekendtgørelse|cirkulære|anordning|forordning|bekg)[a-zæøå]*(?:[^,.]{0,60}?nr\.?\s*|\s+)([0-9]+)\s*af\s*([0-9]{1,2})[./]\s*([0-9]{1,2}|[a-zæøå]+)\s*([0-9]{4})`)

// ParseTarget reads "bekendtgørelse nr. 1399 af 12. december 2019" out of a preamble.
func ParseTarget(preamble string) Target {
	m := reTarget.FindStringSubmatch(preamble)
	if m == nil {
		t := Target{Raw: preamble}
		if k := reOldInstrument.FindStringSubmatch(preamble); k != nil {
			t.Kind = strings.ToLower(k[1])
		}
		return t
	}
	t := Target{Raw: m[0], Number: Atoi(m[1])}
	day, year := Atoi(m[2]), Atoi(m[4])
	if month := MonthNumber(m[3]); day > 0 && month > 0 && year > 0 {
		t.Date = time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC)
	}
	t.Earlier = earlierActs(preamble[strings.Index(preamble, m[0])+len(m[0]):])
	if n := reLawNamed.FindStringSubmatch(preamble); n != nil {
		t.Title = strings.Join(strings.Fields(n[1]), " ")
	}
	return t
}

// reLawNamed is the law a change list names before its consolidation: "I lov om almene boliger
// m.v., jf. lovbekendtgørelse nr. 106 af 21. februar 2005 …".
var reLawNamed = regexp.MustCompile(`(?i)^\s*(?:§\s*[0-9]+\s*[a-zæøå]?\.?\s*)?I\s+([^,»]{4,200}?)\s*,\s*jf\.\s*(?:lov)?bekendtgørelse`)

var (
	// reAmendedBy opens the list of earlier amendments: "som ændret ved", "som ændret bl.a. ved",
	// "senest ved".
	reAmendedBy = regexp.MustCompile(`(?i)\bsom\s+(?:senest\s+)?ændret\s+(?:bl\.\s*a\.\s+|blandt andet\s+)?ved\s+`)
	// reEarlierAct is one act of that list: "§ 4 i lov nr. 202 af 27. februar 2015", "lov nr. 1585
	// af 27. december 2019".
	reEarlierAct = regexp.MustCompile(`(?i)(?:§\s*([0-9]+(?:\s*[a-zæøå](?:[^\p{L}]|$))?)\s*,?\s*(?:i|og)\s+)?(?:lov|bekendtgørelse)\s+nr\.?\s*([0-9]+)\s+af\s+([0-9]{1,2})\.\s*([a-zæøå]+)\s+([0-9]{4})`)
	// reAmendedByEnd closes it: the list runs to the sentence's "foretages følgende ændring(er)".
	reAmendedByEnd = regexp.MustCompile(`(?i)\bforetages\b`)
)

// earlierActs reads the acts a preamble says amended the instrument already.
func earlierActs(rest string) []Target {
	loc := reAmendedBy.FindStringIndex(rest)
	if loc == nil {
		return nil
	}
	rest = rest[loc[0]:]
	if end := reAmendedByEnd.FindStringIndex(rest); end != nil {
		rest = rest[:end[0]]
	}
	var out []Target
	for _, m := range reEarlierAct.FindAllStringSubmatch(rest, -1) {
		month := MonthNumber(m[4])
		if month == 0 {
			continue
		}
		out = append(out, Target{Raw: m[0], Number: Atoi(m[2]), Section: strings.Join(strings.Fields(strings.TrimRight(m[1], " ,.;")), ""),
			Date: time.Date(Atoi(m[5]), time.Month(month), Atoi(m[3]), 0, 0, 0, 0, time.UTC)})
	}
	return out
}

// reOldInstrument is a preamble naming an instrument by kind and date alone: "I forordning af 2.
// december 1825 …", "I anordning af …", "I plakat af …".
var reOldInstrument = regexp.MustCompile(`(?i)^\s*(?:§\s*[0-9]+\s*[a-zæøå]?\.\s*)?i\s+(forordning|anordning|plakat|reglement|reskript|kongelig resolution)\s+af\s+[0-9]`)

// MonthNumber resolves a Danish month name, tolerating the numeric form too.
func MonthNumber(s string) int {
	s = strings.ToLower(strings.TrimRight(strings.TrimSpace(s), "."))
	if n := Atoi(s); n > 0 {
		return n
	}
	return months[s]
}

// months is the Danish month vocabulary used in dates throughout the corpus.
var months = map[string]int{
	"januar": 1, "februar": 2, "marts": 3, "april": 4, "maj": 5, "juni": 6,
	"juli": 7, "august": 8, "september": 9, "oktober": 10, "november": 11, "december": 12,
}

var (
	// The colon after "til" is the convention, not the rule: "ændres »X« til »Y«" and
	// "ændres »X«: »Y«" both occur, and reading them as unrecognised refused plain substitutions.
	reOldNew = regexp.MustCompile(`(?is)ændres\s*(.*?)\s*(?:til\s*:\s*(.*)|til\s+(».*)|:\s*(».*))$`)
	reQuoted = regexp.MustCompile(`(?s)»([^»«]*)«|\x{201c}([^\x{201d}]*)\x{201d}|\x{201e}([^\x{201f}]*)\x{201f}|"([^"]*)"`)
	reNewPar = regexp.MustCompile(`(?i)indsættes\s+(?:en ny|et nyt|nye)\s+(?:paragraf|§| stk|nummer)`)
	// nonOperativeMarker matches the parenthetical notes a Danish change list uses in place of an
	// instruction. They describe the provision's fate or its territorial scope; none of them asks
	// for a word to change.
	nonOperativeMarker = regexp.MustCompile(`(?i)(sættes ikke i kraft|træder ikke i kraft|ophævet|gælder ikke for|fastsættes ikke)`)
)

// Brought is what a change carries besides its own sentence, as the front end found it.
type Brought struct {
	Any       bool   // the change brings new text at all
	Structure bool   // that text is a provision, a subsection or a chapter
	First     string // the text of the first thing it brings
}

// Classify decides what a numbered change does from its wording and from what it brings, and
// returns the quoted old and new wording the sentence carries. Structured evidence wins over prose
// inference, and EditUnknown says when neither is enough.
func Classify(definition, prose string, addr Address, b Brought) (kind EditKind, old, new string) {
	def := definition
	if def == "" {
		def = prose
	}
	low := strings.ToLower(def)
	trimmed := strings.TrimSpace(def)

	// Text in parentheses in a change list is a note about the provision, not an instruction to
	// change it: "(Sættes ikke i kraft for Færøerne)", "(Ophævet)". Only a definition that is
	// *entirely* parenthetical counts, so a real edit with a parenthetical afterthought stays one.
	if strings.HasPrefix(trimmed, "(") && strings.HasSuffix(trimmed, ")") && nonOperativeMarker.MatchString(trimmed) {
		return EditNonOperative, "", ""
	}

	// "I bilag 3 indsættes i gruppen »…« efter »Aramit«: »Atrazin 95)« med maksimalgrænseværdien
	// »0,1«" edits a table's rows and cells. The quotations name groups, rows and values, and read
	// as words to replace or delete they named the wrong ones.
	if reTableCells.MatchString(def) {
		return EditUnknown, "", ""
	}

	// "Overalt i loven ændres »X« til: »Y«" carries an old/new pair but no address, and its § is
	// inside the quoted text — see Address.Everywhere.
	if addr.Everywhere {
		// Every operation the sentence carries is carried out everywhere; the engine reads them
		// all, and the first is what this change is called.
		if ops := parseOperations(def); len(ops) > 0 {
			o := ops[0].asEdit(Edit{Address: addr})
			return EditReplaceEverywhere, o.Old, o.New
		}
		return EditUnknown, old, new
	}

	// "Stk. 2 bliver herefter stk. 3." on its own. A change that both inserts something and says
	// the old subsection becomes the new one is an insertion — the renumbering is the consequence,
	// not a separate instruction — so the operative verbs rule this out before it is classified.
	if strings.Contains(low, "bliver herefter") &&
		!strings.Contains(low, "indsættes") && !strings.Contains(low, "ophæves") &&
		!strings.Contains(low, "udgår") && !strings.Contains(low, "ændres") &&
		!strings.Contains(low, "affattes") && !strings.Contains(low, "erstattes") {
		return EditRenumber, "", ""
	}

	// The quoted-pair form is the most reliable prose pattern: both the old and the new wording
	// are present verbatim, so it can be applied and verified.
	//
	// Unless the sentence goes on to bring new text: "I § 17, stk. 5, ændres »A« til: »B«, og som 2.
	// pkt. indsættes:" is an insertion whose sentence also edits words, and read as a replacement
	// the new sentence it brings was dropped while the change was reported applied.
	if m := reOldNew.FindStringSubmatch(def); m != nil && !(b.Any && bringsAfterQuotes(def)) {
		old, new = firstQuoted(m[1]), firstQuoted(m[2]+m[3]+m[4]) // one of the three spellings matched
		if old != "" && new != "" {
			return EditReplaceText, old, new
		}
	}

	// "Overskriften før § 38 »A« affattes således: »B«" rewrites a heading in prose.
	if addr.Heading && strings.Contains(low, "affattes") && !b.Any {
		if q := QuotedPassages(def); len(q) > 0 {
			return EditReplaceProvision, old, q[len(q)-1]
		}
	}

	if b.Any {
		switch {
		case strings.Contains(low, "ophæves") && strings.Contains(low, "i stedet"):
			// "§ 106, stk. 3, ophæves, og i stedet indsættes: …" replaces what it repeals.
			return EditReplaceProvision, old, b.First
		case reNewPar.MatchString(def), strings.Contains(low, "indsættes"), strings.Contains(low, "tilføjes"):
			// "I punkt 4.3 tilføjes: »12. …«" says the same as "indsættes".
			return EditInsertProvision, old, b.First
		case strings.Contains(low, "ophæves"), strings.Contains(low, "affattes"), strings.Contains(low, "i stedet for"):
			return EditReplaceProvision, old, b.First
		case b.Structure:
			return EditReplaceProvision, old, b.First
		}
	}

	// No new text: fall back to prose with quoted passages.
	quotes := QuotedPassages(def)
	switch {
	case strings.Contains(low, "ophæves") && strings.Contains(low, "i stedet") && len(quotes) > 0:
		// "Bilag 2, punkt 57 og 58 ophæves, og i stedet indsættes: »57. …«" replaces what it
		// repeals with the words it quotes; read as an insertion of those words, they were added
		// beside the parts they replace.
		return EditReplaceProvision, old, quotes[len(quotes)-1]
	case strings.Contains(low, "ophæves") && len(quotes) == 0:
		// Repealing with nothing quoted means the whole unit goes.
		return EditDeleteProvision, old, new
	case strings.Contains(low, "ophæves") && reQuotedRepeal.MatchString(def):
		// "ophæves »X«" or "»X« ophæves": the quotation next to the verb is what goes. A sentence
		// quoting other things around its repeal does not say which words go.
		return EditDeleteText, reQuotedRepeal.FindStringSubmatch(def)[1] + reQuotedRepeal.FindStringSubmatch(def)[2], new
	// "I § 6, stk. 1, udgår »§ 3, stk. 2-3,«." is the ordinary Danish way to delete a passage;
	// "slettes" says the same.
	case (strings.Contains(low, "udgår") || strings.Contains(low, "slettes")) && len(quotes) > 0:
		return EditDeleteText, quotes[0], new
	case strings.Contains(low, "erstattes") && len(quotes) > 1:
		return EditReplaceText, quotes[0], quotes[1]
	case strings.Contains(low, "udgår"):
		return EditDeleteProvision, old, new
	case strings.Contains(low, "indsættes") && len(quotes) > 0:
		new = quotes[0]
		if len(quotes) > 1 {
			new = quotes[1]
		}
		if len(quotes) > 2 && (reInSectionQuoted.MatchString(def) || reUnderHeading.MatchString(def)) {
			// "I Bilag 1 indsættes i afsnittet »Kriterium 2: …« efter »Indikator 2.b: …«: »…«": the
			// section, the passage the words follow, and the words.
			new = quotes[2]
		}
		return EditInsertText, old, new
	}
	return EditUnknown, old, new
}

// reInSectionQuoted names the section of an annex an insertion goes into by its quoted title.
var reInSectionQuoted = regexp.MustCompile(`(?i)\bindsættes\s+i\s+afsnit(?:tet)?\s+»`)

// firstQuoted returns the first quoted passage in s, or the trimmed string when nothing is quoted.
func firstQuoted(s string) string {
	if q := QuotedPassages(s); len(q) > 0 {
		return q[0]
	}
	return Squeeze(QuoteStrip(s))
}

// QuotedPassages returns every quoted passage in order. Amendments delimit passages with » « but
// curly quotes appear too, and both spellings show up in the same document.
func QuotedPassages(s string) []string {
	var out []string
	for _, m := range reQuoted.FindAllStringSubmatch(s, -1) {
		for _, g := range m[1:] {
			if g = Squeeze(g); g != "" {
				out = append(out, g)
				break
			}
		}
	}
	return out
}

// reBringsTail is a change sentence that ends by bringing new text after its quoted passages:
// "…, og som 2. pkt. indsættes:", "…, og stk. 3 affattes således:".
var reBringsTail = regexp.MustCompile(`(?i)(?:indsættes|affattes(?:\s+således)?|tilføjes)\s*:?\s*$`)

// bringsAfterQuotes reports a sentence whose last words, after its last quoted passage, bring
// new text.
func bringsAfterQuotes(def string) bool {
	i := strings.LastIndexAny(def, "«”")
	if i < 0 {
		return false
	}
	return reBringsTail.MatchString(def[i:])
}

var (
	// reCloserTypedAsOpener is "»§ 25, stk. 1 og 3» til:": the closing mark typed as an opening one.
	reCloserTypedAsOpener = regexp.MustCompile(`»([^»«]{1,400}?)»(\s*(?:til\b|ændres\b|udgår\b|indsættes\b|[,.:]|$))`)
	// reOpenerTypedAsCloser is "ændres «nr. 1-4« til:": the opening mark typed as a closing one.
	reOpenerTypedAsCloser = regexp.MustCompile(`(^|[\s:])«([^»«]{1,400}?)«`)
)

// RepairQuotes mends the quotation marks of a change sentence that typed one of a pair the wrong
// way round, which left its old and new wording unreadable. A sentence whose marks balance is left
// as it is.
func RepairQuotes(def string) string {
	if alternated, ok := alternateQuotes(def); ok {
		return alternated
	}
	if strings.Count(def, "»") == strings.Count(def, "«") {
		return def
	}
	if strings.Count(def, "»") > strings.Count(def, "«") {
		return reCloserTypedAsOpener.ReplaceAllString(def, "»$1«$2")
	}
	return reOpenerTypedAsCloser.ReplaceAllString(def, "$1»$2«")
}

// alternateQuotes re-pairs the marks of a sentence that quotes flat passages, none inside another,
// when they do not already alternate: "ændres «Styrelsen …» til: «den …«" opens, closes, opens and
// closes. A sentence whose marks already alternate, or that nests a quotation, is left alone.
func alternateQuotes(def string) (string, bool) {
	if strings.Contains(def, "»»") || strings.Contains(def, "««") {
		return def, false
	}
	runes := []rune(def)
	var at []int
	for i, r := range runes {
		if r == '»' || r == '«' {
			at = append(at, i)
		}
	}
	if len(at) < 2 || len(at)%2 != 0 {
		return def, false
	}
	// Marks that nest properly, "»X »Y« Z«" as much as "»X« … »Y«", are read as written.
	depth, nests := 0, true
	for _, i := range at {
		if runes[i] == '»' {
			depth++
		} else {
			depth--
		}
		if depth < 0 {
			nests = false
		}
	}
	if nests && depth == 0 {
		return def, false
	}
	for k, i := range at {
		if k%2 == 0 {
			runes[i] = '»'
		} else {
			runes[i] = '«'
		}
	}
	return string(runes), true
}

// reTableCells is a change to a table's rows and values.
var reTableCells = regexp.MustCompile(`(?i)\bmed\s+(?:maksimal)?grænseværdi(?:en)?\b|\bi\s+gruppen\s*»[^«]*«\s*som\s+nye?\s+produkt`)

// reQuotedRepeal is a quotation beside "ophæves": the words it repeals.
var reQuotedRepeal = regexp.MustCompile(`(?i)ophæves\s*:?\s*»([^«]+)«|»([^«]+)«\s*,?\s*ophæves`)
