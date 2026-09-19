package webserver

import (
	"net/http"
	"strings"
	"testing"
)

// The pages were served as their content block alone for a long time: no doctype, no head,
// no style sheet, no header, no footer. Every rule of that style sheet existed in this
// package, so reading the source showed a styled site and only a browser knew the truth.
// A page is therefore asserted to arrive as a page -- the layout markers present exactly
// once, wrapped around the content they were supposed to carry.
func TestEveryPageCarriesItsLayout(t *testing.T) {
	site, _ := newTestSite(t, true)
	for _, tc := range []struct{ path, content string }{
		{"/", "Søg i dansk lovgivning"},
		{"/browse", "Alle dokumenter"},
		{"/search?q=testlov", "Søgning"},
		{"/dok?ref=" + accCanonical, "Testlov"},
	} {
		body, code := get(t, site, tc.path)
		if code != 200 {
			t.Errorf("%s: status %d", tc.path, code)
			continue
		}
		for _, marker := range []string{"<!doctype html>", `<html lang="da">`, "<style>", "prefers-color-scheme", `class="wrap"`, "<footer>"} {
			if !strings.Contains(body, marker) {
				t.Errorf("%s: the served page has no %q", tc.path, marker)
			}
		}
		for _, only := range []string{"<html", "<body>", "</html>"} {
			if n := strings.Count(body, only); n != 1 {
				t.Errorf("%s: %d copies of %q", tc.path, n, only)
			}
		}
		if tc.content != "" && !strings.Contains(body, tc.content) {
			t.Errorf("%s: the content block did not arrive either", tc.path)
		}
		// One page must not be able to open a second document: the body closes and nothing
		// after it starts another.
		if i := strings.Index(body, "</html>"); i != len(body)-len("</html>") {
			t.Errorf("%s: bytes after the closing html tag", tc.path)
		}
	}
}

// /status is the one endpoint a machine reads, so it has to stay plain text rather than grow
// a layout for the sake of consistency.
func TestStatusStaysPlainText(t *testing.T) {
	site, _ := newTestSite(t, false)
	resp, err := http.Get(site.URL + "/status")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if got := resp.Header.Get("Content-Type"); !strings.HasPrefix(got, "text/plain") {
		t.Errorf("status content type is %q, and curl is a client too", got)
	}
}
