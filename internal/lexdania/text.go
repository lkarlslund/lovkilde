package lexdania

import "github.com/lkarlslund/lovkilde/internal/lexdoc"

// The text rules moved to lexdoc, because they are rules about Danish legal text rather than
// about the register's markup: the XML front end and the HTML front end must normalise
// identically, or a provision quoted from one source fails to match in the other. These
// aliases keep the reader's own vocabulary working while the code below it is rewritten
// against the model, and they are why there is still exactly one normaliser.
//
// Text handling has two distinct jobs, and conflating them causes the most damaging class of
// bug in a consolidation engine. Display text keeps the document's own typography and is what
// a reader should see. Match text is a normalised form used only to test whether an
// amendment's quoted passage is present in the target. Normalising display text would corrupt
// the archive's rendering; matching on raw text fails constantly, because an amendment quotes
// "§ 267, stk. 1," as one string while the target stores it as three <Char> runs split by
// formatting, with newlines and indentation between them.

// Squeeze collapses all whitespace runs to single spaces and trims the ends.
func Squeeze(s string) string { return lexdoc.Squeeze(s) }

// DisplayText is the readable form of a node, whitespace-normalised but otherwise faithful to
// the source.
func DisplayText(n *Node) string { return lexdoc.Squeeze(n.PlainText()) }

// MatchText is the normalised form used for locating quoted passages.
func MatchText(s string) string { return lexdoc.MatchText(s) }

// matchRunes normalises s and records, for every normalised rune, the rune index in s that
// produced it, which is what lets a located match be spliced back into the tree.
func matchRunes(s string) ([]rune, []int) { return lexdoc.MatchRunes(s) }

// runeIndex finds needle in haystack and returns the rune offset, or -1.
func runeIndex(haystack, needle []rune) int { return lexdoc.RuneIndex(haystack, needle) }

// QuoteStrip removes the Danish quotation marks an amendment uses to delimit a passage.
func QuoteStrip(s string) string { return lexdoc.QuoteStrip(s) }

// collapseBlankLines squeezes blank lines produced by block rendering.
func collapseBlankLines(s string) string { return lexdoc.CollapseBlankLines(s) }
