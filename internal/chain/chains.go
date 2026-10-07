package chain

import (
	"container/list"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/lkarlslund/retsinformation/internal/lexdoc"
)

// Entry is one document as recorded in the archive's file index.
type Entry struct {
	Path         string
	Kind         string // the medium directory: lta, ltb, ltc, mt, retsinfo, ft, fob, accn
	Accession    string
	Year         int
	Number       int
	DiesSigni    string
	DocumentType string
	Status       lexdoc.Status
	Title        string
}

// ELIPath is the canonical ELI path for this document, e.g. eli/lta/2025/331.
func (e Entry) ELIPath() string { return fmt.Sprintf("eli/%s/%d/%d", e.Kind, e.Year, e.Number) }

// Catalog is the index of a library of documents: one entry per accession, in the order the
// documents were found, which the consolidation indexes built over it inherit. An archive builds
// it by scanning files; the compiled artifact carries it.
type Catalog struct {
	entries  []Entry
	byAccn   map[string]int
	byNumber map[int][]int // publication number → entry indexes; date disambiguates
}

// Chains is consolidation over a library of documents: the catalogue, each document as a Law, the
// changes each act makes, and what each consolidation's notes say it does not print yet. The
// archive reads these from the files; the compiled artifact from what its build stored. The
// methods are the same either way, so the two give the same answers.
type Chains struct {
	*Catalog

	// law opens a document as consolidation reads it.
	law func(accession string) (*Law, bool)
	// readAct reads an act's own changes and entry-into-force provisions; act adds the orders
	// that set it in force. The path is reported even when the act could not be read.
	readAct func(accession string) (*ActRecord, bool)
	// notes are the changes a document's notes say it does not print yet; see Chains.waiting.
	notes func(accession string) []WaitingChange

	// acts keeps amending acts already read for consolidation. Parsing the chain was 85% of an
	// as-of answer — BR18's seventeen acts re-read on every request — while an act's changes
	// never differ between requests. The library is a snapshot, so the cache lives exactly as
	// long as it does.
	acts actCache

	// orders indexes the orders that set an act in force ("Bekendtgørelse om ikrafttræden af lov
	// om …") by the act title they name, built the first time an act leaves its date to a minister.
	ordersOnce sync.Once
	orders     map[string][]Entry
	citesOnce  sync.Once
	orderCites map[string][]Entry // orders by the acts they cite, "nr. 539 af 8. juni 2006"

	// lineages keeps the consolidations each law's preamble chain says it continues, read the
	// first time a change list does not name the law the plain way: opening them for every law
	// on every request doubled the time an answer took.
	lineageMu sync.Mutex
	lineages  map[string][]TargetHint

	// consolidations indexes the consolidations of each law by title, and waitingNotes the
	// changes each one's notes say it does not print yet; see Chains.waiting.
	consolidationsOnce sync.Once
	consolidations     map[string][]Entry
	waitingMu          sync.Mutex
	waitingNotes       map[string][]WaitingChange
	texts              map[string]string
	contents           map[string]*lexdoc.Content
}

func (a *Catalog) Add(e Entry) {
	// The archive can hold the same accession twice: xml/accn/<rank>/<year>/… is
	// byte-identical to the medium-path copy, which is why accessions are the
	// deduplication key in the first place. Keep the medium path, since it is the
	// canonical ELI resource and yields a usable ELIPath.
	if prev, ok := a.byAccn[e.Accession]; ok {
		if a.entries[prev].Kind != "accn" || e.Kind == "accn" {
			return
		}
		a.entries[prev] = e
		return
	}
	a.byAccn[e.Accession] = len(a.entries)
	a.byNumber[e.Number] = append(a.byNumber[e.Number], len(a.entries))
	a.entries = append(a.entries, e)
}

// Len is the number of distinct documents indexed.
func (a *Catalog) Len() int { return len(a.entries) }

// Entries returns the index sorted by kind, year and number.
func (a *Catalog) Entries() []Entry {
	out := append([]Entry(nil), a.entries...)
	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		if out[i].Year != out[j].Year {
			return out[i].Year < out[j].Year
		}
		return out[i].Number < out[j].Number
	})
	return out
}

// Find returns the index entry for an accession.
func (a *Catalog) Find(accession string) (Entry, bool) {
	i, ok := a.byAccn[accession]
	if !ok {
		return Entry{}, false
	}
	return a.entries[i], true
}

// Resolve finds documents a preamble hint refers to: "bekendtgørelse nr. 1399 af 12.
// december 2019". Number alone is ambiguous across years, so date and year are used
// to narrow the candidates.
func (a *Catalog) Resolve(h TargetHint) []Entry {
	var out []Entry
	for _, i := range a.byNumber[h.Number] {
		e := a.entries[i]
		if !h.Date.IsZero() {
			signed, ok := ParseDate(e.DiesSigni)
			if ok && !signed.IsZero() {
				if !signed.Equal(h.Date) {
					continue
				}
			} else if e.Year != h.Date.Year() {
				continue
			}
		}
		out = append(out, e)
	}
	return out
}

