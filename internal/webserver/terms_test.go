package webserver

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/lkarlslund/lovkilde/internal/disclaimer"
	"github.com/lkarlslund/lovkilde/internal/eula"
)

// No page answers a browser that has not accepted the terms; accepting them sends it back where
// it was going, on this site only, and from then on every page answers.
func TestPagesAnswerOnlyOnceTheTermsAreAccepted(t *testing.T) {
	site, srv := newTestSite(t, false)
	dataDir := t.TempDir()
	srv.EULA = eula.Open(dataDir)
	noFollow := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

	resp, err := noFollow.Get(site.URL + "/browse?kind=lta")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if loc := resp.Header.Get("Location"); resp.StatusCode != http.StatusSeeOther || loc != "/eula?next=%2Fbrowse%3Fkind%3Dlta" {
		t.Fatalf("a page before the terms: %s, to %q", resp.Status, loc)
	}
	body, code := getAnon(t, site.URL+"/eula?next=%2Fbrowse")
	if code != http.StatusOK || !strings.Contains(body, disclaimer.Terms[len(disclaimer.Terms)-1].Text) || !strings.Contains(body, `name="accept"`) {
		t.Errorf("the terms page (%d) does not show the terms and the box to tick:\n%s", code, body)
	}
	if body, code := getAnon(t, site.URL+"/status"); code != http.StatusForbidden || !strings.Contains(body, disclaimer.Terms[0].Text) {
		t.Errorf("/status before the terms: %d\n%s", code, body)
	}

	post := func(form url.Values) *http.Response {
		t.Helper()
		resp, err := noFollow.PostForm(site.URL+"/eula", form)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp
	}
	if resp := post(url.Values{"next": {"/browse"}}); resp.StatusCode != http.StatusOK || len(resp.Cookies()) != 0 {
		t.Errorf("unticked: %s, cookies %v", resp.Status, resp.Cookies())
	}
	resp = post(url.Values{"next": {"//elsewhere.example/"}, "accept": {"yes"}})
	if loc := resp.Header.Get("Location"); loc != "/" {
		t.Errorf("accepting sent the browser to %q, want this site's front page", loc)
	}
	resp = post(url.Values{"next": {"/browse?kind=lta"}, "accept": {"yes"}})
	if loc := resp.Header.Get("Location"); loc != "/browse?kind=lta" || len(resp.Cookies()) != 1 || resp.Cookies()[0].Value != disclaimer.TermsVersion {
		t.Fatalf("accepting: to %q, cookies %v", loc, resp.Cookies())
	}
	if _, code := get(t, site, "/browse?kind=lta"); code != http.StatusOK {
		t.Errorf("a page after the terms: %d", code)
	}
	// Accepted once is accepted on the server: another browser, and the server after a restart.
	if _, code := getAnon(t, site.URL+"/status"); code != http.StatusOK {
		t.Errorf("a browser without the cookie, after the terms were accepted on the server: %d", code)
	}
	if !eula.Open(dataDir).Accepted() {
		t.Error("the acceptance was not kept in the data directory")
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

// A browser gets the status as a page carrying the whole disclaimer; curl gets plain text.
func TestStatusIsAPageForABrowserWithTheDisclaimer(t *testing.T) {
	site, _ := newTestSite(t, true)
	req, _ := http.NewRequest(http.MethodGet, site.URL+"/status", nil)
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	req.AddCookie(&http.Cookie{Name: termsCookie, Value: disclaimer.TermsVersion})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := readAll(resp)
	if !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/html") || !strings.Contains(body, "<h1>Status</h1>") ||
		!strings.Contains(body, disclaimer.ShortDanish) || !strings.Contains(body, disclaimer.Terms[5].Text) {
		t.Errorf("the status page for a browser:\n%s", body)
	}
	if body, _ := get(t, site, "/status"); strings.Contains(body, "<") {
		t.Errorf("curl got HTML:\n%s", body)
	}
}
