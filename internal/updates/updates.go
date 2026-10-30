// Package updates is what a server knows and does about newer versions of itself and of its law
// texts. Two scheduled checks ask whether one is published and record the answer; nothing is
// downloaded until someone asks for it — on the settings page, through the MCP tool update, or with
// "lovkilde program download" and "lovkilde artifact download".
package updates

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/lkarlslund/lovkilde/internal/artifactdir"
	"github.com/lkarlslund/lovkilde/internal/checks"
	"github.com/lkarlslund/lovkilde/internal/compiled"
	"github.com/lkarlslund/lovkilde/internal/release"
	"github.com/lkarlslund/lovkilde/internal/softwareupdate"
)

// What can be updated.
const (
	App     = "app"     // the program
	Dataset = "dataset" // the law texts
)

// Config is what a server has to update with.
type Config struct {
	DataDir string
	Checks  *checks.Checks
	// Repo is where the law texts are published; nil for a server that serves one artifact and
	// follows none.
	Repo release.Repo
	// Software asks about published builds; nil for a build that does not know its commit.
	Software *softwareupdate.Checker
	// Restarts says the server restarts as a newer build once it is downloaded; otherwise the
	// build runs from the next start.
	Restarts bool
	// Every is how often each check runs unless the settings page says otherwise.
	Every checks.Interval
}

// Updates is a server's checks and the updates started from them.
type Updates struct {
	cfg Config

	mu      sync.Mutex
	running map[string]bool
	result  map[string]string // how the last update of each finished, as a reader is told
}

// New registers the checks a server can run with cfg.Checks: the law texts when it follows a
// release, the program when it knows its own build.
func New(cfg Config) *Updates {
	u := &Updates{cfg: cfg, running: map[string]bool{}, result: map[string]string{}}
	if cfg.Repo != nil {
		cfg.Checks.Add(checks.Dataset, cfg.Every, release.LastChecked(cfg.DataDir), u.checkDataset)
	}
	if cfg.Software != nil {
		cfg.Checks.Add(checks.Software, cfg.Every, time.Time{}, u.checkSoftware)
	}
	return u
}

// Checks is the server's checks, for the settings page.
func (u *Updates) Checks() *checks.Checks { return u.cfg.Checks }

// Run runs the checks when they are due, until ctx ends.
func (u *Updates) Run(ctx context.Context) { u.cfg.Checks.Run(ctx) }

// SoftwareURL is the page the published build is downloaded from, "" when this build cannot be
// compared with it.
func (u *Updates) SoftwareURL() string {
	if u.cfg.Software == nil {
		return ""
	}
	return u.cfg.Software.URL()
}

// Can says whether this server can update what.
func (u *Updates) Can(what string) bool {
	switch what {
	case App:
		return u.cfg.Software != nil
	case Dataset:
		return u.cfg.Repo != nil
	}
	return false
}

// checkName is the check that asks about what.
func checkName(what string) string {
	if what == App {
		return checks.Software
	}
	return checks.Dataset
}

// State is what is known about updating what: the last check, whether an update is being
// downloaded, and how the last one ended.
type State struct {
	Check   checks.State
	Running bool
	Result  string
}

func (u *Updates) State(what string) State {
	st, _ := u.cfg.Checks.State(checkName(what))
	u.mu.Lock()
	defer u.mu.Unlock()
	return State{Check: st, Running: u.running[what], Result: u.result[what]}
}

const needsUpgrade = "Der er udgivet nye lovtekster i et nyere format, som kræver en nyere version af programmet. Serveren bruger fortsat sine nuværende tekster, indtil programmet er opdateret."

func (u *Updates) checkDataset(ctx context.Context) checks.Outcome {
	av, err := release.CheckArtifact(ctx, u.cfg.Repo, u.cfg.DataDir)
	_ = release.MarkChecked(u.cfg.DataDir)
	// Data in a newer document model needs a newer program first; the last artifact of this
	// program's model is still offered when it is not the one in use.
	switch {
	case errors.Is(err, release.ErrProgramTooOld):
		slog.Warn("the published artifact needs a newer version of lovkilde", "model", av.Newer, "reads", compiled.ModelVersion)
		return checks.Outcome{Summary: needsUpgrade, Upgrade: true}
	case errors.Is(err, release.ErrDataTooOld):
		return checks.Outcome{Summary: "Denne version af programmet læser et nyere format af lovteksterne, end der er udgivet endnu. Serveren bruger fortsat sine nuværende tekster, indtil udgivelsen kommer."}
	case err != nil:
		slog.Warn("checking for a newer published artifact failed", "release", u.cfg.Repo.String(), "err", err)
		return checks.Outcome{Err: err.Error()}
	}
	out := checks.Outcome{Upgrade: av.Newer > 0}
	if av.Current != "" {
		out.Summary = "Den udgivne udgave er den, der bruges."
		if out.Upgrade {
			out.Summary = needsUpgrade
		}
		return out
	}
	out.Available, out.Version = true, danishDate(av.Manifest.Published)
	out.Summary = "Der er udgivet en nyere udgave af lovteksterne"
	if out.Version != "" {
		out.Summary += " (" + out.Version + ")"
	}
	out.Summary += ". Den hentes, når du beder om det."
	return out
}