// Versions lists the amendment chain of a law, marking which amending documents the library holds.
func (a *Chains) Versions(accession string) ([]Version, error) {
	law, ok := a.law(accession)
	if !ok {
		return nil, fmt.Errorf("lexdania: %s is not in the archive", accession)
	}
	vs := law.Versions()
	for i := range vs {
		if e, found := a.Find(vs[i].Accession); found {
			vs[i].Found = true
			vs[i].Path = e.Path
		}
	}
	return vs, nil
}

// LawAsOf reconstructs a law as it stood at a date.
func (a *Chains) LawAsOf(accession string, asOf time.Time) (*View, error) {
	law, ok := a.law(accession)
	if !ok {
		return nil, fmt.Errorf("lexdania: %s is not in the library", accession)
	}
	v := lawAsOf(law, asOf, a.act, a.chainHelp(law))
	v.Path = law.Path
	return v, nil
}

// LawAdoptedAsOf reconstructs a law as amended by every act signed by a date, the parts not yet in
// force included; see LawAdoptedAsOf.
func (a *Chains) LawAdoptedAsOf(accession string, asOf time.Time) (*View, error) {
	law, ok := a.law(accession)
	if !ok {
		return nil, fmt.Errorf("lexdania: %s is not in the library", accession)
	}
	v := lawAsOfMode(law, asOf, a.act, true, a.chainHelp(law))
	v.Path = law.Path
	return v, nil
}

// LawBefore reconstructs a law as the amending act met it: every act before it, the ones taking
// effect earlier on the same day included, and nothing from the act itself. It is what a refusal of
// one of the act's changes is judged against.
func (a *Chains) LawBefore(accession, act string) (*View, error) {
	law, ok := a.law(accession)
	if !ok {
		return nil, fmt.Errorf("lexdania: %s is not in the library", accession)
	}
	vs, err := a.Versions(accession)
	if err != nil {
		return nil, err
	}
	var eff time.Time
	for _, v := range vs {
		if v.Accession == act {
			eff = v.Effective
		}
	}
	full := lawAsOfMode(law, eff, a.act, true, a.chainHelp(law))
	skip := map[string]bool{}
	from := false
	for _, c := range full.Chain {
		from = from || c.Accession == act
		if from && c.Effective.Equal(eff) {
			skip[c.Accession] = true
		}
	}
	// An act is drafted against the law as every act passed before it left it, in force or not.
	v := lawAsOfMode(law, eff, func(accession string) (*ActRecord, bool) {
		if skip[accession] {
			return &ActRecord{}, false
		}
		return a.act(accession)
	}, true, a.chainHelp(law))
	v.Path = law.Path
	return v, nil
}

// act reads one amending act, from the cache when it has been read before.
func (a *Chains) act(accession string) (*ActRecord, bool) {
	if act, ok := a.acts.get(accession); ok {
		return act, true
	}
	act, ok := a.readAct(accession)
	if !ok {
		return act, false
	}
	if !act.Bill && act.InForce.Default.IsZero() {
		// "Justitsministeren fastsætter tidspunktet for lovens ikrafttræden": the date is in an
		// order of its own.
		if e, ok := a.Find(accession); ok {
			merged := *act
			merged.InForce = mergeInForce(lexdoc.InForce{Default: act.InForce.Default, Rules: append([]lexdoc.InForceRule(nil), act.InForce.Rules...)}, a.ordersInForce(e))
			act = &merged
		}
	}
	a.acts.put(accession, act)
	return act, true
}

// ordersInForce reads the orders that set an act in force: those whose title names the act's
// title, signed after it, that cite it by number and date.
func (a *Chains) ordersInForce(act Entry) lexdoc.InForce {
	a.ordersOnce.Do(func() {
		a.orders = map[string][]Entry{}
		for _, e := range a.entries {
			if k := orderKey(e.Title); k != "" {
				a.orders[k] = append(a.orders[k], e)
			}
		}
	})
	signed, ok := ParseDate(act.DiesSigni)
	key := titleKey(ConsolidationTitle(act.Title))
	if !ok || act.Number == 0 || key == "" {
		return lexdoc.InForce{}
	}
	cite := fmt.Sprintf("nr. %d af %d. %s %d", act.Number, signed.Day(), danishMonths[signed.Month()-1], signed.Year())
	var out lexdoc.InForce
	seen := map[string]bool{}
	read := func(o Entry) {
		if d, ok := ParseDate(o.DiesSigni); !ok || d.Before(signed) || seen[o.Accession] {
			return
		}
		seen[o.Accession] = true
		texts, ok := a.orderTexts(o)
		if !ok || !strings.Contains(strings.ToLower(lexdoc.Squeeze(strings.Join(texts, " "))), cite) {
			return
		}
		out = mergeInForce(out, lexdoc.ParseInForceOrder(texts, cite))
	}
	for _, o := range a.orders[key] {
		read(o)
	}
	if out.Default.IsZero() {
		// "Bekendtgørelse om ikrafttræden af lov om ændring af tinglysningsloven …" sets lov nr. 539
		// af 8. juni 2006 "om ændring af lov om tinglysning …" in force: the order names the law
		// by its other name, and only the act it cites finds it.
		a.citesOnce.Do(a.indexOrderCites)
		for _, o := range a.orderCites[cite] {
			read(o)
		}
	}
	return out
}

