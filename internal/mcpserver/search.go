// The search tool: the index behind the same read-only endpoint as lookup.
//
// Search is offered only when an index has been loaded. Advertising a tool that would
// answer "no, that needs an index" is worse than not advertising it, which is why the
// server asks the caller for a file rather than pretending.
package mcpserver

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/lkarlslund/lovkilde/internal/chain"
	"github.com/lkarlslund/lovkilde/internal/disclaimer"
	"github.com/lkarlslund/lovkilde/internal/lexdoc"
	"github.com/lkarlslund/lovkilde/internal/search"
)

// SearchRequest is a full-text query over the mirror.
type SearchRequest struct {
	// Query carries the operator syntax documented in internal/search/query.go: +word,
	// -word, AND, OR, NOT, and a "quoted span" meaning its words in one provision. Plain
	// words stay a coverage-weighted OR.
	Query  string
	Kind   string
	Since  int
	Until  int
	Source string

	// Status narrows to what the register itself calls current ("valid"/"gældende"),
	// historic, or unlabelled ("unknown"). Like Source it is a claim about provenance, and
	// like the register's dates it is the register's, not ours.
	Status string
	Limit  int

	// Kinds and Statuses are the same filters as sets, for the reading page's toggles: a hit
	// passes when its document matches any of them.
	Kinds    []string
	Statuses []string
	Sources  []string

	// Offset is where in the ranked list this page starts, counted in ranked hits, so the
	// next page starts at Offset+Limit whatever this page had to leave out.
	Offset int

	// Within is a document's accession, to search inside that document only.
	Within string

	// NoNear turns off proximity grading, which is the one part of a search that reads text
	// before the answer is rendered. Ranking then comes from the index alone.
	NoNear bool

	// Language is "da" for the reading pages, whose dataset line and caveats are written in
	// Danish; anything else is the English an agent's answer carries.
	Language string
}

// SearchHit is one provision the query matched, with the provenance an answer needs.
type SearchHit struct {
	ELI        string
	Accession  string
	Citation   string
	Address    string
	Provenance string
	Score      float64
	Matched    []string
	Snippet    string

	// Covered of Requested says how much of the query this provision answers. For an AND
	// query it is always full, which is exactly the difference between the two readings.
	Covered   int
	Requested int

	// Gap is how many words apart the query words sat, when proximity was graded: 0 or 1 is
	// the phrase case, -1 means not graded. It is reported rather than buried in the score,
	// because the distance is the fact the asker wanted, not a detail of ranking.
	Gap int

	// Reference is what to pass back to lookup: the ELI path when it identifies one
	// document, and the accession when it does not. Bills store every stage under the
	// same ELI, so a suggested reference has to be the one thing that is unique.
	Reference string
}

// SearchResult is a page of hits plus the caveats that make them interpretable.
type SearchResult struct {
	Query string
	Hits  []SearchHit

	// Total is how many provisions match in all, and Next the Offset of the following page,
	// or 0 when this page reached the end.
	Total  int
	Offset int
	Next   int

	// Cited are the documents the query cites by number ("lov nr. 1399 af 2019"), found in
	// the register's metadata: a document's own number is rarely in its text, so ranking
	// cannot find it, and a reader who typed a citation wants that document first.
	Cited []BrowseItem

	Mirror   string
	Warnings []string
}

// SourceFacets counts the indexed documents by where their text came from — the register's
// own XML, a recovered web page, or nothing — so a page can offer each with its size.
func (s *Server) SourceFacets() []Facet {
	if s.idx() == nil {
		return nil
	}
	// One pass over the document table, a fraction of a millisecond: cheaper than keeping a
	// cache in step with an index that UseIndex can swap.
	counts := map[string]int{}
	for _, d := range s.idx().Docs {
		counts[d.Source]++
	}
	var out []Facet
	for _, name := range []string{"xml", "html", "none"} {
		if counts[name] > 0 {
			out = append(out, Facet{Name: name, Count: counts[name]})
		}
	}
	return out
}

// HasSearch reports whether this server can answer a search at all.
func (s *Server) HasSearch() bool { return s.idx() != nil }

// UseIndex attaches a built index for serving. The server still cannot write anything:
// the index was built by a separate command, and serving reads it.
func (s *Server) UseIndex(i *search.Index) {
	for {
		old := s.served.Load()
		next := &corpus{index: i}
		if old != nil {
			next.docs, next.cat = old.docs, old.cat
		}
		if s.served.CompareAndSwap(old, next) {
			return
		}
	}
}

