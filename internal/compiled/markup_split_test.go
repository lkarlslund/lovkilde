package compiled

import (
	"os"
	"testing"

	"github.com/lkarlslund/lovkilde/internal/lexdania"
	"github.com/lkarlslund/lovkilde/internal/search"
)

// TestMarkupSplit answers how much of the publisher's XML is markup rather than text,
// because that bounds what any redesigned representation can win: zstd only has to pay
// for what is left once the markup is deduplicated, and a designed form only beats it if
// the prose itself compresses worse than the whole XML does.
func TestMarkupSplit(t *testing.T) {
	if os.Getenv("RETSIN_CODEC_BENCH") == "" {
		t.Skip("set RETSIN_CODEC_BENCH=1 to run")
	}
	sample := sampleDocuments(t, "../../data", 1000)
	var xml, text, names, attrKeys, attrVals int
	var elems, attrCount int
	namesUniq := map[string]int{}
	keysUniq := map[string]int{}
	for _, s := range sample {
		doc, err := lexdania.Parse(s.xml)
		if err != nil {
			t.Fatal(err)
		}
		xml += len(s.xml)
		var walk func(n *lexdania.Node)
		walk = func(n *lexdania.Node) {
			elems++
			namesUniq[n.Name]++
			names += len(n.Name)
			for k, v := range n.Attrs {
				attrCount++
				keysUniq[k]++
				attrKeys += len(k)
				attrVals += len(v)
			}
			for _, r := range n.Runs {
				text += len(r)
			}
			for _, e := range n.Elems {
				walk(e)
			}
		}
		walk(doc.Root)
	}
	t.Logf("sample: %d documents, %.1f MB of XML", len(sample), mb(float64(xml)))
	t.Logf("  text runs        %6.1f MB  %5.1f%% of XML   (the prose: irreducible except by the prose's own compressibility)", mb(float64(text)), 100*float64(text)/float64(xml))
	t.Logf("  element names    %6.1f MB  %5.1f%% of XML   (%d elements, %d distinct)", mb(float64(names)), 100*float64(names)/float64(xml), elems, len(namesUniq))
	t.Logf("  attribute names  %6.1f MB  %5.1f%% of XML   (%d attributes, %d distinct)", mb(float64(attrKeys)), 100*float64(attrKeys)/float64(xml), attrCount, len(keysUniq))
	t.Logf("  attribute values %6.1f MB  %5.1f%% of XML", mb(float64(attrVals)), 100*float64(attrVals)/float64(xml))
	t.Logf("  unaccounted      %6.1f MB  %5.1f%% of XML   (tag brackets, quotes, declarations, whitespace)",
		mb(float64(xml-text-names-attrKeys-attrVals)), 100*float64(xml-text-names-attrKeys-attrVals)/float64(xml))
	// What the prose costs on its own, so the floor is visible.
	blob := make([]byte, 0, text)
	for _, s := range sample {
		doc, _ := lexdania.Parse(s.xml)
		var walk func(n *lexdania.Node)
		walk = func(n *lexdania.Node) {
			for _, r := range n.Runs {
				blob = append(blob, r...)
			}
			for _, e := range n.Elems {
				walk(e)
			}
		}
		walk(doc.Root)
	}
	t.Logf("  prose alone zstd'd: %.1f MB = %.1f%% of XML (the floor a designed form cannot go below without losing text)",
		mb(float64(len(mustZstd(t, blob)))), 100*float64(len(mustZstd(t, blob)))/float64(xml))
	_ = search.Options{}
}