// orderTexts is an entry-into-force order's preamble and provisions.
func (a *Chains) orderTexts(o Entry) ([]string, bool) {
	doc, ok := a.law(o.Accession)
	if !ok {
		return nil, false
	}
	c := doc.Model().Content
	texts := []string{lexdoc.SegmentsText(c.Preamble)}
	for _, r := range c.Provisions() {
		texts = append(texts, r.Provision.OwnText())
	}
	return texts, true
}

// indexOrderCites reads every entry-into-force order once for the acts it cites.
func (a *Chains) indexOrderCites() {
	a.ordersOnce.Do(func() {}) // a.orders is filled by ordersInForce before this is called
	a.orderCites = map[string][]Entry{}
	for _, list := range a.orders {
		for _, o := range list {
			texts, ok := a.orderTexts(o)
			if !ok {
				continue
			}
			for _, m := range reCitedAct.FindAllStringSubmatch(strings.ToLower(lexdoc.Squeeze(strings.Join(texts, " "))), -1) {
				a.orderCites[m[0]] = append(a.orderCites[m[0]], o)
			}
		}
	}
	// In the order they were signed, as the title index reads them: the orders an act's dates are
	// merged from must not depend on how a map is walked.
	for k, list := range a.orderCites {
		seen := map[string]bool{}
		uniq := list[:0]
		for _, o := range list {
			if !seen[o.Accession] {
				seen[o.Accession] = true
				uniq = append(uniq, o)
			}
		}
		sort.Slice(uniq, func(i, j int) bool {
			if uniq[i].DiesSigni != uniq[j].DiesSigni {
				return uniq[i].DiesSigni < uniq[j].DiesSigni
			}
			return uniq[i].Accession < uniq[j].Accession
		})
		a.orderCites[k] = uniq
	}
}

// reCitedAct is an act cited by number and date: "nr. 539 af 8. juni 2006".
var reCitedAct = regexp.MustCompile(`nr\. [0-9]+ af [0-9]{1,2}\. (?:januar|februar|marts|april|maj|juni|juli|august|september|oktober|november|december) [0-9]{4}`)

var danishMonths = []string{"januar", "februar", "marts", "april", "maj", "juni", "juli", "august", "september", "oktober", "november", "december"}

// orderKey is the act title an entry-into-force order names, or "" when the entry is no such order.
func orderKey(title string) string {
	t := strings.ToLower(lexdoc.Squeeze(title))
	// "Bekendtgørelse om delvis ikrafttræden af lov om …" sets part of the act in force.
	t = strings.Replace(t, "bekendtgørelse om delvis ikrafttræden af ", "bekendtgørelse om ikrafttræden af ", 1)
	const prefix = "bekendtgørelse om ikrafttræden af "
	if !strings.HasPrefix(t, prefix) {
		return ""
	}
	t = reOrderPart.ReplaceAllString(strings.TrimPrefix(t, prefix), "")
	return titleKey(ConsolidationTitle(t))
}

// reOrderPart is what an order's title says about which parts of the act it sets in force: "visse
// bestemmelser i", "dele af", "§ 1, nr. 21, i".
var reOrderPart = regexp.MustCompile(`^(?:visse\s+|dele\s+af\s+)?(?:(?:lov)?bestemmelser(?:ne)?\s+i\s+|§[^,]*(?:,[^,]*)*?,?\s+i\s+)?`)

// titleKey is the start of a title, enough to find an act's orders by.
func titleKey(t string) string {
	t = strings.TrimRight(strings.TrimSpace(t), ".")
	if r := []rune(t); len(r) > 60 {
		t = string(r[:60])
	}
	if len([]rune(t)) < 12 {
		return ""
	}
	return t
}

// mergeInForce adds the dates b gives to the parts a gives none.
func mergeInForce(a, b lexdoc.InForce) lexdoc.InForce {
	if a.Default.IsZero() {
		a.Default = b.Default
	}
	a.Rules = append(a.Rules, b.Rules...)
	return a
}

// actCacheSize bounds the acts kept. A law's chain is tens of acts and the corpus holds tens of
// thousands, so this keeps every chain a reader is working through without growing to the
// archive.
const actCacheSize = 4096

// actCache is a small least-recently-used map, safe for the concurrent requests a server makes.
type actCache struct {
	mu    sync.Mutex
	items map[string]*list.Element
	order list.List // front is most recently used
}

type actEntry struct {
	accession string
	act       *ActRecord
}

func (c *actCache) get(accession string) (*ActRecord, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.items[accession]
	if !ok {
		return nil, false
	}
	c.order.MoveToFront(el)
	return el.Value.(actEntry).act, true
}

