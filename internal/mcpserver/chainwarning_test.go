package mcpserver

import (
	"strings"
	"testing"

	"github.com/lkarlslund/retsinformation/internal/lexdania"
)

// TestChainWarningNamesTheRealCause is the guard against the sentence BR18 was served with: every
// amending act was in the mirror, and the answer still told the reader one was missing, because
// "not trustworthy" had one message for two different failures.
func TestChainWarningNamesTheRealCause(t *testing.T) {
	view := &lexdania.View{Unresolved: []lexdania.UnresolvedEdit{
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
	view := &lexdania.View{Unresolved: []lexdania.UnresolvedEdit{{Reason: "change wording not recognised by the pattern set"}}}
	r := &LookupResult{Language: "da", AsOf: "2026-09-19", ChainActs: 17, AppliedActs: 17, RefusedEdits: 1}
	r.warnChain(view)
	joined := strings.Join(r.Warnings, "\n")
	if !strings.Contains(joined, "kunne ikke placeres i teksten") {
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
