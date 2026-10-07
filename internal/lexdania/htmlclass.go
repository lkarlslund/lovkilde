package lexdania

import "strings"

// Reading the site's documentHtml back into the canonical tree is a mapping
// problem, not a parsing problem: the payload's <p class="Paragraf"> has to become
// a Paragraf node, because Sections(), Match() and Runs() are all projections over
// canonical element names. Getting that mapping wrong in one direction produces a
// document with no provisions; getting it wrong in the other invents provisions
// that amendments will then be applied to.
//
// So the table below is built from measurement, and each row says which
// measurement supports it:
//
//   - id: the payload carries the canonical XML element GUIDs, so for the paired
//     documents, joining on the id says what a class means. 156 elements matched
//     this way.
//   - text: aligning squeezed text reaches the legacy SGML payloads, which carry no
//     ids at all. 55 elements matched this way, 49 of them unambiguously.
//   - name: the class is a canonical element name (case-insensitively). This covers
//     only 3,751 of the 59,987 measured elements (6.3 %), which is why the table has
//     to be explicit rather than rely on the vocabulary looking self-describing.
//   - layout / presentation: the element carries structure or typography rather
//     than legal meaning.
//
// Two things this table deliberately does not do. It never emits a name outside the
// corpus's own element inventory (testdata/canonical-elements.txt, asserted in the
// test), and it never silently drops an element: an unmapped class comes back as
// roleUnknown, which the builder records rather than discarding. The counts in the
// comments are the measured element counts from testdata/html-vocabulary.txt.

// htmlRole is what an element in a documentHtml payload contributes to the tree.
type htmlRole uint8

const (
	// roleUnknown is the table saying it does not know. The builder records the
	// token on the document; treating an unrecognised class as a plain text run is
	// how a provision quietly stops being a provision.
	roleUnknown         htmlRole = iota
	roleSection                  // a provision or a heading Sections() has to see
	roleMarker                   // the printed number of its parent, as an Explicatus child
	roleText                     // a block of prose: Exitus
	roleInline                   // inline typography: Char, with formaChar when it has a name
	roleTableCell                // Td
	roleTableHeaderCell          // Th
	roleTableRow                 // Tr
	roleTable                    // Table
	roleContainer                // layout wrapper: contributes children, contributes no element
	roleNote                     // footnote text (Nota), or its inline reference
	roleSignature                // UnderskriftGruppe, Underskriver, UnderskriftStedDato, Segl
	roleBreak                    // whitespace or a region boundary: hr, br
	roleSkip                     // presentation or navigation with no canonical counterpart
	// roleCentreret is a centred provision line. Inside the entry-into-force region
	// it is an IkraftCentreretParagraf, the element Sections() reports as appendix
	// material; outside it it is prose with centred typography. Which one a given
	// element is depends on whether an IKraftStreg boundary preceded it, so the
	// classifier cannot answer and hands the decision to the builder.
	roleCentreret
)

// String names a role for log and test output: a coverage report that says
// "role 3: 11,940" is not a report anyone can act on.
func (r htmlRole) String() string {
	switch r {
	case roleUnknown:
		return "unknown"
	case roleSection:
		return "section"
	case roleMarker:
		return "marker"
	case roleText:
		return "text"
	case roleInline:
		return "inline"
	case roleTableCell:
		return "table-cell"
	case roleTableHeaderCell:
		return "table-header"
	case roleTableRow:
		return "table-row"
	case roleTable:
		return "table"
	case roleContainer:
		return "container"
	case roleNote:
		return "note"
	case roleSignature:
		return "signature"
	case roleBreak:
		return "break"
	case roleSkip:
		return "skip"
	case roleCentreret:
		return "centreret"
	}
	return "role?" + string(rune('0'+r))
}

// htmlTarget is one row of the mapping.
type htmlTarget struct {
	Role htmlRole
	Name string // canonical element name; empty for roleContainer, roleBreak, roleSkip
	Attr string // formaChar for roleInline, formaInd for numbered items, "ref" for a footnote reference
	// Reason says why something is dropped. A roleSkip row without one is a silent
	// text-loss bug waiting for someone to notice a paragraph is missing.
	Reason string
	Basis  string // which measurement supports the row
}

