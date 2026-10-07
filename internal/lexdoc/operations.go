package lexdoc

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// One change sentence, several operations.
//
// "I § 5 ændres »a« til: »b«, og efter »c« indsættes: »d«" is one numbered change carrying two
// edits, and "Overalt i forordningen ændres »Amtmand« til: »statsforvaltning«, »Amtmandens« til:
// »statsforvaltningens« …" carries four. Reading the first and dropping the rest applied part of a
// change and reported all of it as applied, which no count can show.

// operation is one edit a change sentence asks for.
type operation struct {
	lead   string   // the sentence between the previous operation and this one: "og i nr. 1"
	place  string   // for a repeal: the units it names, "stk. 4" or "nr. 5 og nr. 6"
	kind   EditKind // replace-text, delete-text or insert-text
	old    string   // the passage replaced or deleted, or the one an insertion goes by
	new    string
	before bool // an insertion in front of old rather than after it
	// whole marks an operation the sentence states in parts the change's own Old and New do not
	// hold: "udgår efter »stk. 3-6«: ».« og indsættes: », eller«".
	whole bool
}

var (
	reQuotePassage = regexp.MustCompile(`»([^»«]*)«`)
	reOpReplace    = regexp.MustCompile(`(?i)Q(\d+)\s*,?\s*(?:ændres\s+|erstattes\s+)?(?:til|med)\s*:?\s*Q(\d+)`)
	reOpSwapAfter  = regexp.MustCompile(`(?i)udgår\s+efter\s+Q(\d+)\s*:?\s*Q(\d+)\s*,?\s*og\s+(?:der\s+)?indsættes\s*:?\s*Q(\d+)`)
	reOpInsert     = regexp.MustCompile(`(?i)(efter|før|foran)\s+Q(\d+)\s*,?\s*(?:indsættes\s*)?:?\s*Q(\d+)`)
	// "udgår i stk. 2 »X«" names the place between the verb and the words; the change's own address
	// already reads it, from everything before the first quotation.
	reOpDelete    = regexp.MustCompile(`(?i)(?:udgår|slettes|ophæves)\s*(?:i\s+(?:(?:stk|nr|litra)\.?\s*[0-9a-zæøå]+|[0-9]+\.\s*(?:og\s+[0-9]+\.\s*)?pkt\.?)(?:\s*,\s*(?:(?:stk|nr|litra)\.?\s*[0-9a-zæøå]+|[0-9]+\.\s*pkt\.?))*\s*,?\s*)?:?\s*Q(\d+)((?:\s*(?:,|og|samt)\s*Q\d+)*)|Q(\d+)((?:\s*(?:,|og|samt)\s*Q\d+)*)\s*,?\s*(?:udgår|slettes)`)
	reQIndex      = regexp.MustCompile(`Q(\d+)`)
	reNestedQuote = regexp.MustCompile(`»»([^»«]*)««`)
	// reOpListReplace is a list of old wordings given new ones: "Q0, Q1 og Q2 til: Q3", "Q0 og Q1
	// til henholdsvis Q2 og Q3".
	reOpListReplace = regexp.MustCompile(`(?i)(Q\d+(?:\s*(?:,|og|samt)\s*Q\d+)+)\s*(?:ændres\s+)?til\s*(henholdsvis)?\s*:?\s*(Q\d+(?:\s*(?:,|og|samt)\s*Q\d+)*)`)
	// A unit repealed in front of its verb, "nr. 5 og nr. 6 ophæves", or after it, "ophæves stk. 1".
	reOpRepealBefore = regexp.MustCompile(`(?i)((?:§+\s*[0-9]+(?:\s*[a-zæøå]\b)?\s*,?\s*)?(?:(?:(?:stk|nr|litra)\.?\s*[0-9a-zæøå]+(?:\s*[-–]\s*[0-9a-zæøå]+)?|[0-9]+\.\s*(?:og\s+[0-9]+\.\s*)?pkt\.?)\s*(?:,|og)?\s*)+)\s*,?\s*(?:ophæves|udgår)\b`)
	reOpRepealAfter  = regexp.MustCompile(`(?i)\b(?:ophæves|udgår)\s+((?:(?:(?:stk|nr|litra)\.?\s*[0-9a-zæøå]+(?:\s*[-–]\s*[0-9a-zæøå]+)?|[0-9]+\.\s*pkt\.?)\s*(?:,|og)?\s*)+)`)
)

