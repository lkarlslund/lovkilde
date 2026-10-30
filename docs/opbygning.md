# Opbygning

## To programmer

- **`lovkilde-update`** henter dokumenterne fra retsinformation.dk og kompilerer dem til ét
  artefakt. Det er det eneste program, der kalder sitet eller læser registrets XML.
- **`lovkilde`** læser artefaktet og intet andet: ingen XML, ingen indsamlingstilstand og intet
  netværk ud over at hente artefakter og tjekke for nye versioner. En test
  (`TestTheServerLinksNoCollectionOrXML`) sikrer, at serveren ikke kommer til at linke
  indsamlingskoden.

## Pakker

| Pakke | Indhold |
|---|---|
| `internal/lexdoc` | Dokumentmodellen: bestemmelser, stykker, numre, kapitler, bilag, prosa med afsnit, fremhævning, lister og tabeller. Adresser (`§ 5, stk. 2`), konsolidering og visning som HTML, markdown og ren tekst. |
| `internal/lexdania` | Læser registrets XML og websidens JSON ind i modellen. |
| `internal/compiled` | Artefaktet: skrivning, læsning og dataformatets version (`ModelVersion`). |
| `internal/search` | Søgeindekset. |
| `internal/mcpserver` | Opslag, søgning og MCP-værktøjerne. |
| `internal/webserver` | Hjemmesiden. |
| `internal/crawl`, `internal/limiter`, `internal/rihttp` | Indsamlingen og dens tempo. |
| `internal/release` | Udgivelse og hentning af artefakter. |
| `internal/updates` | Tjek for nye lovtekster og ny version af programmet, og hentningen, når brugeren beder om den: på `/settings` eller med MCP-værktøjet `update`. |
| `internal/softwareupdate` | Nye versioner af programmet: tjek mod GitHub og hentning til `<datamappe>/program`. `lovkilde` kører den nyeste hentede version, hvis den er nyere end sig selv. |

## Dokumentmodellen

Et dokument læses én gang til `lexdoc.Document`, og alt derefter arbejder på modellen, ikke på
registrets markup. Modellen gemmer ingen elementnavne fra XML'en.

Teksten kommer fra én af to kilder, og modellen siger hvilken:

- `xml`: registrets egen strukturerede tekst.
- `html`: teksten fra retsinformation.dk's webside, for dokumenter hvis XML kun har metadata.
  Registrets egen tekst bruges altid, når den findes.

Tekst uden for bestemmelserne (indledning, bilag skrevet som prosa, en kendelses brødtekst)
gemmes med afsnit, fed og kursiv, listepunkter og tabelceller, så den kan vises med sin
struktur.

## Visning

Én gennemgang af modellen skriver tre former: HTML til hjemmesiden, markdown til MCP-svar og ren
tekst til søgeuddrag. Ordene er de samme i alle tre; kun opmærkningen er forskellig. Derfor kan
en side og et MCP-svar ikke sige noget forskelligt.

## Artefaktet

Artefaktet (`.lkz`) er en zstd-strøm i Zstandard Seekable Format med én frame pr. dokument,
komprimeret mod en indlejret ordbog. Et opslag læser og pakker ét dokument ud. Artefaktet
indeholder:

- hvert dokuments model som JSON,
- hver ændringslovs ændringer, allerede læst,
- et katalog med nummer, titel, type, år, status og datoer for hvert dokument.

Ordbogen ligger fast i `internal/compiled/dictionary`. Så længe den er den samme, komprimeres et
uændret dokument til de samme bytes, og en ny udgivelse overfører kun de dele, der er ændret. En
ny ordbog betyder derfor, at hele artefaktet overføres én gang. Sådan trænes en ny:

```bash
go run ./tools/dictsamples -until <år> -out <mappe>
zstd --train-fastcover -T0 --maxdict=1048576 <mappe>/* -o internal/compiled/dictionary/retsin-N.zdict
```

Mål den på dokumenter fra efter `<år>`, før den tages i brug.

Headeren angiver dataformatets version. En server læser kun sin egen version. Ændres det, der
gemmes, skal `compiled.ModelVersion` sættes op; `TestModelVersionCoversTheStoredModel` fejler,
hvis det glemmes.

## Konsolidering

`lookup` med `as_of` giver en lov som gældende på en dato. Ændringslovene i lovens kæde læses
til `lexdoc.Edit`: hvad ændringen gør, hvor, og med hvilken tekst. Det sker fra registrets
ændringsmarkup, hvor den findes, og ellers fra ændringsteksten. `Content.AsOf` anvender dem i
rækkefølge efter ikrafttræden.

En ændring, der ikke kan placeres sikkert, afvises og står i svaret sammen med grunden. Den
gættes ikke på plads.

## Søgning

Indekset (`.lkiz`) bygges af serveren selv ud fra artefaktet og gemmes ved siden af det. Det
passer kun til det artefakt, det er bygget fra; ellers bygges et nyt. En søgeenhed er en
bestemmelse eller et nummer, så et hit kan citeres præcist. Indekset gemmer ingen lovtekst;
uddrag læses fra artefaktet.

## Udvikling

```bash
make build     # bin/lovkilde og bin/lovkilde-update
make test      # go test ./...
make deploy    # byg og genstart den lokale tjeneste (deploy/lovkilde.service)
```

Nye tests skal være billige: helst en lille test i `internal/lexdoc`, bygget af nogle få
segmenter, frem for en test over hele arkivet.

En ændring af læsningen eller konsolideringen kontrolleres mod arkivet med tre værktøjer, før
og efter ændringen:

```bash
go run ./tools/chainsweep -n 1000 -examples 0 -dump fil.txt   # anvendte og afviste ændringer
go run ./tools/goldenpair -check 300                          # mod lovenes næste lovbekendtgørelse
go run ./tools/pairs <accession>                              # enkelte love i detaljer
```

Et fald i antallet af afviste ændringer er kun en forbedring, hvis de to andre ikke viser flere
forkert placerede.
