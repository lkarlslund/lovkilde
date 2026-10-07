package lexdania

import (
	"reflect"
	"testing"
)

// The oldest payloads print a change's number and address in inline formatting, and the HTML
// reader's display tree holds those pieces apart from their sentence. Lines are read in order.
func TestHTMLLinesKeepInlineFormattingInItsSentence(t *testing.T) {
	got := htmlLines(`<DIV CLASS=PARAGRAPH ID=""P1""><P CLASS=PARAC ALIGN=CENTER> <B>§ 1 </B> </P>
<P>I lov om bygnings- og boligregistrering foretages følgende ændringer: </P>
<P> <B>2.</B> I <I>§ 6, stk. 4,</I> ændres &#187;stk. 3&#171; til: &#187;stk. 1&#171;. </P></DIV>`)
	want := []string{
		"§ 1",
		"I lov om bygnings- og boligregistrering foretages følgende ændringer:",
		"2. I § 6, stk. 4, ændres »stk. 3« til: »stk. 1«.",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("lines =\n%q\nwant\n%q", got, want)
	}
}