// parseOperations reads every edit a change sentence asks for, in the order it asks.
func parseOperations(def string) []operation {
	def = rePktSpace.ReplaceAllString(def, "pkt.") // "2. pkt . , ophæves"
	var quotes []string
	// "til: »»Min Plan««" quotes a word that is itself in quotation marks; the outer pair is the
	// act's, the inner the law's.
	masked := reNestedQuote.ReplaceAllStringFunc(def, func(q string) string {
		quotes = append(quotes, "»"+Squeeze(q[len("»»"):len(q)-len("««")])+"«")
		return " Q" + strconv.Itoa(len(quotes)-1) + " "
	})
	masked = reQuotePassage.ReplaceAllStringFunc(masked, func(q string) string {
		quotes = append(quotes, Squeeze(q[len("»"):len(q)-len("«")]))
		return " Q" + strconv.Itoa(len(quotes)-1) + " "
	})
	q := func(s string) string { return quotes[Atoi(s)] }
	type found struct {
		at int
		op operation
	}
	var all []found
	taken := make([]bool, len(masked)+1)
	claim := func(loc []int) bool {
		for i := loc[0]; i < loc[1]; i++ {
			if taken[i] {
				return false
			}
		}
		for i := loc[0]; i < loc[1]; i++ {
			taken[i] = true
		}
		return true
	}
	// "udgår efter »stk. 3-6«: ».« og indsættes: », eller«": the mark after the words goes, and new
	// words take its place.
	for _, loc := range reOpSwapAfter.FindAllStringSubmatchIndex(masked, -1) {
		if !claim(loc[:2]) {
			continue
		}
		anchor := q(masked[loc[2]:loc[3]])
		all = append(all, found{loc[0], operation{kind: EditReplaceText, whole: true,
			old: joinInsertion(anchor, q(masked[loc[4]:loc[5]]), false), new: joinInsertion(anchor, q(masked[loc[6]:loc[7]]), false)}})
	}
	for _, loc := range reOpInsert.FindAllStringSubmatchIndex(masked, -1) {
		if !claim(loc[:2]) {
			continue
		}
		word := strings.ToLower(masked[loc[2]:loc[3]])
		all = append(all, found{loc[0], operation{kind: EditInsertText, old: q(masked[loc[4]:loc[5]]),
			new: q(masked[loc[6]:loc[7]]), before: word != "efter"}})
	}
	// "ændres »a« og »b« til henholdsvis »A« og »B«" pairs the lists; "ændres »jobplan«,
	// »Jobplan« og »jobplaner« til: »X«" gives every form the one new wording.
	for from := 0; from < len(masked); {
		rel := reOpListReplace.FindStringSubmatchIndex(masked[from:])
		if rel == nil {
			break
		}
		loc := make([]int, len(rel))
		for i, v := range rel {
			loc[i] = v
			if v >= 0 {
				loc[i] = v + from
			}
		}
		olds := reQIndex.FindAllStringSubmatch(masked[loc[2]:loc[3]], -1)
		news := reQIndex.FindAllStringSubmatch(masked[loc[6]:loc[7]], -1)
		pairwise := loc[4] >= 0
		if !pairwise && len(news) > 1 {
			// "»dommeren« og »tinglysningsdommeren« til: »retten«, »dommerens« og …": without
			// "henholdsvis" the new wording is the one after "til", and what follows it starts
			// the next list.
			first := reQIndex.FindStringIndex(masked[loc[6]:loc[7]])
			loc[1], loc[7] = loc[6]+first[1], loc[6]+first[1]
			news = news[:1]
		}
		from = loc[1]
		if pairwise && len(olds) != len(news) || !pairwise && len(news) != 1 {
			continue
		}
		// "»a« til: »b« og »c« til: »d«": a list whose first wording is the new one of the
		// replacement before it is two replacements, not a list.
		if before := strings.ToLower(strings.TrimSpace(masked[:loc[2]])); strings.HasSuffix(before, "til:") ||
			strings.HasSuffix(before, "til") || strings.HasSuffix(before, "med") {
			continue
		}
		if !claim(loc[:2]) {
			continue
		}
		for i, o := range olds {
			n := news[0]
			if pairwise {
				n = news[i]
			}
			all = append(all, found{loc[0] + i, operation{kind: EditReplaceText, old: q(o[1]), new: q(n[1])}})
		}
	}
	for _, loc := range reOpReplace.FindAllStringSubmatchIndex(masked, -1) {
		if !claim(loc[:2]) {
			continue
		}
		all = append(all, found{loc[0], operation{kind: EditReplaceText, old: q(masked[loc[2]:loc[3]]), new: q(masked[loc[4]:loc[5]])}})
	}
	for _, loc := range reOpDelete.FindAllStringSubmatchIndex(masked, -1) {
		if !claim(loc[:2]) {
			// "ændres »Dagbogen, tingbogen« til: »Tingbogen« og »kan« udgår": the list the deletion
			// reads from »Tingbogen« on starts with the replacement's new words; the deletion is of
			// the quotations no other operation holds.
			from := loc[0]
			for from < loc[1] && (taken[from] || masked[from] != 'Q') {
				from++
			}
			rel := reOpDelete.FindStringSubmatchIndex(masked[from:loc[1]])
			if from >= loc[1] || rel == nil || rel[0] != 0 || !claim([]int{from, loc[1]}) {
				continue
			}
			loc = []int{from, loc[1]}
		}
		for _, m := range reQIndex.FindAllStringSubmatch(masked[loc[0]:loc[1]], -1) {
			all = append(all, found{loc[0], operation{kind: EditDeleteText, old: q(m[1])}})
		}
	}
	// Repeals a sentence makes without quotation: "§ 42 k, stk. 4, ophæves, og i stk. 5 ændres »…«",
	// "I § 50 ophæves stk. 1, og i stk. 2 ændres »…«", "…, og nr. 5 og nr. 6 ophæves". Read as a
	// text change, the sentence used to lose its repeal without a word.
	if len(all) > 0 {
		// "stk. 3, 2. pkt., der bliver stk. 2, 2. pkt., ophæves": the units repealed are the ones
		// named before their new numbers, which are blanked, offsets kept.
		plain := reBecomesClause.ReplaceAllStringFunc(masked, func(s string) string { return strings.Repeat(" ", len(s)) })
		for _, loc := range reOpRepealBefore.FindAllStringSubmatchIndex(plain, -1) {
			// "stk. 1, udgår »X«" deletes words; only a verb with no quotation after it repeals.
			after := strings.TrimLeft(masked[loc[1]:], " :")
			if k := strings.Index(after, ","); k >= 0 {
				after = after[:k]
			}
			if strings.Contains(after, "Q") {
				continue // "stk. 5, udgår i 1. pkt. »…«": the words go, not the subsection
			}
			if claim(loc[2:4]) {
				all = append(all, found{loc[2], operation{kind: EditDeleteProvision, place: plain[loc[2]:loc[3]]}})
			}
		}
		for _, loc := range reOpRepealAfter.FindAllStringSubmatchIndex(plain, -1) {
			if claim(loc[2:4]) {
				all = append(all, found{loc[0], operation{kind: EditDeleteProvision, place: masked[loc[2]:loc[3]]}})
			}
		}
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].at < all[j].at })
	out := make([]operation, len(all))
	prev := 0
	if v := reOperativeVerb.FindStringIndex(masked); v != nil {
		prev = v[1] // the first operation's lead is what follows the sentence's first verb
	}
	for i, f := range all {
		out[i] = f.op
		if f.at > prev {
			out[i].lead = masked[prev:f.at]
		}
		end := f.at
		for end < len(taken) && taken[end] {
			end++
		}
		prev = end
	}
	return out
}

