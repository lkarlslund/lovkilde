package mcpserver

import (
	"fmt"
	"time"
)

// horizon is what this server knows about how current its own data is, and the only
// thing "current law" can mean here.
//
// The rule it exists to enforce: an answer is anchored on dates the server can prove --
// the date asked, the moment the last collection pass recorded pulling, the moment the
// index was built -- and never on guesses about what may or may not have happened
// since. A mirror can only hold what the site served, so the honest statement is "as of
// the pull, this is the text, and these were the documents we could not get". Naming
// that boundary is not hedging; guessing past it in either direction is.
type horizon struct {
	Now time.Time

	// PulledAt is when the collection pass the served artifact was built after recorded
	// itself. Known is false when the artifact records none, which is said out loud rather
	// than filled in.
	PulledAt time.Time
	Known    bool

	// Owed, Failed and Digest are what that pass left out: work it did not get to, and
	// documents the site refused. IssuesKnown is false
	// for a record written before the problems were counted, where "unknown" is the
	// truth and "clean" would be a claim.
	Owed        int64
	Failed      int64
	Digest      string
	IssuesKnown bool

	// IndexBuilt is the index file's own stamp, zero if it carries none.
	IndexBuilt time.Time

	// IndexDocs and MirrorDocs are the two coverage counts. They differ routinely --
	// documents arrive between index builds -- and the pair is reported rather than
	// reconciled, because only the pair tells a client whether an absence in a search
	// result means absence in the corpus or merely absence from the index.
	IndexDocs  int
	MirrorDocs int

	// HasIndex is false when no index is attached, in which case the index half of the
	// dataset line says so rather than reporting zero coverage.
	HasIndex bool
}

// horizon reads the current facts. The collection pass is the served artifact's: the artifact is
// what answers are made from, so how current it is is how current they are, and a server that
// swaps in a new artifact reports the new one's pass from that moment.
func (s *Server) horizon() horizon {
	h := horizon{
		Now:        s.clock(),
		MirrorDocs: s.lib().Len(),
		HasIndex:   s.idx() != nil,
	}
	if c := s.cache(); c != nil {
		if pass := c.Artifact().Header.Collection; pass != nil {
			h.Known = true
			h.PulledAt = pass.PulledAt
			h.Owed, h.Failed, h.Digest = pass.Owed, pass.Failed, pass.Digest
			h.IssuesKnown = pass.IssuesKnown
		}
	}
	if s.idx() == nil {
		// A lookup-only server has no index half to report. Reporting zero coverage
		// would read as "the corpus is empty".
		return h
	}
	h.IndexDocs = len(s.idx().Docs)
	if s.idx().Built > 0 {
		h.IndexBuilt = time.Unix(s.idx().Built, 0)
	}
	return h
}

// pull is the half of the line the collection pass is responsible for.
func (h horizon) pull() string {
	if !h.Known {
		return "never pulled (the artifact records no collection pass)"
	}
	when := "last pulled " + h.PulledAt.Format("2006-01-02 15:04:05Z07:00")
	switch {
	case !h.IssuesKnown:
		return when + ", problems not recorded by that pass"
	case h.Owed+h.Failed == 0:
		return when + ", nothing left unresolved"
	case h.Digest == "":
		return fmt.Sprintf("%s, %d unresolved", when, h.Owed+h.Failed)
	default:
		return fmt.Sprintf("%s, %d unresolved (issues %s: %d owed, %d refused by the site and retried weekly)",
			when, h.Owed+h.Failed, h.Digest, h.Owed, h.Failed)
	}
}

// pullDA is pull in Danish, for the reading pages.
func (h horizon) pullDA() string {
	if !h.Known {
		return "aldrig hentet (den kompilerede fil angiver ingen indsamling)"
	}
	when := "senest hentet " + h.PulledAt.Format("2006-01-02 15:04:05Z07:00")
	switch {
	case !h.IssuesKnown:
		return when + ", fejl ikke registreret af den kørsel"
	case h.Owed+h.Failed == 0:
		return when + ", intet uafklaret"
	case h.Digest == "":
		return fmt.Sprintf("%s, %d uafklarede", when, h.Owed+h.Failed)
	default:
		return fmt.Sprintf("%s, %d uafklarede (sag %s: %d afventer, %d afvist af siden og prøves igen ugentligt)",
			when, h.Owed+h.Failed, h.Digest, h.Owed, h.Failed)
	}
}

// LineIn is the dataset line in the language a page or answer is written in.
func (h horizon) LineIn(danish bool) string {
	if !danish {
		return h.Line()
	}
	if !h.HasIndex {
		return fmt.Sprintf("spurgt %s · %s · %d dokumenter, intet søgeindeks tilknyttet",
			h.Now.Format("2006-01-02"), h.pullDA(), h.MirrorDocs)
	}
	built := "udateret"
	if !h.IndexBuilt.IsZero() {
		built = h.IndexBuilt.Format("2006-01-02 15:04:05Z07:00")
	}
	return fmt.Sprintf("spurgt %s · %s · søgeindeks bygget %s, dækker %d af spejlets %d dokumenter",
		h.Now.Format("2006-01-02"), h.pullDA(), built, h.IndexDocs, h.MirrorDocs)
}

// indexStampDA is indexStamp in Danish.
func (h horizon) indexStampDA() string {
	if !h.HasIndex {
		return "intet søgeindeks"
	}
	if h.IndexBuilt.IsZero() {
		return "udateret"
	}
	return h.IndexBuilt.Format("2006-01-02 15:04Z07:00")
}

// Line is the one factual line every answer carries.
func (h horizon) Line() string {
	if !h.HasIndex {
		return fmt.Sprintf("asked on %s · %s · %d documents, no full-text index attached",
			h.Now.Format("2006-01-02"), h.pull(), h.MirrorDocs)
	}
	built := "undated"
	if !h.IndexBuilt.IsZero() {
		built = h.IndexBuilt.Format("2006-01-02 15:04:05Z07:00")
	}
	return fmt.Sprintf("asked on %s · %s · index built %s, covering %d documents where the mirror holds %d",
		h.Now.Format("2006-01-02"), h.pull(), built, h.IndexDocs, h.MirrorDocs)
}

// indexStamp is the index build date in the short form a sentence can carry.
func (h horizon) indexStamp() string {
	if !h.HasIndex {
		return "no index attached"
	}
	if h.IndexBuilt.IsZero() {
		return "undated"
	}
	return h.IndexBuilt.Format("2006-01-02 15:04Z07:00")
}

// Policy is the instruction version of the same rule, for the server instructions a
// client reads once and applies to every answer.
func (h horizon) Policy() string {
	return "Every answer is anchored on the dates in its dataset line: the date asked, " +
		"when the mirror last pulled, and when the index was built. 'Current law' here " +
		"means 'the text the mirror holds as of its last pull', and the answer says so " +
		"with those dates rather than hedging about what may have happened since. If a " +
		"question turns on a date past the last pull, say that this dataset cannot " +
		"answer it and name the pull, rather than guessing in either direction."
}