func (c *actCache) put(accession string, act *ActRecord) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.items == nil {
		c.items = map[string]*list.Element{}
	}
	if el, ok := c.items[accession]; ok {
		c.order.MoveToFront(el)
		return
	}
	c.items[accession] = c.order.PushFront(actEntry{accession, act})
	if c.order.Len() > actCacheSize {
		last := c.order.Back()
		c.order.Remove(last)
		delete(c.items, last.Value.(actEntry).accession)
	}
}

// chainHelp is what the archive adds to a law's own metadata when its chain is resolved.
func (a *Chains) chainHelp(base *Law) chainHelp {
	return chainHelp{sameLaw: a.sameLaw(base), cited: a.citedAct,
		waiting:     func(act Version, e lexdoc.Edit, from time.Time) bool { return a.waiting(base, act, e, from) },
		effectBy:    func(act Version, e lexdoc.Edit, from time.Time) time.Time { return a.takesEffectBy(base, act, e, from) },
		notedInBase: func(act Version, e lexdoc.Edit) bool { return a.listsWaiting(base.Meta.Accession, act, e) },
		openings:    func(cited TargetHint, paragraph string) []string { return a.openings(base, cited, paragraph) }}
}

// citedAct resolves an act a preamble names as an earlier amendment, when the archive holds
// exactly one document by that number and date and it is an act rather than a bill.
func (a *Chains) citedAct(h TargetHint) (Version, bool) {
	if h.Number == 0 || h.Date.IsZero() {
		return Version{}, false
	}
	found := a.Resolve(h)
	if len(found) != 1 {
		return Version{}, false
	}
	e := found[0]
	signed, ok := ParseDate(e.DiesSigni)
	if !ok || signed.IsZero() {
		return Version{}, false
	}
	return Version{Accession: e.Accession, Effective: signed, Title: e.Title}, true
}

// sameLaw recognises a change list aimed at an earlier consolidation of the law being
// reconstructed. An act drafted while "lovbekendtgørelse nr. 1782 af 27. november 2020" was current
// cites that one; by the time it takes effect a newer consolidation may have replaced it, and the
// register lists the act in the newer one's chain. Read as aimed at another law, every change it
// made was dropped. The cited document is the same law when it is an older consolidation under the
// same title.
func (a *Chains) sameLaw(base *Law) func(TargetHint) bool {
	want := ConsolidationTitle(base.Meta.Title)
	signed, _ := ParseDate(base.Meta.DiesSigni)
	// "Lov om ændring af udlændingeloven" is the title of many acts, and none of them is an earlier
	// consolidation of another: a change list aimed at lov nr. 1398 of 2008 is not lov nr. 486 of
	// 2009's, whatever both are called.
	amending := strings.HasPrefix(want, "lov om ændring af ") || strings.HasPrefix(want, "bekendtgørelse om ændring af ")
	names := lawNames(want)
	// "§ 189 i lov nr. 573 af 24. juni 2005 ophæver lov om social service den 1. januar 2007": the
	// act the consolidation names as repealing the law is a new law of the same name, and its
	// changes are that law's, not this one's.
	var replacedBy []TargetHint
	for _, m := range reRepealedByAct.FindAllStringSubmatch(lexdoc.Squeeze(lexdoc.SegmentsText(base.Model().Content.Preamble)), -1) {
		replacedBy = append(replacedBy, lexdoc.ParseTarget(m[0]))
	}
	return func(h TargetHint) bool {
		if h.Empty() || want == "" || amending {
			return false
		}
		for _, r := range replacedBy {
			if r.Number == h.Number && r.Date.Equal(h.Date) {
				return false
			}
		}
		for _, p := range a.lineage(base) {
			// "Hermed bekendtgøres lov om en børne- og ungeydelse, jf. lovbekendtgørelse nr. 972 af
			// 22. september 2006, med de ændringer …": the consolidation this one continues, cited
			// under the law's former title by an act that amended it before the renaming.
			if p.Number == h.Number && !p.Date.IsZero() && p.Date.Equal(h.Date) {
				return true
			}
		}
		found := a.Resolve(h)
		if h.Title != "" && names[ConsolidationTitle(h.Title)] {
			// "I lov om almene boliger m.v., jf. lovbekendtgørelse nr. 106 af 21. februar 2005":
			// the preamble names this law, though the consolidation it cites printed the law's
			// earlier title, "… almene boliger samt støttede private andelsboliger m.v.". Lov nr.
			// 575 of 2005 was read as aimed at another law, and its 40 changes were lost.
			for _, e := range found {
				if d, ok := ParseDate(e.DiesSigni); ok && !signed.IsZero() && d.After(signed) {
					return false // a later consolidation is not an earlier one of this
				}
			}
			return true
		}
		for _, e := range found {
			if e.Accession == base.Meta.Accession || ConsolidationTitle(e.Title) != want {
				continue
			}
			if d, ok := ParseDate(e.DiesSigni); ok && !signed.IsZero() && d.After(signed) {
				continue
			}
			return true
		}
		return false
	}
}

