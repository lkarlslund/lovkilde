package lexdania

import (
	"fmt"
	"strings"
	"testing"

	"github.com/lkarlslund/retsinformation/internal/lexdoc"
)

// TestModelAddressParityOnCorpus resolves every address the amendment inventory produces on
// real documents, both ways, and classifies every disagreement.
//
// This is the assertion that matters most and the one most likely to be waved through: a
// resolver that silently disagrees with the old one does not fail, it applies an amendment to
// the wrong provision, or refuses one that used to land, and neither shows up until a citizen
// is quoted text that was never in force.

// locatedKey names a model resolution in the flat list's vocabulary.
func locatedKey(l lexdoc.Located) string {
	switch {
	case l.Item != nil && l.Stykke != nil && l.Provision != nil:
		return fmt.Sprintf("p:%s,stk%d,nr%d+%s", l.Provision.Address(), l.Stykke.Nr, l.Item.Nr, l.Item.Litra)
	case l.Stykke != nil && l.Provision != nil:
		return fmt.Sprintf("p:%s,stk%d", l.Provision.Address(), l.Stykke.Nr)
	case l.Provision != nil:
		return "p:" + l.Provision.Address()
	case l.Part != nil:
		switch l.Part.Kind {
		case lexdoc.PartChapter:
			return "kapitel:" + l.Part.Number
		case lexdoc.PartAfsnit:
			return "afsnit:" + l.Part.Number
		}
		return "gruppe:" + l.Part.Number
	case l.Appendix != nil:
		return "bilag:" + l.Appendix.Number
	case l.Preamble:
		return "indledning"
	case l.Title:
		return "titel"
	}
	return ""
}

func TestModelAddressParityOnCorpus(t *testing.T) {
	dir := corpusDir(t)
	// Addresses are sparse — most documents carry none — so this walks ten times the
	// ordinary budget to get a sample worth a claim, and everything when asked to.
	n := corpusEntryBudget(t)
	if n > 0 {
		n *= 10
	} else {
		n = 1 << 20
	}
	files := sampleFiles(t, dir, n)

	var docs, addresses, agreed, bothMissing int
	var oldOnly, modelOnly, different, refusedUnrecognised int
	var oldOnlyExamples []string

	for _, f := range files {
		doc, err := Load(f)
		if err != nil {
			t.Logf("parse failed (the crawl may be mid-write): %v", err)
			continue
		}
		m := doc.Model()
		secs := doc.Sections()

		// Provision numbers that exist only inside an annex are the one place the two
		// resolvers are allowed to part company: the flat list searched them because it
		// walked annexes like any other container, and the model will not resolve into an
		// annex by provision number, because "§ 3" in an annex is not the law's § 3.
		inAnnex := map[string]bool{}
		for i := range m.Content.Appendices {
			var order []string
			texts := map[string][]string{}
			addAnnex(&order, texts, &m.Content.Appendices[i])
			for _, k := range order {
				if strings.HasPrefix(k, "p:") {
					inAnnex[k] = true
				}
			}
		}

		docs++
		for _, e := range doc.Amendments() {
			if !e.Address.Recognised {
				refusedUnrecognised++
				continue
			}
			addresses++
			got := Find(secs, e.Address)
			want, ok := m.Content.Find(e.Address)
			oldKey, modelKey := "", ""
			if got != nil {
				oldKey = secKey(got)
			}
			if ok {
				modelKey = locatedKey(want)
			}
			switch {
			case oldKey == "" && modelKey == "":
				bothMissing++
			case oldKey == modelKey:
				agreed++
			case oldKey != "" && modelKey == "":
				if inAnnex[oldKey] {
					oldOnly++
					if len(oldOnlyExamples) < 5 {
						oldOnlyExamples = append(oldOnlyExamples, f+" "+e.Address.String()+" -> "+oldKey)
					}
					continue
				}
				t.Errorf("%s: address %q resolved to %q in the flat list and to nothing in the model",
					f, e.Address.String(), oldKey)
			case oldKey == "" && modelKey != "":
				modelOnly++
				t.Errorf("%s: address %q resolved to %q in the model and to nothing in the flat list",
					f, e.Address.String(), modelKey)
			default:
				different++
				t.Errorf("%s: address %q resolved to %q in the flat list and %q in the model",
					f, e.Address.String(), oldKey, modelKey)
			}
		}
	}

	if addresses == 0 {
		t.Fatalf("vacuous: no recognised addresses across %d documents", docs)
	}
	if agreed == 0 {
		t.Fatal("no address agreed on both paths, which cannot be right")
	}
	if modelOnly != 0 || different != 0 {
		t.Errorf("model resolved %d addresses the flat list could not and %d differently", modelOnly, different)
	}
	t.Logf("%d documents, %d recognised addresses: %d identical resolutions, %d resolved to nothing on both paths, "+
		"%d annex-only numbers the flat list resolved and the model refuses, %d unrecognised addresses refused by both",
		docs, addresses, agreed, bothMissing, oldOnly, refusedUnrecognised)
	for _, ex := range oldOnlyExamples {
		t.Log("annex-only example:", ex)
	}
}

// addAnnex collects an annex's unit keys, provisions included.
func addAnnex(order *[]string, texts map[string][]string, ap *lexdoc.Appendix) {
	var walk func(nodes []lexdoc.Node)
	walk = func(nodes []lexdoc.Node) {
		for _, n := range nodes {
			switch {
			case n.Provision != nil:
				addProvision(order, texts, n.Provision)
			case n.Part != nil:
				walk(n.Part.Order)
			}
		}
	}
	walk(ap.Order)
}
