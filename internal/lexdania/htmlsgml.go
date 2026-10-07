package lexdania

import (
	"regexp"
	"strings"
)

// The oldest payloads are not HTML but SGML rendered as HTML: attribute values are
// quoted with *doubled* quotes, as in name=""P1"" or CLASS=""CentreretLille"".
// No HTML tokenizer reads that as a value. x/net/html stops the value at the first
// inner quote, so it keeps `name=""` and silently discards P1 and the rest of the
// tag -- the loss is invisible unless the attribute was the only thing tying an
// element to its canonical counterpart, which is exactly what NAME=""P1"" does
// (measured: 224 of 818 anchor names in the pilot's legacy payloads).
//
// Rewriting the quotes before tokenizing is preferred over teaching a tokenizer,
// because the alternative is hand-parsing tags, and the measurement that decided
// the tokenizer question showed a hand-written tokenizer recovers exactly what
// x/net/html recovers once the quoting is fixed.
var sgmlQuotedAttr = regexp.MustCompile(`([A-Za-z_:][-A-Za-z0-9_:.]*)\s*=\s*""([^"]*)""`)

// hasSGMLAttrs reports whether src uses doubled-quote attributes at all. The
// rewrite is opt-in per payload so modern payloads -- the overwhelming majority --
// are tokenized byte-for-byte as fetched, which is what makes "the pre-pass cannot
// have touched this document" a claim anyone can check.
//
// It answers exactly what sgmlQuotedAttr.MatchString answers, without running the pattern over
// the payload: an unanchored pattern that may start at any letter was a sixth of a whole
// artifact build, spent mostly on modern payloads that cannot match. Every match holds a
// doubled quote, which a substring search finds at memory speed, so only those places are
// looked at: a name and an equals sign behind, and a closing doubled quote ahead.
func hasSGMLAttrs(src string) bool {
	for from := 0; ; {
		i := strings.Index(src[from:], `""`)
		if i < 0 {
			return false
		}
		if _, ok := sgmlMatchAt(src, from+i, 0); ok {
			return true
		}
		from += i + 1
	}
}

// isRegexpSpace is \s in Go's regexp syntax: tab, newline, form feed, carriage return, space.
func isRegexpSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\f' || c == '\r'
}

func isNameStart(c byte) bool {
	return c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c == '_' || c == ':'
}

func isNameChar(c byte) bool {
	return isNameStart(c) || c >= '0' && c <= '9' || c == '-' || c == '.'
}

// unquoteSGML turns attr=""V"" into attr="V". The pattern is anchored on an
// attribute name rather than on `=""` alone so that prose containing a doubled
// quote stays untouched: only something that already reads as an attribute
// assignment is rewritten.
//
// It writes what sgmlQuotedAttr.ReplaceAllString(src, `$1="$2"`) writes, by the same scan
// hasSGMLAttrs makes: the regexp replacement was a sixth of an artifact build, because the
// payloads that need it are many. Matches are taken left to right without overlap, as the
// regexp takes them; each keeps its name, loses the spaces around the equals sign, and has
// its doubled quotes made single.
func unquoteSGML(src string) string {
	var b strings.Builder
	copied := 0 // src[:copied] is in b
	for from := 0; ; {
		i := strings.Index(src[from:], `""`)
		if i < 0 {
			break
		}
		open := from + i
		m, ok := sgmlMatchAt(src, open, copied)
		if !ok {
			from = open + 1
			continue
		}
		if b.Len() == 0 {
			b.Grow(len(src))
		}
		b.WriteString(src[copied:m.nameStart])
		b.WriteString(src[m.nameStart:m.nameEnd])
		b.WriteString(`="`)
		b.WriteString(src[open+2 : m.valueEnd])
		b.WriteByte('"')
		copied = m.valueEnd + 2
		from = copied
	}
	if copied == 0 {
		return src
	}
	b.WriteString(src[copied:])
	return b.String()
}

// sgmlMatch is where one name=""value"" sits: the name, and the end of the value, after which
// the closing doubled quote follows.
type sgmlMatch struct {
	nameStart, nameEnd, valueEnd int
}

// sgmlMatchAt reads the match whose opening doubled quote is at open, if there is one. The
// name may not reach back before floor, the end of the previous match. The name starts at the
// first character of the run that can start one, which is where a leftmost regexp match begins.
func sgmlMatchAt(src string, open, floor int) (sgmlMatch, bool) {
	j := open - 1
	for j >= floor && isRegexpSpace(src[j]) {
		j--
	}
	if j < floor || src[j] != '=' {
		return sgmlMatch{}, false
	}
	j--
	for j >= floor && isRegexpSpace(src[j]) {
		j--
	}
	nameEnd := j + 1
	start := -1
	for ; j >= floor && isNameChar(src[j]); j-- {
		if isNameStart(src[j]) {
			start = j
		}
	}
	if start < 0 {
		return sgmlMatch{}, false
	}
	end := strings.IndexByte(src[open+2:], '"')
	if end < 0 || open+2+end+1 >= len(src) || src[open+2+end+1] != '"' {
		return sgmlMatch{}, false
	}
	return sgmlMatch{nameStart: start, nameEnd: nameEnd, valueEnd: open + 2 + end}, true
}
