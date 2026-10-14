package webserver

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/lkarlslund/lovkilde/internal/apikey"
	"github.com/lkarlslund/lovkilde/internal/disclaimer"
)

// A server reachable from the network warns next to the settings link until a key is set; once
// one is, a page asks a browser for it, a program must send it, and the browser that set it keeps
// its way in.
func TestAnExposedServerWarnsAndAKeyLocksIt(t *testing.T) {
	h := New(newTestServer(t, false))
	h.Key, h.Exposed = apikey.Open(t.TempDir()), true
	site := httptest.NewServer(h)
	defer site.Close()
	if body, _ := get(t, site, "/"); !strings.Contains(body, `class="warnsign"`) {
		t.Fatal("no warning on an exposed server without a key")
	}

	noFollow := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	terms := &http.Cookie{Name: termsCookie, Value: disclaimer.TermsVersion}
	do := func(method, path string, form url.Values, prep func(*http.Request)) *http.Response {
		t.Helper()
		req, _ := http.NewRequest(method, site.URL+path, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.AddCookie(terms)
		if prep != nil {
			prep(req)
		}
		resp, err := noFollow.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp
	}
	key := apikey.Generate()
	resp := do(http.MethodPost, "/settings", url.Values{"key": {"set"}, "new_key": {key}}, nil)
	if resp.StatusCode != http.StatusSeeOther || len(resp.Cookies()) != 1 || !h.Key.Match(key) {
		t.Fatalf("setting the key: %s, cookies %v", resp.Status, resp.Cookies())
	}
	setter := resp.Cookies()[0]
	if _, code := get(t, site, "/"); code != http.StatusUnauthorized {
		t.Errorf("a request without the key: %d", code)
	}

	browser := func(r *http.Request) { r.Header.Set("Accept", "text/html") }
	if loc := do(http.MethodGet, "/browse", nil, browser).Header.Get("Location"); loc != "/access?next=%2Fbrowse" {
		t.Errorf("a browser without the key went to %q", loc)
	}
	if code := do(http.MethodPost, "/mcp", nil, nil).StatusCode; code != http.StatusUnauthorized {
		t.Errorf("/mcp without the key: %d", code)
	}
	if code := do(http.MethodPost, "/access", url.Values{"key": {"forkert"}}, nil).StatusCode; code != http.StatusUnauthorized {
		t.Errorf("a wrong key: %d", code)
	}
	resp = do(http.MethodPost, "/access", url.Values{"key": {key}, "next": {"/browse"}}, nil)
	if resp.Header.Get("Location") != "/browse" || len(resp.Cookies()) != 1 {
		t.Errorf("the right key: %s to %q", resp.Status, resp.Header.Get("Location"))
	}
	for name, prep := range map[string]func(*http.Request){
		"bearer": func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+key) },
		"cookie": func(r *http.Request) { r.AddCookie(setter) },
	} {
		if code := do(http.MethodGet, "/status", nil, prep).StatusCode; code != http.StatusOK {
			t.Errorf("with the key by %s: %d", name, code)
		}
	}
	if code := do(http.MethodGet, "/status", nil, func(r *http.Request) { r.Header.Set("Authorization", "Bearer forkert") }).StatusCode; code != http.StatusUnauthorized {
		t.Errorf("a wrong bearer key: %d", code)
	}

	do(http.MethodPost, "/settings", url.Values{"key": {"remove"}}, func(r *http.Request) { r.AddCookie(setter) })
	if h.Key.Enabled() {
		t.Error("removing the key left it set")
	}
}