// idx is the index in use, or nil. It is swapped in whole, with its artifact or once a background
// build finishes, while requests are being answered: from nil to an index, or from one index to
// another, never back to nil, so a request that reads it twice cannot see it vanish.
func (s *Server) idx() *search.Index {
	if c := s.served.Load(); c != nil {
		return c.index
	}
	return nil
}

// IndexBuilding marks that a search index is being built for this server, so search is offered
// and answers "being built" rather than "unavailable" until UseIndex delivers it.
func (s *Server) IndexBuilding(building bool) { s.indexBuilding.Store(building) }

// SearchPending reports whether search is coming: no index yet, but one is being built.
func (s *Server) SearchPending() bool { return s.idx() == nil && s.indexBuilding.Load() }

// Search runs a query and renders snippets by going back to the archive, which is why
// a search needs the archive as well as the index.
func (s *Server) Search(req SearchRequest) (*SearchResult, error) {
	if s.idx() == nil {
		if s.SearchPending() {
			if strings.EqualFold(req.Language, "da") {
				return nil, fmt.Errorf("søgeindekset bygges fra den kompilerede fil og er klar om et minut eller to; opslag virker allerede")
			}
			return nil, fmt.Errorf("the search index is being built from the compiled artifact and is ready in a minute or two; lookup already works")
		}
		if strings.EqualFold(req.Language, "da") {
			return nil, fmt.Errorf("der er intet søgeindeks: serveren bygger det selv fra den kompilerede fil, så byg den med `lovkilde-update artifact build` og genstart")
		}
		return nil, fmt.Errorf("no search index: the server builds it from the compiled artifact, so build one with `lovkilde-update artifact build` and restart")
	}
	if strings.TrimSpace(req.Query) == "" {
		if strings.EqualFold(req.Language, "da") {
			return nil, fmt.Errorf("søgningen er tom")
		}
		return nil, fmt.Errorf("query is empty")
	}
	limit := req.Limit
	if limit <= 0 || limit > 50 {
		limit = 10
	}
	offset := req.Offset
	if offset < 0 {
		offset = 0
	}
	// The whole ranked list, so that every page is a slice of one order and the total is
	// the real count. The index cannot see distance between words, so the proximity grade
	// reorders the front of that list — search.NearWindow hits, which bounds the reads.
	hits := s.idx().Rank(search.Query{
		Text:      req.Query,
		Kind:      req.Kind,
		Kinds:     req.Kinds,
		MinYear:   req.Since,
		MaxYear:   req.Until,
		Source:    req.Source,
		Status:    req.Status,
		Statuses:  req.Statuses,
		Sources:   req.Sources,
		Accession: req.Within,
	})
	texts := search.NewUnitTexts(s.unitSource())
	if !req.NoNear {
		hits = s.idx().GradeNear(hits, req.Query, func(h *search.Hit) string {
			return texts.Unit(*h.Doc, h.Addr)
		})
	}
	hits = distinctCitations(hits)
	total := len(hits)
	// A forbidden word can make a passage unshowable, and asking for ten and returning seven
	// because three passages had to be refused is a worse answer than reading on: the page
	// takes from further down until it is full, and Next says where it stopped.
	next := offset
	if offset < len(hits) {
		hits = hits[offset:]
	} else {
		hits = nil
	}
	res := &SearchResult{Query: req.Query, Mirror: s.Status(), Total: total, Offset: offset}
	if offset == 0 {
		res.Cited = s.Cited(req.Query)
	}
	forbidden := search.ExcludedWords(req.Query)
	refused := 0
	for _, h := range hits {
		if len(res.Hits) == limit {
			break
		}
		next++
		label := h.Doc.Title
		if entry, ok := s.lib().Find(h.Doc.Accession); ok {
			label = citation(entry)
		}
		unitText := texts.Unit(*h.Doc, h.Addr)
		snippet := search.SnippetFromWithout(unitText, h.Matched, forbidden, 0)
		if snippet == "" && unitText != "" {
			// The passage that would be shown contains a word the query forbids, so this hit is
			// not shown: the index excluded the unit it matched, which is not always the span a
			// passage is cut from, and the reader only ever sees the passage.
			refused++
			continue
		}
		ref := h.Doc.ELI
		// No ELI path (a document the register numbered 0) or one shared by several
		// documents: the accession is then the only key that names this one.
		if ref == "" || len(s.ambiguous(h.Doc.ELI)) > 1 {
			ref = h.Doc.Accession
		}
		res.Hits = append(res.Hits, SearchHit{
			ELI:        h.Doc.ELI,
			Accession:  h.Doc.Accession,
			Reference:  ref,
			Citation:   label,
			Address:    h.Addr,
			Provenance: h.Doc.Source,
			Score:      h.Score,
			Matched:    h.Matched,
			Covered:    h.Covered,
			Requested:  h.Requested,
			Gap:        h.Gap,
			Snippet:    snippet,
		})
	}
	if next < total {
		res.Next = next
	}
	h := s.horizon()
	danish := strings.EqualFold(req.Language, "da")
	res.Mirror = h.LineIn(danish)
	if w := s.indexMismatch(danish); w != "" {
		res.Warnings = append(res.Warnings, w)
	}
	if danish {
		if refused > 0 {
			res.Warnings = append(res.Warnings, fmt.Sprintf(
				"%d resultat(er) er udeladt, fordi uddraget indeholder et ord, søgningen udelukker: indekset udelukker den bestemmelse, det fandt, og uddraget er ikke altid skåret af den",
				refused))
		}
		if len(res.Hits) == 0 {
			res.Warnings = append(res.Warnings, fmt.Sprintf(
				"ingen bestemmelse i det indekserede materiale svarer til søgningen: indekset dækker %d dokumenter, bygget %s, og spejlet har %d",
				h.IndexDocs, h.indexStampDA(), h.MirrorDocs))
		}
		if h.IndexDocs != h.MirrorDocs {
			res.Warnings = append(res.Warnings, fmt.Sprintf(
				"indekset dækker %d af spejlets %d dokumenter: et resultat her viser, at dokumentet findes, men intet resultat viser ikke, at det mangler (`lovkilde index build` lukker hullet)",
				h.IndexDocs, h.MirrorDocs))
		}
		return res, nil
	}
	if refused > 0 {
		res.Warnings = append(res.Warnings, fmt.Sprintf(
			"%d hit(s) were dropped because the passage shown for them contains a word the query forbids: the index excludes the provision it matched, which is not always the span a passage is cut from",
			refused))
	}
	if len(res.Hits) == 0 {
		// An empty result has to say what it searched rather than looking like a
		// statute that does not mention the words.
		res.Warnings = append(res.Warnings, fmt.Sprintf(
			"no provision in the indexed corpus matches this query: the index covers %d documents built %s, and the mirror holds %d",
			h.IndexDocs, h.indexStamp(), h.MirrorDocs))
	}
	// The horizon line belongs on every answer, so a reader can see which date this result
	// speaks for: "current" is not a property of the corpus, it is a date, and without it a
	// partial result is indistinguishable from a complete one. It is carried once, in Mirror,
	// which the answer prints as its Index field and the page prints above the results; it used
	// to be appended to Warnings as well, so the same sentence appeared twice on the page and
	// twice in the answer, once as a fact and once as a problem.
	if h.IndexDocs != h.MirrorDocs {
		// The two counts differ routinely -- documents arrive between index builds --
		// so both are reported and nothing is inferred about why. What follows from the
		// pair is a rule, not a guess: a hit here proves the document is in the mirror,
		// and a miss proves only that it is not in the index.
		res.Warnings = append(res.Warnings, fmt.Sprintf(
			"the index covers %d of the mirror's %d documents: a hit here proves presence, a miss does not prove absence from the mirror (`lovkilde index build` closes the gap)",
			h.IndexDocs, h.MirrorDocs))
	}
	return res, nil
}

