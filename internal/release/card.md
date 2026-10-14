---
pretty_name: Retsinformation (compiled mirror)
language:
  - da
license: other
license_name: danish-public-legal-documents
license_link: https://www.retsinformation.dk
tags:
  - legal
  - law
  - denmark
  - legislation
size_categories:
  - 100K<n<1M
viewer: false
---

# Retsinformation, mirrored and compiled

> [!CAUTION]
> **Unofficial copy — not the official Retsinformation, and not legal advice.** The texts are
> fetched automatically from [retsinformation.dk](https://www.retsinformation.dk) and processed
> by machine, and they can contain errors. **Consolidated texts in particular** (a law "as of a
> date", with its amending acts worked in) are produced automatically, not by Civilstyrelsen:
> changes may be missing, misplaced or misread, and dates of entry into force may be judged
> wrongly. **All use is at your own risk.** Always check against the official version at
> retsinformation.dk, and seek legal advice before acting on any text.
>
> {{.NoLiability}}

Every document published on [retsinformation.dk](https://www.retsinformation.dk), the Danish
state's register of legislation: acts, consolidated acts, executive orders, circulars,
guidance, the Ombudsman's statements and the rest, as the site publishes them, plus a compiled
artifact that the [Lovkilde](https://github.com/lkarlslund/lovkilde) server reads.

This card is written by the release that publishes the files below; it describes that release.

| | |
|---|---|
| Published | {{.Published.Format "2006-01-02 15:04 UTC"}} |
| Documents in the artifact | {{.Documents}} |
| Document model | {{.Model}} |
| Artifact | `{{.File}}`, {{size .Size}} |
{{- if .XML}}
| Source archive | {{len .XML}} packs, {{.XMLFiles}} files, {{size .XMLSize}} |
{{- end}}

The collector checks the site daily. When a document changed since the last release, it builds
a new artifact and commits the artifact, the collection state and the source packs that changed
in one commit; a day with no changed document publishes no release.

## Files

- **`latest.json`**: the manifest. It names every file below with its size and SHA-256, and
  is committed last, so a reader that starts from it never finds a file missing.
- **`artifact-*.lkz`**: the compiled artifact, every document parsed into one model (seekable
  zstd, one frame per document).
- **`xml/<kind>/<year>.tar.zst`**: the source archive, as the site publishes it. Each pack holds
  the documents' XML and, beside them, JSON sidecars from the site's document API, whose
  `documentHtml` carries the text as HTML where the XML has none.
- **`state/`**: the collector's state (what was fetched, and when), from which the next release
  continues.

## Using it

The Lovkilde server downloads the artifact by itself when it has none, and checks for a newer
one daily:

```
lovkilde serve --release {{.Repo}}
```

`lovkilde artifact download --release {{.Repo}}` installs it without serving. A server that
already has an artifact downloads only the chunks the new one does not share with it.

The source packs are plain `tar` compressed with zstd:

```
tar --zstd -xf lta/1995.tar.zst
```

## Source and terms

The documents come from retsinformation.dk, run by Civilstyrelsen. Laws, executive orders,
circulars, judgments and similar decisions of public authorities are not protected by
copyright in Denmark (ophavsretsloven § 9). Check the site's terms for anything else.

This is an independent mirror; the official versions are the ones on retsinformation.dk.

{{.NoLiability}}
