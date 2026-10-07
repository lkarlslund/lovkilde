package lexdoc

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// Text handling has two distinct jobs, and conflating them causes the most damaging class
// of bug in a consolidation engine.
//
// Display text keeps the document's own typography and is what a reader should see. Match
// text is a normalised form used only to test whether an amendment's quoted passage is
// present in the target. Normalising display text would corrupt the rendering; matching on
// raw text fails constantly, because an amendment quotes "§ 267, stk. 1," as one string
// while the source stores it as three formatting runs with newlines and indentation
// between them.
//
// These rules live here rather than in a reader because they are about Danish legal text,
// not about the register's markup: the XML front end and the HTML front end must normalise
// identically, or a provision quoted from one source fails to match in the other.

// CollapseBlankLines squeezes blank lines produced by block rendering.
func CollapseBlankLines(s string) string {
	lines := strings.Split(s, "\n")
	out := lines[:0]
	for _, l := range lines {
		if strings.TrimSpace(l) == "" {
			continue
		}
		out = append(out, l)
	}
	return strings.Join(out, "\n")
}

// Squeeze collapses all whitespace runs to single spaces and trims the ends.
//
// It is strings.Join(strings.Fields(s), " ") in one pass, and returns s itself when there is
// nothing to squeeze — the usual case, since most text reaching it has been squeezed once
// already. Joining the fields allocated twice per call and was a tenth of an artifact build.
func Squeeze(s string) string {
	if squeezed(s) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		// A word is copied whole; a separator is skipped and stands as one space before the
		// next word.
		j := i
		for j < len(s) {
			c := s[j]
			if c < utf8.RuneSelf {
				if asciiSpace[c] {
					break
				}
				j++
				continue
			}
			r, size := utf8.DecodeRuneInString(s[j:])
			if unicode.IsSpace(r) {
				break
			}
			j += size
		}
		if j > i {
			if b.Len() > 0 {
				b.WriteByte(' ')
			}
			b.WriteString(s[i:j])
			i = j
			continue
		}
		_, size := utf8.DecodeRuneInString(s[i:])
		i += size
	}
	return b.String()
}

// squeezed says whether s is already its own squeezed form: no leading or trailing space,
// no space other than a single ' ' between words.
func squeezed(s string) bool {
	prevSpace := true
	for i := 0; i < len(s); {
		c := s[i]
		if c < utf8.RuneSelf {
			if asciiSpace[c] {
				if c != ' ' || prevSpace {
					return false
				}
				prevSpace = true
			} else {
				prevSpace = false
			}
			i++
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if unicode.IsSpace(r) {
			return false
		}
		prevSpace = false
		i += size
	}
	return !prevSpace || len(s) == 0
}

// asciiSpace is the ASCII part of unicode.IsSpace, as strings.Fields uses it.
var asciiSpace = [utf8.RuneSelf]bool{'\t': true, '\n': true, '\v': true, '\f': true, '\r': true, ' ': true}

// MatchText is the normalised form used for locating quoted passages. Each rule in
// MatchRunes fixes a real difference between how an amendment quotes a passage and how the
// target stores it: non-breaking and thin spaces, curly quotes versus the » « guillemets
// this corpus prefers, soft hyphens from justified typesetting, and whitespace broken
// freely across lines and formatting runs.
func MatchText(s string) string {
	norm, _ := MatchRunes(s)
	return string(norm)
}

// MatchRunes normalises s and records, for every normalised rune, the rune index in s that
// produced it.
//
// The index map is what makes replacement possible at all: a located match has to be
// spliced back into the original text with its typography intact, and once whitespace has
// been collapsed and soft hyphens removed, offsets in the normalised form no longer mean
// anything without a map back.
func MatchRunes(s string) ([]rune, []int) {
	src := []rune(s)
	norm := make([]rune, 0, len(src))
	idx := make([]int, 0, len(src))
	prevSpace := true // suppresses leading whitespace
	for i, r := range src {
		switch {
		case unicode.IsSpace(r):
			r = ' '
		case r == 0x00ad: // soft hyphen disappears entirely, mapping nothing
			continue
		case r == 0x200b || r == 0x200c || r == 0x200d || r == 0xfeff:
			// Zero-width space, non-joiner, joiner and BOM: typesetting marks the
			// archive carries inside words — "H‌øjesteret" — found in 2,953 to 145
			// documents each by a whole-archive hunt, against 7,564 for the soft hyphen
			// above. A drafter quoting a provision does not reproduce them, so the
			// comparison form drops them the same way; leaving them in means the quoted
			// passage is "not present" and the change never happens.
			continue
		case r == 0x201c || r == 0x201e:
			r = 0x00bb // left/right double quote variants → »
		case r == 0x201d || r == 0x201f:
			r = 0x00ab
		case r == 0x2018 || r == 0x2019:
			r = '\''
		case r >= 0x2010 && r <= 0x2014 || r == 0x2212:
			// Hyphens and dashes: "legepladsredskaber og –underlag" is quoted with the dash the
			// drafter's word processor made, and the law prints a plain hyphen.
			r = '-'
		}
		if r == ' ' && prevSpace {
			continue
		}
		norm = append(norm, r)
		idx = append(idx, i)
		prevSpace = r == ' '
	}
	for len(norm) > 0 && norm[len(norm)-1] == ' ' {
		norm = norm[:len(norm)-1]
		idx = idx[:len(idx)-1]
	}
	return norm, idx
}

// RuneIndex finds needle in haystack and returns the rune offset, or -1.
//
// strings.Index would return a byte offset, and this text is Danish: mixing byte and rune
// offsets into splice arithmetic corrupts the characters around the edit point. Provisions
// are kilobytes, not gigabytes, so the plain scan is the right trade.
func RuneIndex(haystack, needle []rune) int {
	if len(needle) == 0 || len(needle) > len(haystack) {
		return -1
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		match := true
		for j, r := range needle {
			if haystack[i+j] != r {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}

// QuoteStrip removes the Danish quotation marks an amendment uses to delimit a passage.
// Both marks are stripped from both ends because a quoted passage may legitimately contain
// quotes of its own.
func QuoteStrip(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "»")
	s = strings.TrimSuffix(s, "«")
	s = strings.TrimPrefix(s, "\"")
	s = strings.TrimSuffix(s, "\"")
	return strings.TrimSpace(s)
}
