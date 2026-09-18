# retsin — Danish legislation mirror

Mirrors [retsininformation.dk](https://www.retsinformation.dk) into a local archive of
canonical ELI XML, and keeps itself current through Civilstyrelsens official change
feed. Written for a full bootstrap of ~200k documents plus years of daily updates,
without getting rate-limited or blocked.

## Status

Bootstrap has **not** been run yet. The pipeline is verified end-to-end against
production (inventory, harvest feed, document fetch, resume, state rebuild), but the
full ~200k-document collection is deliberately left for a human to start — see
[Running it](#running-it).

## What the site actually offers

Three distinct channels, measured rather than assumed:

| Channel | Gives | Limit |
|---|---|---|
| **ELI sitemap** `/eli/sitemap.xml` | Full catalogue: **203,152** ELI URIs (+`lastmod`) across 21 pages of 10k | ~22 requests for the *entire* catalogue |
| **ELI XML** `/eli/{path}/xml` | Canonical XSD-validated document text + metadata (~110 KB avg) | uncached (`cf-cache-status: DYNAMIC`) — every hit is origin work |
| **Harvest API** `api.retsinformation.dk/v1/Documents` | Rolling **10-day** change feed | **published: max 1 call / 10 s** → HTTP 429 |

### Rate limits (published)

The harvesting service documents its own limit in its OpenAPI description
(`https://api.retsinformation.dk/swagger/v1/swagger.json`):

> *"Servicen er kaldt for ofte. Den må kun kaldes en gang pr. 10 sekunder."*

It also states the intended usage is **one call per day**, and that the `date`
argument must fall within the last 10 days. Confirmed empirically: ~8 rapid requests
returned a real `429 Too Many Requests`.

The general terms at <https://www.retsinformation.dk/api> grant free, non-exclusive
use of the open data, but require that use *"ikke belaster Civilstyrelsens
datagrænseflade på en uhensigtsmæssig eller uforholdsmæssig måde"* — must not burden
the interface disproportionately. Cloudflare fronts the site, so a block would be
IP-wide. **There is no published limit for document requests**, which is exactly why
this tool imposes its own throttle there.

### Two facts that shaped the design

- `GET /eli/{any-ELI-path}/xml` serves canonical XML for **every** ELI kind
  (`retsinfo`, `lta`, `ft`, `fob`, `mt`, `accn`). No resolution request is needed to
  find an accession first, so the crawl is exactly **1 request per resource**.
- `eli/retsinfo/2026/10019/xml` and `eli/accn/W20261001925/xml` return
  **byte-identical** documents, so accessions are the natural deduplication key.

### XML vs the website's JSON

The site itself uses `POST /api/document/eli/{path}` (body `{}`), returning
`documentHtml` plus metadata. Compared on the same document:

- **XML carries 82k chars of text; JSON's rendered text is only 61k.** The XML is the
  full document, not a metadata stub — `Ministry`, `Status`, `Rank`, `JournalNumber`,
  `Resume`, `Ref_Accn`, `Concerns` and ~25 other fields are all in it.
- JSON uniquely adds only the site's internal DB `id`, `isHistorical`,
  `pdfLink.isPdfAvailable`, and rarely-populated `editorialNotes`.

**Decision: store XML only.** It is canonical, XSD-validated and a superset. It halves
the requests (≈2.8 days vs ≈5.6), and HTML can be generated locally from it with zero
extra requests.

**One exception, measured later and worth knowing before it surprises anyone:** the XML
is a superset only where Retsinformation *has* structured XML. For older documents the
canonical endpoint serves the record and nothing else — `eli/lta/1995/498/xml` (LBK nr 498
af 21/06/1995, the consolidating act behind the 2005 one) is 2,177 bytes of `<Meta>` with
no `<DokumentIndhold>` at all, and the file in the mirror is **byte-identical** to it: the
crawl dropped nothing, there was nothing to fetch. The same document through the site's
own document API returns `documentHtml` with 88,801 chars of markup and **70,075 chars of
text**, headed `<div class="bjelke">Den fulde tekst</div>` — legacy HTML (`<DIV CLASS=PARA
ID="INDHOLD">`) carrying the whole act. So there are two representations, and they are not
ordered the same way as the earlier comparison suggested:

| | modern document | historic consolidation |
|---|---|---|
| canonical ELI XML | 82k chars, superset | metadata only, **0 chars of text** |
| site's `documentHtml` | 61k chars, lossier | 70k chars, sometimes the **only** text |

How far that reaches in the archive: **29,061 of 61,100 `lta`+`ltb` documents (47.6 %)
have no `<DokumentIndhold>`** — essentially all law before ~1995 (96 % of 1975–1999, 28 %
of 2000–2024, none since 2025), and **3,304 of them are still `Status=Valid`**, only 4 of
those from 2010 onward, which turn out to be Finanslove (published as tables, so genuinely
without structured text). The class is mixed in size, not just in age: four sampled
body-less `Valid` documents gave 3,062 chars of real text (lov om fremmede statsskibe,
1950), 840 (a 1950-51 normeringslov, being tables), 343 (a short bekendtgørelse) and 0
further. The large ones are exactly the historic consolidations of major acts — the
documents a version history wants.

**Consequence:** any pre-1995 full-text feature has to be fed from the site's document
endpoint, which means a second parser and one request per document — a decision, not an
oversight. It also explains why a third party parsing *that* HTML has text where the
mirror has a record, while the mirror has the canonical bytes where their parse truncates.
The decision was eventually taken and measured; see *Backfilling the body-less documents*
below.

### Backfilling the body-less documents

Whether the missing bodies are worth a second parser was answered with a 51-document pilot
rather than argued: 25 body-less documents (one per decade from the 1850s, weighted towards
`Status=Valid`) and 26 documents that *do* have an XML body, so the two representations could
be compared against each other on the same text. All 51 payloads are kept in
`data/pilot-bodies/` (gitignored, 7.9 MB).

Fetching costs nothing that the crawler does not already pay. `POST {site}/api/document/eli/{path}`
is the site's **own** endpoint — `internal/docapi` already implements it as `FetchJSON` on
`limiter.BucketDocument`, with tests, and has never had a caller. The pilot ran 51 requests
1.2 s apart (the crawler's floor is 200 ms, so five times slower than production): **0 errors,
0 throttles, 7.9 MB**. This is not the third-party API, so it does not touch that budget.

**The bodies are there, and they are prose.** Every one of the 25 body-less documents returned
text; 18 of 25 gave 2,000 chars or more, up to 70,073 (skattekontrolloven, 1995) and 27,989
(1915). The two smallest were 341 and 429 chars — short bekendtgørelser that were always short.
None came back empty, so the fetch doubles as the census that classifies the rest.

**`documentHtml` is the same tree, in three markup generations.** The class vocabulary maps
onto the XML element names nearly one for one — `Paragraf`/`ParagrafNr`, `Stk2`/`StkNr`,
`Liste1`/`Liste1Nr`, `Kapitel`/`KapitelOverskrift2`, `ParagrafGruppeOverskrift`, `Fodnote`/
`FodnoteNr`, `Bilagstekst`, `TabelTekst`, `IndholdsfortegnelseId` — and for most documents the
elements carry **the same identifier as the XML**: `<p class="Paragraf" id="id3880dfc9-…">` is
`<Paragraf id="id3880dfc9-18ae-…">`, and a footnote's `<Nota id="Par9_Stk1_Not1">` reappears as
`<a class="FodnoteNr" name="Par9_Stk1_Not1">`. Across the paired documents 9,259 element ids
and 266 of 277 `Nota` ids were found in the HTML, so a pair can be aligned element-by-element
rather than by guessing from layout.

| generation | seen in | shape |
|---|---|---|
| legacy SGML | mostly pre-1960, 13 of 25 stubs | `<DIV CLASS=PARA ID="INDHOLD">`, `<P CLASS=PARAB> <B>§ 1. </B>`, `NAME=""P1""`, `&aelig;`, unquoted attributes, unclosed tags — **not XML-parseable** |
| mid-century lowercase | ~1960–2000 | `<p class="Titel2">`, `<p class="Paragraf" id="id…">`, `<span>` |
| modern | 2000 onward | `<table class="Indholdsfortegnelse">` TOC, anchored `<p class="Paragraf">` + `<span class="StkNr">`, `<table>` for bilag |

Only 16 of the 51 payloads parse as XML at all, so the reader needs a tolerant tokenizer;
`golang.org/x/net/html` is the standard one and is not currently a dependency.

**Fidelity, measured on the paired documents: 99.48 % with no content loss found.** Comparing
every `Paragraf`/`Stk`/`Indledning`/`Afsnit`/`Kapitel`/`Bilag` element of the 24 paired documents
against the HTML — 13,266 elements, footnote (`Nota`) text excluded because it is relocated:

| result | elements | |
|---|---|---|
| text verbatim in the HTML | 13,197 | 99.48 % |
| in a document whose bilag are `<table>` | 46 | 0.35 % |
| inline footnote marker inserted mid-sentence | 14 | 0.11 % |
| heading and body are separate `<p>`, probe spanned both | 5 | 0.04 % |
| whitespace at a link boundary only | 4 | 0.03 % |

Every residual is a *representation* difference, not missing content — including the one that
looked like a second instance of their-side text loss (§ 73 of `lta/2007/1561`): the XML and the
HTML hold the identical sentence and the identical footnote 2. That is the argument for building
the HTML reader **against these paired documents** and not trusting it on stubs until it reproduces
XML-derived structure it already knows.

**Scale, if it is run for real:** 29,061 documents ≈ 4.5 GB at the pilot's mean of 155 KB per
payload, and about ten hours at 1.2 s spacing — or a few hours at the crawler's own 200 ms floor.
The 3,304 `Status=Valid` documents alone are ≈ 0.5 GB and well under an hour.

**Where the payloads go: beside the XML.** `data/xml/lta/1995/498.xml` and
`data/xml/lta/1995/498.json` — one directory per document, so a stub and its body are found
together and the existing path mapping is shared. `lexdania.Scan` only reads `*.xml`, so the
sidecars are invisible to the index and cannot disturb it. The cost is stated honestly: this
relaxes the promise in `internal/archive`'s package doc that the archive holds one immutable
file per document, and it means `data/xml` is no longer a byte-exact mirror of the site's XML
endpoint but a directory of two representations per document. `.json` payloads are the raw
upstream response — keeping only `documentHtml` would throw away `isHistorical`,
`alternativeMedia` and the rest of the triage fields that classify a stub as prose, table or
PDF-only.

**Implemented as `fetch --bodies-scan` / `fetch --bodies`** (`internal/crawl/bodies.go`).
Detection is a file walk, not a Badger query — the archive is the product and the store is
derived, so after a store loss the queue has to be re-derivable from disk. A file is work when
it has no `<DokumentIndhold` element and no sidecar; `accn/` paths are skipped as aliases, since
an accession file is a second route to a document the archive also stores under its own ELI
path, and queueing both spends two requests on one payload.

Queueing happens in a **separate keyspace** (`b|`, beside `i|` for inventory), because one
document is legitimately two facts at once: *we hold the canonical XML* and *we still need its
text*. Folding the body into the inventory row would make the 29k body-less laws look like
unfetched XML, and the crawler would spend origin requests re-downloading metadata it already
has. The state store then answers the question the filesystem cannot — which documents have
**nothing to fetch** — with two terminal states: `gone` for a 404/410, and `empty` for a payload
that came back with no prose (`Finanslove` and their tillægsbevillinger are published as
tables). Both keep their payload on disk, so a future reader can tell *unrecoverable* from
*never fetched*. A payload that fails to parse is `failed`, not `gone`: a Cloudflare challenge
arrives with HTTP 200, and retiring a document because of one would be a silent data-loss bug
spanning years.

The measured scope, so a run can be chosen rather than guessed at: **29,061** `lta`/`ltb`
documents have no XML body, and about **101k** across every kind; at the pilot's mean of 155 KB
that is 4.5 GB for the laws, and ~15 GB for everything *if that mean holds outside `lta`* — the
pilot only measured the laws, and `ft` documents run forty times smaller than `fob` ones, so
treat the second number as an upper bound until a `--bodies-scan` says otherwise. A first run
therefore looks like:

```
retsin fetch --bodies-scan                          # the census, zero requests
retsin fetch --bodies --kind lta --limit 50         # fifty sidecars, one series
retsin fetch --bodies --kind lta,ltb                # the laws, ~10 h at 1.2 s spacing
retsin fetch --bodies --state empty                 # re-test the table documents
```

`retsin status` reports the two representations apart — `canonical xml: N, SIZE` and
`body payloads: N, SIZE` — plus the body queue's state counts, so "how much of the corpus has
text now" is one command. A sidecar already on disk closes its row **without a request**, which
is the resume path after an interrupted run, and a second pass over a completed one issues zero.

### Which tokenizer reads the sidecars, measured before choosing

`documentHtml` arrives in three generations, so the reader's first decision is what tokenizes
it. The choice was measured on the 51 pilot payloads rather than argued, comparing
`golang.org/x/net/html`'s tokenizer against a 90-line hand-rolled one:

| measured | result |
|---|---|
| elements recovered | **both exactly match the raw count: 59,987 of 59,987** on all 51 payloads |
| `class` / `id` / `name` attributes | identical counts on every payload, including unquoted SGML values (`CLASS=PARA`) |
| entities | stdlib `html.UnescapeString` decodes the corpus's entire vocabulary (`&aelig;` 201, `&oslash;` 196, `&aring;` 131, `&nbsp;` 128, numeric ones) — x/net adds nothing here |
| `NAME=""P1""` (SGML doubled quotes) | **x/net loses it**: an empty `name` plus a junk attribute. 224 of 818 anchor `name=` attributes in the sample, in 11 of 51 payloads, all legacy |
| dependency | 1 module, 2 packages (`html`, `html/atom`) |

So x/net is chosen **with a pre-pass** that rewrites `attr=""V""` to `attr="V"`: on the sample
neither tokenizer has an advantage, and the doubled-quote loss is fixable in one regex either
way, but the sample is 51 of 29,061 payloads and the failure mode being guarded against is
silent text loss across the corpus. x/net's *tree* parser is deliberately not used — it fixes
up structure, inserting implied tags and relocating mis-nested ones, which is exactly what a
fidelity mapping must not inherit.

Implied closure is hand-rolled regardless of the tokenizer, and its scope is small enough to
enumerate: the only elements that ever lack an end tag are `hr`, `br`, `col`, `img` (void in
HTML anyway) plus `span`, `tr`, `center` and `p` — 419 `hr` and 26 `span` across the modern
payloads, 44 `hr`, 21 `BR`, 21 `span`, 15 `tr` across the legacy ones, and **zero** across the
mid-century ones. That is a stack with a void-element set, not a tree builder.

### Mapping payload classes onto element names, with evidence per row

Only **6.3 %** of measured payload elements carry a class that *is* one of the 81 canonical
element names, so the vocabulary cannot be mapped by name arithmetic — it has to be a table, and
every row needs a reason. Three independent joins provide one:

| join | matched | what it decided |
|---|---|---|
| element `id` shared with the paired XML | 156 | `StkNr` is `Stk`'s printed number, not a `Stk` — it carries the *parent's* id; `Liste1` is `Indentatio formaInd="Nummer"` |
| squeezed text equal to a canonical element's text | 55 (49 unambiguous) | a cell's prose `TabelTekst` is `Exitus`, while the unclassed `<td>` is the `Td` — canonical shape is `Td > Exitus > Linea > Char` |
| class equal to a canonical name | 6.3 % of elements | enough to place the rest, nowhere near enough to derive them |

`internal/lexdania/htmlclass.go` is that table, and the test asserts coverage of the corpus:
**59,987 of 59,987 elements classified**, against measurements checked in as
`testdata/html-vocabulary.txt` (128 `(tag, class)` tokens) and `testdata/canonical-elements.txt`
(81 names from a 2,045-file random sample). The payloads themselves are gitignored, so the
measurement is the thing that stays reviewable, and the mapping may emit no name that sample has
never seen. Measured split: marker 14,498 · section 12,468 · text 12,266 · table-cell 7,057 ·
inline 5,220 · container 2,832 · table-row 2,536 · skip 957 · note 821 · break 486 · centreret 584
· signature 226.

A class attribute can hold more than one name — the corpus has `span class="Bold Italic
Subscript"` — and when the whole value is not in the table, the first component the table
recognises wins: the editor wrote semantics before typography, and the single-class case never
reaches that path.

Two questions the classifier refuses, because one element cannot answer them:

- **A centred provision line.** `IkraftCentreretParagraf` is appendix material and `Exitus` is
  prose, and the difference is whether the entry-into-force rule (`hr.ikraftstreg`) came first.
  The row returns `roleCentreret` and the builder decides.
- **A class the table has never seen.** It returns `roleUnknown`, which the builder records on the
  document *and* places by tag, so the text still lands. Folding an unknown class into the tag's
  default is how a new provision type silently stops being a provision.

The legacy generation cannot be validated by alignment at all — **no paired document has a legacy
SGML payload** — so those rows rest on text conservation instead.

### Reading a payload back into the canonical tree

`internal/lexdania/htmlbody.go` turns a `documentHtml` payload into a `*Node` tree in the shape
`Parse` produces from XML, so `Sections()`, `Match()` and consolidation handle both without a
branch anywhere downstream. What the payloads force, measured:

- **The mixed-content invariant is built, not repaired.** Every element opens with one empty run;
  text accumulates into it; appending a child closes that run and opens the next. A repair would
  be a guess about where text belonged.
- **Implied closure follows the corpus, not a tree builder.** Only `span` and `tr` are ever left
  unclosed (47 and 15), so only those get rules — and they close the *outermost* match, because
  prose wrappers live inside cells and a new row must reach past them. A new cell must stop at the
  cell, or a table's columns become rows.
- **Footnotes move.** The payload keeps note text at the bottom and the marker where the note is
  referenced; the canonical tree puts the text at the reference, so anchors are created at the
  marker and filled when the text arrives. The same class means both things — in the legacy
  generation the note's *own* marker is an `<a class="FodnoteNr">`, the same tag a reference
  uses — so only being inside the note paragraph tells them apart. A note that finds no anchor
  stays where it was and is counted: unreported, that is a note attached to the wrong sentence.
- **Drops are accounted for.** The text of every skipped subtree is recorded, so conservation
  compares "everything in the payload" with "tree + recorded drops" and needs no copy of the drop
  rules of its own.

Conservation runs over six real payloads checked into `testdata/`, as two separate claims: the
**character counts** must match — whitespace cannot be compared, since adjacent elements are glued
in a payload (`1942Under`) and separated in the tree, and neither is wrong — and the **order** must
match exactly wherever nothing relocated. `Node.Text()` is not document order: it joins one node's
own runs, which puts a paragraph's trailing text ahead of its marker's, so the comparison walks
`Runs`/`Elems` interleaved. All 23 guards in the builder and the pre-pass are proved by patching
each out.

### Measured against the paired corpus

Every one of the 51 pilot payloads also has canonical XML on disk, so each provision in the XML can
be looked for in the tree the builder produced. The falsifiable question is not "is the text in the
payload" — that was measured before any reader existed — but whether the **builder** loses text the
payload had:

| provisions compared | found verbatim | **lost by the builder** | absent from the payload itself |
|---|---|---|---|
| 13,283 | 13,281 (**99.98 %**) | **0** | 2 |

The pilot's estimate with no reader at all was 99.48 %, with the residuals split between documents
whose tables were not parsed and headings whose body is a sibling element. Both categories are now
**zero** — the reader rebuilds tables and closes cells the way HTML does — so the remaining two
provisions are text the payload never carried. The measurement is pinned in
`testdata/paired-fidelity.txt` because the payloads it came from are gitignored, and a run that
finds no paired documents **fails** rather than passing with zeros.

**The legacy generation has no XML to align against**, so those payloads are checked the other way:
reading them must lose nothing and invent nothing. Across all 51 payloads the character counts
match, the interleave invariant holds, no element carries a class outside the table, and — for the
39 documents with no footnote material, which is the only material that moves — the text appears in
the same order. Measured: 11 SGML-generation payloads, 266 footnotes relocated to their reference,
13 left unanchored, 682 elements closed implicitly, and 1,564 stray end tags tolerated.

### Splitting the payload's flat paragraphs into stykker

A payload cannot nest, so it writes a paragraph and its sub-stykker as siblings:

```html
<p class="Paragraf" id="P2"><span class="ParagrafNr" id="Par2_Stk1">§ 2.</span> Herved …</p>
<p class="Stk2"><span class="StkNr" id="Par2_Stk2">Stk. 2.</span> De i Anledning …</p>
```

Canonical nests them — `Paragraf > Explicatus + Stk > Exitus` — and `Sections()` only
recognises a stykke as a **direct child** of a paragraph, and only looks for numbered
items inside one. A flat tree has no "stk. 3" to resolve and never even searches for
"nr. 2", so an amendment citing "§ 2, stk. 3" reports the provision as missing from a
law that contains it. `internal/lexdania/htmlnest.go` rebuilds the nesting under three
rules the corpus decided:

- **Only what canonical nests may be absorbed:** `Stk` (10,432 under paragraph
  elements), `Exitus` (22 directly under a paragraph, 11,161 under a stykke),
  `Indentatio` (3,457 under a stykke) and `AendringsNummer` (261 under a paragraph).
  Anything else ends the paragraph — a stolen heading is invisible in the text, while
  an unresolved address is not.
- **Absorption is segmented, not incremental.** Attaching each element to "the
  paragraph, or else its last stykke" reorders text whenever two elements land at
  different depths: on `lta_1896_71`, § 30's list items went into the stykke while the
  sentence introducing them went into the paragraph, so the items read ahead of their
  own introduction. Each segment head now takes a contiguous slice of the payload's
  order, which makes reordering structurally impossible.
- **The printed number is not necessarily the first child.** The site emits an empty
  anchor (`<span id="P1"/>`) before `<span class="ParagrafNr">`. Requiring the marker
  at index 0 built no first stykke for those paragraphs, left their prose
  unaddressable, and made the payload's own `p.Stk2` the first stykke — shifting every
  later `stk. N` by one. Empty children are skipped; a child holding text is not.

An unnumbered `p.Paragraf` stays flat on purpose: that is the preamble sentence naming
the law an amending act changes, and `amendments.go` reads a paragraph's *own* prose
for it. Payload stykke ids are adopted rather than synthesised — `Par2_Stk1` sits on
the number span and is the same identifier the paired XML carries — and are moved off
the span, so one id names one node.

Measured over the 51 paired documents by comparing the addresses `Sections()` produces
on each side (`testdata/paired-section-parity.txt`):

| canonical addresses matched | …with identical text | absent from the tree | same address, other text |
|---|---|---|---|
| 11,349 → **15,269** | 7,039 → **15,082** (84.7 % of 17,797) | 6,448 → **2,528** | 4,310 → **187** |

25 of the 51 canonical files are metadata-only stubs, where the tree **adds** structure
the XML does not carry — which is the point of the backfill, and why the comparison is a
multiset of addresses rather than a tree diff. Three residual classes remain, each a
separate decision: chapter numbers the payload prints only in the heading beside the
chapter (`kapitel` 217 missing *and* 222 extra — the same chapters read differently by
two trees, not lost), centred provisions whose payload class is filed under prose rather
than under `AendringCentreretParagraf` (most of the remaining `stk`/`paragraf`
residual), and quoted stykker inside an amendment's proposed text, which canonical
excludes deliberately. All 21 guards on the pass are proved by patching each out, and
the harness re-runs an unmutated control around every mutation — an earlier version of
it reported six holes that did not exist, because a mutation that fails to compile is
not evidence and a mutation that changes nothing proves nothing.

### The provision number the payload prints as prose

The register's own XML keeps a provision's number in `<Explicatus>` beside its text. The
payload prints it as the first words *of* the text, usually inside a bold run
(`<P class="Paragraftekst"><B>§&nbsp;6.</B> Arbejdsformidlingen …`), and in the legacy SGML
shape as an element of its own with no period (`<P CLASS=PARAB> <B>§ 5 </B> </P>`). Both
`Sections()` and the model read the marker, so where the text comes only from the payload the
reader had nothing to read from: over a 2,016-document sample, **236 of the 750 documents that
have provisions gave no provision a number at all** — 4,587 of 11,402 provisions, 37 % of the
`lta` shelf. Those documents have text you can search and no way to be told where in them you
are standing.

`attachBody` therefore promotes the printed number into the marker. It is a move and not a copy:
the document's text stays what the page shows, and the number is now also where the XML body
keeps it. Two rules keep the promotion from inventing names. The period decides number from
citation — `§ 6.` names this provision, while `§ 3 finder ikke anvendelse` and
`§ 3, stk. 2 finder …` name another one, so a missing period with prose following refuses. And
the marker has to *be* the number: a numbered item inside a provision is emitted as an
`Explicatus` too and its text quotes other provisions, which initially gave one bekendtgørelse
the number 1 for its § 3, § 4, § 6 and § 11.

Measured after, by `TestHTMLBodyProvisionsGetNumbers` and the serving baseline
(`internal/mcpserver/testdata/serve-baseline-20260918.txt`): **171 of 7,805 provisions in
payload-bodied documents still have no number (2.2 %)**, and the residual is the quoted shape —
"§ 12, stk. 2, affattes således: …" inside a consolidated act, "§ 10, stk. 1, nr. 4: Mundtlig
eksamen …" inside a ministry note — text that cites a provision rather than being one, which
stays unnumbered on purpose. On the same 1,000-document serving sample the unitiser's citations
went from 440 with 250 of them unopenable to **369 with none unopenable**, and the gate no
longer carries an allowance: a cited address that cannot be re-opened is a defect from its first
occurrence.

### Older versions of a law are already in the archive

The obvious question for an archive is whether *superseded* versions of a still-current
act can be downloaded — e.g. what § 12 said in 2019. **They cannot be fetched, and they
do not need to be.** Measured, not assumed:

- The site's own ELI spec (`/static/eli-parameters-documentation.json`) documents **11**
  URL parameters — `pubMedia`, `year`, `month`, `day`, `number`, `language`, `format`,
  `accn`, `classification`, `docType`, `metadataFileFormat`. **None of them is a version,
  date or time-travel parameter.**
- `eli-technical-documentation.json` describes the canonical URI as showing *"the original
  version"* of the document, and the corpus agrees: of 6,000 sampled `lta` documents,
  **zero** cite an amendment dated after their own publication year. ELI XML is the text
  *as promulgated in the gazette*, not a consolidated current text.
- Non-resources confirmed by probing: `/eli/lta/2025/331/version/1/xml` and
  `?version=2` both return the JavaScript SPA shell (HTTP 200 + HTML, so a crawler must
  treat HTML as "no such representation", never as success).

So a law's history is not hidden behind snapshots — **every version is its own
document**: the base act plus each amending act, each with its own ELI URI, all already
listed by the sitemap. What has to be *computed* is the consolidated text at a given
date, by applying amendments in date order. The link to follow is `<Concerns id="…">`,
non-empty in 54.5 % of `lta`, 81.3 % of `retsinfo` and 76.3 % of `mt` documents (2,500
sampled each; `ltc` is 0.1 %, being EU notices). The site does the same computation
client-side — its SPA calls `/api/extremesearch/getcasehistorystatus`.

One trap worth naming: the two digits at the end of an accession number are **not**
version counters. 1,086 accession bases in the archive hold more than one accession, but
1,082 of those are `ft`/`fob` parliamentary items, which simply number each sitting
(`…00`, `…01`, `…02`). Only 4 law bases have more than one, so "group by base, highest
suffix wins" is not a valid way to find current law — `Status`, `EndDate` and
`DateOfHistoricMark` are.

**Consequence for the design:** nothing here justifies a version-discovery crawl. The
archive stays "one immutable file per published document", and consolidation becomes a
job for the derived layer. The qualifier found later is that *being* a document is not the
same as carrying text: for law before ~1995 the canonical XML is often metadata only, so
the version is present and its words are not (see the exception above).

### Cross-checked against a third-party consolidation API

`retsininformation-api.dk` (run by syv.ai) serves the same archive as JSON, including a
computed version history per document. It is a useful second opinion — the same upstream
XML, consolidated by someone else — and it is rate-limited hard: **35 requests per hour,
100 per day**. The comparison cost 23 calls; every payload is cached under `data/jsonapi/`
and each call is written to `data/jsonapi/.ledger.json` *before* it is made, so a re-run
costs nothing and a crashed run cannot double-spend.

What the endpoints mean, measured rather than assumed (skattekontrolloven, 2005/1126, was
the control case):

- `GET /v1/lovgivning/{year}/{number}/versions` is **per document**. Every row carries the
  accession of the law itself, never of the amender; versions are told apart by
  `version_number` plus `effective_date`.
- `/versions/1` is the document **exactly as promulgated**, not the oldest *state*. Proved
  with a control pair on LBK 942 af 02/10/2009: text introduced by an amendment effective
  *before* it was issued is in their v1 and in our XML, and text from an amendment
  effective *after* is in neither. `effective_date` on v1 is not a start date either — for
  that 2009 act it says 2023-01-06, thirteen years after promulgation, which is when the
  service began tracking the document, not when the text began to apply.
- `/versions/at/{date}` resolves **across documents**: asking for 2005/1126 as of
  2005-12-01 returns `A19950049829` (LBK nr 498 af 21/06/1995) — a different document in
  the statute's lineage with its own `version_number`, so that number means nothing
  outside one document.
- `paragraph_count` and `chapter_count` come back null on `/versions/{n}`. The usable
  structure is `structure.chapters[].paragraph_groups[].paragraphs[]`, each with its
  printed `number` ("§ 3 a.") and `stk[]` of `{number, text}` — which is the shape
  `lexdania` produces, so the two can be aligned provision by provision.

Aligned on printed numbers across the ten hardest laws (see the ranking below):

| | result |
|---|---|
| provision numbers | ours is a subset on all ten — no number we produce is missing from theirs. Their extra entries (3–43 per law) are entry-into-force provisions numbered against *other* laws (§ 375, § 438), which `Appendix` deliberately excludes, plus one heading rendered as a paragraph ("§§ 13-17") and numbers repeated six times (§ 9 in lov om aktiv socialpolitik) |
| subsection text | **8,412 of 8,570 aligned subsections identical (98.2 %)**; 598 where *their* text stops dead at a footnote or list marker while ours continues ("1) …"); **zero** where ours stops early, on any of the ten |
| the rest | 152 of the remaining 158 are characters we keep and they drop; 6 are real, and 1 of those is substantive |

The substantive one: § 274 of lov om finansiel virksomhed. Their v1 says the supervisory
authority may have experts follow a firm for "op til **12** måneder"; our text says **6**.
The archived XML contains `op til 6` once and `op til 12` not at all, so our parse is
faithful to the promulgated document and their v1 is not — which is the value of the
exercise even when the answer is "not our bug".

The 152 character differences turned out to be a bug **here**, not a cosmetic gap — see
the zero-width entry under "Bugs the tests caught".

One limit this exposed about our own side: **48 % of `lta`+`ltb` files have no body at
all** (28,547 of 59,776 carry `<Meta>` and no `<DokumentIndhold>`). The case that surfaced
it is `A19950049829`: our file is 2,177 bytes of metadata, and the service's copy of the
same `UniqueDocumentId` 62968 is 68 paragraphs of text. For superseded consolidations of
that era the archive is an index, not a text, so an oldest-revision comparison only means
something where we hold the body.

### Reading the archive: `internal/lexdania`

The archive is only useful if the XML can be loaded without loss, so
`internal/lexdania` parses canonical ELI XML into a **generic tree that keeps every
element, attribute and text run**, then projects the parts that have names onto typed
structs. Nothing is silently dropped, which is what makes the rest of the numbers
below trustworthy.

```go
a, _ := lexdania.Scan(ctx, "data/xml")            // accession -> file index over the archive
d, path, ok := a.Open("A20190021330")             // one document, fully parsed
vs, _ := a.Versions("A20190021330")               // every known version, by effective date
v, _ := a.LawAsOf("A20190021330", date)           // the law as it read on that date
v.Section("§ 12, stk. 2").Display()               // one provision, formatting collapsed
v.Trustworthy()                                   // false if anything could not be applied
```

Decisions worth knowing:

- **A generic tree, not a struct per element.** A struct-per-element design drops
  every element it has not been told about, silently, and the format has 131 distinct
  element names. `Node` keeps `Name, Space, Attrs, Runs, Elems` under one invariant —
  `len(Runs) == len(Elems)+1` — so text and children stay in document order and edits
  can splice text back into the tree without reformatting it.
- **Two identifier schemes, both 12 characters.** `<AccessionNumber>` is a gazette
  accession (`A18521100030`, type code possibly alphanumeric: `G198050837R1`) for
  laws, orders, circulars and rulings — or a parliamentary bill id
  (`201013L00173`, `200423LA0079`) for every `ft/` document. Length alone cannot tell
  them apart.
- **Bills are not law.** `ft/` holds Lovforslag and Betænkning documents whose
  amendment markup is indistinguishable from an enacted act's. `IsBill()` gates the
  consolidator: a proposal that never passed is never applied.
- **Unmapped is visible.** `Meta` types every element name observed in the corpus;
  anything new lands in `Meta.Rest` and raises a warning rather than vanishing. That
  is how two unmapped fields and three identifier shapes were found.
- **Derived text never mutates the base.** `LawAsOf` clones, and a corpus test proves
  it over real documents.

Measured against the live archive (crawl running concurrently, so counts rise between
runs). The sampled corpus tests: **400** documents parsed with **0** failures and **0**
unmapped `Meta` children; **364** numbered changes across **25** amending acts with
**96 %** carrying a recognised address and **98 %** classified into an edit kind;
consolidating **5** modern laws applied **46** changes, with **258** changes correctly
identified as belonging to other laws.

An opt-in scan then reads **every** document instead of a sample —
`RETSIN_FULL_SCAN=1`, **132,000-odd** documents and 6 GB in about half a minute on 12
workers — and reports histograms rather than assertions, because the interesting cases
are the rare ones:

| measured over the whole archive | |
| --- | --- |
| parse failures | **0**; HTML payloads and half-written files counted separately, never fatal |
| tree-invariant violations | **0** |
| numbered changes | **172,840**, of which **1.2 %** stay unclassified |
| by kind | replace-text 50,758 · insert-provision 37,968 · replace-provision 23,006 · insert-text 16,676 · delete-provision 11,865 · delete-text 7,479 · unknown 1,953 · renumber 1,741 · replace-everywhere 908 · non-operative 817 |
| entry-into-force appendices | **11,681** documents, **18,623** provisions numbered like the law's own |

What is still *not* applied is reported as the changes the engine refuses, and the tally
consults `Edit.Kind` and `Address` rather than re-reading the prose — so a shape leaves
the table when the library gains a mechanism for it, instead of sitting there because
this file's regexes were not updated. **10,894 of 172,840 changes (6.30 %) in 4,246
documents** are not applied as written:

| not applied, and why | changes |
| --- | --- |
| insertion at a numbered sentence ("indsættes som 2. pkt.") | 6,735 |
| footnote attached to the document title | 1,528 |
| annex transferred out of the amending act ("affattes som bilag 1") | 1,077 |
| change wording we do not classify | 1,031 |
| address we cannot parse | 518 |
| change names no provision | 4 |

Every one of those is refused with a reason in `View.Unresolved` rather than applied
somewhere plausible. Shapes that were in that table and are not any more, with the counts
the same scan now reports for them: renumbering instructions ("Stk. 2 bliver herefter
stk. 3.", **1,741**) are *checked* rather than executed, because numbering here is
positional and the insertion in the same block has usually already done it — where it has
not, the refusal quotes the subsection count it found; preamble rewrites (**1,271**) and
document-title rewrites (**374**) are addressable units now; global replaces ("Overalt i
loven ændres »X« til: »Y«", **908**) rewrite every
occurrence and stop at word boundaries; territorial and validity notes (**817**) are
recorded as asking for no change to the text; and `Afsnit` numbered in roman numerals
(**540** changes, **4,286** elements) have numbers at all — the elements carry no
`localId`, so until now the section list held an entry with an empty number that no
reference could match.

One measurement is deliberately *not* quoted: applying a real chain to the archived copy
of its law. A consolidated act already contains the amendments that were in force when
it was published, and an amending act cites the accession that existed when it was
written — often not the one archived — so an "applied" rate over real chains measures the
archive's bookkeeping rather than the parser. Address resolution is measured change by
change against the published text of the law named instead, with its exclusions printed
alongside: insertion addresses name a position that does not exist yet, and multi-law
acts address laws other than the one sampled.

Known limits, measured rather than estimated: an instruction that places text *inside* a
sentence ("indsættes som 2. pkt.") is refused rather than appended to the end of the
subsection, and annex rewrites ("Bilag 1 affattes som bilag 1 til denne bekendtgørelse")
are reported unresolved rather than attempted; both are placements the model cannot
represent, and 6,735 plus 1,077 changes sit in `View.Unresolved` because of them. In the
handful of multi-law amendment acts where each block names a different law, target
attribution is still coarse — 49 of the 62 unresolved changes in the sample
came from one such act. Anything not applied is listed in `View.Unresolved` with a
reason, and `Trustworthy()` reports `false`.

## Architecture

```mermaid
flowchart LR
  subgraph CLI["retsin"]
    C1["inventory"] --> C2["harvest"] --> C3["fetch"]
    U["update = inventory+harvest+fetch"]
    S["status / rebuild"]
  end
  subgraph PACE["internal/limiter — process-global, self-learning"]
    B["harvest 12s PINNED<br/>document at floor 200ms (settable)<br/>sitemap 2s, may double"]
  end
  C1 & C2 & C3 --> PACE --> H["internal/rihttp<br/>1 conn/host · honest UA+contact<br/>reports outcome → learner<br/>one request per call · Retry-After → bucket delay"]
  H --> RI["retsininformation.dk"]
  PACE --> RI2["api.retsinformation.dk"]
  H --> RI2
  H --> A["data/xml/… (plain files)"]
  S --> K["data/meta/kv (Badger)"]
```

### Storage: Badger for state, filesystem for the archive

**Badger holds mutable bookkeeping. Legal document bytes live as ordinary files.**

The XML files *are* the product — they must stay greppable, diffable, rsync-able and
readable without this program. So `data/meta/kv` is a *derived* index: if it corrupts,
`retsin rebuild` reconstructs it from the XML on disk at **zero HTTP cost**. Verified:
deleting the store and running `rebuild` recovered every stored document, and the next
`fetch` issued no requests for them.

Considered and rejected: SQLite (`modernc.org` drags ~40 MB of generated `libc` and the
payload still wouldn't belong in a DB) and a TSV-plus-append-log design, which Badger's
transactions replace outright.

Key layout — all values are tab-separated text, so the store stays readable with
`strings`:

```
i|<eli_url>   lastmod  kind  state  attempts  accession  sha256  bytes  error
a|<accession> eli_url  sha256  bytes  changed_at
h|<date>|<accn>|<reason>  doc_id  type  href  seen_at
H|<YYYY-MM-DD>            "1"    fully-harvested day checkpoint
```

### Data layout

```
<data-dir>/
  xml/<kind>/<year>/<id>.xml   canonical documents — the archive
  xml/accn/<rank>/<year>/<accn>.xml
  meta/kv/                     Badger state (derived, rebuildable)
```

Example: `xml/fob/1980/100.xml`, `xml/retsinfo/2026/10019.xml`.

## Pacing: one hard rule, everything else learned

There are exactly two kinds of limit here, and the tool treats them differently.

**Constants — proven obligations.** The harvesting service publishes a 1 call / 10 s
limit and returns a real `429` when exceeded, with no `Retry-After` header to be
polite about. That interval is a contract, not an observation, so it is a **constant**
(`config.HarvestInterval`, 12 s for clock-skew margin) with no flag: there is no
configuration in which going faster is acceptable. The sitemap pace (2 s) is a
constant too, because it was measured rather than guessed.

**Adaptive — everything unpublished.** There is no published limit for document
requests, so guessing one is the wrong move: too fast risks an IP-wide Cloudflare
block, too slow wastes the bootstrap. Instead the learner measures the site and holds
the fastest pace it can sustain — a TCP-AIMD style controller that starts optimistic
and lets the service correct it:

| Event | Action |
|---|---|
| startup | begin at `--document-floor` and stay there while the service is happy |
| 20 consecutive successes (`limiter.Window`) after a throttle | shorten the interval by ÷1.5 (`limiter.StepDown`), working back towards the floor |
| any `429`/`5xx` | **double** the interval immediately, then hold it for a 10 min cooldown before stepping back down |
| any transport failure | neither faster nor slower, but it breaks the success streak, so recovery only happens on clean windows |
| interval | clamped to `[--document-floor, limiter.MaxInterval]` |

Only the **floor** is a flag. It is both the interval a run starts at and the fastest
one the learner returns to, which is why everything else above is a constant: every
measured run behaved the same way, with nothing to react to. More knobs would only be
more ways to misconfigure a crawl.

The floor is a default, not a limit. 200 ms is what a considerate pace looks like for
requests served uncached straight from the origin, but lowering it is honoured rather
than refused — the feedback then comes from the service itself as a 429, which the
learner answers by doubling the interval on its own.

Learning restarts each run — deliberate, because the site's capacity changes and a
persisted fast interval would begin the next run already presumptuous.

**Jitter** is the second pacing flag, and it is about signature rather than speed. A crawl
that runs for days at exactly 200 ms is a recognisable machine, so each wait gets a random
share of the current interval added on top — 10 % by default, up to 20 ms extra at the
floor. It never shortens a wait, so `--document-floor` stays a hard minimum. `--jitter 0`
gives exact spacing, which is the setting to choose when the question is whether a service
limit is what you are hitting: then nothing else is varying, and a 429 means what it says.
The interval and the jitter in force are printed on the progress bar (`fetch @200ms ±10%`)
and in the startup line, so a flag that did not take effect is visible rather than implied.

Measured against production: the 200 ms floor sustained **4.1 documents/s over the
first ~2,100 documents with zero throttles**, which is why the learner starts there
instead of crawling down to it. If that guess is ever wrong, the cost is one doubled
interval — the learner finds the safe pace by being corrected, not by probing upward.

## Anti-blocking measures

1. **Process-global token buckets.** Every request in the process passes one gate per
   bucket, so no phase or caller can bypass pacing, and the 10 s harvest obligation is
   a constant with no flag that can lower it.
2. **A throttle changes the schedule, not the retry count.** Any 429/503/5xx reports
   the outcome to the learner, which doubles the interval and holds it through a
   cooldown. If the server sends `Retry-After`, that wait is applied to the **bucket**
   rather than sleeping the request open, so the next request is delayed too and a
   cancellation still takes effect immediately. The client issues exactly one request
   per call: the crawl re-selects a failed resource on a later sweep instead of the
   HTTP layer hammering a service that has just said no.
3. **Conservative transport.** 1–2 connections per host, HTTP/2, no worker pool. Extra
   concurrency would only raise block risk without finishing sooner, since pacing is
   the bottleneck.
4. **Honest User-Agent** carrying a contact address, so the operator can email rather
   than ban.
5. **Idempotent, resumable phases.** A resource already on disk is closed with **no
   request at all**, so a multi-day crawl can be interrupted freely and a second pass
   costs nothing.
6. **Bounded re-attempts.** A resource is re-tried on a later sweep at most five times
   before it stops consuming the polite budget; permanent 4xx (404/410) is recorded as
   gone and never asked for again.
7. **Run-end warning** if the server throttled us at all during the run.

## Configuration

Flags only — no config file and no environment overrides, so `ps` and a cron line show
exactly what a run will do. Run `retsin --help` for the full list. The important ones:

```
--data-dir DIR           archive + state location (default ./data)
--document-floor 200ms   the interval to start at and the fastest to return to
                         (a default, not a limit: lower is honoured)
--jitter 10              random extra wait, as a percentage of the current interval
                         (0 = exact spacing)
--user-agent STRING      keep a contact address in it
--site / --harvest-api   origins
--request-timeout 1m     per-request timeout
--log-level info         debug, info, warn, error
--progress auto          live bar: auto (terminals only), always, or never
--limit / --batch N      on fetch: trial size and Badger commit batch
--kind KINDS             on fetch: restrict to ELI series, e.g. retsinfo or lta,ltb
--state STATES           on fetch: restrict to inventory states, e.g. failed or gone
--bodies                 on fetch: work document *bodies* instead of canonical XML
--bodies-scan            on fetch: the body census only, zero requests
--order asc|desc|random  on fetch: which end of the catalogue to visit first
--random-seed N          on fetch: fix the shuffle for --order random (0 = pick and log)
```

`--bodies` is a different unit of work, not another kind: `--kind`, `--state`, `--limit`,
`--batch` and `--order` all apply to it, and its states are the body queue's own
(`pending`, `fetched`, `empty`, `failed`, `gone`). See
[Backfilling the missing bodies](#backfilling-the-missing-bodies).

Everything else about pacing is either a published obligation (12 s harvest), a
measurement (2 s sitemap), or the learner's own shape — all constants, all asserted in
`internal/config/config_test.go`.

### Progress bar

`fetch` paints one line that stays put:

```
fetch @250ms  88% |██████████████████████████    | (22/25, 3 docs/s) [6s:0s]
```

The label carries the interval the learner is enforcing *right now*, so the bar
doubles as the pacing readout; the count, docs/sec and ETA come from the bar
library. Three properties were non-negotiable:

- **It never corrupts the log.** The bar and the structured log share stderr, and
  a bar frame ends without a newline — so a log record would otherwise be
  appended to the tail of a frame. `internal/progress.Gate` serializes both
  writers and erases the frame before each record.
- **It disappears by itself.** Off a terminal, or with `--progress never`, the bar is
  simply not constructed, so cron runs and redirected output contain no escape
  sequences and pay nothing.
- **A stall is visible.** The crawl reports every resource, including failures, so
  a bar that stops moving means the crawl stopped, not that the bar broke.
- **It is built with its total.** The pending count is taken before the bar exists,
  so there is no "unknown total" state to render, no placeholder denominator, and no
  frame that reads as a finished crawl. A run with nothing pending gets no bar.

The bar is `github.com/schollz/progressbar/v3` (from awesome-go's list); this
repository adds the gate and the rule above, and nothing else.

Subcommands: `inventory`, `harvest`, `fetch`, `update`, `status`, `rebuild`.

## Running it

```bash
go build -o retsin ./cmd/retsin

# 1. Full catalogue — ~22 requests, ~50 seconds, ~203k resources
./retsin --data-dir /storage/retsinformation inventory

# 2. Official change feed — 10 days × 12s ≈ 2 minutes
./retsin --data-dir /storage/retsinformation harvest

# 3. Documents — 1 request per document
./retsin --data-dir /storage/retsinformation fetch

# Daily thereafter (cron):
./retsin --data-dir /storage/retsinformation update
```

Flags go before or after the subcommand, either form works.

`update` is the daily refill: harvest, then download what is new, changed, or
missing — including the JSON body payloads — and record when it pulled and what it
could not get. `--full` adds the sitemap pass, which is the only
pass with full scope; see [the collection contract](#keeping-it-current-the-collection-contract).

**Measured bootstrap:** the learner reaches the 200 ms floor within ~1,000 documents and
held **4.1 requests/s** there with zero throttles, so 201,580 resources is roughly
**14 hours** of unattended crawling. Daily updates afterwards are ~1 harvest call plus a
few hundred documents.

Archive size is **not** settled. Per-document averages differ by series by a factor of
forty — `fob` runs 43.7 KB, `ft` 1.0 KB — and the two largest series (`lta` with 63,284
rows, `retsinfo` with 82,654) were still unfetched when that was measured, so the honest
band is **3–8 GB**. Sample them with `--kind` below instead of trusting an
extrapolation.

To crawl faster, lower `--document-floor`: the tool honours it and leaves the judgement
to the site. Those requests are served uncached straight from the origin, and the terms
ask users not to burden the interface — which is why 200 ms is the default, not why it
would be a wall. The learner only reaches the floor if the site proves it can take it.

### Keeping it current: the collection contract

A mirror that quietly stops being current is worse than one that is openly behind:
the whole point is that readers do not have to check. So the collection cycle keeps
three stamps, and each authorises something different.

| stamp | written by | what it means |
|---|---|---|
| `H\|<date>` | each harvested feed day | the change feed for that day was read to the end |
| `last_sitemap_inventory` | `inventory` | the catalogue was re-read; every row knows its current `lastmod` |
| `last_full_sweep` | every `update` | when it pulled, how much it still owes, and a fingerprint of which documents those were |

**The 10-day rule.** The harvesting service answers for a rolling ten days
(`harvest.MaxWindowDays`), so a mirror whose last recorded pull is older than that
cannot be refilled honestly: the changes in between are listed nowhere any more.
`retsin update` therefore **fails** rather than collecting a partial set and leaving
the mirror looking current — it fails *before the first request*, and says to run
`update --full`, which re-reads the sitemap and finds every change by `lastmod` for
about 22 requests. `--force` exists for deliberate repair and is the only other way
past; the gate is a flag, not a warning an operator learns to scroll past. A `--full`
run also tolerates a feed gap, since the sitemap pass on that same run covers the
days the feed has forgotten.

**The pull record is an account, not a certificate.** Every pass writes one, complete
or not: the moment it pulled, how many documents it still owes, how many are past the
retry ceiling, and an eight-digit fingerprint of *which* they were. The alternative —
recording nothing until a pass finishes with both queues drained — discards the true
statement "we pulled today, and these fourteen documents would not come", which is the
statement an operator and a reader actually need. A mirror can only hold what the site
agrees to serve, so completeness is not a fact available here and withholding a
timestamp cannot invent it.

The fingerprint does what the counts cannot. `owed=0 stalled=14` on two consecutive
nights says nothing about whether anything changed; the same digest says it is the same
fourteen documents the site refuses, and a moving digest says something new broke. The
counts and digest travel in the state store row, in `data/sweep.stamp` for readers that
cannot open Badger, in `retsin status`, and in the dataset line of every MCP answer.

`owed` counts rows still pending *or* failed while under the retry ceiling; past the
ceiling they are `stalled` and are reported apart, since no automatic pass will ever
fetch them and `retsin fetch --state pending` is the remedy.

**An inventory pass merges; it does not overwrite.** The sitemap reports every
resource it has ever advertised, and the rows it hands over carry only URL, `lastmod`,
kind, and pending. A blind upsert — which is what this did originally — throws away
every hash, accession and attempt counter, so the nightly crawl would re-close 200k
files locally and re-request the ~10k resources already recorded as `gone`. Only a
moved `lastmod` may touch a row: that is the sitemap's only change signal.

**`stale` is why amended documents are reachable at all.** The fetch loop closes any
row whose file is already on disk without asking the site — that is what makes
re-running an inventory pass affordable. It also means an amended law, once
downloaded, can never be updated: the row gets re-pended, the file is found, the row
is closed, and the mirror keeps serving the superseded text while reporting itself
current. So a moved `lastmod` on a row whose bytes we hold marks it `stale`, the one
state the on-disk shortcut does not apply to. `retsin rebuild` deliberately does not
downgrade those rows, because re-deriving state from disk re-derives exactly the
older copy.

**The feed is read back, not just written.** `retsin changes` resolves each harvested
accession against the archive and reports how many are queued, how many were collected
with new bytes, and how many are unknown to the mirror. It exists because the feed is a
dead end as a *mechanism*: it names documents by accession and `href`, never by ELI
URL, so it can describe a change to a document this mirror has never seen without being
able to fetch it. That resolution is the missing piece if the feed is ever to drive
downloads rather than annotate them.

Measured on the live mirror, 2026-09-18 (yesterday's full crawl, today's first refill):

| measurement | value |
|---|---|
| feed entries over the full 10-day window (first harvest ever on this mirror) | 2,195 |
| distinct changes after de-duplicating by date + accession + reason | **424** — the feed re-lists a change on the days that follow it, so entry counts must never be read as change counts |
| feed changes that resolved to a document already in the archive | 424 of 424 |
| feed changes naming a document this mirror has never seen | 0 |
| rows the first inventory pass queued, out of 201,616 | 1,438 |
| documents the refill collected | ~1,300 rows worked, **35 genuinely new** files |
| rows left owing after the refill | 1 (a `retsinfo` path the site answers **HTTP 500**), carried in the pull record and fingerprinted; it later joined the 14 rows past the retry ceiling |
| permanently failing rows, reported and not blocking | 14 |
| request rate | 47–52 documents/min, dropping to 400 ms pacing when the site errors |
| `held rows with a September lastmod that were stored before their own lastmod` | **0 of 308** |

**The site cannot be asked cheaply.** `HEAD`/`GET` on an `/xml` ELI path returns no
`ETag`, no `Last-Modified` and no `Cache-Control`, so there is no conditional request to
make: confirming that a `stale` row is still the same document costs the whole
transfer. Revalidation is therefore a budget question, not a protocol trick.

**`lastmod` moves in bulk and means less than it looks.** A second inventory pass half
an hour after the first marked 872 rows stale, and a third took that to 1,188 — Civilstyrelsen
refreshes `lastmod` across many resources at once, and the sitemap also advertises some
resources twice with disagreeing dates (203,188 entries for 201,616 resources). Hence the
strict rule above — only a *later* date is a change — and hence the census: of the 308
held rows whose `lastmod` is in September, **none** had been stored before its own
`lastmod`, so the archive is not demonstrably behind. `TestStaleContentCensus` (gated,
`RETSIN_STALE_CENSUS=1`) re-asks the site whether moved `lastmod`s really mean new bytes,
and its report is checked in at `internal/crawl/testdata/stale-census-20260918.txt`; the
trap set is the number that will matter once the mirror has been running long enough to
fall behind.

**Whether the feed has to drive downloads is still open, and it is a rule rather than a taste.**
The feed names an accession and never an ELI URL, so a change to a document this mirror has never
seen cannot be fetched from the feed alone — only a sitemap pass can find it. The rule the decision
is being made against: **one** such accession means the feed has to drive downloads; zero across
every harvested day is the only evidence that leaves the feed as annotation and reconciliation.
Measured so far, over the eight feed days the store holds (2026-09-09 to 2026-09-18, 424 distinct
changes): 424 resolved to held resources, **0** named an unseen document, 0 were left queued or
behind. The collection cycle harvests the rolling window on every run, so the days keep
accumulating; `retsin changes --report` writes each reconciliation to a dated file under
`data/reports/` that is never overwritten, so the decision is read off files rather than off
whatever the last person to run the command remembers.

### Narrowing a run

Pending resources are visited in ELI URL order by default, so a series that sorts late stays
out of reach until everything before it is done — see [Changing the order](#changing-the-order)
for `--order`. `--kind` selects by ELI kind, which is the axis the sitemap itself groups
on:

```
retsin fetch --kind retsinfo --limit 50    # sample one series
retsin fetch --kind lta,ltb --kind ltc     # several at once: comma-separated or repeated
```

`--limit N` caps the run; `--batch N` sizes the pending window. The bar's total honours
the filter, and a kind with nothing pending warns rather than finishing silently.

### Changing the order

The catalogue is stored in ELI URL order, which is chronological within a kind, so the
default walk meets 1873 before last month's law. `--order` chooses the other end:

```
retsin fetch --order desc --limit 20     # the twenty most recent documents
retsin fetch --order random --limit 200  # an unbiased sample of the catalogue
```

`desc` walks the same keyspace backwards and pages by cursor exactly like `asc`, so it costs
what a forward crawl costs, and reaches the part of the corpus that is read and amended
most first.

`random` is not a scan direction. The keyspace is stored in URL order, so a shuffled order
has no cursor to resume from: the selection is materialised once as ELI URLs — a few tens of
megabytes for all 201,579 rows — and drained in shuffled order, which is cheaper than
re-scanning the catalogue once per batch to re-derive the same shuffle. The seed is logged at
`debug` level, and `--random-seed` repeats a sample exactly.

`asc` stays the default deliberately: a mirror aiming to finish should finish in order, and a
random default would turn a partial crawl into a shallow skim of everything.

All three compose with `--kind` and `--state`, all three honour `--limit`, and all three
visit each selected row once per invocation. An order that does not exist is an error, not a
silent fallback to another one.

### Retrying failures

`--state` selects by what happened to a row last, which is how failures get a second chance
without re-crawling anything:

```
retsin fetch --state failed                 # retry every failure
retsin fetch --state gone --kind retsinfo   # re-test 404s in one series
retsin fetch --state fetched                # re-check rows that claim to be on disk
```

A default run takes `pending` plus `failed` rows still under the retry limit, so a
permanently dead URL cannot spin forever. **Naming a state lifts that limit**: the ceiling
exists to protect the automatic sweep, and `--state failed` is a deliberate override, so a
row already tried 99 times is fetched again. `gone` covers both ways a representation can be
missing (404, or HTML served where XML was expected) and is worth re-testing after changing
`--site`. `fetched` is the repair path for a row whose file has gone missing — with the file
present it closes without a request, so only the genuinely missing rows cost anything.

States compose with `--kind`, the bar's total follows both filters, and a state name that
does not exist is an error listing the valid ones rather than a silent empty run.

Each invocation visits each selected row **once**: the scan pages forward by ELI URL, so a
row that is still in the selection after being fetched — a 404 re-tried under `--state gone`
is written back as `gone` — is not offered again in the same run. Re-running the command is
what re-tests them, and the forward cursor also means a long crawl no longer re-walks the
whole 200k-row keyspace once per batch.

### Backfilling the missing bodies

Half the law corpus has no text in its canonical XML: `/eli/…/xml` returns metadata only, and
the document's prose exists nowhere in the mirror. The site does have it, in the JSON payload its
own pages are rendered from, so `fetch --bodies` fetches that payload and stores it **beside**
the XML — `data/xml/lta/1995/498.json` next to `498.xml`. See
[Backfilling the body-less documents](#backfilling-the-body-less-documents) for the pilot that
established this works and what the payloads contain.

```
retsin fetch --bodies-scan                     # census only: 0 requests
retsin fetch --bodies --kind lta --limit 50    # 50 requests, one series
retsin fetch --bodies --kind lta,ltb           # the laws, ~10 h at 1.2 s
retsin fetch --bodies --state empty            # re-test the table-only documents
```

The scan and the fetch are separate on purpose: `--bodies-scan` prints what the archive holds
(scanned, with an XML body, sidecars already held, queued) and costs nothing, so the scope of a
spend is chosen from a measurement. `--bodies` then walks that queue through the **same**
adaptive document bucket as the XML crawl — same floor, same back-off, same throttle learner —
because the endpoint is the site's own and deserves the same care, not because it has a quota.
It has no quota; the third-party `retsinformation-api.dk` budget is a separate ledger and this
path does not touch it.

Three properties are what make a 10-hour run safe to interrupt:

- a sidecar that already exists closes its row **without a request**, so a run that died
  between the write and the row costs nothing to resume;
- a second pass over a completed queue issues **zero** requests, including over the rows that
  ended `gone` or `empty` — those are conclusions, not failures;
- nothing here can overwrite canonical bytes: writing a sidecar under the `.xml` extension is
  refused by `internal/archive` rather than honoured.

`retsin status` then reports `canonical xml` and `body payloads` separately, plus the body
queue per state, which is the census: `fetched` rows have text, `empty` rows are documents the
site has no text for either.

### Retrying body failures

`--state` means the same thing for bodies as for XML, over the body queue's own states:

```
retsin fetch --bodies --state failed   # transient faults: timeouts, challenge pages
retsin fetch --bodies --state gone     # documents the site answered 404 for
retsin fetch --bodies --state empty    # table documents, to check they still have no prose
```

Naming a state lifts the retry ceiling, exactly as in the XML path, and an unknown state name
is an error listing the valid ones. `empty` is worth re-testing only after a parser change —
re-testing it otherwise is 10k requests for a known answer.

### Sampling paired ground truth

The archive now holds 191,394 canonical XML files (9.4 GiB) and 101,727 fetched payloads (3.2 GiB), so
the question has moved from "get the data" to "is the reader right". For that there are two candidate
references, and only one of them can settle anything:

**The reference is the canonical `<DokumentIndhold>` the archive already holds.** Two representations
published by the same publisher from the same source can disagree *usefully*: a disagreement is evidence
about the reader either way. An aggregator's transcription cannot do that — it can only agree with itself,
so measuring against it proves agreement, not correctness. `internal/docapi` therefore stays a comparison
tool, not a yardstick.

That means the population to sample is the ~89,700 documents whose XML **does** carry the text — the exact
opposite of `fetch --bodies`. Sampling is two phases, because phase one costs CPU and phase two costs
politeness:

```
retsin sample --strata                      # what each stratum exists to measure
retsin sample --plan --quota centreret=250 --quota random=400 --seed 20260917
retsin sample --fetch --limit 30            # trial: does the site even serve HTML for these?
retsin sample --fetch                       # the rest, resumable
```

`--plan` reads the archive and writes a manifest; nothing is requested until it is reviewed. Strata are
defined by canonical element markers **pinned against `internal/lexdania/testdata/canonical-elements.txt`**
— `TestSampleNeedlesNameRealCanonicalElements` refuses a needle that is not in that vocabulary, because a
stratum named for an element that does not exist fills its quota with documents that lack the shape it
claims to measure, and the resulting measurement looks authoritative.

Three rules in the selection, each because a plausible alternative was wrong:

- **Targeted strata fill before `random`.** The targeted shapes are scarce; a quota that lost a contention
  with the largest bucket would get none. The cost is stated rather than hidden: `random` is random among
  the unchosen, not among the whole corpus.
- **Round-robin across ELI kinds inside a stratum.** A flat shuffle is dominated by whichever kind is
  largest, so a defect confined to one series survives exactly the sample size chosen to find it.
- **A document claimed by two strata is fetched once and labelled twice.** Two rows would count one
  document twice in the frequency ranking the sample exists to produce.

Payloads go to `data/paired-truth/` in the archive's own layout — **never** into `data/xml`, never into the
state store. A sidecar beside a document that already has canonical XML would make the loader's rule ("use
the sidecar when the XML has no body") ambiguous in precisely the files written to test it, and would
inflate the body census with deliberate duplicates.

Run `--fetch --limit 30` before the full manifest: if the site serves little `documentHtml` for documents
that already have XML, the premise needs restating, and that is a 30-request discovery rather than a
2,500-request one.

### Serving the mirror to agents: `retsin mcp`

```bash
retsin mcp                     # Model Context Protocol over stdin/stdout
```

One tool, `lookup`, in the three forms a caller is likely to hold:

```json
{"method":"tools/call","params":{"name":"lookup","arguments":
  {"document":"eli/lta/2019/1399","address":"§ 1"}}}
{"method":"tools/call","params":{"name":"lookup","arguments":
  {"document":"B20190139905","as_of":"2021-05-01","max_chars":8000}}}
```

`document` accepts the ELI path, that path with the site in front of it (with or
without the trailing `/xml`), the path without the `eli/` segment, or an accession, in
either case. `address` is Danish citation form (`§ 267, stk. 3 nr. 2`, `kapitel 5`,
`bilag 2`, `indledningen`); `as_of` consolidates the amendment chain instead of reading
one stored version.

Three properties are deliberate:

- **Read-only, and not by convention.** The command never opens the Badger state store.
  Badger takes an exclusive lock, so a long-lived server process would lock the nightly
  crawl out, or fail to start while the crawl runs. Serving needs the file archive, so
  that is all it opens — and a process that cannot write is easier to trust in front of
  a legal corpus.
- **Text comes from the compiled artifact when one exists.** `retsin mcp` reads
  `<data-dir>/artifact.bin` — `--artifact` and `--artifact-cache-mb` move it — so a lookup is a
  seek and a decode rather than an XML parse, and the status line says which of the two a client
  is being served from. An artifact written by another build is refused at startup with the side
  that is stale named, because the file holds the model as JSON and would otherwise decode into
  this server's types with another revision's meaning.
- **Every answer is markdown, and names its text.** `- **Text source:** xml|html|none` with
  the payload path for `html`, plus the mirror's document count and index time; the statute
  itself is fenced so nothing in it is re-read as markup. `html` means the sentence was
  recovered by our reader, and the note says what that is worth (84.9 % of paired documents
  matched byte for byte).
- **A tool is advertised only when it can answer.** `search` appears in `tools/list` only
  when an index file was loaded; with none, the server serves `lookup` and says so on
  stderr. A client that discovers mid-conversation that a listed tool cannot answer has
  already trusted the list.
- **`search` returns provisions, not documents.** Each hit carries ELI, accession, section
  address, the document's text source and a passage read back out of the archive — the
  index deliberately stores no legal text. See the next section for how it is built and
  what the measurements decided.

Stdout carries the protocol and nothing else, so logs go to stderr. To exercise it by
hand, keep stdin open — the server exits when the pipe closes, which is correct
behaviour for a client that spawns it and costs a confusing empty run otherwise:

```bash
( printf '%s\n' \
  '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"smoke","version":"0"}}}' \
  '{"jsonrpc":"2.0","method":"notifications/initialized"}' \
  '{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}' \
  ; sleep 5 ) | ./bin/retsin mcp
```

The protocol is the official Go SDK (`github.com/modelcontextprotocol/go-sdk`). It is
one dependency rather than a hand-rolled JSON-RPC subset because the specification is
still moving and getting it subtly wrong produces a server that "works with my client".
The sandbox cannot reach the module checksum database, so the recorded `go.sum` hashes
were compared against the public `sum.golang.org` lookup for `v1.8.0` and match.

### Full-text search: `retsin index build`, and what the measurement decided

```bash
retsin index build                      # whole mirror → data/search.idx
retsin search "pligt fyringsanlæg" --limit 5
retsin search "ubetinget råderet" --source xml --since 1990
```

The index is derived data, like `data/meta/kv`: rebuilt from the files at zero HTTP cost,
written to a temporary name and renamed so a reader never sees a half-built one. It is a
plain file rather than a second database for two reasons — Badger's exclusive lock would
keep the crawl and the server from running at the same time, and SQLite was already
considered and rejected for the state store (see *Storage* above) for reasons that apply
at least as strongly here.

Measured on the whole mirror (2026-09-18): **191,393 documents → 5,505,907 units,
4,345,622 terms, 938 MB, 11 m 18 s to build, 24.5 GB peak RSS**. Query latency on the
sampled measurement was p50 0.2 ms / p95 3 ms; the brute-force baseline over the same
text extrapolates to ~0.7 s per query, so the index is roughly three orders of magnitude
faster and ranks — but grep is not hopeless, which is why the index stores no text and
adds no dependency. The 24.5 GB peak is the in-memory term map: a two-pass sort-merge
build is the known follow-up for smaller machines.

**The unit is the provision, not the document.** A hit in a 300-page act has to name
"§ 267, stk. 3", and that address is produced by the same `AddressOf` that `lookup`
parses back with `ParseAddress` — a round-trip guard runs over the fixture addresses,
because a hit whose address cannot be re-opened is a hit the user cannot read. Title text
is indexed as its own unit (`titel`): searching for a law by name is the most common
legal query there is. Documents with no numbered sections (20,870 of them, mostly
folketingssager) index as one whole-document unit rather than silently vanishing.

Four decisions were made by measurement rather than by taste
(`internal/search/testdata/index-measurement-20260917.txt`, reproduce line in its header):

| candidate | result | decision |
|---|---|---|
| reversed terms (compound heads: *afgift* → *ejendomsafgift*) | +62 % size, recall **fell**: aa-spelling 0.75→0.25, citation 0.50→0.05 | **deleted** — reversed terms share the sorted dictionary and steal prefix-expansion slots; compound-head recall was already 1.000 |
| folded stream (inflectional endings) | recall identical on every group (inflected 1.000 both ways), +31 % size, one term query got *worse* | **off by default** — and structurally redundant: folding only removes suffixes, so the stem is always a prefix of the word, which right-extension already reaches |
| BM25 `b` (length penalty) | citation 0.500 at b=0.75/0.45/0.20, **1.000 at b=0**; term 0.700→0.740, phrase 0.967→1.000 | **b = 0** — a document with no numbered sections is one long unit, and that unit is exactly what "lov nr. 1399" needs, so penalising length penalised the right answer |
| positions in postings | every phrase query's top-10 did contain the words adjacent (3/3) | **not stored** — the only measured need for them did not materialise |

Danish-specific normalisation, all guarded: æ/ø/å are never folded into other letters
(*tanke* and *tænke* are different words, so a diacritic-insensitive index returns the
wrong statute confidently); the pre-1948 spelling of Å as "Aa" *is* folded, at word start
only, so a modern query reaches old text; one-rune tokens are dropped (they are list
markers, not words); digits survive, because "lov nr. 1399" is a citation.

Three honesty properties are pinned by tests rather than asserted in prose:

- **`eli/ft/2020/54` matches five documents, not one.** Folketingssager store every stage
  of a bill (LSF, LSV, BTL, BSF, BRB) under one ELI path, and their texts differ. `lookup`
  refuses such a path and lists the accessions; `search` points its "read it" line at the
  accession whenever the ELI is ambiguous. This was found by the first real query, not by
  reading the spec — an ELI path is a locator for acts, not a primary key.
- **The index says how old it is.** The build timestamp and the scoring parameter travel
  in the file header, and a search result reports what *the index* covers — plus a second
  warning whenever that differs from the mirror's current count, in both directions (an
  index behind the crawl, or built over an entirely different directory).
- **A format mismatch is refused, not tolerated.** Adding fields to the header shifted
  every table by eight bytes; a shifted read of a self-describing table can produce counts
  that look entirely plausible. `Load` now rejects a version it did not write and says to
  rebuild.

Guards: 34 mutations of the reader, encoder, ranking, filters and answer rendering, all
caught with 0 holes (each by the test that names it), plus `internal/lexdania/scanroot_test.go`
for the empty-mirror case that produced the first confusing "0 documents" measurement.

### Compiled serving: `retsin artifact build`

```bash
retsin artifact build                   # whole mirror → data/artifact.bin
```

The artifact is the mirror's own document model (`internal/lexdoc`) as JSON, one
independently decodable zstd frame per document, in the
[Zstandard Seekable Format](https://github.com/facebook/zstd/blob/dev/contrib/seekable_format/zstd_seekable_compression_format.md)
so the result is still a plain zstd stream that ordinary tools read. It exists because
`lookup` re-parsed the publisher's XML on every request and `search` re-parsed it again for
snippets.

The frames are the layout, which took a measurement to get right: `Flush()` on a zstd
encoder ends a *block* and leaves the frame open, so a "frame per document" written with
Flush is one frame with n blocks — not seekable, not independently decodable. `EncodeAll`
writes a complete frame per call. What the writer stores is the model, so no publisher
element name or attribute map is in the file: the refusal reasons an answer gives come from
the model's vocabulary, and the artifact cannot leak the schema by accident.

Three properties are enforced by the reader rather than by discipline. An artifact from a
different build is **refused**, with both commits named, the side that is out of date identified
from the artifact's own timestamp, and the remedy in the message. The
document table and the frame table are **cross-checked against each other** on open, so a
build that wrote one without the other fails instead of serving half a mirror. A document
the artifact does not hold is an **error**, not an empty answer, because coverage is the one
claim a reader of an answer cannot check for themselves.

Measured over the same 1,000 documents as the codec bench
(`internal/compiled/testdata/artifact-20260918.txt`):

| | publisher XML | artifact |
|---|---|---|
| raw | 59.9 MB | 28.5 MB of model JSON |
| on disk (zstd 3) | 5.7 MB | 5.6 MB |
| per lookup | 691 µs (parse) | 122 µs (read + decode) |
| whole mirror projected | — | ~1.07 GB |

The codec bench's earlier note that a compiled form would be *bigger* than the XML was a
property of the struct it measured: `cDoc` copied a section's run text out per section, while
the model keeps one segment slice per provision and its subsections and conditions are spans
over it. The artifact is argued for on lookup speed, but it does not cost size either — and
the difference between those two claims is a measurement rather than a hope.

### Serving from the artifact

`retsin mcp` reads `data/artifact.bin` when it is there, and says in every answer's dataset
line which of the two it is reading. What moved onto it: whole-document lookup, addressed
lookup, the provenance and warnings an answer carries, and the unit text a search answer cuts
its snippets from and grades proximity on — which was the second XML parse per query. What did
not: an `as_of` answer still consolidates through the tree engine and parses the files it
splices into, because M012T011 has not yet decided that the model's subsection numbering is
the one to serve; and building an index reads XML, since the artifact is what a build produces.

The version pin is the commit the artifact was compiled from plus whether that tree was
uncommitted, both stamped by the Go toolchain and recorded in the header. Same commit with a
different working tree counts as a different build — that is the state where a model type was
edited and not committed. A mismatch names the side that is out of date, using the artifact's
own timestamp against the binary's commit date, because the operator can rebuild either one and
only the file says which needs it.

Measured in one run over the same 1,000 documents, both paths, same machine and file cache
(`internal/mcpserver/testdata/serve-baseline-20260918.txt`):

| query | by parse | by artifact |
|---|---|---|
| whole document, p50 | 471 µs | 69 µs (6.8x) |
| whole document, mean | 2.016 ms | 188 µs (10.7x) |
| one provision addressed, p50 | 626 µs | 69 µs (9.1x) |
| one hit's unit text, p50 | 625 µs | 72 µs (8.7x) |

Speed is the argument, so it is measured; agreement is the requirement, so it is gated: of 1,369
answers and 369 snippets served through both paths, none differed. The payload cache's own share
of that is 0 µs at this resolution — for documents of a few kilobytes it saves the seek and one
frame decompression out of ~178 µs, so the win is over the XML parse and not over the cache,
which is worth saying out loud because the cache is the part that looks like the optimisation.

### Text provenance: which documents have real text, and where it came from

`Archive.Open` and `LoadWithBody` attach the text recovered from a `.json` sidecar to a
document whose XML has none, so the 101,727 stored bodies are reachable through the same
`Document` as everything else. Three rules, all guarded in `internal/lexdania/bodies_test.go`:

1. the register's own `<DokumentIndhold>` always wins — a sidecar is never read for a document
   that has a body, so authoritative text can never be replaced by our reconstruction;
2. whatever ends up in the tree says which of the two it is (`Document.Source` is `xml`, `html`
   or `none`, with `Document.Sidecar` naming the payload), and that survives `Clone`, because
   every consolidated view is a copy;
3. a payload that is missing, unreadable or empty leaves the document metadata-only with a
   warning naming the file. Absent text is a gap to report, not an error to hide.

The prose test walks the subtree with `PlainText()`. `Node.Text()` joins only a node's own
runs, so an `<DokumentIndhold>` whose prose lives inside `<Paragraf>` and `<Stk>` — every real
one — reads as empty, and the loader would then have overwritten the register's text with ours.
The test that pins this is `TestCanonicalBodyIsNeverReplacedByASidecar`.

### What the whole-corpus census measured (2026-09-17)

`RETSIN_BODIES_CENSUS=1 go test -timeout 4h -run TestBodyCensus ./internal/lexdania/ -v`
reads all 101,727 payloads (3.2 GiB) in about 25 s. `data/xml` is not in git, so the report is
checked in as `internal/lexdania/testdata/body-census-20260917.txt`. Ranked by measured size:

| measured | count | what it means |
|---|---|---|
| stykker with no paragraph above them | 255,828 of 846,130 | 30 % of the `Stk` elements are unreachable by address: `Sections()` only recognises a stykke under a paragraph |
| documents producing no section at all | 20,870 (20.5 %) | 12,654 of them `ft`, and all 44 `ltb`; these documents can be found but not cited by section |
| sections produced with no text | 288,323 of 1,291,836 | a quarter of the section list is a marker with nothing in it |
| elements named `""` | 12,371 → **0** | fixed on this census's first run: `h1`–`h6` had no mapping, so the builder made an element with no name for them (and for `thead`, `ins`, `del`, `s`, Word smart-tags); a nameless element holds its text and is invisible to every name-based walk |
| documents with under 1 kB of prose | 13,917 | mostly `retsinfo` items; short payloads are legitimate, but this bucket is where truncation would hide |
| `p.MsoNormal`, `p.Paragraftekst`, `p.NormalInd9`, `p.TekstV` | 419,011 / 243,663 / 161,829 / 131,858 | classes outside the table; the fallback (`Exitus`) is the right reading, so these are missing explicit rows rather than lost text |
| stykker whose printed "Stk. N" contradicts their position | 233 in canonical XML, 1,923 in the tree | `Sections()` numbers stykker by position. The register itself disagrees in 233 cases out of 57,515, so positional numbering is right 99.4 % of the time and wrong in a way that silently cites the wrong paragraph; the tree's 1,923 are mostly ours, since the reader adds an implicit first stykke for a paragraph's unmarked prose |
| elements absorbed by the nesting pass | 1,031,964, into 0 wrapped paragraphs | the wrap counter is not firing at corpus scale while the absorb counter is; one of the two instruments is wrong |
| footnotes relocated / unanchored | 0 / 0 | the pilot exercised footnote handling, so a corpus-wide zero needs explaining before the counters are trusted |

Two instrument numbers above are suspicious in the *reporting*, which is why the census has
internal self-tests (`bodycensus_internal_test.go`) rather than only the corpus run.

### What the paired ground truth measured (2,108 documents)

`RETSIN_PAIRED_TRUTH=../../data/paired-truth go test -timeout 2h -run TestPairedTruthParity ./internal/lexdania/ -v`,
sampled by `retsin sample` (seed 20260917) from the 89,667 documents that have both a canonical
body and a payload. Reference is the publisher's own XML, never a third-party API. Full report:
`internal/lexdania/testdata/paired-truth-20260917.txt`.

110,374 canonical sections across 2,108 documents: **86.9 % of addresses resolve in the tree
read from the same document's HTML, 84.9 % with byte-identical text**; 14,465 missing
(stk 4,822, paragraf 3,799, kapitel 2,643, bilag 1,392), 2,249 matched with different text,
22,764 in the tree that the canonical body does not have. Unaddressed rate by stratum ranks the
fix list: **amendments 53.6 %**, centreret 20.1 %, nota 16.5 %, random 15.9 %, stor 15.5 %,
tabel 13.8 %, liste 12.4 %, bilag 10.8 %, kapitel 7.8 %, afsnit 7.2 %. The amendment number is
not the `<AendringNyTekst>` exclusion — both sides of this comparison are enumerated by
`sectionsIn`, so the exclusion cancels; the payloads for amendments render the amended text as
parallel-text tables (`div.ParallelTekst`, unmapped) instead.

Only 8 documents with a canonical body predate 1945 and 61 more predate 1980, so ground truth
cannot reach the generations the reader most needs to get right. That is the reason the census
above exists: paired truth measures correctness where it can be measured, the census measures
frequency everywhere.

## Recovery

If `meta/kv` is lost or suspect, in this exact order:

```bash
./retsin --data-dir /storage/retsinformation rebuild     # relink on-disk XML → zero requests
./retsin --data-dir /storage/retsinformation inventory   # re-discover the catalogue (~22 req)
./retsin --data-dir /storage/retsinformation fetch       # resume; already-held docs are skipped
```

`rebuild` alone recovers only what is on disk; the catalogue comes back from the cheap
`inventory` pass.

## Tests

```bash
go test ./...
```

Covering: the pacing **constants** and the document-floor default, pacing guarantees,
**the adaptive learner** (step-down after a clean window, doubling on throttle,
floor/ceiling clamps, pinned buckets ignoring all feedback, `Retry-After` applied to the
bucket and clamped), live learner integration against a throttling fake server,
one-request-per-call throttle handling, flag parsing on either side
of the subcommand, gzip handling, non-retryable errors, UA
identity, context cancellation (including a cancelled throttle wait), ELI path↔archive-path mapping (including
path-traversal rejection), Badger round-trips and pending/limit semantics, harvest
checkpointing, XML parsing against **three genuine fixture schemas**, the log/bar
writer gate (a record must erase the frame, and never add a clear of its own),
bar modes including silent-off-a-terminal, and end-to-end `Fetch` progress
reporting: one report per resource, correct totals, and a resume pass that
reports while issuing zero requests.

The body backfill's guards are the expensive kind, so each is pinned by a test and then
**proved by patching it out** — a guard no mutation can break is decoration. The set: only a
file with no `<DokumentIndhold` and no sidecar becomes work; `accn/` paths are aliases; recorded
states survive a re-scan; `gone` is terminal while a parse failure stays retryable; `empty`
does not look like `fetched`; `--limit` clamps the *requests made*, not just the batches
started; body rows never surface in the inventory queue or vice versa; a short stored record
decodes instead of erroring; a sidecar can never be written as `.xml`, including under
`.XML`; an extension cannot smuggle path segments; and `TextLen` counts neither tags nor
invisible marks. Each mutation is anchored to one line, restores the file afterwards, and is
rejected if it breaks compilation instead of the assertion — a build failure proves nothing
about a guard.

`internal/lexdania` runs three times over: against hand-built fixtures that mirror the
element shapes of real documents, against a sample of the real archive, and — on
request — across every document in it:

```
RETSIN_FULL_SCAN=1 go test -timeout 4h -run TestScanAll ./internal/lexdania/ -v
```

The corpus tests are skipped when `data/xml` is absent (or point elsewhere with
`RETSIN_TEST_DATA`) and tolerate a small parse-failure rate because the crawl writes
while they read; they log the measured coverage numbers quoted above rather than
asserting guesses. They also read a bounded slice — a few hundred entries, recent laws
first — because the index begins in 1852, and walking it in that order parses thousands
of documents before reaching anything with an amending act: an unbounded loop over
`Entries()` was a 25-second test that became a four-minute one under `-race`. Reading
the whole archive is what the explicit hunt is for, not every regression check. The full
scan does the same at 6 GB scale and adds the hypotheses a
sample never reaches — whether any document has provisions written without `<Stk>`,
which containers actually hold provisions, how many `Afsnit` are numbered in roman
numerals, and which wordings never classify.

The same walk answers a question a sample cannot: *which laws are hardest for this
parser*. `TestHardestLaws` scores every law document that has a body by the shapes that
have actually broken this code — refused and unresolved edits, appendix collisions,
provisions whose `localId` disagrees with the printed number, provisions printed with a
number the law already uses, chains where two amendments share an effective date, and the
end-to-end case where consolidation achieves nothing — and prints the ranking with the
API URL of each law's oldest revision. It excludes the 29,924 law documents that are
metadata-only, and amending acts, which have no chain to be hard about. `RETSIN_TOP_N`
widens the printout; the top of the table is mostly the same five statutes re-consolidated
over and over (finansiel virksomhed, rettens pleje, udlændingeloven, ATP, and the
entry-into-force quotations each reissue carries).

Two opt-in dumps let an external source be compared against us without building anything:
`RETSIN_DUMP_CHAINS=A,B,C` writes our version chain per law, and
`RETSIN_DUMP_STRUCTURE=ACC,ACC@YYYY-MM-DD` writes our provisions, printed numbers,
subsections and text — as parsed, and after consolidating the amendments in force on that
date — to `RETSIN_DUMP_OUT`.

A guard is not believed until it is proved: each fix in `internal/lexdania` is written
out again by hand — the appendix skip removed, `donorClone` reduced to `Clone`, roman
numerals read additively, a renumbering trusted instead of checked — and the guard must
fail. A guard that still passes describes a bug the code no longer has, and two of them
did exactly that, which is how a case-sensitive matcher and a duplicated fallback were
found (below).

### Bugs the tests caught

- **The answers could not see their own horizon, twice over.** Giving every answer a
  dataset line (date asked, when the mirror last pulled, when the index was built)
  produced two defects that only tests written against the new contract could see.
  `horizon()` returned early when no full-text index was attached, before reading the
  collection record — so a lookup-only server, which is the ordinary configuration, said
  "never pulled" for a mirror crawled an hour ago; and the lookup path assigned
  `res.Warnings = doc.Warnings()` instead of appending, silently dropping the note about
  amendments the register's own metadata names. The first hides unless a test attaches no
  index; the second unless a test asserts a warning that is not the document's own.

- **A nightly inventory pass erased the whole catalogue's state, and an amended law
  could never be re-downloaded.** Two separate defects in the same path, both found by
  actually running yesterday→today rather than by reading the code. `PutInventoryBatch`
  wrote the sitemap's rows blind, so every `retsin update` reset all ~200k rows to
  pending with no hash, no accession and no attempt counter — the fetch then re-closed
  200k files locally and re-requested the ~10k resources already recorded as `gone`. And
  the fetch loop closes any row whose file is on disk without asking the site, which
  means the row a moved `lastmod` had just re-pended was closed rather than re-fetched:
  an amended document was unreachable forever after its first download, while the mirror
  reported itself current. The first is guarded by `TestPutInventoryBatchPreservesState`;
  the second needed a state of its own — `stale`, exempt from the on-disk shortcut — and
  is guarded by `TestInventoryPassMarksHeldRowsStale` plus `TestFetchReRequestsStaleRows`,
  which fails if the shortcut is re-applied to stale rows.

- **An empty anchor before the paragraph number silently shifted every stykke.** The
  site writes `<span id="P1"/>` before `<span class="ParagrafNr">`, so a reader that
  took the marker only at index 0 built no first stykke, and the payload's own
  `p.Stk2` became "stk. 1" — 3,699 canonical stykker unaddressable and every later
  reference one place early. Matched-with-identical-text went from 7,039 to 15,082.
- **Absorbing into two different depths reordered law text.** The list items of § 30
  landed inside the stykke while the sentence introducing them landed in the
  paragraph, and since the stykke precedes that sentence, the items read ahead of
  their own introduction. Conservation caught it as a reorder at character 17,591 of
  21,917; segmenting the absorbed run fixed it.
- **The conservation test's failure message had its two labels swapped,** so the first
  reading of every reorder report pointed at the wrong side of the comparison — long
  enough to send one round of debugging after the tree.
- **A mutation harness reported six holes that did not exist,** because a patch that
  leaves a name unused fails to compile (no test runs, so "not green" looked like
  "not caught") and one patch changed nothing at all. The harness now refuses to
  judge anything it cannot build, and brackets every mutation with a control run.
- **A guard that asked the code under test to justify itself caught nothing.** The sampler
  checked each manifest label by calling the same predicate the planner used, so patching
  out the size gate relaxed the check in the same breath and the mutation ran clean. The
  test now re-implements the three gates independently — needles, size, year — which is
  duplication done on purpose, and the reason `TestPlanStratumSelectsByMarker` is allowed
  to look redundant.
- **Manual `Accept-Encoding: gzip`** disabled Go's auto-decompression, so the sitemap
  arrived as 241 raw gzip bytes and failed XML decoding with a misleading syntax error.
- **Badger `Reverse` + `Prefix`** iterated empty, which would have silently broken
  harvest resumption. Replaced with a forward scan. `--order desc` needs reverse
  anyway, so `scanInventory` bounds the range by hand (seek above the last key of the
  prefix, stop at the first key outside it) and
  `TestBadgerPrefixAndReverseDoNotMix` pins the upstream behaviour: if that pairing
  ever starts returning rows, the hand-rolled bound can go.
- **`xml:"Meta>AccessionNumber"` inside a struct already bound to `Meta`** made the
  decoder look for `Dokument/Meta/Meta/AccessionNumber`. Go reports this by leaving the
  field *empty with no error*, so every document looked like it had no accession.
- **Flags after the subcommand were silently ignored** — `fetch -limit 3` ran uncapped.
  Now parsed per-subcommand, and misplaced global flags error out.
- **An HTML page counted as a retryable failure.** Some catalogue URIs have no XML
  representation: requesting `/xml` answers **HTTP 200 with the website's own page**.
  That was classified as a parse failure, so those resources were re-requested on
  every pass, up to five times each — harmless once, wasteful for years of daily
  updates. Now detected by payload and recorded as `gone`, alongside real 404s.
  Found in the first live run, not in tests: the fixture corpus had no example of it.
- **A retry sweep that never ended.** `--state gone` re-fetches a 404, the 404 is written
  back as `gone`, and the row therefore *still matches the filter* — so the next batch,
  which re-scanned from the start of the keyspace, offered the same rows again forever. A
  live run showed `255/83` on the bar and, in the log, URL order wrapping from
  `1967/9000` back to `1748/10001`: three full passes over the same 83 rows. `--state
  failed` looped the same way, because naming a state deliberately lifts the attempt
  ceiling, and that ceiling had been the only thing terminating the loop. Found in a live
  run by the operator, then pinned: the fetch loop now pages forward by ELI URL, so each
  selected row is attempted at most once per invocation. Deleting the cursor line makes
  `TestFetchStateRetryVisitsEachRowOnce` hang into its 5 s timeout, so the guard is real.
- **`Meta.Title` in `internal/docapi` is tagged `xml:"Titel"`.** The element is
  `DocumentTitle`, so the field never populates. Found while writing `lexdania`; **not
  fixed**, because `docapi` is superseded by it and nothing reads the field today. It
  is the failure mode `lexdania` avoids by projecting `Meta` by observed element name
  and warning on anything unmapped.
- **`Kapitel` and `Afsnit` ended the section walk.** The walker recorded the container
  and returned, so a law organised `Afsnit > Kapitel > ParagrafGruppe > Paragraf`
  reported **two sections and no paragraphs at all**. Every amendment to it then said
  "address not found" — including a real 2026 order where 0 of 4 changes applied.
  Containers are descended into now; `TestSectionsDescendIntoChapters` pins it.
- **Deleting a heading deleted the provision.** "Overskriften før § 3 ophæves" removed
  § 3 outright, so the same act's next change, "§ 3 ophæves", found nothing. Headings
  (`Rubrica`) and provisions are separate edits now, and
  `TestHeadingRepealThenProvisionRepeal` applies the real pair in order.
- **A heading rewrite was rejected before it was ever considered.** The
  replace-provision branch required structured new text, but "overskriften … ændres
  til: »X«" arrives as prose only. The heading case now runs ahead of that guard.
- **`§ 2 a.` did not match `§ 2a`.** Printed numbers carry a litra with a space;
  amendments cite it without one, so every lettered provision was unaddressable.
  `paraLitra` records the suffix.
- **Numbered items are `<Index><Indentatio>`, not `<Nr>`.** One real order contains 0
  `<Nr>` elements and 27 `<Index>` elements, so `stk. 1, nr. 2` resolved to nothing.
  Numbered items are now collected from both shapes, and plain `Indentatio` used for
  layout is still excluded rather than invented as a provision.
- **Numbered items were searched at the wrong depth.** `Find` looked for `nr` among a
  paragraph's direct children, but they are children of the `stk`.
- **`udgår` was not in the pattern set.** "I § 6, stk. 1, udgår »§ 3, stk. 2-3,«." is
  the ordinary Danish deletion wording and showed up as an unrecognised change in real
  2026 amendments.
- **Insertions were anchored on the wrong side.** "Efter § 3 indsættes en ny § 4" in an
  act that also repeals § 3 anchored positionally and then inserted *before* the lower
  neighbour, filing the new provision between § 1 and § 2. The anchor now carries its
  direction.
- **One accession regex hid two real schemes.** Requiring letter+11 digits marked every
  `ft/` bill and every alphanumeric type code (`G198050837R1`) malformed, and a
  fixed 5-digit bill number broke the 4-digit modern form.
- **The entry-into-force appendix renumbers from one, and the parser walked into it.**
  A consolidated act quotes the entry-into-force paragraph of every act that has amended
  it, and each quotation keeps the number it had in *its own* amending act — measured
  over the archive: § 2, § 13, § 20, § 3, § 5, § 70, out of order and with no `localId`.
  **9,707** documents carry **28,473** such provisions, and **15,688** of them are printed
  with the same number as a provision of the law itself. So "§ 1 ophæves" deleted a
  historical entry-into-force quotation from a law that has no § 1, `§§ 2-3 ophæves` swept
  the appendix's § 2 in with the real ones, and an insertion anchored "after § 3" — found
  positionally, by nearest number below 3 — landed inside `<Ikraft>`, making new law part
  of a historical note. `Section.Appendix` now marks them and `Find`, `rangeSections` and
  `positionalAnchor` skip them, so such an edit is refused in `Unresolved` instead of
  quietly corrupting the wrong text. Patching out each of the three skips fails
  `TestAddressResolutionIgnoresIkraftAppendix` or `TestInsertionAnchorsOutsideAppendix`.
- **Inserted provisions arrived wearing the amending act's `localId`.** Proposed text is
  written inside the *amending* act, where a new § 3 is `<Paragraf localId="9">` because
  9 is where it sits in that act's own sequence. Spliced into the law unchanged, that 9
  became the provision's number: the new § 3 could not be found by anyone, including the
  consolidator's own next pass, and the id also collided with the target law's `localId`
  values, which is what `<Ref>` elements resolve against. `donorClone` drops `localId` on
  the way in, so numbering comes from the printed `Explicatus` — which agrees with
  `localId` on 20,410 of 20,417 sampled provisions that carry both, so nothing else moves.
  Replace `donorClone` with `Clone` and `TestInsertionAnchorsOutsideAppendix` can no
  longer find the § 3 a it has just inserted.
- **A progress bar that rendered nothing at all.** The bar library latches its
  `finished` state the first time it renders while its max is still 0 — which is
  how `fetch` started, since the pending count only arrived with the first report.
  Setting the label triggers a render, so the very first label update killed the bar
  for the rest of the run, with no error and no output. The first fix was a wrapper
  that refused to paint until a total was known; the real fix is structural — the
  pending count is taken *before* the bar is built, so a zero-max bar never exists.
  `OptionClearOnFinish` was the second trap: it suppresses the completed frame, so a
  crawl that reaches its last document shows nothing.
- **`PendingInventory(ctx, 0, …)` returned one row.** The stop condition compared
  `len(out) >= limit` directly, so the "unlimited" value the rest of the tool uses
  (`--limit 0`) stopped the scan after the first document. `TestPendingInventoryDescending`
  asked for the whole catalogue and got a single row; the guard is now `limit > 0 && …`.
- **`--jitter 0` switched pacing off entirely.** Making the built-in 20 % jitter
  configurable introduced two bugs at once: `JitterPercentDefault / 100` is *integer*
  division, so the default came out as 0, and the new `jitter > 0` guard had been put on
  the branch that **respects the reserved slot** instead of on the jitter inside it. With
  jitter at 0, `Wait` returned immediately — silently discarding both the minimum
  interval and any `Retry-After` already applied to the bucket. `TestDelayPacesTheWholeBucket`
  and two `rihttp` throttle tests failed within the same second of writing it.
  `TestJitterZeroStillPaces` now pins the case on its own, and one assertion keeps
  `config.Default().JitterPercent` equal to the limiter's own default so the two cannot
  drift.

- **Roman numerals added up letter by letter, so "IV" did not exist.** `Afsnit` elements
  are the one structural level that carries no `localId` and prints its number as a roman
  numeral (`<Explicatus>Afsnit I</Explicatus>`), so the section list held 4,286 entries with
  an empty `Number` that no reference could match. Reading the printed numeral fixed that,
  and the first version of the reader then rejected every IV and IX: it looked a
  two-character symbol up in a table of single symbols, found nothing, and fell back to
  adding `i` and `v` side by side. Subtractive notation has to be a comparison between
  adjacent symbols. `TestRomanAfsnitNumbering` caught it on the run that introduced it, and
  keeps the rejections pinned too — Danish words are built from roman letters, so a
  round-trip check through `romanOf` is what stops "id" being numbered as 501.
- **`\bændres` cannot match anything.** Go's `\b` is an *ASCII* word boundary, so the
  alternation `\b(affattes|ændres|ophæves|indsættes|erstattes)\b` silently never matched
  the one verb that starts with `æ` — and the document-title branch was the only user of
  that regex, so "I lovens titel ændres »X« til: »Y«", the commonest way a Danish act edits
  its own title, kept coming out as an unrecognised address while the pattern looked
  correct to anyone reading it. The boundary is now `(?:^|[^\p{L}])`, Unicode-aware, which
  still refuses to read "forændres" as the verb. `TestTitleEditsSeePastASCIIWordBoundaries`
  fails with `\b` restored, and an A/B scan of the same archive (fix patched out) shows the
  bucket moving: 564 changes unparseable and 435 title addresses without it, 518 and 486 with
  it. Nothing in the corpus test suite could have found this by *volume* — it is 0.03 % of
  the changes; it is the same failure mode as the appendix and roman-numeral bugs, a regex
  that is quietly dead.
- **Zero-width marks in the archive made quoted amendments unappliable.** Of the 158
  subsections where our text disagreed with retsininformation-api.dk's and theirs was not
  simply truncated, 152 were characters we keep in the comparison form and they drop:
  U+00AD soft hyphen in 7,564 documents, U+200D in 3,448, U+200C in 2,953, U+200B in 145,
  U+FEFF in 30 — counted by a hunt over the whole archive, and visible in real text as
  `Ting­lysningsrettens` and `H‌øjesteret`. Soft hyphens were already dropped there, for
  exactly the reason that applies to the rest: a drafter quoting a provision does not
  reproduce the other document's typesetting. The zero-width marks were not, so an
  amendment quoting "Højesteret" could not find the provision that spells it with a
  non-joiner, and the change was rejected as *quoted passage not present* — reported as
  though the law did not contain the words, which is the same failure mode as the dead
  `\b` above: invisible, and silently a refusal instead of an edit. `matchRunes` now drops
  U+200B, U+200C, U+200D and U+FEFF alongside U+00AD; the mutation (case removed) fails
  `TestZeroWidthProvisionIsStillAmendable` with exactly that reason. Rendered text
  deliberately still keeps the marks — only the comparison form is normalised.
- **A guard that passed with the fix taken out.** Of the eight mutations written to prove
  this round's fixes, two survived. The word-boundary assertion looked for `Skibet`
  surviving a global replace of `»skib«`, but text matching is case-sensitive, so the naive
  implementation never touched it and the test could not tell the two apart — the fixture
  now carries a lowercase longer word (`når skibet bruges`), which the naive version really
  does rewrite into `fartøjet`. The other was duplicated logic: `applyRenumber` fell back to
  the block's running § a second time, so removing the substitution in `applyOne` changed
  nothing observable and left that path untested. One mechanism now, tested once. Both
  survivors are in `TestNewAddressUnitsApply`, and all eight mutations fail.
- **An element constructed before its meaning was decided.** The footnote marker's reading
  (reference or the note's own number) depends on whether a note paragraph is open, and that
  decision ran *after* `&Node{Name: target.Name}` had already been built — so renaming the target
  changed nothing and every legacy note produced a second footnote. Context-dependent readings are
  now resolved before anything is constructed.
- **The tag cannot tell a footnote's marker from a footnote reference.** The legacy note's own
  marker is `<a class="FodnoteNr" name="Not1" href="#Henvisning_Not1">`, so the discriminator is
  containment, not the tag: a tie-break on `a` vs `span` looked plausible and was wrong.
- **A `switch` arm made unreachable by a broader arm above it.** The cell case tested `td`/`th`
  *after* a case that also matched them, so every `<td>` closed its own row and a table's columns
  became rows. Found by the test that counts cells in a row, not by inspection.
- **A helper that was wrong in both directions at once.** The conservation reference walk tracked
  skipped subtrees with a plain depth counter, which drifts on the stray end tags and unclosed
  elements these payloads contain — it then blamed the builder for text it had quietly decided was
  skipped. Joining text tokens with a space, meanwhile, *invented* word separators the payload
  never had (`Skævinge 1)` where the markup has `Skævinge1)`). Both artifacts are gone: the walk is
  tag-aware, and the content comparison ignores whitespace entirely.
- **A freshly built node with no run panics the first flush.** `len(Runs) == len(Elems)+1` means a
  childless element has exactly one run; creating one with `Runs: nil` made the next text append
  index `[-1]`. The panic was immediate, which is the only kindness in that class of bug.

- **A measurement that measured nothing.** The payload filename → archive path conversion forgot the
  `.xml` suffix, so all 51 payloads looked unpaired, the fidelity test reported `documents=0`, and
  it **passed**. Zero documents is now a failure, not a result — a vacuous check is worse than an
  absent one because it looks like evidence.
- **A comparison that truncated in the middle of a letter.** Taking a probe's first 160 *bytes*
  cuts Danish text inside an `å` or an `ø`; an incomplete rune matches nothing, so the measurement
  would have reported builder losses that were its own truncation — and it later corrupted the test
  output's encoding when the runner decoded it. Truncation is by runes everywhere now.
- **A control text dumber than the thing under test.** The "was this text in the payload at all?"
  control stripped tags by scanning for `<` and `>`, which keeps the quotes inside attributes and
  the punctuation in malformed markup: it invented 36,816 missing `"` characters for one payload.
  The control is tokenized with the same library now, which is the point of a control.
- **A pinned baseline that could not fail.** The first mutation written against the fidelity pin
  disabled the comparison and the suite still passed, because the numbers had not changed. The pin
  is only meaningful if perturbing the *measurement* trips it, which is now the mutation.
- **Multi-class attributes.** `class="Bold Italic Subscript"` is three names in one attribute; the
  whole-value lookup never tried the components, so three elements in one payload went unreported.

## Notes and limitations

- The 12 s harvest pace is a constant with no flag, so the published 1 call / 10 s
  obligation cannot be configured away. The 200 ms document floor is a **default**, not
  a limit: `Validate()` only refuses a non-positive value, and pushing it lower is a
  deliberate operator choice answered by the service's own 429.
- The harvest feed only covers a rolling 10 days. An outage longer than that can only
  be repaired via `inventory`, which the tool warns about explicitly.
- `inventory` reports ingested rows including any cross-page duplicates; unique
  resources are ~201,600 (Badger is keyed by ELI URL, so duplicates collapse).
- Only the XML pass is implemented. `docapi.FetchJSON` exists as a working client for
  the site's JSON representation, but there is no sidecar crawl; XML is a superset for
  practical purposes.
- **Environment quirk:** this machine's `go` env file and module sumdb path are
  read-only, so adding dependencies needs `GOSUMDB=off`. All deps are already vendored
  into `go.sum`.

## Legal

Laws and regulations are not subject to copyright (Danish Copyright Act § 9). Use is
governed by [Civilstyrelsens terms](https://www.retsinformation.dk/api): free and
unrestricted, including commercial use, provided you do not imply official endorsement
and do not burden the service disproportionately. This tool identifies itself with a
contact address and self-throttles on those grounds.
