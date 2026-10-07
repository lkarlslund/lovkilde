package lexdania

import "github.com/lkarlslund/retsinformation/internal/chain"

// targetsLaw is chain.TargetsLaw for a document's metadata as this package reads it.
func targetsLaw(h TargetHint, m Meta, _ AccessionInfo) bool {
	return chain.TargetsLaw(h, CatalogueMetaOf(m))
}
