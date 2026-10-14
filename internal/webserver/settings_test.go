package webserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lkarlslund/retsinformation/internal/checks"
	"github.com/lkarlslund/retsinformation/internal/disclaimer"
	"github.com/lkarlslund/retsinformation/internal/softwareupdate"
)

// The settings page sets how often each check runs and runs one at once; while the software
// check has found a newer build, every page says so.
func TestSettingsSetIntervalsAndCheckNow(t *testing.T) {
	srv := newTestServer(t, false)
	chk := checks.Open(t.TempDir())
	var ran atomic.Int32
	chk.Add(checks.Dataset, checks.Off, time.Now(), func(context.Context) checks.Outcome {
		ran.Add(1)
		return checks.Outcome{Summary: "Den udgivne udgave er den, der bruges."}
	})
	chk.Add(checks.Software, checks.Daily, time.Time{}, func(context.Context) checks.Outcome { return checks.Outcome{Newer: 4} })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go chk.Run(ctx)
	h := New(srv)
	h.Checks, h.Software = chk, &softwareupdate.Checker{Repo: "o/n", Tag: "nightly"}
	site := httptest.NewServer(h)
	defer site.Close()

	post := func(form url.Values) {
		t.Helper()
		req, _ := http.NewRequest(http.MethodPost, site.URL+"/indstillinger", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.AddCookie(&http.Cookie{Name: termsCookie, Value: disclaimer.TermsVersion})
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}
	post(url.Values{checks.Dataset: {string(checks.Monthly)}, checks.Software: {string(checks.Weekly)}})
	if d, _ := chk.State(checks.Dataset); d.Every != checks.Monthly {
		t.Errorf("dataset interval %s, want monthly", d.Every)
	}
	if s, _ := chk.State(checks.Software); s.Every != checks.Weekly {
		t.Errorf("software interval %s, want weekly", s.Every)
	}
	post(url.Values{"tjek": {checks.Dataset}})
	for i := 0; ran.Load() == 0 && i < 200; i++ {
		time.Sleep(5 * time.Millisecond)
	}
	if ran.Load() != 1 {
		t.Fatalf("check now ran the dataset check %d times", ran.Load())
	}
	for i := 0; i < 200; i++ {
		if s, _ := chk.State(checks.Software); s.Last.Newer == 4 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	body, code := get(t, site, "/indstillinger")
	if code != http.StatusOK || !strings.Contains(body, `value="maanedligt" checked`) || !strings.Contains(body, "Tjek nu") {
		t.Errorf("the settings page (%d):\n%s", code, body)
	}
	if !strings.Contains(body, "4 ændringer nyere") || !strings.Contains(body, "https://github.com/o/n/releases/tag/nightly") {
		t.Errorf("the page does not say a newer build is out:\n%s", body)
	}
}