func (u *Updates) checkSoftware(ctx context.Context) checks.Outcome {
	rel, err := u.cfg.Software.Latest(ctx)
	switch {
	case err != nil:
		return checks.Outcome{Err: err.Error()}
	case rel.Behind == 0:
		return checks.Outcome{Summary: "Denne version er den nyeste udgivne."}
	}
	n := rel.Behind
	out := checks.Outcome{Available: true, Newer: n, Version: rel.Tag, Downloaded: u.downloaded()}
	out.Summary = fmt.Sprintf("Version %s er udgivet, %d %s nyere end denne.", rel.Tag, n, map[bool]string{true: "ændring", false: "ændringer"}[n == 1])
	switch {
	case out.Downloaded && u.cfg.Restarts:
		out.Summary += " Den er hentet, og serveren genstarter med den om et øjeblik."
	case out.Downloaded:
		out.Summary += " Den er hentet og bruges, næste gang Lovkilde startes."
	default:
		out.Summary += " Den hentes, når du beder om det."
	}
	return out
}

// downloaded says a build newer than this one is kept in the data directory.
func (u *Updates) downloaded() bool {
	current, ok := compiled.CurrentBuild()
	if !ok {
		return false
	}
	_, stamp, ok := softwareupdate.Newest(softwareupdate.Dir(u.cfg.DataDir))
	return ok && stamp.Time.After(current.Time)
}

// Check runs both checks now and says what they found, a line each.
func (u *Updates) Check(ctx context.Context) string {
	var lines []string
	for _, what := range []string{App, Dataset} {
		if !u.Can(what) {
			continue
		}
		out, _ := u.cfg.Checks.Do(ctx, checkName(what))
		line := label(what) + ": "
		if out.Err != "" {
			line += "tjekket fejlede: " + out.Err
		} else {
			line += out.Summary
		}
		if st := u.State(what); st.Running {
			line += " Den hentes nu."
		}
		lines = append(lines, line)
	}
	if len(lines) == 0 {
		return "Denne server kan ikke tjekke for opdateringer: den bruger en fast udgave af lovteksterne, og programmet ved ikke, hvilken kode det er bygget fra."
	}
	return strings.Join(lines, "\n")
}

func label(what string) string {
	if what == App {
		return "Programmet"
	}
	return "Lovteksterne"
}

// Start downloads and puts to use a newer version of what, if one is published. It asks first,
// and answers at once when there is nothing newer; the download itself runs in the background,
// because it can take minutes, and State and Note tell how it goes.
func (u *Updates) Start(ctx context.Context, what string) (string, error) {
	switch {
	case what != App && what != Dataset:
		return "", fmt.Errorf("unknown update %q: use %q or %q", what, App, Dataset)
	case what == App && !u.Can(App):
		return "", errors.New("denne version af programmet ved ikke, hvilken kode den er bygget fra, og kan ikke sammenlignes med de udgivne")
	case what == Dataset && !u.Can(Dataset):
		return "", errors.New("serveren bruger en fast udgave af lovteksterne (--artifact) og følger ingen udgivelse")
	}
	u.mu.Lock()
	if u.running[what] {
		u.mu.Unlock()
		return label(what) + ": opdateringen er allerede i gang.", nil
	}
	u.running[what] = true
	u.mu.Unlock()

	out, _ := u.cfg.Checks.Do(ctx, checkName(what))
	if out.Err != "" || !out.Available || out.Downloaded {
		u.finish(what, "")
		if out.Err != "" {
			return "", errors.New("tjekket fejlede: " + out.Err)
		}
		return label(what) + ": " + out.Summary, nil
	}
	ctx = context.WithoutCancel(ctx)
	go func() {
		if what == App {
			u.finish(what, u.app(ctx, out.Version))
		} else {
			u.finish(what, u.dataset(ctx))
		}
	}()
	if what == App {
		return fmt.Sprintf("Version %s af programmet hentes nu.", out.Version) + u.appAfter(), nil
	}
	return "Den nye udgave af lovteksterne hentes nu; det tager typisk et par minutter. Serveren tager den i brug, når den er hentet, og svarer imens med de nuværende tekster.", nil
}

func (u *Updates) finish(what, result string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.running[what] = false
	if result != "" {
		u.result[what] = result
	}
}

func (u *Updates) appAfter() string {
	if u.cfg.Restarts {
		return " Når den er hentet, genstarter serveren med den; siderne og værktøjerne er væk i de sekunder, det tager."
	}
	return " Den bruges, næste gang Lovkilde startes."
}