// reRepealedByAct is a consolidation's note on the act that repeals the law: "§ 189 i lov nr. 573
// af 24. juni 2005 ophæver lov om social service".
var reRepealedByAct = regexp.MustCompile(`(?i)\blov\s+nr\.\s*[0-9]+\s+af\s+[0-9]{1,2}\.\s*[a-zæøå]+\s+[0-9]{4}\s+ophæver\b`)

// lawNames are the names a law goes by: its title, the title without the short name it prints in
// parentheses, and that short name: "lov om indkomstbeskatning af selvstændige erhvervsdrivende
// (virksomhedsskatteloven)", "lov om indkomstbeskatning af selvstændige erhvervsdrivende",
// "virksomhedsskatteloven".
func lawNames(title string) map[string]bool {
	out := map[string]bool{title: true}
	if m := reShortName.FindStringSubmatch(title); m != nil {
		out[strings.TrimSpace(title[:len(title)-len(m[0])])] = true
		if strings.HasSuffix(m[1], "loven") || strings.HasSuffix(m[1], "lov") {
			out[m[1]] = true
		}
	}
	return out
}

// reShortName is the short name a title ends with: "(virksomhedsskatteloven)".
var reShortName = regexp.MustCompile(`\s*\(([^()]+)\)\s*$`)

// predecessorHints are the consolidations a consolidation's preamble says it continues: "Herved
// bekendtgøres lov om …, jf. lovbekendtgørelse nr. 972 af 22. september 2006, med de ændringer …".
func predecessorHints(base *Law) []TargetHint {
	var text strings.Builder
	for _, sg := range base.Model().Content.Preamble {
		text.WriteString(sg.Text)
		text.WriteString(" ")
	}
	pre := Squeeze(text.String())
	if low := strings.ToLower(pre); !strings.Contains(low, "hermed bekendtgøres") && !strings.Contains(low, "herved bekendtgøres") {
		return nil
	}
	var out []TargetHint
	for _, m := range rePredecessor.FindAllString(pre, -1) {
		if h := lexdoc.ParseTarget(m); !h.Empty() {
			out = append(out, h)
		}
	}
	return out
}

// lineage is the consolidations this one continues, and the ones those continue, as their
// preambles say: lov om gasforsyning of 2023 continues one of April 2023, which continues lov om
// naturgasforsyning as consolidated in 2020, the law's name before it was changed. Four steps
// back reach every renaming the census meets; each step opens a document.
func (a *Chains) lineage(base *Law) []TargetHint {
	a.lineageMu.Lock()
	if got, ok := a.lineages[base.Meta.Accession]; ok {
		a.lineageMu.Unlock()
		return got
	}
	a.lineageMu.Unlock()
	out := a.readLineage(base)
	a.lineageMu.Lock()
	if a.lineages == nil {
		a.lineages = map[string][]TargetHint{}
	}
	a.lineages[base.Meta.Accession] = out
	a.lineageMu.Unlock()
	return out
}

func (a *Chains) readLineage(base *Law) []TargetHint {
	var out []TargetHint
	seen := map[string]bool{base.Meta.Accession: true}
	queue := predecessorHints(base)
	for len(queue) > 0 && len(out) < 4 {
		h := queue[0]
		queue = queue[1:]
		out = append(out, h)
		found := a.Resolve(h)
		if len(found) != 1 || seen[found[0].Accession] {
			continue
		}
		seen[found[0].Accession] = true
		if doc, ok := a.law(found[0].Accession); ok {
			queue = append(queue, predecessorHints(doc)...)
		}
	}
	return out
}

// rePredecessor is a consolidation cited in a consolidation's preamble.
var rePredecessor = regexp.MustCompile(`(?i)\b(?:lov)?bekendtgørelse\s+nr\.\s*[0-9]+\s+af\s+[0-9]{1,2}\.\s*[a-zæøå]+\s+[0-9]{4}`)

// ConsolidationTitle is a law's title with the consolidation's own words taken off: "Bekendtgørelse
// af lov om X" and "Lov om X" name the same law.
func ConsolidationTitle(t string) string {
	t = strings.ToLower(Squeeze(t))
	t = strings.TrimPrefix(t, "bekendtgørelse af ")
	if i := strings.Index(t, "\\n"); i >= 0 {
		t = t[:i]
	}
	if i := strings.Index(t, "\n"); i >= 0 {
		t = t[:i]
	}
	return strings.TrimSpace(t)
}

// WaitingChange is a change a consolidation's note says it does not print, because a minister has
// yet to set it in force or it takes effect later: the act by number and day of signing, its
// provision, and the changes of it (all of them when Numbers is empty).
type WaitingChange struct {
	Repeal  bool      `json:"repeal,omitempty"` // the note says the change is repealed, not that it waits
	Number  int       `json:"number"`
	Signed  time.Time `json:"signed"`
	Section string    `json:"section,omitempty"`
	Numbers [][2]int  `json:"numbers,omitempty"`
}