// Basis values, so a guess stays distinguishable from a measurement when a row needs
// revisiting. The test rejects anything else.
const (
	basisID           = "id"
	basisText         = "text"
	basisName         = "name"
	basisLayout       = "layout"
	basisPresentation = "presentation"
)

// generatedTOC is shared by the four classes of the site's own table of contents.
const generatedTOC = "generated table of contents, which the canonical XML does not carry"

// htmlClasses maps a lowercased class onto its target: the measured vocabulary of
// the payload corpus (testdata/html-vocabulary.txt).
var htmlClasses = map[string]htmlTarget{
	// Provisions. "paragraf" is the row with a direct id measurement. The legacy
	// SGML spellings are the same element from an older converter: <DIV
	// CLASS=PARAGRAPH> wraps a paragraph and <P CLASS=PARAB> is the paragraph, so
	// the wrapper stays transparent -- mapping both to Paragraf would nest a
	// provision inside itself and number it twice.
	"paragraf":  {Role: roleSection, Name: "Paragraf", Basis: basisID},
	"paragraph": {Role: roleContainer, Basis: basisLayout},
	"parab":     {Role: roleSection, Name: "Paragraf", Basis: basisName},
	// PARA is legacy prose with no provision number of its own; PARAC is the same
	// thing centred, which is why that one stays undecided.
	"para":        {Role: roleText, Name: "Exitus", Basis: basisName},
	"paratemp":    {Role: roleSection, Name: "TilParagraf", Basis: basisName},
	"tilparagraf": {Role: roleSection, Name: "TilParagraf", Basis: basisName},

	// Subsections. Both halves of the modern pair carry their parent's id, which is
	// the measurement that says Stk2 *is* the Stk and StkNr is only its number.
	"stk":  {Role: roleSection, Name: "Stk", Basis: basisID},
	"stk2": {Role: roleSection, Name: "Stk", Basis: basisID},
	// The legacy and mid-century ways of writing an indented item's prose.
	"punkt":     {Role: roleText, Name: "Indentatio", Attr: "Nummer", Basis: basisName},
	"normalind": {Role: roleText, Name: "Exitus", Basis: basisText},

	// Printed numbers. Each of these spans shares its parent element's id, so
	// emitting them as siblings would duplicate the provision they belong to; they
	// become that element's Explicatus instead.
	"paragrafnr":      {Role: roleMarker, Name: "Explicatus", Basis: basisID},
	"stknr":           {Role: roleMarker, Name: "Explicatus", Basis: basisID},
	"liste1nr":        {Role: roleMarker, Name: "Explicatus", Basis: basisID},
	"liste2nr":        {Role: roleMarker, Name: "Explicatus", Basis: basisID},
	"liste3nr":        {Role: roleMarker, Name: "Explicatus", Basis: basisName},
	"aendringnr":      {Role: roleMarker, Name: "Explicatus", Basis: basisName},
	"cparagrafnummer": {Role: roleMarker, Name: "Explicatus", Basis: basisName},
	"nummer":          {Role: roleMarker, Name: "Explicatus", Basis: basisName},

	// Numbered and lettered items inside a subsection. The id measurement is what
	// separates these from plain indentation: canonical numbered items are
	// <Indentatio formaInd="Nummer"> with an Explicatus marker, and a plain
	// Indentatio is only layout -- treating every one as an item would invent
	// provisions, which is the same trap numberedIndentation guards against in XML.
	"liste1":     {Role: roleText, Name: "Indentatio", Attr: "Nummer", Basis: basisID},
	"liste2":     {Role: roleText, Name: "Indentatio", Attr: "Nummer", Basis: basisID},
	"liste3":     {Role: roleText, Name: "Indentatio", Attr: "Nummer", Basis: basisName},
	"plain-list": {Role: roleContainer, Basis: basisLayout},

	// Chapters, afsnit and paragraph groups. The heading arrives as its own element
	// and becomes the container's Rubrica; the number lives on the container, which
	// is what Sections() reads, so mapping a heading onto a second Kapitel would
	// invent a chapter.
	"kapitel":            {Role: roleSection, Name: "Kapitel", Basis: basisID},
	"kapiteloverskrift2": {Role: roleText, Name: "Rubrica", Basis: basisText},
	// The pair the mid-century payloads print inside <DIV CLASS=KAPITEL>, in about 3,450 sampled
	// chapters each: the printed "Kapitel 3" and its heading. Unmapped, every such chapter was a
	// part with no number, so "Overskriften til kapitel 13" had nothing to resolve to.
	"kapitelnummer":     {Role: roleMarker, Name: "Explicatus", Basis: basisName},
	"kapiteloverskrift": {Role: roleText, Name: "Rubrica", Basis: basisName},
	// An afsnit's number and heading, "Afsnit X" and "Investeringsrådgivere", printed the way a
	// chapter's are. Unread, every afsnit of lov om finansiel virksomhed had no number, and "efter
	// afsnit X indsættes" found nothing to go by.
	"afsnitsnummer":            {Role: roleMarker, Name: "Explicatus", Basis: basisName},
	"afsnitsoverskrift":        {Role: roleText, Name: "Rubrica", Basis: basisName},
	"ktitle1":                  {Role: roleText, Name: "Rubrica", Basis: basisName},
	"ktitle2":                  {Role: roleText, Name: "Rubrica", Basis: basisName},
	"atitle1":                  {Role: roleText, Name: "Rubrica", Basis: basisName},
	"afsnit":                   {Role: roleSection, Name: "Afsnit", Basis: basisName},
	"afsnitoverskrift":         {Role: roleText, Name: "Rubrica", Basis: basisName},
	"paragrafgruppeoverskrift": {Role: roleSection, Name: "ParagrafGruppe", Basis: basisName},
	"paragrafgruppe":           {Role: roleSection, Name: "ParagrafGruppe", Basis: basisName},
	"bog":                      {Role: roleSection, Name: "Bog", Basis: basisName},
	"bogoverskrift":            {Role: roleText, Name: "Rubrica", Basis: basisName},
	"bogoverskriftstreg":       {Role: roleBreak, Basis: basisLayout},

	// Preamble and titles. The text measurement found indledning2 landing on
	// Indledning five times and Praeambel once; Indledning is what Sections() looks
	// for, and Praeambel is the rarer spelling of the same prose.
	"indledning":  {Role: roleSection, Name: "Indledning", Basis: basisText},
	"indledning2": {Role: roleSection, Name: "Indledning", Basis: basisText},
	"praeambel":   {Role: roleSection, Name: "Praeambel", Basis: basisName},
	"titel":       {Role: roleSection, Name: "Titel", Basis: basisName},
	"titel2":      {Role: roleSection, Name: "Titel", Basis: basisName},
	"undertitel2": {Role: roleSection, Name: "UnderTitel", Basis: basisName},
	"sub-title":   {Role: roleSection, Name: "UnderTitel", Basis: basisName},
	"title":       {Role: roleSection, Name: "Titel", Basis: basisName},

	// Annexes: Bilag is the annex, BilagIndhold its content.
	"bilag": {Role: roleSection, Name: "Bilag", Basis: basisName},
	// Both spellings are in the corpus: bilagstekst (the genitive, 272 elements) and
	// bilagtekst (25), and the site is not consistent about which it emits.
	"bilagstekst":      {Role: roleText, Name: "BilagIndhold", Basis: basisName},
	"bilagtekst":       {Role: roleText, Name: "BilagIndhold", Basis: basisName},
	"bilagstekstliste": {Role: roleText, Name: "BilagIndhold", Basis: basisName},
	"bilagtekstliste":  {Role: roleText, Name: "BilagIndhold", Basis: basisName},
	"bilagstreg":       {Role: roleBreak, Basis: basisLayout},

	// Entry into force. This is the region Sections() reports as appendix material,
	// and in the HTML the boundary is only visible as a centred rule, so the break
	// has to be a signal rather than noise.
	"ikraftstreg":       {Role: roleBreak, Basis: basisLayout},
	"ikrafttekst":       {Role: roleSection, Name: "Ikraft", Basis: basisName},
	"ikraft":            {Role: roleSection, Name: "Ikraft", Basis: basisName},
	"rykningsklausul":   {Role: roleSection, Name: "Rykningsklausul", Basis: basisText},
	"centreretparagraf": {Role: roleCentreret, Basis: basisName},
	"parac":             {Role: roleCentreret, Basis: basisName},
	"cparagraftekst":    {Role: roleText, Name: "Exitus", Basis: basisName},

	// Amending acts: the "Ændring 1" heading carries the change's number, which is
	// what AendringsNummer means in the canonical tree.
	"aendringmednummer":  {Role: roleSection, Name: "AendringsNummer", Basis: basisName},
	"aendringspunkt":     {Role: roleText, Name: "Exitus", Basis: basisName},
	"aendretbestemmelse": {Role: roleInline, Name: "Char", Basis: basisPresentation},

	// Footnotes. This is the pair the pilot's measurement paid for: 266 of 277 Nota
	// ids in the XML reappear as a name= on the inline marker, which is the only
	// thing tying a footnote to the sentence it qualifies.
	"fodnote": {Role: roleNote, Name: "Nota", Basis: basisText},
	// The endnote form older payloads use, under "Officielle noter": the same note, printed at
	// the end and linked by <a name="FN501">.
	"slutnotetekst": {Role: roleNote, Name: "Nota", Basis: basisName},
	// The same endnote as Word exported it, in 216 payloads: unread, a title note such as the
	// EU directives B20050010005 implements was a stray paragraph after the last annex.
	"msofootnotetext":   {Role: roleNote, Name: "Nota", Basis: basisName},
	"fodnotenr":         {Role: roleNote, Name: "Nota", Attr: "ref", Basis: basisName},
	"fodnotehenvisning": {Role: roleNote, Name: "Nota", Attr: "ref", Basis: basisName},

	// Tables. About a fifth of a modern payload's elements are table markup, which
	// is why the 46 table residuals were the fidelity harness's known hard case.
	// TabelTekst measured onto the Td's text rather than a new element: the div is
	// how the cell's prose is wrapped, not a structure of its own.
	"tabeltekst":      {Role: roleText, Name: "Exitus", Basis: basisText},
	"smalltabeltekst": {Role: roleText, Name: "Exitus", Basis: basisText},
	"tabeltekst2":     {Role: roleText, Name: "Exitus", Basis: basisText},
	"tabeloverskrift": {Role: roleText, Name: "Rubrica", Basis: basisText},
	"containertable":  {Role: roleContainer, Basis: basisLayout},
	"maintable":       {Role: roleContainer, Basis: basisLayout},
	"subtable":        {Role: roleContainer, Basis: basisLayout},
	"tab1":            {Role: roleTable, Name: "Table", Basis: basisName},

	// Signature blocks.
	"givet":           {Role: roleSignature, Name: "UnderskriftStedDato", Basis: basisName},
	"givet-line":      {Role: roleSignature, Name: "UnderskriftStedDato", Basis: basisName},
	"underskrevet-af": {Role: roleSignature, Name: "Underskriver", Basis: basisName},
	"underskrift":     {Role: roleSignature, Name: "UnderskriftGruppe", Basis: basisName},
	"underskriver":    {Role: roleSignature, Name: "Underskriver", Basis: basisName},
	"sign1":           {Role: roleSignature, Name: "Underskriver", Basis: basisName},
	"sign2":           {Role: roleSignature, Name: "Underskriver", Basis: basisName},
	"minister":        {Role: roleSignature, Name: "UnderskriverTekst", Basis: basisName},
	"segl":            {Role: roleSignature, Name: "Segl", Basis: basisName},

	// Hymns (church Acts) and prose or headings that are not provisions.
	"hymne2":             {Role: roleSection, Name: "Hymne", Basis: basisText},
	"tekstoverskrift":    {Role: roleText, Name: "Rubrica", Basis: basisText},
	"tekstoverskriftb":   {Role: roleText, Name: "Rubrica", Basis: basisText}, // a chapter's title in the older payloads: retsplejeloven 2005
	"overskriftstekst1":  {Role: roleText, Name: "Rubrica", Basis: basisName},
	"overskriftstekst3":  {Role: roleText, Name: "Rubrica", Basis: basisName},
	"overskriftsnummer2": {Role: roleMarker, Name: "Explicatus", Basis: basisName},
	"tekst":              {Role: roleText, Name: "Exitus", Basis: basisName},
	"tekst2":             {Role: roleText, Name: "Exitus", Basis: basisName},
	"tekstgenerel":       {Role: roleText, Name: "Exitus", Basis: basisName},

	// Typography. formaChar is how the canonical tree records the same thing, and
	// keeping it matters because MatchText compares the runs an amendment quotes.
	"italic":      {Role: roleInline, Name: "Char", Attr: "Italic", Basis: basisPresentation},
	"bold":        {Role: roleInline, Name: "Char", Attr: "Bold", Basis: basisPresentation},
	"bolditalic":  {Role: roleInline, Name: "Char", Attr: "BoldItalic", Basis: basisPresentation},
	"notbold":     {Role: roleInline, Name: "Char", Basis: basisPresentation},
	"subscript":   {Role: roleInline, Name: "Char", Attr: "Subscript", Basis: basisPresentation},
	"superscript": {Role: roleInline, Name: "Char", Attr: "Superscript", Basis: basisPresentation},

	// Navigation and chrome the canonical XML has no element for. Dropping these is
	// a decision with a stated reason, not an oversight: the content is the site's
	// own table of contents and the "Den fulde tekst" banner, neither of which the
	// XML carries, so keeping them would add text the canonical tree does not have
	// and every comparison against a paired document would then disagree.
	"bjelke":                   {Role: roleSkip, Reason: "site banner, not document text", Basis: basisLayout},
	"indholdsfortegnelse":      {Role: roleSkip, Reason: generatedTOC, Basis: basisLayout},
	"indholdsfortegnelseid":    {Role: roleSkip, Reason: generatedTOC, Basis: basisLayout},
	"indholdsfortegnelsetekst": {Role: roleSkip, Reason: generatedTOC, Basis: basisLayout},
	"toc":                      {Role: roleSkip, Reason: generatedTOC, Basis: basisLayout},
	"lsp8l":                    {Role: roleSkip, Reason: "converter artefact with no content of its own", Basis: basisLayout},
}

