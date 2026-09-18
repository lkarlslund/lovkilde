package lexdania

import "regexp"

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
func hasSGMLAttrs(src string) bool { return sgmlQuotedAttr.MatchString(src) }

// unquoteSGML turns attr=""V"" into attr="V". The pattern is anchored on an
// attribute name rather than on `=""` alone so that prose containing a doubled
// quote stays untouched: only something that already reads as an attribute
// assignment is rewritten.
func unquoteSGML(src string) string {
	if !hasSGMLAttrs(src) {
		return src
	}
	return sgmlQuotedAttr.ReplaceAllString(src, `$1="$2"`)
}
