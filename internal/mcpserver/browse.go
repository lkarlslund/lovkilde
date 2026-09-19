// Listing and cross-linking, so a reader can get somewhere without knowing what to type.
//
// Search answers a question someone already formed. Browsing is the other half: what is on a
// shelf, what year, and from a document, where else does this one lead. The two things a legal
// document genuinely links to are its version chain and the acts it names, and the mirror already
// holds both as facts on the model — Identity.AmendedBy, Identity.Concerns, the archive's Versions
// — so nothing here invents a relation by matching titles.
//
// A link is offered only for a document this mirror holds, and where the register names an act we
// do not have, that is said rather than hidden: a link to a page that does not exist is worse than
// a sentence saying the amending act was never collected.
package mcpserver

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/lkarlslund/retsinformation/internal/lexdania"
	"github.com/lkarlslund/retsinformation/internal/lexdoc"
)

// BrowseRequest is a shelf listing. Zero fields mean "no filter".
type BrowseRequest struct {
	Kind   string
	Year   int
	Title  string // substring, case-insensitive; Danish letters are not folded
	Offset int
	Limit  int

	// Status is "Valid", "Historic" or "unknown" (or the Danish spelling of any of them):
	// the register's own claim about the document, read from the file's Meta block. It is
	// the register's opinion, and the UI words it that way — filtering to "kun gældende"
	// asks what Retsinformation believes, not whether we have verified that nothing newer
	// has repealed the act.
	Status string

	// TypeCode is a substring of the publisher's document-type code ("LOKDOK", "#LOKDOK").
	// Substring rather than a dropdown because the mirror holds the codes and no name table.
	TypeCode string
}

// BrowseItem is one document in a listing. Reference is the accession, because it is the only
// thing that names one document: an ELI path can stand for several stages of a bill.
type BrowseItem struct {
	Reference    string
	ELI          string
	Title        string
	Kind         string
	Year         int
	Number       int
	DocumentType string

	// Status is the register's claim about this document: "Valid", "Historic", or "" when
	// the payload says nothing.
	Status string
}

// statusFacetName names one status bucket for a facet. "unknown" is a name a <select> can
// carry: an empty option value would collide with "no filter chosen", which is a different
// thing — and a reader who cannot select the unlabelled documents gets a filter that quietly
// drops them.
func statusFacetName(s lexdania.Status) string {
	switch s {
	case lexdania.StatusValid:
		return "Valid"
	case lexdania.StatusHistoric:
		return "Historic"
	}
	return "unknown"
}

// statusFilter turns the caller's word into a predicate, or nil for "no opinion".
func statusFilter(want string) func(lexdania.Status) bool {
	switch strings.ToLower(strings.TrimSpace(want)) {
	case "":
		return nil
	case "valid", "gældende", "gaeldende":
		return func(s lexdania.Status) bool { return s == lexdania.StatusValid }
	case "historic", "historisk", "ophævet", "ophaevet":
		return func(s lexdania.Status) bool { return s == lexdania.StatusHistoric }
	case "unknown", "ukendt":
		return func(s lexdania.Status) bool {
			return s != lexdania.StatusValid && s != lexdania.StatusHistoric
		}
	}
	return nil
}

// statusFacets orders the three buckets by size, with the unknown bucket last: it is the one
// that needs explaining, so it should not be the first number a reader compares.
func statusFacets(counts map[string]int) []Facet {
	out := make([]Facet, 0, 3)
	for _, name := range []string{"Valid", "Historic", "unknown"} {
		if n := counts[name]; n > 0 {
			out = append(out, Facet{Name: name, Count: n})
		}
	}
	return out
}

// Facet is a count beside a filter choice.
type Facet struct {
	Name  string
	Count int
}

// BrowseResult is a page plus the facets that make the next click obvious.
type BrowseResult struct {
	Items  []BrowseItem
	Total  int
	Offset int
	Limit  int
	Kind   string
	Year   int
	Title  string

	// Kinds counts the whole archive, and Years counts within the kind filter: a reader who
	// picked a shelf should see what years that shelf has, and one who has not should not be
	// told about a year that disappears the moment they pick a shelf.
	Kinds []Facet
	Years []Facet

	// Status is the filter that was applied, and Statuses counts the archive three ways:
	// the count for "unknown" is the reason a status filter cannot be read as a guarantee.
	Status   string
	Statuses []Facet

	// TypeCode echoes the code filter so paging keeps it: a link that drops a filter shows the
	// reader a total they did not ask for.
	TypeCode string
}

