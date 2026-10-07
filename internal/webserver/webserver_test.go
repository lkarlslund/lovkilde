package webserver

import (
	"context"
	"encoding/json"
	"html"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/lkarlslund/retsinformation/internal/lexdania"
	"github.com/lkarlslund/retsinformation/internal/mcpserver"
	"github.com/lkarlslund/retsinformation/internal/search"
)

// The claims these tests exist to keep are the two the package comment makes: a page and an
// answer are the same read of the same bytes, and a document's own text cannot become markup on
// the way to a screen. Everything else — the search page, the rail, the table of contents — is
// only worth having if the links actually go where they say, so the crawl test is the load-bearing
// one, and it is deliberately the last.

const (
	accCanonical = "A19980002X"
	accHTML      = "A19990001X"
	accEmpty     = "A19970003X"
	accUnheld    = "A20010099X"
	accAmender   = "A19990001X"

	// injectedText is what a document's own words look like when they are also markup. The
	// register escapes it in its XML, so the model holds the literal characters, and the page
	// has to show them as text.
	injectedText = "KANONISK TEKST <script>alert('fra dokument')</script>"
)

func meta(acc, doctype, title string, year, number int, refs ...string) string {
	return `<Meta><AccessionNumber>` + acc + `</AccessionNumber><Year>` + strconv.Itoa(year) +
		`</Year><Number>` + strconv.Itoa(number) + `</Number>` +
		`<DocumentType>` + doctype + `</DocumentType><Status>Valid</Status>` +
		`<DiesSigni>1999-01-01</DiesSigni><DocumentTitle>` + title + `</DocumentTitle>` +
		strings.Join(refs, "") + `</Meta>`
}

// changeRef is the register's own statement that a later document amended this one. It is
// indirect on purpose: an anchor element, then value elements that name it.
func changeRef(id, accn, effective, title string) string {
	return `<Change id="` + id + `"/>` +
		`<Ref_Accn REFid="` + id + `">` + accn + `</Ref_Accn>` +
		`<Ref_Af REFid="` + id + `">` + effective + `</Ref_Af>` +
		`<Ref_Text REFid="` + id + `">` + title + `</Ref_Text>`
}

func sidecarWithHTML(markup string) string {
	raw, err := json.Marshal([]struct {
		DocumentHTML string `json:"documentHtml"`
	}{{DocumentHTML: markup}})
	if err != nil {
		panic(err)
	}
	return string(raw)
}