// waiting reports whether a change an act leaves to a minister still waits as of the newest
// consolidation of the law the archive holds that was signed after the act: its notes list the
// change as not printed ("De ændringer, der følger af § 1, nr. 2, 8-13 …, i lov nr. 721 af 27.
// april 2021, er ikke indarbejdet …, da tidspunktet for ikrafttræden fastsættes af …"). A
// consolidation that no longer lists it prints it, set in force by an order the archive may not
// hold. With no consolidation after the act, nothing says it waits.
func (a *Chains) waiting(base *Law, act Version, e lexdoc.Edit, from time.Time) bool {
	return a.waitingUntil(base, act, e, from, nil)
}

// takesEffectBy is when a change a consolidation named as waiting is known to be in the law: the
// first consolidation after the last that names it, which prints it in. Lov nr. 1191 of 2021's
// § 1, nr. 18, waited in udlændingeloven's consolidation of September 2021 and is printed by that
// of August 2023; made from the day the act was signed, it took the "nr. 1-6" lov nr. 2622 of
// December 2021 changes. Zero when no consolidation named it, or one still does.
func (a *Chains) takesEffectBy(base *Law, act Version, e lexdoc.Edit, from time.Time) time.Time {
	var until time.Time
	if a.waitingUntil(base, act, e, from, &until) {
		return time.Time{}
	}
	return until
}

func (a *Chains) waitingUntil(base *Law, act Version, e lexdoc.Edit, from time.Time, waitedUntil *time.Time) bool {
	a.consolidationsOnce.Do(func() {
		a.consolidations = map[string][]Entry{}
		for _, en := range a.entries {
			if strings.HasPrefix(strings.ToLower(en.Title), "bekendtgørelse af ") {
				k := ConsolidationTitle(en.Title)
				a.consolidations[k] = append(a.consolidations[k], en)
			}
		}
	})
	actEntry, ok := a.entryOf(act.Accession)
	if !ok {
		return false
	}
	signed, ok := ParseDate(actEntry.DiesSigni)
	if !ok {
		return false
	}
	if from.Before(signed) {
		from = signed
	}
	var later []Entry
	for _, en := range a.consolidations[ConsolidationTitle(base.Meta.Title)] {
		if d, ok := ParseDate(en.DiesSigni); ok && d.After(from) {
			later = append(later, en)
		}
	}
	if len(later) == 0 {
		return false
	}
	slices.SortFunc(later, func(x, y Entry) int { return strings.Compare(x.DiesSigni, y.DiesSigni) })
	for _, en := range later {
		if a.listsRepeal(en.Accession, act, e) {
			// "Ændringen, der følger af § 1, nr. 25, i lov nr. 1000 af 30. august 2015, ophæves
			// den 1. januar 2020": repealed before it ever took effect.
			return true
		}
	}
	// The newest consolidation says whether it still waits; one before it may not have said so
	// in words this reads.
	last := -1
	for i, en := range later {
		if a.listsWaiting(en.Accession, act, e) {
			last = i
		}
	}
	switch {
	case last == len(later)-1:
		return true
	case last < 0:
		// No consolidation says it waits. One that does not print it while a later one does
		// says when it came: lov nr. 1337 of 2008 left § 2, nr. 1, vægtafgiftsloven's day
		// charge, to the minister; the consolidations of 2010 and 2011 print § 4 as it stood
		// and say nothing of nr. 1, and that of 2012 prints the charge.
		if k := a.firstPrinting(later, e); k > 0 && waitedUntil != nil {
			*waitedUntil, _ = ParseDate(later[k].DiesSigni)
		}
		return false
	}
	if waitedUntil != nil {
		*waitedUntil, _ = ParseDate(later[last+1].DiesSigni)
	}
	// Named as waiting, and no longer: set in force by an order the archive may not hold, or
	// left behind — lov nr. 947 of 1999, § 2, nr. 4-7, still waited in spiritusafgiftsloven's
	// consolidation of 2020, and the next one, rewriting § 20 a, no longer names it. A change
	// still waiting ten years on goes in only where the consolidation that stops naming it
	// prints its wording.
	listed, _ := ParseDate(later[last].DiesSigni)
	if listed.Before(signed.AddDate(10, 0, 0)) {
		return false
	}
	wording := []rune(e.NewWording())
	if len(wording) < 40 {
		return true
	}
	text := a.consolidationText(later[last+1].Accession)
	has := func(at int) bool { return strings.Contains(text, string(wording[at:at+30])) }
	mid, end := (len(wording)-30)/2, len(wording)-30
	return !has(0) && !(has(mid) && has(end))
}

// firstPrinting is the first of the consolidations that prints a change's new wording, long
// enough to tell, or -1.
func (a *Chains) firstPrinting(later []Entry, e lexdoc.Edit) int {
	wording := []rune(e.NewWording())
	if len(wording) < 40 {
		return -1
	}
	for k, en := range later {
		text := a.consolidationText(en.Accession)
		has := func(at int) bool { return strings.Contains(text, string(wording[at:at+30])) }
		mid, end := (len(wording)-30)/2, len(wording)-30
		if has(0) && (has(mid) || has(end)) {
			return k
		}
	}
	return -1
}

