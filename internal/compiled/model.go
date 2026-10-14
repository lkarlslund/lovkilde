package compiled

import (
	"errors"
	"fmt"
)

// ErrOtherModel is an artifact holding another document model than this program reads: a newer
// program, or an artifact of the model it reads, is the way out.
var ErrOtherModel = errors.New("the artifact holds another document model")

// ModelVersion names the document model the artifact stores: what a document compiles to, not
// which commit compiled it. A server reads an artifact whose model version is its own, whatever
// commit built either side, so a change to rendering, templates, the consolidation engine or the
// tools' wording deploys with a restart; only a change to what is stored needs a rebuild.
//
// Bump it whenever a document compiles differently: a reader or normalisation change, a field
// added to or removed from internal/lexdoc's types, a change in what a field means.
// TestModelVersionCoversTheStoredModel compiles a committed sample of real documents through the
// same path `lovkilde-update artifact build` uses and fails, naming this constant, when their stored form
// changed and the version did not.
const ModelVersion = 2

// CheckModel refuses an artifact whose stored model this server does not read, and accepts one
// that holds this server's model version from any build. An artifact written before model versions
// were recorded falls back to the build check: without a version, the commit is all there is.
func (a *Artifact) CheckModel() error { return a.checkModel(ModelVersion) }

// checkModel compares against the model a server reads, taken as a parameter so a test can stand
// in for a server of another model.
func (a *Artifact) checkModel(reads int) error {
	if a.Header.Model == 0 {
		return a.CheckCurrentBuild()
	}
	if a.Header.Model == reads {
		return nil
	}
	if a.Header.Model < reads {
		return fmt.Errorf("%s holds document model %d, and this server reads model %d: the way documents compile has changed since it was built. Rebuild it with `lovkilde-update artifact build`: %w",
			a.path, a.Header.Model, reads, ErrOtherModel)
	}
	return fmt.Errorf("%s holds document model %d, newer than the model %d this server reads. Rebuild and restart the server, since it would decode the artifact's documents as a model it does not have: %w",
		a.path, a.Header.Model, reads, ErrOtherModel)
}
