// The search tool: the index behind the same read-only endpoint as lookup.
//
// Search is offered only when an index has been loaded. Advertising a tool that would
// answer "no, that needs an index" is worse than not advertising it, which is why the
// server asks the caller for a file rather than pretending.
package mcpserver

import (
	"fmt"
	"strings"

	"github.com/lkarlslund/retsinformation/internal/lexdania"
	"github.com/lkarlslund/retsinformation/internal/search"
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
	Limit  int

	// NoNear turns off proximity grading, which is the one part of a search that reads text
	// before the answer is rendered. Ranking then comes from the index alone.
	NoNear bool
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
	Query    string
	Hits     []SearchHit
	Mirror   string
	Warnings []string
}

// HasSearch reports whether this server can answer a search at all.
func (s *Server) HasSearch() bool { return s.index != nil }

// UseIndex attaches a built index for serving. The server still cannot write anything:
// the index was built by a separate command, and serving reads it.
func (s *Server) UseIndex(i *search.Index) { s.index = i }

// Search runs a query and renders snippets by going back to the archive, which is why
// a search needs the archive as well as the index.
func (s *Server) Search(req SearchRequest) (*SearchResult, error) {
	if s.index == nil {
		return nil, fmt.Errorf("no search index is loaded: run `retsin index build` and start the server with --index")
	}
	if strings.TrimSpace(req.Query) == "" {
		return nil, fmt.Errorf("query is empty")
	}
	limit := req.Limit
	if limit <= 0 || limit > 50 {
		limit = 10
	}
	// The index cannot see distance between words, so the page it ranks best is not
	// necessarily the page a reader wants. Over-fetching gives the proximity grade a front to
	// reorder, and the extra reads are bounded by search.NearWindow — which is the cost the
	// grading was designed to keep small by not needing an index change at all.
	fetch := limit
	if !req.NoNear {
		fetch = limit * 2
		if fetch > 50 {
			fetch = 50
		}
	}
	hits := s.index.Search(search.Query{
		Text:    req.Query,
		Kind:    req.Kind,
		MinYear: req.Since,
		MaxYear: req.Until,
		Source:  req.Source,
		Limit:   fetch,
	})
	texts := search.NewUnitTexts()
	if !req.NoNear {
		hits = s.index.GradeNear(hits, req.Query, func(h *search.Hit) string {
			return texts.Unit(h.Doc, h.Addr)
		})
	}
	if len(hits) > limit {
		hits = hits[:limit]
	}
	res := &SearchResult{Query: req.Query, Mirror: s.Status()}
	for _, h := range hits {
		label := h.Doc.Title
		if entry, ok := s.archive.Find(h.Doc.Accession); ok {
			label = citation(entry)
		}
		unitText := texts.Unit(h.Doc, h.Addr)
		snippet := search.SnippetFrom(unitText, h.Matched, 0)
		ref := h.Doc.ELI
		if len(s.ambiguous(h.Doc.ELI)) > 1 {
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
	h := s.horizon()
	res.Mirror = h.Line()
	if len(res.Hits) == 0 {
		// An empty result has to say what it searched rather than looking like a
		// statute that does not mention the words.
		res.Warnings = append(res.Warnings, fmt.Sprintf(
			"no provision in the indexed corpus matches this query: the index covers %d documents built %s, and the mirror holds %d",
			h.IndexDocs, h.indexStamp(), h.MirrorDocs))
	}
	// The dataset line belongs on every answer, so a reader can see which date this
	// result speaks for. "Current" is not a property of the corpus, it is a date, and
	// without it a partial result is indistinguishable from a complete one.
	res.Warnings = append(res.Warnings, h.Line())
	if h.IndexDocs != h.MirrorDocs {
		// The two counts differ routinely -- documents arrive between index builds --
		// so both are reported and nothing is inferred about why. What follows from the
		// pair is a rule, not a guess: a hit here proves the document is in the mirror,
		// and a miss proves only that it is not in the index.
		res.Warnings = append(res.Warnings, fmt.Sprintf(
			"the index covers %d of the mirror's %d documents: a hit here proves presence, a miss does not prove absence from the mirror (`retsin index build` closes the gap)",
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
		fmt.Fprintf(&b, "- **Dataset:** %s\n\n", r.Mirror)
	}
	if len(r.Hits) == 0 {
		b.WriteString("No matches in the indexed corpus.\n")
	}
	for i, h := range r.Hits {
		fmt.Fprintf(&b, "### %d. %s", i+1, h.Citation)
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
	case string(lexdania.TextFromHTML):
		return "text: html (recovered by our reader)"
	case string(lexdania.TextFromNone):
		return "text: none"
	default:
		return "text: xml (register's own body)"
	}
}