// String renders a result as markdown: the consumer is a model rendering this in a
// chat, and a hit whose address is not copy-pasteable is a hit that gets retyped wrong.
func (r *SearchResult) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "## Search: %q\n\n", r.Query)
	if r.Mirror != "" {
		fmt.Fprintf(&b, "- **Dataset:** %s\n", r.Mirror)
	}
	for _, c := range r.Cited {
		fmt.Fprintf(&b, "- **Cited document:** %s (%s nr. %d af %d) — `lookup` with `document=\"%s\"`\n", c.Title, orDash(c.DocumentType), c.Number, c.Year, c.Reference)
	}
	fmt.Fprintf(&b, "- **Matches:** %d provisions in all", r.Total)
	if len(r.Hits) > 0 {
		fmt.Fprintf(&b, "; these are %d–%d", r.Offset+1, r.Offset+len(r.Hits))
	}
	if r.Next > 0 {
		fmt.Fprintf(&b, "; more with `offset=%d`", r.Next)
	}
	fmt.Fprintf(&b, "\n> **Disclaimer:** %s\n\n", disclaimer.ShortEnglish)
	if len(r.Hits) == 0 {
		b.WriteString("No matches in the indexed corpus.\n")
	}
	for i, h := range r.Hits {
		fmt.Fprintf(&b, "### %d. %s", r.Offset+i+1, h.Citation)
		if h.Address != "" {
			fmt.Fprintf(&b, " — %s", h.Address)
		}
		b.WriteString("\n\n")
		fmt.Fprintf(&b, "`%s` · accession `%s` · **%s** · score %.2f · matched %d of %d: %s",
			h.ELI, h.Accession, provenanceLabel(h.Provenance), h.Score, h.Covered, h.Requested,
			strings.Join(h.Matched, ", "))
		if h.Gap >= 0 {
			// The gap is the answer to "did they really occur together", so it goes on the
			// line the reader looks at, not in a field they have to know to ask for.
			fmt.Fprintf(&b, " · words %d apart", h.Gap)
		}
		b.WriteString("\n\n")
		if h.Snippet != "" {
			fmt.Fprintf(&b, "> %s\n\n", strings.ReplaceAll(strings.TrimSpace(h.Snippet), "\n", " "))
		}
		fmt.Fprintf(&b, "Read it: `lookup` with `document=\"%s\"`, `address=\"%s\"`\n\n", h.Reference, h.Address)
	}
	for _, w := range r.Warnings {
		fmt.Fprintf(&b, "> **Note:** %s\n\n", w)
	}
	return b.String()
}

