package webserver

import (
	"context"
	"fmt"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lkarlslund/lovkilde/internal/search"
)

// TestSearchCountsEverythingAndLoadsTheRestOnRequest: the page states how many provisions match
// in all, shows the first screenful, and links the next one; the next one arrives as a bare
// fragment numbered where the first stopped. Two runs of a document's unaddressed text are one
// place to send a reader, so they are one hit and count once.
func TestSearchCountsEverythingAndLoadsTheRestOnRequest(t *testing.T) {
	srv := newTestServer(t, false)
	var units []search.Unit
	for i := 1; i <= 25; i++ {
		units = append(units, search.Unit{Doc: 0, Addr: fmt.Sprintf("§ %d", i), Text: "kanonisk"})
	}
	units = append(units, search.Unit{Doc: 0, Text: "kanonisk løs tekst"}, search.Unit{Doc: 0, Text: "mere kanonisk løs tekst"})
	idx, err := search.Build(context.Background(),
		[]search.DocMeta{{Accession: accCanonical, ELI: "eli/lta/1998/2", Path: filepath.Join(t.TempDir(), "x.xml"), Kind: "lta", Year: 1998, Number: 2, Title: "Testlov", Source: "xml"}},
		units, search.Options{})
	if err != nil {
		t.Fatal(err)
	}
	srv.UseIndex(idx)
	site := httptest.NewServer(New(srv))
	t.Cleanup(site.Close)

	body, _ := get(t, site, "/search?q=kanonisk&kind=lta&kind=ft")
	if !strings.Contains(body, `class="count">26 bestemmelser`) {
		t.Errorf("the page does not state the total of 26 distinct places:\n%s", body)
	}
	if n := strings.Count(body, `<article class="hit">`); n != searchPageSize {
		t.Errorf("first screen shows %d hits, want %d", n, searchPageSize)
	}
	if strings.Contains(body, "<select") {
		t.Error("the search form still has a dropdown")
	}
	if !strings.Contains(body, `type="checkbox" name="kind" value="lta" checked`) {
		t.Errorf("the lta toggle is not shown as on:\n%s", body)
	}
	if !strings.Contains(body, `name="source" value="xml"`) || !strings.Contains(body, "kan citeres, som den står") {
		t.Errorf("the source toggles or their explanation are missing:\n%s", body)
	}
	if !strings.Contains(body, `data-more`) || !strings.Contains(body, "offset=20") {
		t.Fatalf("no link to the next screenful:\n%s", body)
	}

	more, _ := get(t, site, "/search?q=kanonisk&kind=lta&kind=ft&offset=20&del=1")
	if strings.Contains(more, "<!doctype") || strings.Contains(more, "<form") {
		t.Errorf("the next screenful came with the page around it:\n%s", more)
	}
	if n := strings.Count(more, `<article class="hit">`); n != 6 {
		t.Errorf("the next screenful has %d hits, want the remaining 6", n)
	}
	if !strings.Contains(more, `class="n">21.`) || strings.Contains(more, "data-more") {
		t.Errorf("the next screenful should start at 21 and be the last:\n%s", more)
	}
	if strings.Count(more+body, "hele dokumentet") != 1 {
		t.Errorf("the document's loose text is listed more than once")
	}
}

func TestNumbersAreWrittenTheDanishWay(t *testing.T) {
	for n, want := range map[int]string{0: "0", 999: "999", 1000: "1.000", 191589: "191.589", 1234567: "1.234.567"} {
		if got := num(n); got != want {
			t.Errorf("num(%d) = %q, want %q", n, got, want)
		}
	}
}

// TestACitationLeadsToItsDocument: a document's own number is rarely in its text, so "lov nr.
// 2 af 1998" is answered from the register's metadata, above the ranked provisions.
func TestACitationLeadsToItsDocument(t *testing.T) {
	site, _ := newTestSite(t, true)
	body, _ := get(t, site, "/search?q="+url.QueryEscape("lov nr. 2 af 1998"))
	if !strings.Contains(body, `class="cited"`) || !strings.Contains(body, "Testlov med indlejret markup") {
		t.Fatalf("the cited document is not offered:\n%s", body)
	}
	if !strings.Contains(body, "ref="+accCanonical) {
		t.Errorf("the cited document is not linked by its accession")
	}
}
