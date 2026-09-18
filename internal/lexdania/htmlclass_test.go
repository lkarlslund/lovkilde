package lexdania

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// measuredToken is one row of the vocabulary measurement: a (tag, class) pair the
// pilot corpus actually contains, with its element counts per generation.
type measuredToken struct {
	tag, class                 string
	total, legacy, mid, modern int
}

// readMeasuredVocabulary loads the checked-in measurement of the payload vocabulary.
// The payloads themselves are gitignored, so the measurement is what a reviewer can
// actually check the mapping against.
// TestClassifyHTMLMultipleClasses pins the rule for a class attribute holding more
// than one name, which the pilot corpus does -- span class="Bold Italic Subscript".
func TestClassifyHTMLMultipleClasses(t *testing.T) {
	cases := []struct {
		tag, class, wantName string
		wantRole             htmlRole
	}{
		{"span", "Bold Italic Subscript", "Char", roleInline},
		{"span", "Italic Subscript", "Char", roleInline},
		// A class the table has never seen stays unknown even when a sibling names
		// something it does know: reporting it is the point.
		{"p", "HeltNyKlasse Paragraf", "Paragraf", roleSection},
	}
	for _, c := range cases {
		got := classifyHTML(c.tag, c.class)
		if got.Name != c.wantName || got.Role != c.wantRole {
			t.Errorf("classifyHTML(%q, %q) = %v %q, want %v %q", c.tag, c.class, got.Role, got.Name, c.wantRole, c.wantName)
		}
	}
	if got := classifyHTML("p", "HeltNyKlasse HeltNyAndenKlasse"); got.Role != roleUnknown {
		t.Errorf("unknown classes resolved to %v %q, want unknown", got.Role, got.Name)
	}
}

func readMeasuredVocabulary(t *testing.T) []measuredToken {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "html-vocabulary.txt"))
	if err != nil {
		t.Fatalf("vocabulary measurement missing: %v", err)
	}
	var out []measuredToken
	for _, line := range strings.Split(string(raw), "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Split(line, "\t")
		if len(f) != 6 {
			t.Fatalf("malformed vocabulary row %q", line)
		}
		n := func(s string) int { v, _ := strconv.Atoi(s); return v }
		out = append(out, measuredToken{f[0], f[1], n(f[2]), n(f[3]), n(f[4]), n(f[5])})
	}
	if len(out) < 100 {
		t.Fatalf("only %d tokens measured, want the full pilot vocabulary", len(out))
	}
	return out
}

// canonicalInventory is the element-name list the mapping is allowed to emit.
func canonicalInventory(t *testing.T) map[string]bool {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "canonical-elements.txt"))
	if err != nil {
		t.Fatalf("canonical inventory missing: %v", err)
	}
	out := map[string]bool{}
	for _, line := range strings.Split(string(raw), "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out[line] = true
	}
	if len(out) < 50 {
		t.Fatalf("only %d canonical names, want the measured inventory", len(out))
	}
	return out
}

// The whole point of the table is that nothing is dropped quietly: every (tag,
// class) pair measured in the pilot corpus has to be classified. A class that
// arrives at roleUnknown is recorded by the builder, but a class that has *been*
// measured and still has no row is a gap in the mapping, and this is where it
// surfaces.
func TestEveryMeasuredTokenIsClassified(t *testing.T) {
	tokens := readMeasuredVocabulary(t)
	var unknown []string
	var elements, classified int
	byRole := map[htmlRole]int{}
	for _, tk := range tokens {
		target := classifyHTML(tk.tag, tk.class)
		elements += tk.total
		if target.Role == roleUnknown {
			unknown = append(unknown, tk.tag+"."+tk.class)
			continue
		}
		classified += tk.total
		byRole[target.Role] += tk.total
	}
	if len(unknown) > 0 {
		t.Errorf("%d measured tokens are unclassified (%d of %d elements): %v",
			len(unknown), elements-classified, elements, unknown)
	}
	t.Logf("coverage: %d/%d elements classified; by role %v", classified, elements, byRole)
}

// Emitting a name the corpus has never used produces a tree that looks structured
// and matches nothing: Sections() would not see it, and an amendment address would
// report the provision as missing.
func TestMappingOnlyEmitsCanonicalNames(t *testing.T) {
	inventory := canonicalInventory(t)
	tokens := readMeasuredVocabulary(t)
	for _, tk := range tokens {
		target := classifyHTML(tk.tag, tk.class)
		if target.Name == "" || target.Role == roleUnknown {
			continue
		}
		if !inventory[target.Name] {
			t.Errorf("%s.%s maps to %q, which has never been seen under "+
				"<DokumentIndhold> in the archive", tk.tag, tk.class, target.Name)
		}
	}
}

// Dropping text has to be a stated reason, not an empty row.
func TestSkippedRolesCarryAReason(t *testing.T) {
	for class, target := range htmlClasses {
		if target.Role == roleSkip && target.Reason == "" {
			t.Errorf("class %q is skipped with no reason recorded", class)
		}
	}
	for tag, target := range htmlTags {
		if target.Role == roleSkip && target.Reason == "" {
			t.Errorf("tag %q is skipped with no reason recorded", tag)
		}
	}
}