// htmlTags maps elements that carry no class -- roughly a quarter of the measured
// 59,987 elements: table cells, inline formatting and bare paragraphs.
var htmlTags = map[string]htmlTarget{
	"td":       {Role: roleTableCell, Name: "Td", Basis: basisLayout},
	"th":       {Role: roleTableHeaderCell, Name: "Th", Basis: basisLayout},
	"tr":       {Role: roleTableRow, Name: "Tr", Basis: basisLayout},
	"table":    {Role: roleTable, Name: "Table", Basis: basisLayout},
	"caption":  {Role: roleText, Name: "Rubrica", Basis: basisLayout},
	"col":      {Role: roleSkip, Reason: "column widths are layout, not text", Basis: basisLayout},
	"colgroup": {Role: roleSkip, Reason: "column widths are layout, not text", Basis: basisLayout},

	"p":       {Role: roleText, Name: "Exitus", Basis: basisLayout},
	"div":     {Role: roleContainer, Basis: basisLayout},
	"span":    {Role: roleInline, Name: "Char", Basis: basisLayout},
	"li":      {Role: roleText, Name: "Indentatio", Basis: basisLayout},
	"dd":      {Role: roleText, Name: "Indentatio", Basis: basisLayout},
	"dt":      {Role: roleText, Name: "Indentatio", Basis: basisLayout},
	"pre":     {Role: roleText, Name: "Exitus", Basis: basisLayout},
	"ul":      {Role: roleContainer, Basis: basisLayout},
	"ol":      {Role: roleContainer, Basis: basisLayout},
	"dl":      {Role: roleContainer, Basis: basisLayout},
	"dir":     {Role: roleContainer, Basis: basisLayout},
	"center":  {Role: roleContainer, Basis: basisLayout},
	"section": {Role: roleContainer, Basis: basisLayout},
	"article": {Role: roleContainer, Basis: basisLayout},
	"main":    {Role: roleContainer, Basis: basisLayout},

	"i":      {Role: roleInline, Name: "Char", Attr: "Italic", Basis: basisPresentation},
	"em":     {Role: roleInline, Name: "Char", Attr: "Italic", Basis: basisPresentation},
	"b":      {Role: roleInline, Name: "Char", Attr: "Bold", Basis: basisPresentation},
	"strong": {Role: roleInline, Name: "Char", Attr: "Bold", Basis: basisPresentation},
	"sup":    {Role: roleInline, Name: "Char", Attr: "Superscript", Basis: basisPresentation},
	"sub":    {Role: roleInline, Name: "Char", Attr: "Subscript", Basis: basisPresentation},
	"u":      {Role: roleInline, Name: "Char", Basis: basisPresentation},
	"font":   {Role: roleInline, Name: "Char", Basis: basisPresentation},
	"big":    {Role: roleInline, Name: "Char", Basis: basisPresentation},
	"small":  {Role: roleInline, Name: "Char", Basis: basisPresentation},
	"nobr":   {Role: roleInline, Name: "Char", Basis: basisPresentation},
	"strike": {Role: roleInline, Name: "Char", Basis: basisPresentation},
	// A bare link is text with a target; the payload keeps the target elsewhere, and
	// footnote links are matched by class above.
	"a": {Role: roleInline, Name: "Char", Basis: basisPresentation},

	// Headings that arrive as bare h1-h6 carry no class at all, which is how the
	// site's own converter writes some generations' section titles. Rubrica is what
	// the class table maps every other heading class to, so the reading is the same
	// one the editor's class would have selected; only its origin is the tag.
	"h1": {Role: roleText, Name: "Rubrica", Basis: basisLayout},
	"h2": {Role: roleText, Name: "Rubrica", Basis: basisLayout},
	"h3": {Role: roleText, Name: "Rubrica", Basis: basisLayout},
	"h4": {Role: roleText, Name: "Rubrica", Basis: basisLayout},
	"h5": {Role: roleText, Name: "Rubrica", Basis: basisLayout},
	"h6": {Role: roleText, Name: "Rubrica", Basis: basisLayout},

	// Table sections: structural wrappers with no canonical element of their own.
	// Their rows must land in the Table, so they get nothing but a place to nest.
	"thead":      {Role: roleContainer, Basis: basisLayout},
	"tbody":      {Role: roleContainer, Basis: basisLayout},
	"tfoot":      {Role: roleContainer, Basis: basisLayout},
	"address":    {Role: roleContainer, Basis: basisLayout},
	"figure":     {Role: roleContainer, Basis: basisLayout},
	"figcaption": {Role: roleText, Name: "Rubrica", Basis: basisLayout},

	// Revision markup. The vocabulary has no element for "inserted" or "deleted"
	// inside a provision, so the text is kept as ordinary inline text: dropping a
	// deletion would silently change the quoted statute, and inventing an element
	// name would produce a tree nothing downstream can read.
	"ins":   {Role: roleInline, Name: "Char", Basis: basisPresentation},
	"del":   {Role: roleInline, Name: "Char", Basis: basisPresentation},
	"s":     {Role: roleInline, Name: "Char", Basis: basisPresentation},
	"mark":  {Role: roleInline, Name: "Char", Basis: basisPresentation},
	"label": {Role: roleInline, Name: "Char", Basis: basisPresentation},

	"br":       {Role: roleBreak, Basis: basisLayout},
	"hr":       {Role: roleBreak, Basis: basisLayout},
	"img":      {Role: roleSkip, Reason: "image bytes are not text; PDF links are in the payload", Basis: basisLayout},
	"style":    {Role: roleSkip, Reason: "stylesheet, not document text", Basis: basisLayout},
	"script":   {Role: roleSkip, Reason: "script, not document text", Basis: basisLayout},
	"noscript": {Role: roleSkip, Reason: "script fallback, not document text", Basis: basisLayout},
}