// consolidationText is a consolidation's text reduced for matching, kept for the next change that
// asks.
func (a *Chains) consolidationText(accession string) string {
	a.waitingMu.Lock()
	if t, ok := a.texts[accession]; ok {
		a.waitingMu.Unlock()
		return t
	}
	a.waitingMu.Unlock()
	text := ""
	if c := a.consolidationContent(accession); c != nil {
		// The provisions' own words: a note may quote the change still to come.
		var b strings.Builder
		for _, r := range c.Provisions() {
			b.WriteString(r.Provision.OwnText())
			b.WriteByte(' ')
		}
		for _, ap := range c.Appendices {
			b.WriteString(lexdoc.SegmentsText(ap.Segments))
			b.WriteByte(' ')
		}
		text = lexdoc.MatchText(b.String())
	}
	a.waitingMu.Lock()
	if a.texts == nil || len(a.texts) > 512 {
		a.texts = map[string]string{}
	}
	a.texts[accession] = text
	a.waitingMu.Unlock()
	return text
}

// listsWaiting reports whether the consolidation's notes list the act's change as not printed.
func (a *Chains) listsWaiting(consolidation string, act Version, e lexdoc.Edit) bool {
	return a.lists(consolidation, act, e, false)
}

// listsRepeal reports whether the consolidation's notes say the act's change is repealed.
func (a *Chains) listsRepeal(consolidation string, act Version, e lexdoc.Edit) bool {
	return a.lists(consolidation, act, e, true)
}

func (a *Chains) lists(consolidation string, act Version, e lexdoc.Edit, repeal bool) bool {
	actEntry, ok := a.entryOf(act.Accession)
	if !ok {
		return false
	}
	signed, ok := ParseDate(actEntry.DiesSigni)
	if !ok {
		return false
	}
	n := lexdoc.Atoi(strings.TrimRight(strings.TrimSpace(e.Number), "."))
	for _, w := range a.waitingIn(consolidation) {
		if w.Repeal != repeal {
			continue
		}
		if w.Number != actEntry.Number || !w.Signed.Equal(signed) || w.Section != e.Section {
			continue
		}
		if len(w.Numbers) == 0 {
			return true
		}
		for _, span := range w.Numbers {
			if n >= span[0] && n <= span[1] {
				return true
			}
		}
	}
	return false
}

// entryOf is the archive's entry for an accession.
func (a *Catalog) entryOf(accession string) (Entry, bool) {
	i, ok := a.byAccn[accession]
	if !ok {
		return Entry{}, false
	}
	return a.entries[i], true
}

// reWaitingNote is a note's change not printed: "følger af § 1, nr. 2, 8-13 og 26, i lov nr. 721
// af 27. april 2021, er ikke indarbejdet".
var reWaitingNote = regexp.MustCompile(`(?i)følger\s+af\s+(§\s*[0-9][^§]{0,200}?(?:§[^§]{0,80}?){0,3}?)\s*,?\s*i\s+lov\s+nr\.\s*([0-9]+)\s+af\s+([0-9]{1,2}\.\s*\p{L}+\s+[0-9]{4}).{0,600}?(?:er\s+ikke\s+(?:indarbejdet|medtaget)|træder\s+i\s+kraft\s+efter|ikke\s+(?:er\s+)?(?:trådt|sat)\s+i\s+kraft)`)

// reRepealedNote is a note on a change repealed before it took effect: "Ændringen, der følger af §
// 1, nr. 25, i lov nr. 1000 af 30. august 2015, ophæves den 1. januar 2020", "at § 1, nr. 25, i lov
// nr. 1000 af 30. august 2015 ophæves".
var reRepealedNote = regexp.MustCompile(`(?i)(§\s*[0-9]+\s*,\s*nr\.\s*[0-9][^§]{0,80}?)\s*,?\s*i\s+lov\s+nr\.\s*([0-9]+)\s+af\s+([0-9]{1,2}\.\s*\p{L}+\s+[0-9]{4})\s*,?\s*ophæves\b`)

// waitingRef is a note's reference cleaned to the act's own provisions and changes: "§ 1, nr. 6, 7 og
// 8 (delvis), med undtagelse af § 212 og § 223, og nr. 9-34" is "§ 1, nr. 6, 7 og 8, 9-34".
func waitingRef(ref string) string {
	ref = reParenthesis.ReplaceAllString(ref, "")
	ref = reExcept.ReplaceAllString(ref, "")
	ref = reAndNr.ReplaceAllString(ref, ", ")
	return strings.TrimRight(strings.TrimSpace(ref), ",")
}

var (
	reParenthesis = regexp.MustCompile(`\s*\([^)]*\)`)
	reExcept      = regexp.MustCompile(`(?i),?\s*med\s+undtagelse\s+af\s+[^,]*`)
	reAndNr       = regexp.MustCompile(`(?i)\s*,?\s*og\s+nr\.\s*`)
)

