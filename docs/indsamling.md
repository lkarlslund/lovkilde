# Selv indsamle fra retsinformation.dk

Lovkilde-serveren læser én fil, *artefaktet*, som den selv henter. Artefaktet bygges af
`lovkilde-update`, der henter dokumenterne fra retsinformation.dk, holder dem opdaterede og
kompilerer dem. Denne side handler om `lovkilde-update`.

## Byg

```bash
make build        # bin/lovkilde og bin/lovkilde-update
```

## Første indsamling

```bash
lovkilde-update --data-dir /sti/til/data inventory   # katalog fra ELI-sitemappet
lovkilde-update --data-dir /sti/til/data harvest     # ændringsfeedet for de seneste 10 dage
lovkilde-update --data-dir /sti/til/data fetch       # dokumenterne, ét kald pr. dokument
lovkilde-update --data-dir /sti/til/data fetch --bodies   # tekst til dokumenter uden tekst i XML
lovkilde-update --data-dir /sti/til/data artifact build   # kompiler artefaktet
```

Det er omkring 200.000 dokumenter og 14 GB XML. En afbrudt kørsel kan bare startes igen:
dokumenter, der allerede ligger på disken, hentes ikke igen.

## Daglig opdatering

```bash
lovkilde-update --data-dir /sti/til/data update
lovkilde-update --data-dir /sti/til/data artifact build --mode patch
```

`update` læser ændringsfeedet og henter det, der er nyt, ændret eller mangler. Feedet dækker
kun de seneste 10 dage. Er den sidste kørsel ældre end det, stopper `update` og beder om
`update --full`, som også læser hele sitemappet. `--force` springer kontrollen over.

`artifact build --mode patch` kompilerer kun de dokumenter, hvis filer er ændret, og kopierer
resten fra det forrige artefakt. Er der intet ændret, skrives intet. En ændring af dataformatet
kræver `--mode full` (standard).

En server, der peger på samme datamappe (`lovkilde serve --data-dir …`), skifter selv til et
nyt artefakt.

## Hensyn til retsinformation.dk

Civilstyrelsens [vilkår](https://www.retsinformation.dk/api) kræver, at brugen ikke belaster
tjenesten urimeligt. Derfor:

- Hvert kald venter mindst `--floor` (standard 200 ms) plus en tilfældig tillægsventetid
  (`--jitter`, standard 10 %). Der bruges højst to forbindelser pr. vært.
- Svarer en tjeneste `429`, `503` eller `504`, fordobles ventetiden for den tjeneste, og den
  holdes i 10 minutter. `Retry-After` overholdes.
- User-Agent indeholder en kontaktadresse.
- Dokumenter, sitet afviser, prøves igen efter en uge. `404` og `410` noteres som væk.

## Kommandoer

| Kommando | Gør |
|---|---|
| `inventory` | Læser kataloget fra ELI-sitemappet |
| `harvest` | Læser ændringsfeedet |
| `fetch` | Henter XML for alt, der mangler eller er ændret |
| `update` | `harvest` + `fetch`; den daglige kørsel. `--full` tager også `inventory` med |
| `status` | Viser katalog, arkiv, sidste kørsel og hvad der mangler |
| `changes` | Viser feedets ændringer, og om de er hentet |
| `rebuild` | Genskaber tilstanden fra XML-filerne på disken uden at kalde sitet |
| `artifact build` | Kompilerer artefaktet (`--mode full` eller `patch`) |
| `publish` | Udgiver artefaktet og tilstanden |
| `restore` | Henter en udgivet tilstand ind i en tom datamappe |
| `sample` | Henter par af XML og websideversion til test af HTML-læseren |

Nyttige flag til `fetch`:

```
--kind lta,ltb          kun disse serier
--state failed          kun rækker med denne tilstand (pending, fetched, failed, gone, stale)
--limit 50              højst så mange
--order desc|random     rækkefølge
--bodies                hent tekst til dokumenter uden tekst i XML
--bodies-scan           tæl dem uden at kalde sitet
```

`lovkilde-update --help` viser alle flag.

## Datamappen

Standard er `.lovkilde` i hjemmemappen, både for `lovkilde` og `lovkilde-update`.

```
.lovkilde/
  xml/<type>/<år>/<nr>.xml     dokumenterne, som retsinformation.dk udleverer dem
  xml/<type>/<år>/<nr>.json    teksten fra websiden, for dokumenter uden tekst i XML
  state/                       indsamlingens tilstand (JSON Lines)
  sweep.stamp                  hvornår der sidst blev indsamlet
  artifacts/                   artefakter (.lkz) og søgeindeks (.lkiz)
  artifact.current             navnet på det artefakt, der er i brug
```

`state/` kan altid genskabes:

```bash
lovkilde-update rebuild     # fra XML-filerne, uden kald
lovkilde-update inventory   # kataloget
lovkilde-update fetch       # det, der mangler
```

## Udgivelse

En udgivelse er artefaktet og indsamlingens tilstand, samlet i et *release-repository*: en lokal
mappe eller et Hugging Face-datasæt (`hf://ejer/navn`).

```bash
lovkilde-update publish --release hf://ejer/navn            # artefakt og tilstand
lovkilde-update publish --release hf://ejer/navn --with-xml # også XML-arkivet, pakket pr. type og år
lovkilde-update restore --release hf://ejer/navn --with-artifact
```

`latest.json` beskriver udgivelsen: hver fils størrelse og SHA-256 og artefaktets dataformat.
Den skrives sidst, så den aldrig peger på en fil, der ikke er der. Skifter en udgivelse
dataformat, bliver det forrige formats sidste artefakt liggende, så servere, der endnu ikke er
opdateret, stadig har noget at læse.

En server henter fra et andet repository med `lovkilde serve --release hf://ejer/navn`.

`scripts/hf-release.sh` er hele den daglige udgivelse fra en ren checkout: hent den udgivne
tilstand, opdater, byg, udgiv. Den kræver `HF_REPO` og `HF_TOKEN` (eller en `.env`-fil med dem).