// The rows that a measurement supports must keep agreeing with it. These are the
// 156 id joins and the 55 text joins from the paired documents; if one of these
// drifts, the mapping has diverged from the evidence rather than from a comment.
func TestMeasuredRowsStillMatchTheirEvidence(t *testing.T) {
	cases := []struct {
		class, tag, name string
		role             htmlRole
		why              string
	}{
		{"Paragraf", "p", "Paragraf", roleSection, "id join, 38 elements"},
		{"Stk2", "p", "Stk", roleSection, "id join, 61 elements share the Stk id"},
		{"StkNr", "span", "Explicatus", roleMarker, "id join: the number shares its parent's id"},
		{"ParagrafNr", "span", "Explicatus", roleMarker, "id join: shares Paragraf or Stk"},
		{"Liste1Nr", "span", "Explicatus", roleMarker, "id join, 18 elements"},
		{"Liste1", "p", "Indentatio", roleText, "id join through Liste1Nr"},
		{"Kapitel", "p", "Kapitel", roleSection, "id join, 4 elements"},
		{"TabelTekst2", "div", "Exitus", roleText, "text join, 40 elements land in a Td"},
		{"NormalInd", "p", "Exitus", roleText, "text join, 23 elements"},
		{"Hymne2", "p", "Hymne", roleSection, "text join, 4 elements"},
		{"Rykningsklausul", "p", "Rykningsklausul", roleSection, "text join, 2 elements"},
		{"Fodnote", "p", "Nota", roleNote, "text join; 266 of 277 Nota ids reappear as name="},
		{"TekstOverskrift", "p", "Rubrica", roleText, "text join, 6 elements"},
	}
	for _, tc := range cases {
		got := classifyHTML(tc.tag, tc.class)
		if got.Name != tc.name || got.Role != tc.role {
			t.Errorf("%s mapped to %+v, want %s role %d (%s)", tc.class, got, tc.name, tc.role, tc.why)
		}
	}
}

// An unseen class must come back as unknown rather than being folded into prose.
// This is the guard for the 29k payloads the pilot did not sample.
func TestUnseenClassIsUnknownNotText(t *testing.T) {
	for _, class := range []string{"Nope", "ParagrafX", "Liste", "STK2x", ""} {
		if got := classifyHTML("p", class); class != "" && got.Role != roleUnknown {
			t.Errorf("classifyHTML(p, %q) = %+v, want roleUnknown so the builder records it", class, got)
		}
	}
	// A tag with no class is still classified, because table cells and inline runs
	// never carry one: 15,000 of the measured elements are classless.
	if got := classifyHTML("td", ""); got.Name != "Td" {
		t.Errorf("classless td = %+v, want Td", got)
	}
}

// The numbered families are the one place the mapping generalises beyond what was
// measured, so the generalisation itself is pinned: digits are the only variation,
// and a "…Nr" of an unknown family is still a marker.
func TestNumberedFamiliesGeneralise(t *testing.T) {
	cases := []struct {
		class, name string
		role        htmlRole
	}{
		{"Titel3", "Titel", roleText},
		{"Hymne4", "Hymne", roleText},
		{"Liste4", "Indentatio", roleText},
		{"Liste4Nr", "Explicatus", roleMarker},
		{"Sign3", "Underskriver", roleText},
		{"Overskriftstekst7", "Rubrica", roleText},
	}
	for _, tc := range cases {
		got := classifyHTML("p", tc.class)
		if got.Name != tc.name {
			t.Errorf("family %q -> %+v, want name %s", tc.class, got, tc.name)
		}
	}
	if got := classifyHTML("p", "Titelx"); got.Role != roleUnknown {
		t.Errorf("a non-numeric variant generalised: %+v", got)
	}
	// The digit is what licenses the generalisation. "NopeNr" has no measured family
	// behind it, so treating any "…Nr" as a marker would invent provisions from an
	// unseen class.
	if got := classifyHTML("span", "NopeNr"); got.Role != roleUnknown {
		t.Errorf("an unmeasured …Nr family generalised: %+v", got)
	}
}

// The centred-provision rows are deliberately undecided: the region decides.
func TestCentredProvisionsStayUndecided(t *testing.T) {
	for _, class := range []string{"CentreretParagraf", "PARAC"} {
		if got := classifyHTML("p", class); got.Role != roleCentreret {
			t.Errorf("%s = %+v, want roleCentreret for the builder to resolve", class, got)
		}
	}
}

// Some generations' converters write section titles as bare h1-h6 with no class, and
// revision markup as ins/del/s: about 7,100 heading elements and 1,900 revision ones
// across the 101,727 stored payloads. Before they had entries they fell through the
// builder into an element named "", which every name-based walk then skipped.
func TestClasslessHeadingAndRevisionTagsHaveReadings(t *testing.T) {
	for _, tag := range []string{"h1", "h2", "h3", "h4", "h5", "h6", "figcaption"} {
		if got := classifyHTML(tag, ""); got.Name != "Rubrica" {
			t.Errorf("classifyHTML(%q, \"\") = %+v, want Rubrica, which is what every "+
				"classed heading in the table becomes", tag, got)
		}
	}
	for _, tag := range []string{"ins", "del", "s", "mark"} {
		if got := classifyHTML(tag, ""); got.Name != "Char" {
			t.Errorf("classifyHTML(%q, \"\") = %+v, want Char: the text has to survive even "+
				"though the vocabulary cannot say it was inserted", tag, got)
		}
	}
	for _, tag := range []string{"thead", "tbody", "tfoot"} {
		if got := classifyHTML(tag, ""); got.Role != roleContainer {
			t.Errorf("classifyHTML(%q, \"\") = %+v, want a container so the rows land in the Table", tag, got)
		}
	}
	// The general hole, not just the tags that happened to be measured: a target with
	// no name is only safe when nothing is expected to be built from it.
	for tag, target := range htmlTags {
		switch target.Role {
		case roleContainer, roleSkip, roleBreak:
			continue
		}
		if target.Name == "" {
			t.Errorf("tag %q has role %v but no element name, so it would build an unnamed node", tag, target.Role)
		}
	}
}