// classifyHTML says what one payload element becomes. The class wins over the tag,
// because the class is the semantic claim and the tag is only the container it was
// rendered in -- and where they disagree, the class is what the site's editor set.
func classifyHTML(tag, class string) htmlTarget {
	if class == "" {
		return htmlTagFallback(tag)
	}
	key := strings.ToLower(class)
	t, ok := htmlClasses[key]
	if !ok {
		t, ok = classifyHTMLFamily(key)
	}
	// A class attribute can hold several names, and the measured corpus does it in
	// both directions -- "Bold Italic Subscript" carries typography that the table
	// knows individually. The first one the table recognises wins, because semantics
	// outrank typography and the editor wrote them in that order; only the whole-value
	// lookup failing gets here, so a single-class payload never changes behaviour.
	if !ok && strings.ContainsAny(class, " \t") {
		for _, field := range strings.Fields(key) {
			if field == "" {
				continue
			}
			if t2, ok2 := htmlClasses[field]; ok2 {
				t, ok = t2, true
				break
			}
			if t2, ok2 := classifyHTMLFamily(field); ok2 {
				t, ok = t2, true
				break
			}
		}
	}
	if ok {
		// Footnote references and footnote markers deliberately stay the same target
		// here. The tag cannot tell them apart -- in the legacy generation the note's
		// own marker is <a class="FodnoteNr" name="Not1" href="#Henvisning_Not1">,
		// the same tag a reference uses, and in the modern one the marker is a span
		// while the reference is a link. What distinguishes them is being inside the
		// note paragraph, which is context this function never sees, so the builder
		// decides (see htmlBuilder.start).
		return t
	}
	// A class the table does not know is reported even though the tag would have
	// given an acceptable default: the element's text still lands in the right
	// place via htmlTagFallback, but folding an unknown semantic claim into that
	// default is how a new provision type stops being a provision.
	return htmlTarget{Role: roleUnknown}
}

