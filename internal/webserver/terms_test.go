package webserver

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/lkarlslund/retsinformation/internal/disclaimer"
)

// No page answers a browser that has not accepted the terms; accepting them sends it back where
// it was going, on this site only, and from then on every page answers.
func TestPagesAnswerOnlyOnceTheTermsAreAccepted(t *testing.T) {
	site, _ := newTestSite(t, false)
	noFollow := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

	resp, err := noFollow.Get(site.URL + "/browse?kind=lta")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if loc := resp.Header.Get("Location"); resp.StatusCode != http.StatusSeeOther || loc != "/vilkaar?next=%2Fbrowse%3Fkind%3Dlta" {
		t.Fatalf("a page before the terms: %s, to %q", resp.Status, loc)
	}
	body, code := getAnon(t, site.URL+"/vilkaar?next=%2Fbrowse")
	if code != http.StatusOK || !strings.Contains(body, disclaimer.Terms[len(disclaimer.Terms)-1].Text) || !strings.Contains(body, `name="accepterer"`) {
		t.Errorf("the terms page (%d) does not show the terms and the box to tick:\n%s", code, body)
	}
	if body, code := getAnon(t, site.URL+"/status"); code != http.StatusForbidden || !strings.Contains(body, disclaimer.Terms[0].Text) {
		t.Errorf("/status before the terms: %d\n%s", code, body)
	}

	post := func(form url.Values) *http.Response {
		t.Helper()
		resp, err := noFollow.PostForm(site.URL+"/vilkaar", form)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp
	}
	if resp := post(url.Values{"next": {"/browse"}}); resp.StatusCode != http.StatusOK || len(resp.Cookies()) != 0 {
		t.Errorf("unticked: %s, cookies %v", resp.Status, resp.Cookies())
	}
	resp = post(url.Values{"next": {"//elsewhere.example/"}, "accepterer": {"ja"}})
	if loc := resp.Header.Get("Location"); loc != "/" {
		t.Errorf("accepting sent the browser to %q, want this site's front page", loc)
	}
	resp = post(url.Values{"next": {"/browse?kind=lta"}, "accepterer": {"ja"}})
	if loc := resp.Header.Get("Location"); loc != "/browse?kind=lta" || len(resp.Cookies()) != 1 || resp.Cookies()[0].Value != disclaimer.TermsVersion {
		t.Fatalf("accepting: to %q, cookies %v", loc, resp.Cookies())
	}
	if _, code := get(t, site, "/browse?kind=lta"); code != http.StatusOK {
		t.Errorf("a page after the terms: %d", code)
	}
}

func getAnon(t *testing.T, u string) (string, int) {
	t.Helper()
	resp, err := http.Get(u)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := readAll(resp)
	if err != nil {
		t.Fatal(err)
	}
	return body, resp.StatusCode
}
