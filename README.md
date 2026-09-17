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

## Architecture

```mermaid
flowchart LR
  subgraph CLI["retsin"]
    C1["inventory"] --> C2["harvest"] --> C3["fetch"]
    U["update = inventory+harvest+fetch"]
    S["status / rebuild"]
  end
  subgraph PACE["internal/limiter — process-global"]
    B["harvest 12s | document 1.2s | sitemap 2s"]
  end
  C1 & C2 & C3 --> PACE --> H["internal/rihttp<br/>1 conn/host · honest UA+contact<br/>429 → Retry-After + exp backoff+jitter"]
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

## Anti-blocking measures

1. **Process-global token buckets.** The 10 s harvesting limit is structural — no
   caller can bypass it, and `config.Validate()` refuses to start if
   `harvest_interval < 10s`.
2. **Throttle-aware backoff.** Any 429/503/504 honours `Retry-After`, then backs off
   exponentially 10 s → 15 min **with jitter**, and pushes the bucket forward so the
   *next* request is delayed too. Jitter prevents retry synchronisation.
3. **Conservative transport.** 1–2 connections per host, HTTP/2, no worker pool. Extra
   concurrency would only raise block risk without finishing sooner, since pacing is
   the bottleneck.
4. **Honest User-Agent** carrying a contact address, so the operator can email rather
   than ban.
5. **Idempotent, resumable phases.** A resource already on disk is closed with **no
   request at all**, so a multi-day crawl can be interrupted freely and a second pass
   costs nothing.
6. **Bounded retries.** Resources failing `MaxAttempts` times stop being retried
   instead of consuming the polite budget forever; non-retryable 4xx never retries.
7. **Run-end warning** if the server throttled us at all during the run.

## Running it

```bash
go build -o retsin ./cmd/retsin

# 1. Full catalogue — ~22 requests, ~50 seconds, ~203k resources
./retsin -config example-config.json inventory

# 2. Official change feed — 10 days × 12s ≈ 2 minutes
./retsin -config example-config.json harvest

# 3. Documents — 1 request per document, ~2.8 days at the default 1.2s
./retsin -config example-config.json fetch

# Daily thereafter (cron):
./retsin -config example-config.json update
```

`update` = inventory + harvest + fetch and is safe to run on any schedule: unchanged
documents are never re-requested.

**Estimated bootstrap:** 203,152 resources at 1.2 s pacing ≈ **2.8 days**, ≈22 GB raw.
Daily updates afterwards are ~1 harvest call plus a few hundred documents.

To go faster, lower `document_interval` — but note those requests are served uncached
straight from the origin, and the terms ask users not to burden the interface. The
first pass deliberately uses a conservative default; tighten it only if the run shows
headroom. Flags: `fetch -limit N` caps a test run, `-batch N` sizes the pending window.

## Recovery

If `meta/kv` is lost or suspect, in this exact order:

```bash
./retsin -config example-config.json rebuild     # relink on-disk XML → zero requests
./retsin -config example-config.json inventory   # re-discover the catalogue (~22 req)
./retsin -config example-config.json fetch       # resume; already-held docs are skipped
```

`rebuild` alone recovers only what is on disk; the catalogue comes back from the cheap
`inventory` pass.

## Tests

```bash
go test ./...
```

Covering: pacing guarantees, 429/`Retry-After` backoff, gzip handling, non-retryable
errors, UA identity, context cancellation, ELI path↔archive-path mapping (including
path-traversal rejection), Badger round-trips and pending/limit semantics, harvest
checkpointing, and XML parsing against **three genuine fixture schemas**.

### Bugs the tests caught

- **Manual `Accept-Encoding: gzip`** disabled Go's auto-decompression, so the sitemap
  arrived as 241 raw gzip bytes and failed XML decoding with a misleading syntax error.
- **Badger `Reverse` + `Prefix`** iterated empty, which would have silently broken
  harvest resumption. Replaced with a forward scan.
- **`xml:"Meta>AccessionNumber"` inside a struct already bound to `Meta`** made the
  decoder look for `Dokument/Meta/Meta/AccessionNumber`. Go reports this by leaving the
  field *empty with no error*, so every document looked like it had no accession.
- **Flags after the subcommand were silently ignored** — `fetch -limit 3` ran uncapped.
  Now parsed per-subcommand, and misplaced global flags error out.

## Notes and limitations

- `internal/config.Validate()` enforces the 10 s harvest floor and a 200 ms document
  floor; you cannot accidentally configure an aggressive crawl.
- The harvest feed only covers a rolling 10 days. An outage longer than that can only
  be repaired via `inventory`, which the tool warns about explicitly.
- `inventory` reports ingested rows including any cross-page duplicates; unique
  resources are ~201,600 (Badger is keyed by ELI URL, so duplicates collapse).
- `fetch_json` is plumbed but the JSON sidecar pass is not implemented; XML is a
  superset for practical purposes.
- **Environment quirk:** this machine's `go` env file and module sumdb path are
  read-only, so adding dependencies needs `GOSUMDB=off`. All deps are already vendored
  into `go.sum`.

## Legal

Laws and regulations are not subject to copyright (Danish Copyright Act § 9). Use is
governed by [Civilstyrelsens terms](https://www.retsinformation.dk/api): free and
unrestricted, including commercial use, provided you do not imply official endorsement
and do not burden the service disproportionately. This tool identifies itself with a
contact address and self-throttles on those grounds.
