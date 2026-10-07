package lexdania

import (
	"math/rand"
	"reflect"
	"testing"
)

// sameTree is the rule the shortcut lives by: whenever it accepts a document, encoding/xml
// accepts it too and builds the identical tree. Giving up is always allowed.
func sameTree(t *testing.T, doc string) (accepted bool) {
	t.Helper()
	fast, ok := fastTree([]byte(doc))
	if !ok {
		return false
	}
	slow, err := slowTree([]byte(doc))
	if err != nil {
		t.Fatalf("the shortcut accepted what encoding/xml refuses (%v):\n%q", err, doc)
	}
	if !reflect.DeepEqual(fast, slow) {
		t.Fatalf("the trees differ:\n%q\nfast %#v\nslow %#v", doc, fast, slow)
	}
	return true
}

const fastXMLDoc = "<?xml version=\"1.0\" encoding=\"UTF-8\"?>\r\n<Dokument>\r\n<Meta><AccessionNumber>A1</AccessionNumber>" +
	"<DocumentTitle>Lov om P&amp;T &lt;x&gt; &quot;q&quot; &apos;a&apos;</DocumentTitle></Meta>\r\n" +
	"<DokumentIndhold><Paragraf id=\"P1\" formaChar='a&amp;b'>\r\n<Explicatus>§ 1.</Explicatus> Tekst​ med <Char formaChar=\"Kursiv\">kursiv</Char>" +
	" og &#230;&#x00F8;&aring; ]] > og &nbsp;slut<Tom/><Tom2 a=\"1\" /></Paragraf>\r</DokumentIndhold>\r\n</Dokument>\r\n"

// TestFastXMLIsEncodingXML: the shortcut takes the forms the register serves, and on every
// input — those forms, the edge cases around them, and thousands of corruptions of a realistic
// document — it either gives up or builds exactly the tree encoding/xml builds.
func TestFastXMLIsEncodingXML(t *testing.T) {
	if !sameTree(t, fastXMLDoc) {
		t.Fatal("the shortcut gave up on the register's ordinary form")
	}
	for _, doc := range []string{
		`<Dokument/>`, `<Dokument></Dokument >`, `<!-- c --><Dokument>a<!-- x -->b<?pi x?>c</Dokument>`,
		`<?xml version="1.0"?><Dokument a = "x" b='y'>t</Dokument>`,
		`<?xml version="1.1"?><Dokument/>`, `<?xml version="1.0" encoding="ISO-8859-1"?><Dokument/>`,
		`<Dokument xmlns="u"/>`, `<a:Dokument xmlns:a="u"/>`, `<Dokument a:b="c"/>`, `<!DOCTYPE x><Dokument/>`,
		`<Dokument><![CDATA[x]]></Dokument>`, `<Dokument>]]></Dokument>`, `<Dokument>&bogus;</Dokument>`,
		`<Dokument>&#0;</Dokument>`, `<Dokument>&#xD800;</Dokument>`, `<Dokument>&#65;&#x41;</Dokument>`,
		"<Dokument>\x01</Dokument>", "<Dokument>\xff</Dokument>", "<Dokument>￾</Dokument>",
		`<Dokument a=x/>`, `<Dokument a/>`, `<Dokument a="<"/>`, `<Dokument><a></b></Dokument>`,
		`<Dokument>`, `text<Dokument/>`, "\ufeff<Dokument/>", `<Dokument><!-- a -- b --></Dokument>`,
		"<Dokument>a\r\nb\rc\n</Dokument>", "<Dokument a=\"x\r\ny\"/>", `<Dokument>&amp</Dokument>`,
		`<1Dokument/>`, `<Dokument a="1" a="2"/>`, `<Dokument>&frac12;&sup2;</Dokument>`,
	} {
		sameTree(t, doc)
	}

	pieces := []string{"<", ">", "/", "&", ";", "&amp;", "&#", "x", "\"", "'", "=", " ", "\r", "\r\n", "]]>", ":", "<!--", "-->", "<?", "?>", "æ", "​", "a", "Tom", "</Tom>", "<Tom>"}
	rng := rand.New(rand.NewSource(3))
	accepted := 0
	for i := 0; i < 30000; i++ {
		doc := []byte(fastXMLDoc)
		for k := rng.Intn(3) + 1; k > 0; k-- {
			at := rng.Intn(len(doc))
			switch rng.Intn(3) {
			case 0: // insert
				doc = []byte(string(doc[:at]) + pieces[rng.Intn(len(pieces))] + string(doc[at:]))
			case 1: // delete
				end := min(len(doc), at+rng.Intn(4)+1)
				doc = append(doc[:at:at], doc[end:]...)
			default: // replace
				doc[at] = pieces[rng.Intn(len(pieces))][0]
			}
		}
		if sameTree(t, string(doc)) {
			accepted++
		}
	}
	if accepted == 0 {
		t.Error("no corrupted document was accepted, so the comparison proved nothing")
	}
	t.Logf("%d of 30000 corrupted documents taken by the shortcut, all identical to encoding/xml", accepted)
}