func (u *Updates) app(ctx context.Context, tag string) string {
	dir := softwareupdate.Dir(u.cfg.DataDir)
	path, _, err := u.cfg.Software.Download(ctx, dir, tag)
	if err != nil {
		slog.Warn("downloading the newer program failed", "version", tag, "err", err)
		return fmt.Sprintf("Version %s kunne ikke hentes: %v", tag, err)
	}
	softwareupdate.Prune(dir, path)
	slog.Info("downloaded a newer lovkilde", "version", tag, "path", path)
	u.cfg.Checks.Do(ctx, checks.Software)
	return fmt.Sprintf("Version %s er hentet.", tag) + u.appAfter()
}

func (u *Updates) dataset(ctx context.Context) string {
	res, err := release.FetchArtifact(ctx, u.cfg.Repo, u.cfg.DataDir)
	_ = release.MarkChecked(u.cfg.DataDir)
	if err != nil {
		slog.Warn("downloading the newer artifact failed", "release", u.cfg.Repo.String(), "err", err)
		return "De nye lovtekster kunne ikke hentes: " + err.Error()
	}
	LogTransfer(res.Transfer)
	slog.Info("installed a newer published artifact", "path", res.Path, "downloaded", res.Downloaded, "reused", res.Reused)
	u.cfg.Checks.Do(ctx, checks.Dataset)
	if !res.Installed {
		return "Den udgivne udgave af lovteksterne er allerede den, der bruges."
	}
	return fmt.Sprintf("Den nye udgave af lovteksterne, %s, er hentet (%s hentet, %s genbrugt fra disken). Serveren tager den i brug inden for et minut.",
		strings.TrimSuffix(filepath.Base(res.Path), artifactdir.Ext), ByteSize(res.Downloaded), ByteSize(res.Reused))
}

// Note is a line for the end of an MCP answer while an update is published or under way, for the
// assistant to pass on; "" when there is none. It tells the assistant that the user starts an
// update, never the assistant.
func (u *Updates) Note() string {
	var have, going []string
	startable := false
	if u.Can(App) {
		st := u.State(App)
		switch {
		case st.Running:
			going = append(going, "a newer version of the lovkilde program is being downloaded")
		case st.Check.Last.Available && st.Check.Last.Downloaded && u.cfg.Restarts:
			going = append(going, "version "+st.Check.Last.Version+" of the lovkilde program is downloaded, and the server restarts as it shortly")
		case st.Check.Last.Available && st.Check.Last.Downloaded:
			going = append(going, "version "+st.Check.Last.Version+" of the lovkilde program is downloaded and runs the next time lovkilde is started")
		case st.Check.Last.Available:
			have = append(have, fmt.Sprintf("a newer version of the lovkilde program, %s (%d changes newer)", st.Check.Last.Version, st.Check.Last.Newer))
			startable = true
		}
	}
	if u.Can(Dataset) {
		st := u.State(Dataset)
		switch {
		case st.Running:
			going = append(going, "a newer edition of the law texts is being downloaded; answers use the current texts until it is in use")
		case st.Check.Last.Available:
			have = append(have, "a newer edition of the law texts, published "+st.Check.Last.Version)
			startable = true
		}
		if st.Check.Last.Upgrade {
			have = append(have, "law texts in a newer format that only a newer version of the program can read; until the program is updated this server gets no new texts")
			startable = startable || u.Can(App)
		}
	}
	var parts []string
	if len(have) > 0 {
		parts = append(parts, "Updates are available: "+strings.Join(have, "; ")+".")
	}
	if len(going) > 0 {
		parts = append(parts, "In progress: "+strings.Join(going, "; ")+".")
	}
	if len(parts) == 0 {
		return ""
	}
	note := "Note for the assistant: " + strings.Join(parts, " ")
	if startable {
		note += " Tell the user. Start an update only when the user asks for it, with the tool update (action \"app\" for the program, \"dataset\" for the law texts); never on your own."
	}
	return note
}

// LogTransfer warns when Xet storage could not be used and an artifact was downloaded whole.
func LogTransfer(t release.Transfer) {
	if t.XetErr != nil {
		slog.Warn("downloaded the artifact whole: Xet storage failed", "err", t.XetErr)
	}
}

// ByteSize writes a byte count the Danish way, in the largest unit under 1,000.
func ByteSize(n int64) string {
	const units = "kMGT"
	f, u := float64(n), -1
	for f >= 1000 && u < len(units)-1 {
		f /= 1000
		u++
	}
	if u < 0 {
		return fmt.Sprintf("%d B", n)
	}
	return strings.Replace(fmt.Sprintf("%.1f %cB", f, units[u]), ".", ",", 1)
}

var danishMonths = []string{"januar", "februar", "marts", "april", "maj", "juni", "juli", "august", "september", "oktober", "november", "december"}

// danishDate is a date as a Danish reader writes it, "30. oktober 2026"; "" for none.
func danishDate(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	t = t.Local()
	return fmt.Sprintf("%d. %s %d", t.Day(), danishMonths[t.Month()-1], t.Year())
}