// provenanceLabel is the one-line form shared with lookup.
func provenanceLabel(source string) string {
	switch source {
	case string(lexdoc.SourceHTMLPayload):
		return "text: html (recovered by our reader)"
	case string(lexdoc.SourceNone):
		return "text: none"
	default:
		return "text: xml (register's own body)"
	}
}

// distinctCitations keeps the best hit for each place a reader can be sent. A document's text
// outside every numbered provision is indexed as several capped runs, all cited as the
// document itself, and listing them separately shows the reader the same link and the same
// passage two or three times, and counts it as many times in the total.
func distinctCitations(hits []search.Hit) []search.Hit {
	type place struct{ acc, addr string }
	seen := make(map[place]bool, len(hits))
	out := hits[:0]
	for _, h := range hits {
		k := place{h.Doc.Accession, h.Addr}
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, h)
	}
	return out
}

var (
	reCitedNumber = regexp.MustCompile(`(?i)\bnr\.?\s*(\d{1,5})\b`)
	reCitedYear   = regexp.MustCompile(`\b(1[89]\d\d|20\d\d)\b`)
)

// Cited finds the documents a query cites by number: "lov nr. 1399 af 2019", "bek. nr. 12
// af 3. januar 2020", "nr. 1399". Without a year every document of that number is a
// candidate, so the newest few are offered; a type word ("lov", "bekendtgørelse") puts the
// documents of that type first.
func (s *Server) Cited(query string) []BrowseItem {
	m := reCitedNumber.FindStringSubmatch(query)
	if m == nil {
		return nil
	}
	number, _ := strconv.Atoi(m[1])
	year := 0
	if y := reCitedYear.FindAllString(query, -1); len(y) > 0 {
		year, _ = strconv.Atoi(y[len(y)-1])
	}
	q := strings.ToLower(query)
	wantType := ""
	switch {
	case strings.Contains(q, "lovbekendtgørelse") || strings.Contains(q, "lbk"):
		wantType = "LBK"
	case strings.Contains(q, "bekendtgørelse") || strings.Contains(q, "bek."):
		wantType = "BEK"
	case strings.Contains(q, "lov"):
		wantType = "LOV"
	case strings.Contains(q, "cirkulære"):
		wantType = "CIR"
	case strings.Contains(q, "vejledning"):
		wantType = "VEJ"
	}
	var found []chain.Entry
	for _, e := range s.lib().Entries() {
		if e.Number == number && (year == 0 || e.Year == year) {
			found = append(found, e)
		}
	}
	typed := func(e chain.Entry) bool {
		return wantType != "" && strings.HasPrefix(strings.ToUpper(e.DocumentType), wantType)
	}
	sort.SliceStable(found, func(i, j int) bool {
		a, b := found[i], found[j]
		if typed(a) != typed(b) {
			return typed(a)
		}
		if (a.Kind == "lta") != (b.Kind == "lta") {
			return a.Kind == "lta"
		}
		if a.Year != b.Year {
			return a.Year > b.Year
		}
		return a.Accession < b.Accession
	})
	if len(found) > 5 {
		found = found[:5]
	}
	out := make([]BrowseItem, 0, len(found))
	for _, e := range found {
		out = append(out, BrowseItem{
			Reference: e.Accession, ELI: e.ELIPath(), Title: e.Title, Kind: e.Kind,
			Year: e.Year, Number: e.Number, DocumentType: e.DocumentType, Status: string(e.Status),
		})
	}
	return out
}