// Browse lists documents newest-first, paging over the scanned archive index. The index is
// already in memory — it is what lookup resolves against — so this is a filter and a sort, not a
// read, and no page of it touches the disk.
func (s *Server) Browse(req BrowseRequest) *BrowseResult {
	limit := req.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	needle := strings.ToLower(strings.TrimSpace(req.Title))
	res := &BrowseResult{Offset: max(req.Offset, 0), Limit: limit, Kind: req.Kind, Year: req.Year, Title: req.Title, Status: req.Status, TypeCode: req.TypeCode}

	var matches []lexdania.Entry
	kindCounts := map[string]int{}
	yearCounts := map[string]int{}
	statusCounts := map[string]int{}
	wantStatus := statusFilter(req.Status)
	for _, e := range s.archive.Entries() {
		kindCounts[e.Kind]++
		statusCounts[statusFacetName(e.Status)]++
		if req.Kind != "" && e.Kind != req.Kind {
			continue
		}
		if wantStatus != nil && !wantStatus(e.Status) {
			continue
		}
		if req.TypeCode != "" && !strings.Contains(e.DocumentType, req.TypeCode) {
			continue
		}
		yearCounts[fmt.Sprint(e.Year)]++
		if req.Year != 0 && e.Year != req.Year {
			continue
		}
		if needle != "" && !strings.Contains(strings.ToLower(e.Title), needle) {
			continue
		}
		matches = append(matches, e)
	}
	res.Total = len(matches)

	sort.Slice(matches, func(i, j int) bool {
		a, b := matches[i], matches[j]
		if a.Year != b.Year {
			return a.Year > b.Year
		}
		if a.Number != b.Number {
			return a.Number > b.Number
		}
		return a.Accession < b.Accession
	})

	for i := res.Offset; i < len(matches) && i-res.Offset < limit; i++ {
		e := matches[i]
		res.Items = append(res.Items, BrowseItem{
			Reference: e.Accession, ELI: e.ELIPath(), Title: e.Title, Kind: e.Kind,
			Year: e.Year, Number: e.Number, DocumentType: e.DocumentType,
			Status: string(e.Status),
		})
	}
	res.Statuses = statusFacets(statusCounts)
	res.Kinds = facets(kindCounts, false)
	if req.Kind != "" {
		res.Years = facets(yearCounts, true)
	}
	return res
}

// RelatedGroup is one heading in a document's cross-link rail.
type RelatedGroup struct {
	Name  string
	Items []RelatedItem
}

// RelatedItem is one link out of a document, or the statement that there cannot be one.
type RelatedItem struct {
	Title     string
	Reference string // empty when this mirror does not hold the document
	Effective string // the date the change takes effect, when the register gave one
	Held      bool
}

// Related answers "where does this document lead?" from the model's own facts: the amending
// chain, the acts this one is about, and the other versions of the same law. Each entry is
// marked with whether the mirror actually holds it, which is a different claim from the
// register naming it.
func (s *Server) Related(ref string) ([]RelatedGroup, error) {
	entry, err := s.find(ref)
	if err != nil {
		return nil, err
	}
	m, _, err := s.model(entry)
	if err != nil {
		return nil, err
	}
	var out []RelatedGroup
	if items := s.relatedFrom("Ændret ved", m.Identity.AmendedBy); len(items) > 0 {
		out = append(out, RelatedGroup{Name: "Ændret ved", Items: items})
	}
	if items := s.relatedFrom("Vedrører", m.Identity.Concerns); len(items) > 0 {
		out = append(out, RelatedGroup{Name: "Vedrører", Items: items})
	}
	if versions := s.archiveVersions(entry.Accession); len(versions) > 0 {
		out = append(out, RelatedGroup{Name: "Øvrige udgaver af denne lov", Items: versions})
	}
	return out, nil
}

func (s *Server) relatedFrom(_ string, cites []lexdoc.Cite) []RelatedItem {
	var items []RelatedItem
	for _, c := range cites {
		item := RelatedItem{Title: c.Title, Effective: dateOrEmpty(c.Effective)}
		if _, ok := s.archive.Find(c.Accession); ok {
			item.Reference, item.Held = c.Accession, true
		}
		items = append(items, item)
	}
	return items
}

// archiveVersions lists the chain from the archive rather than the model, because the chain is
// what tells a reader which version they are reading and which came after.
func (s *Server) archiveVersions(accession string) []RelatedItem {
	versions, err := s.archive.Versions(accession)
	if err != nil {
		return nil
	}
	var items []RelatedItem
	for _, v := range versions {
		if v.Accession == accession {
			continue
		}
		item := RelatedItem{Title: v.Title, Effective: dateOrEmpty(v.Effective)}
		if v.Found {
			item.Reference, item.Held = v.Accession, true
		}
		items = append(items, item)
	}
	return items
}

// facets turns a count map into a stable list. Year names sort newest first, because a reader
// picking a year wants the recent ones within reach; medium names sort as words.
func facets(counts map[string]int, newestFirst bool) []Facet {
	out := make([]Facet, 0, len(counts))
	for name, n := range counts {
		out = append(out, Facet{Name: name, Count: n})
	}
	sort.Slice(out, func(i, j int) bool {
		if newestFirst {
			return out[i].Name > out[j].Name
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// dateOrEmpty prints an effective date, or nothing. A zero time is "the register did not say",
// and printing 0001-01-01 for that would be a date the register never gave.
func dateOrEmpty(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format("2006-01-02")
}