// htmlTagFallback is the default for an element with no class -- about a quarter of
// the measured corpus -- and what the builder uses for an element whose class is
// unknown, so unknown means "record this", not "lose the text".
func htmlTagFallback(tag string) htmlTarget {
	if t, ok := htmlTags[strings.ToLower(tag)]; ok {
		return t
	}
	return htmlTarget{Role: roleUnknown}
}

// classifyHTMLFamily covers the numbered variants of a few families, which is how
// the site writes a heading level into a class name (Titel2, Hymne2, Liste3,
// TabelTekst2, Overskriftstekst3, KTitle1). Trailing digits are the only variation
// the mapping generalises over; anything else has to be added explicitly so a new
// shape gets reviewed instead of absorbed.
func classifyHTMLFamily(class string) (htmlTarget, bool) {
	stem := strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return -1
		}
		return r
	}, class)
	// Only digit variation is generalised over: Titel3 is Titel, Titelx is not.
	if stem == class || stem == "" {
		return htmlTarget{}, false
	}
	// A numbered "…Nr" is the marker of its family whatever that family is, and the
	// digits can sit inside it ("Liste1Nr").
	if strings.HasSuffix(stem, "nr") {
		return htmlTarget{Role: roleMarker, Name: "Explicatus", Basis: basisName}, true
	}
	families := map[string]htmlTarget{
		"titel":            {Role: roleSection, Name: "Titel", Basis: basisName},
		"hymne":            {Role: roleSection, Name: "Hymne", Basis: basisName},
		"bilagstekst":      {Role: roleText, Name: "BilagIndhold", Basis: basisName},
		"bilagtekst":       {Role: roleText, Name: "BilagIndhold", Basis: basisName},
		"tabeltekst":       {Role: roleText, Name: "Exitus", Basis: basisName},
		"smalltabeltekst":  {Role: roleText, Name: "Exitus", Basis: basisName},
		"liste":            {Role: roleText, Name: "Indentatio", Attr: "Nummer", Basis: basisName},
		"sign":             {Role: roleSignature, Name: "Underskriver", Basis: basisName},
		"overskriftstekst": {Role: roleText, Name: "Rubrica", Basis: basisName},
		"ktitle":           {Role: roleText, Name: "Rubrica", Basis: basisName},
		"atitle":           {Role: roleText, Name: "Rubrica", Basis: basisName},
		"tab":              {Role: roleTable, Name: "Table", Basis: basisName},
		// "Stk0" holds a provision's opening text, "<b>§ 1.</b> Denne lov …", and its later
		// subsections; read as prose, lov om finansiel virksomhed 2006 had no § 1.
		"stk": {Role: roleSection, Name: "Stk", Basis: basisName},
	}
	if t, ok := families[stem]; ok {
		return t, true
	}
	return htmlTarget{}, false
}
