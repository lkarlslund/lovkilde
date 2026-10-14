package webserver

import (
	"net/http"

	"github.com/lkarlslund/retsinformation/internal/apikey"
	"github.com/lkarlslund/retsinformation/internal/checks"
)

const settingsPath = "/indstillinger"

// checkView is one scheduled check as the settings and status pages show it.
type checkView struct {
	checks.State
	Title, Explains string
	Intervals       []checks.Interval
}

// checkTexts says what each check is for, in the order the pages list them.
var checkTexts = []struct{ Name, Title, Explains string }{
	{checks.Dataset, "Nye udgaver af lovteksterne",
		"Serveren spørger, om der er udgivet en nyere udgave af datasættet. Findes der en, hentes den og tages i brug automatisk; kun de dele, der er ændret, hentes."},
	{checks.Software, "Nye versioner af programmet",
		"Serveren spørger GitHub, om der er udgivet en nyere version af retsin. Den opdaterer ikke sig selv: findes der en, står det øverst på alle sider, og det er op til den, der driver serveren, at installere den."},
}

func (h *Handler) checkViews() []checkView {
	var out []checkView
	for _, t := range checkTexts {
		if st, ok := h.Checks.State(t.Name); ok {
			out = append(out, checkView{State: st, Title: t.Title, Explains: t.Explains, Intervals: checks.Intervals})
		}
	}
	return out
}

// settings shows how often each check runs and when it last did, and takes a new interval or a
// request to check now; it also sets, makes or removes the API key.
func (h *Handler) settings(w http.ResponseWriter, r *http.Request) {
	var made, problem string
	if action := r.PostFormValue("noegle"); r.Method == http.MethodPost && action != "" {
		if made, problem = h.keyAction(w, r, action); made == "" && problem == "" {
			http.Redirect(w, r, settingsPath, http.StatusSeeOther)
			return
		}
	} else if r.Method == http.MethodPost && h.Checks != nil {
		if name := r.PostFormValue("tjek"); name != "" {
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
		running = running || v.Running
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