// newTestServer lays out the same three text states the tool tests use, with two additions the
// interface needs: a document whose text contains markup, and an amendment chain where one
// amending act is in the mirror and one is not. Those are the two cases a rail can get wrong.
func newTestServer(t *testing.T, withIndex bool) *mcpserver.Server {
	root := t.TempDir()
	write := func(rel, content string) {
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("lta/1998/2.xml", "<Dokument>"+meta(accCanonical, "Lov", "Testlov med indlejret markup", 1998, 2,
		changeRef("change_1", accAmender, "2000-01-01", "Testbekendtgørelse om intet"),
		changeRef("change_2", accUnheld, "2003-05-01", "Lov vi aldrig hentede"))+
		"<DokumentIndhold><Paragraf><Explicatus>§ 1.</Explicatus>"+
		"<Stk><Explicatus>Stk. 1.</Explicatus>"+strings.ReplaceAll(injectedText, "<", "&lt;")+
		"</Stk></Paragraf></DokumentIndhold></Dokument>")
	write("lta/1999/1.xml", "<Dokument>"+meta(accHTML, "Bekendtgørelse", "Testbekendtgørelse om intet", 1999, 1)+"</Dokument>")
	write("lta/1999/1.json", sidecarWithHTML(`<p class="Paragraf"><span class="ParagrafNr">§ 1.</span> Bekendtgørelsen finder anvendelse fra ...</p><p class="Stk2"><span class="StkNr">Stk. 2.</span> Tekst læst ud af HTML.</p>`))
	write("lta/1997/3.xml", "<Dokument>"+meta(accEmpty, "Bekendtgørelse", "Tom dokument", 1997, 3)+"</Dokument>")

	a, err := lexdania.Scan(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	if a.Len() != 3 {
		t.Fatalf("indexed %d documents, want 3 (skipped %d)", a.Len(), a.Skipped)
	}
	srv := mcpserver.New(a, "https://www.retsinformation.dk/", "test")
	if withIndex {
		srv.UseIndex(mustIndex(t, filepath.Join(root, "lta/1998/2.xml")))
	}
	return srv
}

func mustIndex(t *testing.T, xmlPath string) *search.Index {
	idx, err := search.Build(context.Background(),
		[]search.DocMeta{{Accession: accCanonical, ELI: "eli/lta/1998/2", Path: xmlPath, Kind: "lta", Year: 1998, Number: 2, Title: "Testlov", Source: "xml"}},
		[]search.Unit{{Doc: 0, Addr: "§ 1", Text: injectedText}},
		search.Options{})
	if err != nil {
		t.Fatal(err)
	}
	return idx
}

// newTestSite returns a running site and the server behind it: a test that checks a link has to
// be able to ask whether the thing it points at resolves.
func newTestSite(t *testing.T, withIndex bool) (*httptest.Server, *mcpserver.Server) {
	srv := newTestServer(t, withIndex)
	site := httptest.NewServer(New(srv))
	t.Cleanup(site.Close)
	return site, srv
}

func get(t *testing.T, site *httptest.Server, path string) (string, int) {
	resp, err := http.Get(site.URL + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	body, err := readAll(resp)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return body, resp.StatusCode
}

func readAll(resp *http.Response) (string, error) {
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	return string(raw), err
}

// TestMCPEndpointAnswersARealClient is the claim that one port serves both audiences: an SDK
// client connecting over HTTP gets the same tool list and the same document text as one spawned
// onto a pipe. Hand-rolling JSON-RPC here would only prove we could talk to ourselves.
func TestMCPEndpointAnswersARealClient(t *testing.T) {
	site, _ := newTestSite(t, true)
	client := mcp.NewClient(&mcp.Implementation{Name: "webserver-test", Version: "test"}, nil)
	session, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{
		Endpoint: site.URL + "/mcp",
		// The standalone SSE stream is for server-initiated messages; this server sends
		// none, and a test that holds an idle connection open proves nothing about tools.
		DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		t.Fatalf("connect to /mcp: %v", err)
	}
	defer session.Close()

	tools, err := session.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, tool := range tools.Tools {
		names[tool.Name] = true
	}
	if !names["lookup"] || !names["search"] {
		t.Errorf("tools over HTTP = %v, want lookup and search", names)
	}

	call, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "lookup",
		Arguments: map[string]any{"document": "eli/lta/1998/2", "address": "§ 1, stk. 1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if call.IsError {
		t.Fatalf("lookup over HTTP returned an error result: %v", call.Content)
	}
	var text string
	for _, c := range call.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			text += tc.Text
		}
	}
	if !strings.Contains(text, "KANONISK TEKST") {
		t.Errorf("lookup over HTTP did not carry the document text:\n%s", text)
	}
	// The provenance line has to survive the transport too: an answer that arrived without it
	// would look as trustworthy as one that came from the register's own body.
	if !strings.Contains(strings.ToLower(text), "xml") {
		t.Errorf("answer over HTTP lost its provenance:\n%s", text)
	}
}

func hrefs(body string) []string {
	re := regexp.MustCompile(`href="([^"]*)"`)
	var out []string
	for _, m := range re.FindAllStringSubmatch(body, -1) {
		out = append(out, m[1])
	}
	return out
}

// TestEveryInternalLinkResolves is the load-bearing test for a browsable corpus: an interface
// where a third of the links 404 is worse than a list of documents. It walks each page, takes
// every internal link it advertises, and requires each to answer.
func TestEveryInternalLinkResolves(t *testing.T) {
	site, _ := newTestSite(t, true)
	visited := map[string]bool{}
	frontier := []string{"/", "/search?q=kanonisk", "/browse", "/browse?kind=lta&year=1999",
		"/dok?ref=" + url.QueryEscape(accCanonical), "/status", "/dok?ref=" + url.QueryEscape(accHTML)}

	for depth := 0; len(frontier) > 0 && depth < 4; depth++ {
		var next []string
		for _, path := range frontier {
			if visited[path] {
				continue
			}
			visited[path] = true
			body, code := get(t, site, path)
			if code != http.StatusOK {
				t.Errorf("GET %s → %d", path, code)
				continue
			}
			for _, href := range hrefs(body) {
				href = html.UnescapeString(href)
				if !strings.HasPrefix(href, "/") || strings.HasPrefix(href, "//") {
					continue // off-site links (the register's ELI page) belong to the register
				}
				if strings.HasPrefix(href, "/mcp") {
					continue // the protocol endpoint answers POST, not GET
				}
				if !strings.Contains(href, "?") && strings.HasPrefix(href, "/dok") {
					t.Errorf("%s links to %s without naming a document", path, href)
				}
				next = append(next, href)
			}
		}
		frontier = dedupe(next)
	}
	if len(visited) < 8 {
		t.Errorf("crawl reached %d pages, expected the interface to be interconnected: %v", len(visited), visited)
	}
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// TestSearchResultLinksTheProvisionItQuoted: a hit is a provision, so the link has to carry the
// address rather than land at the top of a law and leave the reader to hunt.
func TestSearchResultLinksTheProvisionItQuoted(t *testing.T) {
	site, srv := newTestSite(t, true)
	body, code := get(t, site, "/search?q=kanonisk")
	if code != http.StatusOK {
		t.Fatalf("search page status = %d", code)
	}
	var link string
	for _, href := range hrefs(body) {
		href = html.UnescapeString(href)
		if strings.HasPrefix(href, "/dok") {
			link = href
			break
		}
	}
	if link == "" {
		t.Fatalf("no /dok link on the search page:\n%s", body)
	}
	parsed, err := url.Parse(link)
	if err != nil {
		t.Fatalf("link %q does not parse: %v", link, err)
	}
	values := parsed.Query()
	// The link's ref is whatever the server says uniquely names the hit — the ELI path when one
	// path holds one document, the accession when it holds several stages of a bill. What a
	// reader needs is not which key was chosen but that following it opens the law the snippet
	// came from, so that is what gets checked.
	res, err := srv.Lookup(mcpserver.LookupRequest{Document: values.Get("ref"), MaxChars: 10})
	if err != nil {
		t.Fatalf("the search page links to %q, which does not resolve: %v", values.Get("ref"), err)
	}
	if res.Accession != accCanonical {
		t.Errorf("link ref %q resolves to %q, want %q", values.Get("ref"), res.Accession, accCanonical)
	}
	if values.Get("addr") != "§ 1" {
		t.Errorf("link addr = %q, want the provision that matched (§ 1)", values.Get("addr"))
	}
	if !strings.Contains(body, "KANONISK TEKST") {
		t.Errorf("search page did not show the matching text:\n%s", body)
	}
}

func TestSearchPageEscapesWhatTheReaderTyped(t *testing.T) {
	site, _ := newTestSite(t, true)
	body, _ := get(t, site, "/search?q="+url.QueryEscape(`<b>"kanonisk"</b>`))
	if strings.Contains(body, `value="<b>`) {
		t.Errorf("the query came back as markup:\n%s", body)
	}
	if !strings.Contains(body, "&lt;b&gt;") {
		t.Errorf("the query was not escaped into the form field:\n%s", body)
	}
}

// TestDocumentPageMarksUpWhatItSays: the table of contents and the body are produced in the same
// walk, so every entry has to land somewhere real. A TOC of dead anchors is the specific way a
// legal reader gets silently misled about where a rule is.
func TestDocumentPageMarksUpWhatItSays(t *testing.T) {
	site, _ := newTestSite(t, true)
	body, code := get(t, site, "/dok?ref="+url.QueryEscape(accCanonical))
	if code != http.StatusOK {
		t.Fatalf("document page status = %d", code)
	}
	anchors := regexp.MustCompile(`id="([a-z0-9-]+)"`)
	ids := map[string]bool{}
	for _, m := range anchors.FindAllStringSubmatch(body, -1) {
		ids[m[1]] = true
	}
	if len(ids) == 0 {
		t.Fatalf("document page has no anchors at all:\n%s", body)
	}
	for _, href := range hrefs(body) {
		if !strings.HasPrefix(href, "#") {
			continue
		}
		if !ids[href[1:]] {
			t.Errorf("contents links to #%s, which the page never marked", href[1:])
		}
	}
	if !strings.Contains(body, "Indhold") {
		t.Errorf("document page has no contents block:\n%s", body)
	}
}

// A narrowed view carries the whole law's contents and only one part of its text, so its links
// have to leave the page rather than land on it. This is the case the fixture missed and the real
// mirror found: 522 contents links on narrowed pages pointed at parts that were not there.
func TestNarrowedViewLinksToThePageThatHoldsEachPart(t *testing.T) {
	site, _ := newTestSite(t, true)
	body, code := get(t, site, "/dok?ref="+url.QueryEscape(accCanonical)+"&addr="+url.QueryEscape("§ 1, stk. 1"))
	if code != http.StatusOK {
		t.Fatalf("narrowed page status = %d", code)
	}
	offsite := 0
	for _, href := range hrefs(body) {
		href = html.UnescapeString(href)
		if !strings.HasPrefix(href, "/dok") || !strings.Contains(href, "#") {
			continue
		}
		offsite++
		target, anchor, _ := strings.Cut(href, "#")
		page, code := get(t, site, target)
		if code != http.StatusOK {
			t.Errorf("contents link %s → %d", href, code)
			continue
		}
		if !strings.Contains(page, `id="`+anchor+`"`) {
			t.Errorf("contents link %s lands on a page that never marks %s", href, anchor)
		}
	}
	if offsite == 0 {
		t.Fatalf("a narrowed page with contents has no link leaving for the part it cannot show:\n%s", body)
	}
}

func TestDocumentPageKeepsDocumentMarkupInert(t *testing.T) {
	site, _ := newTestSite(t, true)
	body, _ := get(t, site, "/dok?ref="+url.QueryEscape(accCanonical))
	if !strings.Contains(body, "KANONISK TEKST") {
		t.Fatalf("the page lost the document's text:\n%s", body)
	}
	if strings.Contains(body, "<script>alert(") {
		t.Errorf("document text became a live script tag on the page")
	}
	if !strings.Contains(body, "&lt;script&gt;") {
		t.Errorf("document text was not escaped; reader sees nothing of the markup it should see:\n%s", body)
	}
}

// TestRelatedRailMarksWhatTheMirrorLacks: the register names acts the crawl never fetched. Those
// are shown as named gaps, never as links that 404 — the difference between a listing and a
// broken promise.
func TestRelatedRailMarksWhatTheMirrorLacks(t *testing.T) {
	site, _ := newTestSite(t, true)
	body, _ := get(t, site, "/dok?ref="+url.QueryEscape(accCanonical))
	if !strings.Contains(body, "Testbekendtgørelse om intet") {
		t.Fatalf("the amending act the mirror holds is missing from the rail:\n%s", body)
	}
	if !strings.Contains(body, accUnheld) && !strings.Contains(body, "Lov vi aldrig hentede") {
		t.Fatalf("the amending act the mirror lacks is missing entirely:\n%s", body)
	}
	if !strings.Contains(body, "ikke i dette spejl") {
		t.Errorf("the missing act is not marked as missing:\n%s", body)
	}
	for _, href := range hrefs(body) {
		if strings.Contains(href, accUnheld) {
			t.Errorf("the rail links to a document this mirror does not hold: %s", href)
		}
	}
}

// An address that names something real must open it, and one that names nothing must say so with
// the list of what does exist — the same contract the lookup tool has, since the page is a
// different door on the same room.
func TestDocumentPageAnswersTheSameAsTheTool(t *testing.T) {
	site, _ := newTestSite(t, true)
	body, code := get(t, site, "/dok?ref="+url.QueryEscape(accCanonical)+"&addr="+url.QueryEscape("§ 1, stk. 1"))
	if code != http.StatusOK {
		t.Fatalf("provision page status = %d", code)
	}
	if !strings.Contains(body, "KANONISK TEKST") {
		t.Errorf("provision page did not show the provision:\n%s", body)
	}
	_, code = get(t, site, "/dok?ref="+url.QueryEscape(accCanonical)+"&addr="+url.QueryEscape("§ 999"))
	if code != http.StatusNotFound {
		t.Errorf("a nonexistent address answered %d, want 404", code)
	}
}

// A server without an index is a real deployment, not a broken one: it can read and browse, and
// it has to say plainly that it cannot search rather than answer with an empty page.
func TestSiteWithoutAnIndexStillBrowses(t *testing.T) {
	site, _ := newTestSite(t, false)
	body, code := get(t, site, "/")
	if code != http.StatusOK {
		t.Fatalf("front page status = %d", code)
	}
	if !strings.Contains(body, "Søgeindeks") {
		t.Errorf("front page hides the fact that search is unavailable:\n%s", body)
	}
	body, code = get(t, site, "/search?q=kanonisk")
	if code != http.StatusOK {
		t.Fatalf("search page status = %d", code)
	}
	if !strings.Contains(body, "artifact build") {
		t.Errorf("search page does not say how to get search:\n%s", body)
	}
	body, code = get(t, site, "/browse")
	if code != http.StatusOK {
		t.Fatalf("browse status = %d", code)
	}
	if !strings.Contains(body, accCanonical) && !strings.Contains(body, "Testlov") {
		t.Errorf("browse listing is empty without an index:\n%s", body)
	}
}

func TestUnknownPathIsNotFound(t *testing.T) {
	site, _ := newTestSite(t, true)
	if _, code := get(t, site, "/dokument"); code != http.StatusNotFound {
		t.Errorf("unknown path answered %d, want 404", code)
	}
	if _, code := get(t, site, "/dok"); code != http.StatusBadRequest {
		t.Errorf("a document page with no ref answered %d, want 400", code)
	}
}
