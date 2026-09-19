package webserver

import (
	"net/url"
	"strings"
	"testing"
)

// TestNotesAreInThePagesLanguage guards the regression this page nearly shipped with: the server
// filled its Language field only on the as-of path, so the ordinary document page fell back to
// English prose — and the page that used to recognise and discard that sentence had just been
// deleted, so the English text landed on a Danish reading page with nothing left to catch it.
//
// The rule the page rests on is that the server composes its own prose in the language asked for,
// rather than the page recognising sentences it does not want by phrase matching.
func TestNotesAreInThePagesLanguage(t *testing.T) {
	site, _ := newTestSite(t, false)

	// The English sentences this page must never print, whichever path built the answer.
	never := []string{
		"document(s) named in this document's own metadata",
		"could not be placed",
		"not in the mirror",
		"The text above is this accession as published",
	}

	for _, want := range []struct {
		path string
		note string
	}{
		// The plain page says the text is as published and points at as_of; the as-of page has
		// nothing to point at and says instead what it is: a consolidation, and of how many acts.
		{"/dok?ref=" + url.QueryEscape(accCanonical), "Ifølge dokumentets egne metadata"},
		{"/dok?ref=" + url.QueryEscape(accCanonical) + "&as_of=2030-01-01", "Teksten er konsolideret"},
	} {
		body, code := get(t, site, want.path)
		if code != 200 {
			t.Fatalf("GET %s = %d", want.path, code)
		}
		for _, english := range never {
			if strings.Contains(body, english) {
				t.Errorf("%s prints English prose on a Danish page: %q", want.path, english)
			}
		}
		if !strings.Contains(body, want.note) {
			t.Errorf("%s has no %q note", want.path, want.note)
		}
	}
}