// waitingIn reads the changes a consolidation's notes say it does not print yet.
func (a *Chains) waitingIn(accession string) []WaitingChange {
	a.waitingMu.Lock()
	if w, ok := a.waitingNotes[accession]; ok {
		a.waitingMu.Unlock()
		return w
	}
	a.waitingMu.Unlock()
	out := a.notes(accession)
	a.waitingMu.Lock()
	if a.waitingNotes == nil {
		a.waitingNotes = map[string][]WaitingChange{}
	}
	a.waitingNotes[accession] = out
	a.waitingMu.Unlock()
	return out
}

// WaitingNotes reads the changes a document's notes say it does not print yet from its text lines:
// "De ændringer, der følger af § 1, nr. 2, 8-13 …, i lov nr. 721 af 27. april 2021, er ikke
// indarbejdet". The archive reads them from the files when asked; the artifact build stores them.
func WaitingNotes(lines []string) []WaitingChange {
	var out []WaitingChange
	{
		var texts []string
		for _, l := range lines {
			if strings.Contains(l, "lov nr.") && (strings.Contains(l, "følger af") || strings.Contains(l, "ophæves")) {
				texts = append(texts, lexdoc.Squeeze(l))
			}
		}
		for _, t := range texts {
			for _, m := range reWaitingNote.FindAllStringSubmatch(t, -1) {
				d, ok := ParseDate(m[3])
				if !ok {
					tg := lexdoc.ParseTarget("I lov nr. " + m[2] + " af " + m[3] + " foretages følgende ændringer:")
					d, ok = tg.Date, !tg.Date.IsZero()
				}
				if !ok {
					continue
				}
				for _, r := range lexdoc.InForceRulesOf(waitingRef(m[1])) {
					out = append(out, WaitingChange{Number: lexdoc.Atoi(m[2]), Signed: d, Section: r.Section, Numbers: r.Numbers})
				}
			}
			for _, m := range reRepealedNote.FindAllStringSubmatch(t, -1) {
				tg := lexdoc.ParseTarget("I lov nr. " + m[2] + " af " + m[3] + " foretages følgende ændringer:")
				if tg.Date.IsZero() {
					continue
				}
				for _, r := range lexdoc.InForceRulesOf(waitingRef(m[1])) {
					out = append(out, WaitingChange{Repeal: true, Number: lexdoc.Atoi(m[2]), Signed: tg.Date, Section: r.Section, Numbers: r.Numbers})
				}
			}
		}
	}
	return out
}

// openings are a provision's subsections as the consolidation cited printed them, by their
// opening words; nothing when the change cites the base itself.
func (a *Chains) openings(base *Law, cited TargetHint, paragraph string) []string {
	found := a.Resolve(cited)
	if len(found) != 1 || found[0].Accession == base.Meta.Accession {
		return nil
	}
	c := a.consolidationContent(found[0].Accession)
	if c == nil {
		return nil
	}
	loc, ok := c.Find(lexdoc.Address{Recognised: true, Paragraph: paragraph})
	if !ok || loc.Provision == nil {
		return nil
	}
	return lexdoc.StykkeOpenings(loc.Provision)
}

// consolidationContent is a consolidation read into the model once, for the evidence later chains
// ask of it.
func (a *Chains) consolidationContent(accession string) *lexdoc.Content {
	a.waitingMu.Lock()
	if c, ok := a.contents[accession]; ok {
		a.waitingMu.Unlock()
		return c
	}
	a.waitingMu.Unlock()
	var c *lexdoc.Content
	if doc, ok := a.law(accession); ok {
		m := doc.Model().Content
		c = &m
	}
	a.waitingMu.Lock()
	if a.contents == nil || len(a.contents) > 512 {
		// Bounded: a server answering for many laws over time would otherwise keep every
		// consolidation it ever consulted.
		a.contents = map[string]*lexdoc.Content{}
	}
	a.contents[accession] = c
	a.waitingMu.Unlock()
	return c
}

// NewCatalog is an empty catalogue, filled by Add in the order documents are found.
func NewCatalog() *Catalog { return &Catalog{byAccn: map[string]int{}, byNumber: map[int][]int{}} }

// ScanOrder is the catalogue's entries in the order they were added, which the consolidation
// indexes inherit; Entries sorts.
func (a *Catalog) ScanOrder() []Entry { return append([]Entry(nil), a.entries...) }

// NewChainsFor is consolidation over a catalogue that is already built, reading documents through
// the given providers: each document as a Law, each act's record, and each document's notes.
func NewChainsFor(c *Catalog, law func(string) (*Law, bool), act func(string) (*ActRecord, bool), notes func(string) []WaitingChange) *Chains {
	return &Chains{Catalog: c, law: law, readAct: act, notes: notes}
}

// ResolveAdopted is a law as amended by every act signed by a date, the parts not yet in force
// included, with this library's help resolving the chain.
func (a *Chains) ResolveAdopted(base *Law, asOf time.Time) *View {
	return lawAsOfMode(base, asOf, a.act, true, a.chainHelp(base))
}
