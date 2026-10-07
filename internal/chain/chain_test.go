package chain

import (
	"testing"
	"time"

	"github.com/lkarlslund/retsinformation/internal/lexdoc"
)

// A later act repealing an earlier act's changes before they took effect leaves them out (lov nr.
// 743 of June 2018's changes to aktivloven § 11, from 1 January 2019, repealed from that day by lov
// nr. 1525 of December 2018); a change that took effect before the repeal stays.
func TestChangesRepealedBeforeTheyTookEffectAreLeftOut(t *testing.T) {
	d := func(y, m, day int) time.Time { return time.Date(y, time.Month(m), day, 0, 0, 0, 0, time.UTC) }
	st := chainStep{Version: Version{Accession: "A", Effective: d(2018, 6, 8)},
		InForce: lexdoc.InForce{Default: d(2019, 1, 1), Rules: []lexdoc.InForceRule{{Section: "1", Numbers: [][2]int{{1, 1}}, Date: d(2018, 7, 1)}}},
		Edits:   []lexdoc.Edit{{Section: "1", Number: "1."}, {Section: "1", Number: "2."}, {Section: "2", Number: "1."}}}
	rules := lexdoc.InForceRulesOf("§ 1 og § 2")
	got := withoutRepealed(st, []actRepeal{{act: "A", rules: rules, from: d(2019, 1, 1)}})
	if len(got) != 1 || got[0].Number != "1." || got[0].Section != "1" {
		t.Errorf("kept %+v, want only § 1, nr. 1, which took effect before the repeal", got)
	}
	if !reActRepeal.MatchString("§ 2, nr. 2, 6, 8 og 10, ophæves.") || reActRepeal.MatchString("§ 11, stk. 5 og 6, ophæves.") || reActRepeal.MatchString("§ 35 b ophæves.") {
		t.Error("reActRepeal reads a law's subsection or lettered provision as an act's change")
	}
}

func TestAConsolidationNamesTheActRepealingIt(t *testing.T) {
	pre := "Herved bekendtgøres lov om social service, jf. lovbekendtgørelse nr. 906 af 26. september 2005. § 189 i lov nr. 573 af 24. juni 2005 ophæver lov om social service den 1. januar 2007."
	m := reRepealedByAct.FindString(pre)
	if got := lexdoc.ParseTarget(m); got.Number != 573 || got.Date.Year() != 2005 {
		t.Errorf("%q → %+v", m, got)
	}
}

// A law's short name and its title without the short name are the law's names too.
func TestALawGoesByItsShortName(t *testing.T) {
	names := lawNames(ConsolidationTitle("Bekendtgørelse af lov om indkomstbeskatning af selvstændige erhvervsdrivende (virksomhedsskatteloven)"))
	for _, n := range []string{"virksomhedsskatteloven", "lov om indkomstbeskatning af selvstændige erhvervsdrivende"} {
		if !names[n] {
			t.Errorf("%q not among %v", n, names)
		}
	}
	if lawNames("lov om en aktiv beskæftigelsesindsats")["aktiv"] {
		t.Error("a word of the title taken for a name")
	}
}
