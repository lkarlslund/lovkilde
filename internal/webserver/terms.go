package webserver

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/lkarlslund/lovkilde/internal/disclaimer"
)

// The terms of use are accepted once per browser, in a cookie that holds the version of the terms
// accepted; a new version asks again. Nothing but the terms page and the MCP endpoint, which asks
// through its own tool, answers a browser that has not accepted them.
const (
	termsPath   = "/eula"
	termsCookie = "retsin_vilkaar"
)

func acceptedTerms(r *http.Request) bool {
	c, err := r.Cookie(termsCookie)
	return err == nil && c.Value == disclaimer.TermsVersion
}

// localPath is next if it is a path on this site, and "/" otherwise: the terms page sends the
// reader back where they were going, and nowhere else.
func localPath(next string) string {
	if !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") || strings.HasPrefix(next, "/\\") {
		return "/"
	}
	return next
}

// requireTerms answers a request that has not accepted the terms: a page is sent to the terms
// first, and the plain-text status says how to accept.
func (h *Handler) requireTerms(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/status" {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprintf(w, "%s\nSend cookien %s=%s for at bekræfte, at du har læst og accepterer vilkårene.\n",
			disclaimer.TermsText(), termsCookie, disclaimer.TermsVersion)
		return
	}
	http.Redirect(w, r, termsPath+"?"+url.Values{"next": {r.URL.RequestURI()}}.Encode(), http.StatusSeeOther)
}

func (h *Handler) terms(w http.ResponseWriter, r *http.Request) {
	next := localPath(r.FormValue("next"))
	if r.Method == http.MethodPost {
		if r.PostFormValue("accept") != "yes" {
			h.renderTerms(w, next, true, false)
			return
		}
		http.SetCookie(w, &http.Cookie{
			Name: termsCookie, Value: disclaimer.TermsVersion, Path: "/",
			Expires: time.Now().AddDate(1, 0, 0), HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: r.TLS != nil,
		})
		http.Redirect(w, r, next, http.StatusSeeOther)
		return
	}
	h.renderTerms(w, next, false, acceptedTerms(r))
}

func (h *Handler) renderTerms(w http.ResponseWriter, next string, unticked, accepted bool) {
	h.render(w, "terms", map[string]any{
		"PageTitle": "Vilkår for brug",
		"Title":     disclaimer.TermsTitle,
		"Terms":     disclaimer.Terms,
		"Accept":    disclaimer.TermsAccept,
		"Next":      next,
		"Unticked":  unticked,
		"Accepted":  accepted,
	})
}
