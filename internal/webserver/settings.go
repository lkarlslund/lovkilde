package webserver

import (
	"net/http"

	"github.com/lkarlslund/lovkilde/internal/apikey"
	"github.com/lkarlslund/lovkilde/internal/checks"
	"github.com/lkarlslund/lovkilde/internal/updates"
)

const settingsPath = "/settings"

// checkView is one scheduled check as the settings and status pages show it, with the update it
// offers.
type checkView struct {
	checks.State
	Title, Explains string
	Intervals       []checks.Interval
	Update, Button  string // what the button starts, and what it says
	Offer           bool   // a newer version is published and can be fetched
	Fetching        bool   // it is being downloaded
	Result          string // how the last update ended
}

// checkTexts says what each check is for, in the order the pages list them.
var checkTexts = []struct{ Name, Update, Title, Explains, Button string }{
	{checks.Dataset, updates.Dataset, "Nye udgaver af lovteksterne",
		"Serveren spørger, om der er udgivet en nyere udgave af lovteksterne. Den hentes først, når du beder om det; kun de dele, der er ændret, hentes, og serveren tager den i brug af sig selv.",
		"Hent de nye lovtekster"},
	{checks.Software, updates.App, "Nye versioner af programmet",
		"Serveren spørger GitHub, om der er udgivet en nyere version af lovkilde. Den hentes først, når du beder om det, og serveren genstarter så med den.",
		"Hent og brug den nye version"},
}

func (h *Handler) checkViews() []checkView {
	var out []checkView
	for _, t := range checkTexts {
		st, ok := h.Checks.State(t.Name)
		if !ok {
			continue
		}
		v := checkView{State: st, Title: t.Title, Explains: t.Explains, Intervals: checks.Intervals, Update: t.Update, Button: t.Button}
		if h.Updates != nil {
			u := h.Updates.State(t.Update)
			v.Fetching, v.Result = u.Running, u.Result
			v.Offer = st.Last.Available && !st.Last.Downloaded && !u.Running
		}
		out = append(out, v)
	}
	return out
}

// settings shows how often each check runs and when it last did, and takes a new interval or a
// request to check now; it also sets, makes or removes the API key.
func (h *Handler) settings(w http.ResponseWriter, r *http.Request) {
	var made, problem string
	if action := r.PostFormValue("key"); r.Method == http.MethodPost && action != "" {
		if made, problem = h.keyAction(w, r, action); made == "" && problem == "" {
			http.Redirect(w, r, settingsPath, http.StatusSeeOther)
			return
		}
	} else if r.Method == http.MethodPost && h.Checks != nil {
		if what := r.PostFormValue("update"); what != "" && h.Updates != nil {
			if _, err := h.Updates.Start(r.Context(), what); err != nil {
				http.Error(w, "Opdateringen kunne ikke startes: "+err.Error(), http.StatusBadRequest)
				return
			}
		} else if name := r.PostFormValue("check"); name != "" {
			h.Checks.Now(name)
		} else {
			for _, t := range checkTexts {
				if v := checks.Interval(r.PostFormValue(t.Name)); v != "" && h.Checks.Has(t.Name) {
					if err := h.Checks.Set(t.Name, v); err != nil {
						http.Error(w, "Ugyldigt valg: "+err.Error(), http.StatusBadRequest)
						return
					}
				}
			}
		}
		http.Redirect(w, r, settingsPath, http.StatusSeeOther)
		return
	}
	views := h.checkViews()
	running := false
	for _, v := range views {
		running = running || v.Running || v.Fetching
	}
	h.render(w, "settings", map[string]any{
		"PageTitle": "Indstillinger",
		"Checks":    views,
		"Running":   running,
		"Software":  h.Checks.Has(checks.Software),
		"Exposed":   h.Exposed,
		"KeySet":    h.Key.Enabled(),
		"CanKey":    h.Key != nil,
		"MinKey":    apikey.MinLength,
		"Made":      made,
		"Problem":   problem,
	})
}
