package mcpserver

import (
	"strings"
	"testing"

	"github.com/lkarlslund/retsinformation/internal/lexdania"
	"github.com/lkarlslund/retsinformation/internal/lexdoc"
)

// TestChainWarningNamesTheRealCause is the guard against the sentence BR18 was served with: every
// amending act was in the mirror, and the answer still told the reader one was missing, because
// "not trustworthy" had one message for two different failures.
func TestChainWarningNamesTheRealCause(t *testing.T) {
	view := &lexdania.View{Unresolved: []lexdoc.UnresolvedEdit{
		{Reason: `address "§ 21, stk. 2" not found in the current text`},
		{Reason: `address "§ 523, stk. 2" not found in the current text`},
		{Reason: "change wording not recognised by the pattern set"},
	}}
	r := &LookupResult{AsOf: "2026-09-19", ChainActs: 17, AppliedActs: 17, RefusedEdits: len(view.Unresolved)}
	r.warnChain(view)

	joined := strings.Join(r.Warnings, "\n")
	if strings.Contains(joined, "not in the mirror") {
		t.Errorf("the answer blames the mirror although nothing is missing:\n%s", joined)
	}
	if !strings.Contains(joined, "3 of the changes") {
		t.Errorf("the refusal count is missing:\n%s", joined)
	}
	// The two "address … not found" reasons are one class of failure, and the reader learns more
	// from being told it twice than from two near-identical lines.
	if !strings.Contains(joined, "2× address not found in the current text") {
		t.Errorf("refusal reasons were not grouped by class:\n%s", joined)
	}
}

// TestChainWarningInDanish keeps the reading page from printing English prose it has to recognise
// and discard, which is what it used to do with a hand-written copy of the sentence.
func TestChainWarningInDanish(t *testing.T) {
	view := &lexdania.View{Unresolved: []lexdoc.UnresolvedEdit{{Reason: "change wording not recognised by the pattern set"}}}
	r := &LookupResult{Language: "da", AsOf: "2026-09-19", ChainActs: 17, AppliedActs: 17, RefusedEdits: 1}
	r.warnChain(view)
	joined := strings.Join(r.Warnings, "\n")
	if !strings.Contains(joined, "kunne ikke indarbejdes i teksten") {
		t.Errorf("the Danish page got no Danish warning:\n%s", joined)
	}
	if strings.Contains(joined, "could not be placed") {
		t.Errorf("the Danish page got the English sentence too:\n%s", joined)
	}

	// A missing document is the other failure, and it has to name the accession it lacks.
	r2 := &LookupResult{Language: "da", AsOf: "2026-09-19", ChainActs: 3, AppliedActs: 2, MissingActs: []string{"B20240000005"}}
	r2.warnChain(view)
	got := strings.Join(r2.Warnings, "\n")
	if !strings.Contains(got, "findes ikke i spejlet") || !strings.Contains(got, "B20240000005") {
		t.Errorf("missing amending acts are not reported with their accession:\n%s", got)
	}
}

// TestRefusedChangesAreListedAndMarked is the answer a model reads for a consolidated text with a
// change the engine could not place: the change itself, with its act's wording and the reason, in
// a section of its own ahead of the text, and a marker under the provision it concerns.
func TestRefusedChangesAreListedAndMarked(t *testing.T) {
	refused := []lexdoc.UnresolvedEdit{
		{Edit: lexdoc.Edit{Source: "B20210156105", Number: "2.", Address: lexdoc.ParseAddress("§ 5, stk. 1, 1. pkt."),
			Definition: "I § 5, stk. 1, 1. pkt. ændres »stk. 2 og 3« til: »stk. 2«."}, Reason: "quoted passage not present in § 5, stk. 1"},
		{Edit: lexdoc.Edit{Source: "B20210156105", Number: "3.", Address: lexdoc.ParseAddress("§ 3 a"),
			Definition: "I § 3 a indsættes efter »2019«:", NewText: []lexdoc.Segment{{Text: "», forudsat …«"}}}, Reason: "address not found"},
	}
	s := &Server{}
	r := &LookupResult{AsOf: "2026-10-05", RefusedEdits: 2}
	r.RefusedChanges = s.refusedChanges(refused, "§ 5")
	if len(r.RefusedChanges) != 2 || !r.RefusedChanges[0].Concerns || r.RefusedChanges[1].Concerns {
		t.Fatalf("the change to § 5 is not first and marked: %+v", r.RefusedChanges)
	}
	r.Text = markRefused("§ 3 a.\n\nTekst.\n\n§ 5.\n\nDet er en betingelse.", r.RefusedChanges)
	out := r.String()
	for _, want := range []string{
		"### Changes not placed in this text (2)",
		"1 of them concerns the provision asked for",
		"Wording:** I § 5, stk. 1, 1. pkt. ændres »stk. 2 og 3« til: »stk. 2«.",
		"Not placed because:** quoted passage not present in § 5, stk. 1",
		"I § 3 a indsættes efter »2019«: », forudsat …«",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("answer lacks %q:\n%s", want, out)
		}
	}
	if strings.Index(out, "### Changes not placed") > strings.Index(out, "### Text") {
		t.Error("the list comes after the text, where truncation can cut it")
	}
	if !strings.Contains(r.Text, "§ 5.\n\n> **Change not placed here:** see item 1") || !strings.Contains(r.Text, "§ 3 a.\n\n> **Change not placed here:** see item 2") {
		t.Errorf("provisions are not marked:\n%s", r.Text)
	}
}
