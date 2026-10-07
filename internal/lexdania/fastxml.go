package lexdania

import (
	"bytes"
	"encoding/xml"
	"strings"
	"unicode/utf8"
)

// fastTree reads the document tree the way decodeElement reads it from encoding/xml, for the
// subset of XML the register actually serves: a UTF-8 declaration, elements and attributes with
// plain names, double- or single-quoted values, the five predefined entities and the HTML ones,
// decimal and hex character references, comments and processing instructions, and CRLF line ends.
//
// It is not a second XML parser so much as a shortcut past the first one. Anything outside that
// subset — a namespace, a DOCTYPE, CDATA, a non-UTF-8 declaration, an unknown entity, a
// character XML does not allow, a mismatched end tag — makes it give up (ok false), and Parse
// reads the document with encoding/xml as before, errors included. So the shortcut only has to
// agree with encoding/xml on what it accepts, and TestFastXMLIsEncodingXML holds it to that.
//
// encoding/xml's tokenizer was two fifths of an artifact build's CPU, reading byte by byte
// through an interface and allocating a token per tag.
func fastTree(data []byte) (*Node, bool) {
	p := &fastParser{src: data}
	for {
		p.skipSpace()
		if p.i >= len(p.src) {
			return nil, false // no root; encoding/xml says which error
		}
		if p.src[p.i] != '<' {
			// Text before the root: encoding/xml hands it over as character data, which
			// Parse ignores, but it still has to be well-formed text.
			if _, ok := p.text(); !ok {
				return nil, false
			}
			continue
		}
		if p.has("<?") {
			if !p.procInst() {
				return nil, false
			}
			continue
		}
		if p.has("<!--") {
			if !p.comment() {
				return nil, false
			}
			continue
		}
		if p.has("<!") || p.has("</") {
			return nil, false
		}
		p.i++ // '<'
		root, ok := p.element()
		if !ok {
			return nil, false
		}
		return root, true
	}
}

type fastParser struct {
	src []byte
	i   int
	buf []byte // scratch for text that needs decoding
}

func (p *fastParser) has(s string) bool {
	return len(p.src)-p.i >= len(s) && string(p.src[p.i:p.i+len(s)]) == s
}

// isXMLSpace is the whitespace encoding/xml skips between markup: space, tab, CR, LF.
func isXMLSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\r' || c == '\n' }

func (p *fastParser) skipSpace() {
	for p.i < len(p.src) && isXMLSpace(p.src[p.i]) {
		p.i++
	}
}

