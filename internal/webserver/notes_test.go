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

// The page is Danish throughout, the engine's reasons for leaving a change out included: each
// class is named in Danish with its place, and a reason without a class still gets Danish.
func TestRefusalReasonsAreDanish(t *testing.T) {
	for reason, want := range map[string]string{
		"quoted passage not present in § 5, stk. 1":                    "den citerede tekst står ikke i § 5, stk. 1",
		`address "§ 7, stk. 15" not found in the current text`:         "§ 7, stk. 15 findes ikke i teksten",
		"a reason nobody has written a class for yet":                  "ændringen kunne ikke placeres automatisk i teksten",
		`bilag 1 does not print "3.6" at the head of exactly one line`: "bilag 1 viser ikke det punkt, ændringen placeres ved",
	} {
		if got := refusalReasonDA(reason); got != want {
			t.Errorf("%q: got %q, want %q", reason, got, want)
		}
	}
}

// A provision a refused change names carries a note at its head linking to the change.
func TestARefusedChangeIsMarkedAtItsProvision(t *testing.T) {
	body := `<section id="p-5"><p class="stk">§ 5. Tekst.</p></section>`
	got := markRefusedHTML(body, map[string][]string{"p-5": {"ikke-indarbejdet-1"}})
	if !strings.Contains(got, `<section id="p-5"><p class="refused-mark">En ændring af denne bestemmelse er ikke indarbejdet (se <a href="#ikke-indarbejdet-1">nr. 1</a>`) {
		t.Errorf("provision not marked:\n%s", got)
	}
}