// reSubAddress is "i nr. 1" or "i stk. 2" between two operations of one change: it narrows the
// operations after it to a part of the change's address.
var reSubAddress = regexp.MustCompile(`(?i)\bi\s+(stk|nr|litra)\.?\s*([0-9]+|[a-zæøå])\b`)

// reSubSentence is "og i 2. pkt." between two operations.
var reSubSentence = regexp.MustCompile(`(?i)\bi\s+([0-9]+)\.\s*(?:og\s+([0-9]+)\.\s*)?(?:pkt|punktum)`)

// narrow applies the sub-address an operation's lead names to the change's address. A lead naming
// a § of its own is a new address altogether.
func (o operation) narrow(a Address) (Address, bool) {
	if o.lead == "" {
		return a, false
	}
	if strings.Contains(o.lead, "§") {
		if b := ParseAddress(o.lead); b.Paragraph != "" {
			return b, true
		}
	}
	if m := reSubSentence.FindStringSubmatch(o.lead); m != nil {
		a.Sentence, a.SentenceTo, a.SentenceList = Atoi(m[1]), 0, false
		if m[2] != "" {
			a.SentenceTo, a.SentenceList = Atoi(m[2]), true // "i 5. og 8. pkt."
		}
		return a, true
	}
	loc := reSubAddress.FindStringIndex(o.lead)
	if loc == nil {
		return a, false
	}
	// The sub-address runs on through its levels: "og i stk. 1, nr. 1, udgår" names nr. 1 of stk. 1,
	// and reading only "stk. 1" aimed the deletion at the whole subsection.
	rest := o.lead[loc[0]+1:]
	for {
		m := reSubLevel.FindStringSubmatch(rest)
		if m == nil {
			break
		}
		rest = rest[len(m[0]):]
		switch {
		case m[3] != "":
			a.Sentence, a.SentenceTo, a.SentenceList = Atoi(m[3]), 0, false
		case strings.EqualFold(m[1], "stk"):
			a.Stk, a.StkTo, a.Nr, a.Litra, a.Sentence, a.SentenceTo = Atoi(m[2]), 0, 0, "", 0, 0
		case strings.EqualFold(m[1], "nr"):
			a.Nr, a.Litra, a.Sentence, a.SentenceTo = Atoi(m[2]), "", 0, 0
		case strings.EqualFold(m[1], "litra"):
			a.Litra, a.Sentence, a.SentenceTo = strings.ToLower(m[2]), 0, 0
		}
	}
	return a, true
}

