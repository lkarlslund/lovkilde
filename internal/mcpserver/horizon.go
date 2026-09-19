package mcpserver

import (
	"fmt"
	"time"

	"github.com/lkarlslund/retsinformation/internal/crawl"
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

	// PulledAt is when the last collection pass recorded itself. Known is false when
	// no pass has published a record, which is said out loud rather than filled in.
	PulledAt time.Time
	Known    bool

	// Owed, Stalled and Digest are what that pass could not get. IssuesKnown is false
	// for a record written before the problems were counted, where "unknown" is the
	// truth and "clean" would be a claim.
	Owed        int64
	Stalled     int64
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

	// err explains an unknown horizon when unknown is not simply "no pass has run", so
	// an operator can tell "never collected" from "the record will not parse".
	err error
}

// horizon reads the current facts. The published record is re-read on every call, which
// costs one small read and lets a server that has been running since last night report
// tonight's collection instead of the one it started against.
func (s *Server) horizon() horizon {
	h := horizon{
		Now:        s.clock(),
		MirrorDocs: s.archive.Len(),
		HasIndex:   s.index != nil,
	}
	// The collection record is read first and unconditionally: it belongs to the
	// mirror, not to the index, so a server serving lookup without a full-text index
	// still has to say how current its data is.
	rec, found, err := crawl.ReadSweepRecord(s.SweepStampPath)
	if err != nil {
		// A record that will not parse is worse than none: it would be quoted as though
		// it were a date.
		h.err = err
	} else {
		h.Known = found
		h.PulledAt = rec.PulledAt
		h.Owed, h.Stalled, h.Digest = rec.Owed, rec.Stalled, rec.Digest
		h.IssuesKnown = rec.IssuesKnown
	}
	if s.index == nil {
		// A lookup-only server has no index half to report. Reporting zero coverage
		// would read as "the corpus is empty".
		return h
	}
	h.IndexDocs = len(s.index.Docs)
	if s.index.Built > 0 {
		h.IndexBuilt = time.Unix(s.index.Built, 0)
	}
	return h
}

// pull is the half of the line the collection pass is responsible for.
func (h horizon) pull() string {
	if !h.Known {
		if h.err != nil {
			return fmt.Sprintf("last update pull unreadable (%v)", h.err)
		}
		return "never pulled (no collection pass has run here)"
	}
	when := "last pulled " + h.PulledAt.Format("2006-01-02 15:04:05Z07:00")
	switch {
	case !h.IssuesKnown:
		return when + ", problems not recorded by that pass"
	case h.Owed+h.Stalled == 0:
		return when + ", nothing left unresolved"
	case h.Digest == "":
		return fmt.Sprintf("%s, %d unresolved", when, h.Owed+h.Stalled)
	default:
		return fmt.Sprintf("%s, %d unresolved (issues %s: %d owed, %d past the retry ceiling)",
			when, h.Owed+h.Stalled, h.Digest, h.Owed, h.Stalled)
	}
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
