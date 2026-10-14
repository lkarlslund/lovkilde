package webserver

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/lkarlslund/retsinformation/internal/apikey"
)

// While a key is set, nothing but the key page answers without it. A program sends it as
// "Authorization: Bearer <key>"; a browser gives it once on the key page and keeps a cookie that
// holds the key's hash, not the key.
const (
	accessPath   = "/adgang"
	accessCookie = "retsin_adgang"
)

func bearer(r *http.Request) (string, bool) {
	scheme, key, ok := strings.Cut(r.Header.Get("Authorization"), " ")
	return strings.TrimSpace(key), ok && strings.EqualFold(scheme, "Bearer")
}

// authorized says whether r may be answered: no key is set, or r carries it.
func (h *Handler) authorized(r *http.Request) bool {
	if !h.Key.Enabled() {
		return true
	}
	if key, ok := bearer(r); ok {
		return h.Key.Match(key)
	}
	c, err := r.Cookie(accessCookie)
	return err == nil && h.Key.MatchToken(c.Value)
}

// requireKey answers a request without the key: a page is sent to the key page, and a program —
// the MCP endpoint, or anything that does not ask for HTML — is told to send it.
func (h *Handler) requireKey(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/mcp" || !strings.Contains(r.Header.Get("Accept"), "text/html") {
		w.Header().Set("WWW-Authenticate", `Bearer realm="retsin"`)
		http.Error(w, "Serveren kræver en API-nøgle: send den som \"Authorization: Bearer <nøgle>\".", http.StatusUnauthorized)
		return
	}
	http.Redirect(w, r, accessPath+"?"+url.Values{"next": {r.URL.RequestURI()}}.Encode(), http.StatusSeeOther)
}

// keepAccess lets this browser in from now on.
func (h *Handler) keepAccess(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name: accessCookie, Value: h.Key.Token(), Path: "/",
		Expires: time.Now().AddDate(1, 0, 0), HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: r.TLS != nil,
	})
}

// access is the key page: it takes the key and sends the browser where it was going.
func (h *Handler) access(w http.ResponseWriter, r *http.Request) {
	next := localPath(r.FormValue("next"))
	if !h.Key.Enabled() {
		http.Redirect(w, r, next, http.StatusSeeOther)
		return
	}
	wrong := false
	if r.Method == http.MethodPost {
		if h.Key.Match(r.PostFormValue("noegle")) {
			h.keepAccess(w, r)
			http.Redirect(w, r, next, http.StatusSeeOther)
			return
		}
		wrong = true
		w.WriteHeader(http.StatusUnauthorized)
	}
	h.render(w, "access", map[string]any{"PageTitle": "API-nøgle", "Next": next, "Wrong": wrong})
}

// exposedWithoutKey says whether the server listens beyond this machine with no key set: the
// warning next to the settings link.
func (h *Handler) exposedWithoutKey() bool { return h.Exposed && !h.Key.Enabled() }

// keyAction carries out a key change from the settings page, and returns a newly made key to show
// once, or the message for a key it refused.
func (h *Handler) keyAction(w http.ResponseWriter, r *http.Request, action string) (made, problem string) {
	if h.Key == nil {
		return "", "Denne server kan ikke gemme en API-nøgle."
	}
	var err error
	switch action {
	case "gem":
		err = h.Key.Set(r.PostFormValue("ny_noegle"))
	case "lav":
		made = apikey.Generate()
		err = h.Key.Set(made)
	case "fjern":
		err = h.Key.Clear()
	}
	switch {
	case errors.Is(err, apikey.ErrTooShort):
		return "", "Nøglen skal være mindst 16 tegn lang."
	case err != nil:
		return "", "Nøglen kunne ikke gemmes: " + err.Error()
	}
	if h.Key.Enabled() {
		h.keepAccess(w, r)
	}
	return made, ""
}