// reSubLevel is one level of a sub-address, read in turn from where "i" leaves off.
var reSubLevel = regexp.MustCompile(`(?i)^[\s,]*(?:(stk|nr|litra)\.?\s*([0-9]+|[a-zæøå])\b|([0-9]+)\.\s*(?:pkt|punktum)\.?)`)

// addressBeforeOperations is a compound change's own address: what its sentence names before the
// first quotation. Read from the whole sentence, "I § 25 b, stk. 4, ændres »A« til »B«, og i nr. 1
// ændres »C« til »D«" took its nr. from the later clause and aimed every operation at nr. 1.
func addressBeforeOperations(def string) Address {
	if i := strings.Index(def, "»"); i > 0 {
		return ParseAddress(def[:i])
	}
	return ParseAddress(def)
}

// asEdit is the change with this operation in place of what its sentence was read as.
func (o operation) asEdit(e Edit) Edit {
	e.Old, e.New, e.Anchor, e.AnchorBefore = o.old, o.new, "", false
	switch o.kind {
	case EditInsertText:
		e.Kind, e.Old, e.Anchor, e.AnchorBefore = EditInsertText, "", o.old, o.before
		if e.Address.Everywhere {
			e.Kind, e.Old, e.New = EditReplaceEverywhere, o.old, joinInsertion(o.old, o.new, o.before)
		}
	case EditDeleteText:
		e.Kind = EditDeleteText
		if e.Address.Everywhere {
			e.Kind = EditReplaceEverywhere
		}
	default:
		e.Kind = EditReplaceText
		if e.Address.Everywhere {
			e.Kind = EditReplaceEverywhere
		}
	}
	return e
}

// reOperativeVerb is the first verb of a change sentence that does something: "der bliver stk. 29"
// renames a place the sentence goes on naming, so the places after it are not an operation's own.
var reOperativeVerb = regexp.MustCompile(`(?i)(?:^|[^\p{L}])(affattes|ændres|ophæves|indsættes|tilføjes|udgår|erstattes)(?:$|[^\p{L}])|»`)
