package mcpserver

import (
	"path/filepath"
	"time"

	"github.com/lkarlslund/retsinformation/internal/compiled"
)

// StatusInfo is what Status says, as fields, for a page that lays it out.
type StatusInfo struct {
	Version   string
	Revision  string // the commit this binary was built from, "" when it does not know
	Modified  bool   // built from a checkout with uncommitted changes
	Started   time.Time
	Documents int

	// Artifact is the file text is served from, empty before one is given.
	Artifact      string
	ArtifactBuilt time.Time
	ArtifactBuild string // the source revision the artifact was compiled from

	// PulledAt is when the collection pass the artifact was built from finished, zero when it
	// recorded none; Owed and Failed are the documents it left unresolved.
	PulledAt     time.Time
	Owed, Failed int64

	// Index is the search index: built at IndexBuilt with IndexTerms words, being built, or absent.
	Index         bool
	IndexBuilding bool
	IndexBuilt    time.Time
	IndexTerms    int
	IndexStale    string // why the index does not match the artifact, or ""
}

// StatusInfo reports the mirror being served.
func (s *Server) StatusInfo() StatusInfo {
	st := StatusInfo{Version: s.version, Started: s.started, Documents: s.lib().Len()}
	if b, ok := compiled.CurrentBuild(); ok {
		st.Revision, st.Modified = b.Revision, b.Modified
	}
	if c := s.cache(); c != nil {
		h := c.Artifact().Header
		st.Artifact = filepath.Base(c.Artifact().Path())
		st.ArtifactBuilt, st.ArtifactBuild = h.Created, h.Build
		if p := h.Collection; p != nil {
			st.PulledAt, st.Owed, st.Failed = p.PulledAt, p.Owed, p.Failed
		}
	}
	if ix := s.idx(); ix != nil {
		st.Index, st.IndexTerms = true, len(ix.Terms)
		if ix.Built > 0 {
			st.IndexBuilt = time.Unix(ix.Built, 0)
		}
	}
	st.IndexBuilding = s.SearchPending()
	st.IndexStale = s.indexMismatch(true)
	return st
}
