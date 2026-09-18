package compiled

import (
	"fmt"
	"runtime/debug"
	"time"
)

// The build stamp is what lets a reader tell a stale artifact from a corrupt one.
//
// The artifact's payload is the JSON form of this repository's Go types, so an artifact written by
// another revision is not an older-but-valid file: it is yesterday's struct decoded into today's,
// which answers questions about a law with fields that no longer mean what they meant. The layout
// pin alone cannot catch that, because the layout is unchanged while the meaning moved.
//
// What the Go toolchain stamps into a binary built from a checkout — the commit, and whether the
// tree was dirty — is therefore recorded in the artifact's header when it is written, and compared
// when one is opened for serving. A dirty tree counts as a different build than a clean one at the
// same commit: that is exactly the state in which a type changed and was not committed.

// BuildStamp is a binary's own source identity, as the Go toolchain recorded it at link time.
type BuildStamp struct {
	// Revision is the commit the binary was built from, "" when the binary was not built from a
	// checkout the toolchain could read.
	Revision string

	// Modified says the tree carried uncommitted changes at build time.
	Modified bool

	// Time is the stamped commit's date, which is what makes "which side is stale" answerable
	// rather than merely "they disagree".
	Time time.Time
}

// String is the stamp as a build is named in an error: a short revision, dirty or not.
func (s BuildStamp) String() string {
	if s.Revision == "" {
		return "unknown revision"
	}
	rev := s.Revision
	if len(rev) > 12 {
		rev = rev[:12]
	}
	if s.Modified {
		rev += "-dirty"
	}
	return rev
}

// CurrentBuild reports the running binary's source stamp, or false when it carries none.
//
// It is a variable because a test binary is never stamped — the toolchain records vcs metadata in
// main packages only — so the alternative to a seam is that nothing in this repository can exercise
// what a server does when an artifact disagrees with its own build. Production reads the stamp the
// linker left; a test stands in for a build that has one.
var CurrentBuild = readBuildInfo

// readBuildInfo reads the stamp of the running binary, or reports false when it carries none —
// which happens for binaries assembled outside a checkout, and for every test binary.
func readBuildInfo() (BuildStamp, bool) {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return BuildStamp{}, false
	}
	var out BuildStamp
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			out.Revision = s.Value
		case "vcs.modified":
			out.Modified = s.Value == "true"
		case "vcs.time":
			t, err := time.Parse(time.RFC3339, s.Value)
			if err == nil {
				out.Time = t
			}
		}
	}
	return out, out.Revision != ""
}

// CheckNotOlderThan refuses an artifact compiled before the data it is being offered as.
//
// The build pin catches code moving underneath an artifact; this catches the other half, which is
// that a compiled file is a statement about a corpus at a moment. A crawl that ran after the
// artifact was built leaves the mirror holding documents the artifact has never seen, and an answer
// that says "369 of 1,000 documents" while reading a file from before them is a coverage claim the
// reader cannot check. The remedy is a rebuild, not an argument.
func (a *Artifact) CheckNotOlderThan(when time.Time, what string) error {
	if when.IsZero() || !a.Header.Created.Before(when) {
		return nil
	}
	return fmt.Errorf("%s is older than the mirror it would be served from: it was compiled %s, and %s happened %s. Rebuild it with `retsin artifact build`",
		a.path, a.Header.Created.Format(time.RFC3339), what, when.Format(time.RFC3339))
}

// CheckCurrentBuild refuses an artifact this binary cannot be trusted to serve, and says which side
// is out of date rather than only that they disagree: the remedy differs, and the reader is the one
// who has to run it.
func (a *Artifact) CheckCurrentBuild() error {
	if a.Header.Build == "" {
		return fmt.Errorf("%s: written %s without recording which build wrote it, so there is no way to tell whether its document model is the one this server reads. Rebuild it with `retsin artifact build`",
			a.path, a.Header.Created.Format(time.RFC3339))
	}
	current, have := CurrentBuild()
	wrote := BuildStamp{Revision: a.Header.Build, Modified: a.Header.Modified, Time: a.Header.Created}
	// The commit and the dirty flag are the whole comparison. The times are not compared for
	// equality: the header records when the artifact was written, and the binary carries when its
	// commit was made, which are different quantities that only order usefully.
	if have && a.Header.Build == current.Revision && a.Header.Modified == current.Modified {
		return nil
	}
	if !have {
		return fmt.Errorf("%s: built from %s, but this binary carries no source stamp so the two cannot be compared. Rebuild the server from the checkout, or rebuild the artifact with it",
			a.path, wrote)
	}
	// Same commit, different working tree: neither side is older, so saying one is stale would be a
	// guess. This is the state where a model type was edited and not committed.
	if a.Header.Build == current.Revision {
		return fmt.Errorf("%s: written from uncommitted files at %s, while this server was built from the committed tree at the same commit (or the other way round). The document model is whatever the working tree said it was, so rebuild whichever side you changed most recently",
			a.path, wrote)
	}
	if a.Header.Created.Before(current.Time) {
		return fmt.Errorf("%s is stale: it was written %s from %s, before %s which this server was built from. Rebuild the artifact with `retsin artifact build`",
			a.path, a.Header.Created.Format(time.RFC3339), wrote, current)
	}
	return fmt.Errorf("%s is from a newer source than this server: it was written %s from %s, while this binary is %s. Rebuild and restart the server, since it would decode the artifact's documents as a model it does not have",
		a.path, a.Header.Created.Format(time.RFC3339), wrote, current)
}