// name reads an element or attribute name. Only ASCII letters, digits, '_', '-' and '.' are
// accepted; a colon (a namespace) or anything non-ASCII is left to encoding/xml.
func (p *fastParser) name() (string, bool) {
	start := p.i
	for p.i < len(p.src) {
		c := p.src[p.i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '_' || p.i > start && (c >= '0' && c <= '9' || c == '-' || c == '.') {
			p.i++
			continue
		}
		break
	}
	if p.i == start || p.i < len(p.src) && (p.src[p.i] == ':' || p.src[p.i] >= utf8.RuneSelf) {
		return "", false
	}
	return string(p.src[start:p.i]), true
}

// element reads one element after its '<', children included, as decodeElement builds it.
func (p *fastParser) element() (*Node, bool) {
	name, ok := p.name()
	if !ok {
		return nil, false
	}
	n := &Node{Name: name}
	var attrs []string // key, value pairs until the map is made
	selfClosing := false
	for {
		p.skipSpace()
		if p.i >= len(p.src) {
			return nil, false
		}
		c := p.src[p.i]
		if c == '>' {
			p.i++
			break
		}
		if c == '/' {
			if p.i+1 >= len(p.src) || p.src[p.i+1] != '>' {
				return nil, false
			}
			p.i += 2
			selfClosing = true
			break
		}
		key, ok := p.name()
		if !ok || strings.HasPrefix(key, "xmlns") {
			return nil, false
		}
		p.skipSpace()
		if p.i >= len(p.src) || p.src[p.i] != '=' {
			return nil, false
		}
		p.i++
		p.skipSpace()
		if p.i >= len(p.src) || p.src[p.i] != '"' && p.src[p.i] != '\'' {
			return nil, false
		}
		quote := p.src[p.i]
		p.i++
		val, ok := p.decode(quote)
		if !ok {
			return nil, false
		}
		p.i++ // the closing quote
		attrs = append(attrs, key, val)
	}
	n.Attrs = make(map[string]string, len(attrs)/2)
	for k := 0; k < len(attrs); k += 2 {
		n.Attrs[attrs[k]] = attrs[k+1]
	}
	if selfClosing {
		n.Runs = []string{""}
		return n, true
	}

	// Content: text runs between child elements; a comment or processing instruction is
	// dropped without closing the run, as decodeElement drops them.
	var run strings.Builder
	closeRun := func() {
		n.Runs = append(n.Runs, stripZeroWidth(run.String()))
		run.Reset()
	}
	for {
		if p.i >= len(p.src) {
			return nil, false
		}
		if p.src[p.i] != '<' {
			t, ok := p.text()
			if !ok {
				return nil, false
			}
			run.WriteString(t)
			continue
		}
		switch {
		case p.has("</"):
			p.i += 2
			end, ok := p.name()
			if !ok || end != name {
				return nil, false
			}
			p.skipSpace()
			if p.i >= len(p.src) || p.src[p.i] != '>' {
				return nil, false
			}
			p.i++
			closeRun()
			return n, true
		case p.has("<!--"):
			if !p.comment() {
				return nil, false
			}
		case p.has("<?"):
			if !p.procInst() {
				return nil, false
			}
		case p.has("<!"):
			return nil, false // CDATA or a declaration
		default:
			p.i++
			child, ok := p.element()
			if !ok {
				return nil, false
			}
			closeRun()
			n.Elems = append(n.Elems, child)
		}
	}
}

// text reads character data up to the next '<'.
func (p *fastParser) text() (string, bool) {
	return p.decode('<')
}

// decode reads up to stop ('<' for text, the quote for an attribute value), leaving p.i on it,
// and returns the text as encoding/xml delivers it: entities and character references replaced,
// CR LF and a lone CR made LF. Text with none of those is copied in one piece.
func (p *fastParser) decode(stop byte) (string, bool) {
	start := p.i
	plain := true
	for p.i < len(p.src) {
		c := p.src[p.i]
		if c == stop {
			break
		}
		switch {
		case c == '&' || c == '\r':
			plain = false
			p.i++
		case c == '<':
			return "", false // a '<' inside a quoted value
		case c == '>' && stop == '<' && p.i >= start+2 && p.src[p.i-1] == ']' && p.src[p.i-2] == ']':
			return "", false // "]]>" outside CDATA, which strict encoding/xml refuses
		case c < 0x20:
			if c != '\t' && c != '\n' {
				return "", false
			}
			p.i++
		case c < utf8.RuneSelf:
			p.i++
		default:
			r, size := utf8.DecodeRune(p.src[p.i:])
			if r == utf8.RuneError && size == 1 || !isXMLChar(r) {
				return "", false
			}
			p.i += size
		}
	}
	if p.i >= len(p.src) {
		return "", false
	}
	seg := p.src[start:p.i]
	if plain {
		return string(seg), true
	}
	p.buf = p.buf[:0]
	for k := 0; k < len(seg); k++ {
		c := seg[k]
		switch c {
		case '\r':
			p.buf = append(p.buf, '\n')
			if k+1 < len(seg) && seg[k+1] == '\n' {
				k++
			}
		case '&':
			semi := bytes.IndexByte(seg[k+1:], ';')
			if semi < 0 {
				return "", false
			}
			rep, ok := entity(string(seg[k+1 : k+1+semi]))
			if !ok {
				return "", false
			}
			p.buf = append(p.buf, rep...)
			k += semi + 1
		default:
			p.buf = append(p.buf, c)
		}
	}
	return string(p.buf), true
}

// entity resolves what stands between '&' and ';' the way strict encoding/xml with
// Entity = xml.HTMLEntity does, or reports that it would not.
func entity(name string) (string, bool) {
	switch name {
	case "lt":
		return "<", true
	case "gt":
		return ">", true
	case "amp":
		return "&", true
	case "apos":
		return "'", true
	case "quot":
		return `"`, true
	}
	if strings.HasPrefix(name, "#") {
		return charRef(name[1:])
	}
	if !validEntityName(name) {
		return "", false
	}
	v, ok := xml.HTMLEntity[name]
	return v, ok
}

// charRef reads a decimal or x-prefixed hex character reference.
func charRef(s string) (string, bool) {
	base := 10
	if strings.HasPrefix(s, "x") {
		base, s = 16, s[1:]
	}
	if s == "" || len(s) > 8 {
		return "", false
	}
	var r rune
	for i := 0; i < len(s); i++ {
		c := s[i]
		var d rune
		switch {
		case c >= '0' && c <= '9':
			d = rune(c - '0')
		case base == 16 && c >= 'a' && c <= 'f':
			d = rune(c-'a') + 10
		case base == 16 && c >= 'A' && c <= 'F':
			d = rune(c-'A') + 10
		default:
			return "", false
		}
		r = r*rune(base) + d
	}
	if !isXMLChar(r) {
		return "", false
	}
	return string(r), true
}

func validEntityName(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || i > 0 && c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}

// isXMLChar is XML's Char production, which strict encoding/xml enforces on text.
func isXMLChar(r rune) bool {
	return r == 0x09 || r == 0x0A || r == 0x0D ||
		r >= 0x20 && r <= 0xD7FF || r >= 0xE000 && r <= 0xFFFD || r >= 0x10000 && r <= 0x10FFFF
}

// comment skips "<!-- … -->". A "--" inside, which XML forbids, is left to encoding/xml.
func (p *fastParser) comment() bool {
	p.i += 4
	end := bytes.Index(p.src[p.i:], []byte("--"))
	if end < 0 || p.i+end+2 >= len(p.src) || p.src[p.i+end+2] != '>' {
		return false
	}
	p.i += end + 3
	return true
}

// procInst skips "<? … ?>". The XML declaration must declare UTF-8 or nothing: encoding/xml
// refuses any other encoding without a charset reader, and so must the shortcut.
func (p *fastParser) procInst() bool {
	end := bytes.Index(p.src[p.i+2:], []byte("?>"))
	if end < 0 {
		return false
	}
	end += 2 // from p.i, past the "<?" the search started after
	body := string(p.src[p.i+2 : p.i+end])
	target := body
	if k := strings.IndexAny(body, " \t\r\n"); k >= 0 {
		target = body[:k]
	}
	if !validEntityName(target) {
		return false // not a plain name; encoding/xml decides
	}
	if target == "xml" {
		if v := declaredAttr(body, "version"); v != "" && v != "1.0" {
			return false
		}
		if enc := declaredAttr(body, "encoding"); enc != "" && !strings.EqualFold(enc, "utf-8") {
			return false
		}
	}
	p.i += end + 2
	return true
}

// declaredAttr reads one pseudo-attribute of the XML declaration, or "?" when it is written in
// a form the shortcut does not read.
func declaredAttr(decl, name string) string {
	i := strings.Index(decl, name)
	if i < 0 {
		return ""
	}
	rest := strings.TrimLeft(decl[i+len(name):], " \t\r\n")
	if !strings.HasPrefix(rest, "=") {
		return "?" // not a form the shortcut reads; encoding/xml decides
	}
	rest = strings.TrimLeft(rest[1:], " \t\r\n")
	if rest == "" || rest[0] != '"' && rest[0] != '\'' {
		return "?"
	}
	q := rest[0]
	end := strings.IndexByte(rest[1:], q)
	if end < 0 {
		return "?"
	}
	return rest[1 : 1+end]
}
