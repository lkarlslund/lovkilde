# Lovkilde

Dansk lovgivning fra retsinformation.dk som søgbar hjemmeside og som MCP-server til
AI-assistenter. Lovkilde kører på din egen maskine og henter selv sine data.

> [!CAUTION]
> **Uofficiel kopi – ikke den officielle Retsinformation og ikke juridisk rådgivning.**
> Teksterne hentes automatisk fra [retsinformation.dk](https://www.retsinformation.dk) og
> behandles maskinelt, og de kan indeholde fejl. **Især konsoliderede tekster** (en lov som
> gældende på en dato, med ændringslovene indarbejdet) laves automatisk af dette program, ikke af
> Civilstyrelsen: ændringer kan mangle, stå forkert eller være læst forkert, og
> ikrafttrædelsesdatoer kan være vurderet forkert. **Al brug sker på eget ansvar.** Kontrollér
> altid mod den officielle udgave på retsinformation.dk, og søg juridisk rådgivning, før du
> handler på baggrund af en tekst.
>
> Projektet har ingen tilknytning til Civilstyrelsen eller retsinformation.dk. Programmet og
> data leveres, som de er, uden garanti af nogen art, og forfatterne påtager sig intet ansvar for
> tab eller skade som følge af brugen.

## Hvad kan den

- **Søge** i alle dokumenter i Retsinformation. Et hit er en bestemmelse (`§ 5, stk. 2`), ikke
  bare et dokument.
- **Vise** et dokument med indholdsfortegnelse, links til ændrede og ændrende dokumenter og
  link til den officielle side.
- **Slå en bestemmelse op** med dansk citatform: `§ 267, stk. 3, nr. 2`, `kapitel 5`, `bilag 2`.
- **Vise en lov som gældende på en dato.** Lovkilde indarbejder selv ændringslovene. Teksten er
  ikke officiel og er mærket som sådan.
- **Sige hvor teksten kommer fra.** Hvert svar oplyser, om teksten er registrets egen
  strukturerede tekst eller genskabt fra retsinformation.dk's webside.

Hjemmesiden og MCP-serveren giver de samme svar fra de samme data.

## Kom i gang

1. Hent programmet til dit styresystem fra
   [den seneste udgivelse](https://github.com/lkarlslund/lovkilde/releases/latest) og pak det ud.
2. Start det: dobbeltklik på det, eller kør det fra en terminal:

   ```bash
   lovkilde
   ```

3. Hjemmesiden åbner i din browser på <http://127.0.0.1:8787>. Accepter vilkårene.

Første gang henter Lovkilde datasættet (ca. 1,3 GB) og bygger sit søgeindeks. Siderne virker,
mens indekset bygges; søgningen slår til, når det er færdigt.

Data ligger i mappen `.lovkilde` i din hjemmemappe, uanset hvorfra programmet startes, eller hvor
du peger med `--data-dir`. Har serveren allerede hentet nyere data, bruges de.

## Brug med en AI-assistent

Lovkilde er en MCP-server. Når `lovkilde` kører, er den på `http://127.0.0.1:8787/mcp`.

**Claude Code:**

```bash
claude mcp add --transport http lovkilde http://127.0.0.1:8787/mcp
```

**Programmer, der selv starter serveren** (f.eks. Claude Desktop), bruger `lovkilde mcp`, som
taler MCP over stdin/stdout:

```json
{
  "mcpServers": {
    "lovkilde": {
      "command": "/sti/til/lovkilde",
      "args": ["mcp"]
    }
  }
}
```

Værktøjerne:

| Værktøj | Gør |
|---|---|
| `eula` | Henter vilkårene for brug. Assistenten skal vise dem til dig, og først når du har accepteret, kalder den igen med `accept: true`. Indtil da svarer de andre værktøjer ikke. |

Vilkårene accepteres én gang pr. server, enten på hjemmesiden eller gennem `eula`, og det gælder for
begge og huskes, også når serveren genstartes. Ændres vilkårene, skal de accepteres igen.
| `lookup` | Henter et dokument eller en bestemmelse. `document` er en ELI-sti (`eli/lta/2019/1399`) eller et accessionsnummer (`B20190139905`); `address` er en bestemmelse; `as_of` (ÅÅÅÅ-MM-DD) giver loven som gældende på datoen. |
| `search` | Søger. Bøjningsformer og sammensatte ord findes. `a AND b` kræver begge, `-ord` udelukker, `'to ord'` kræver begge i samme bestemmelse. Kan afgrænses på type, år, tekstkilde og status. |

## Hjemmesiden

| Side | Indhold |
|---|---|
| `/` | Søgefelt og hvad serveren indeholder |
| `/search` | Søgeresultater, med samme syntaks som ovenfor og filtre for type, år og status |
| `/browse` | Alle dokumenter efter type, år og forbogstav |
| `/doc?ref=…` | Et dokument; `&addr=§ 5, stk. 2` viser én bestemmelse |
| `/status` | Hvad serveren indeholder, hvor nyt det er, og den fulde ansvarsfraskrivelse |
| `/settings` | Hvor ofte der tjekkes for nye data og ny version, og API-nøgle |
| `/eula` | Vilkårene for brug |
| `/mcp` | MCP-serveren |

## På netværket

Som standard lytter Lovkilde kun på denne maskine. Sådan gør du den tilgængelig for andre:

```bash
lovkilde serve --addr 0.0.0.0:8787 --no-browser
```

Så står der ⚠ ved "Indstillinger", indtil du har sat en **API-nøgle** dér. En nøgle er altid
valgfri. Er den sat, beder hjemmesiden om den én gang pr. browser, og en MCP-klient sender den
som `Authorization: Bearer <nøgle>`:

```bash
claude mcp add --transport http lovkilde http://server:8787/mcp --header "Authorization: Bearer <nøgle>"
```

Der er ingen TLS. Sæt en reverse proxy foran, hvis serveren skal kunne nås fra internettet.

## Opdateringer

- **Data:** Serveren tjekker dagligt for et nyere datasæt og tager det i brug af sig selv. Kun
  de ændrede dele hentes.
- **Programmet:** Serveren tjekker dagligt, om der er en nyere version. Findes der en, hentes den
  til `.lovkilde/program`, og det står øverst på alle sider. Næste gang Lovkilde startes, kører
  den nyeste hentede version, også hvis du starter en ældre kopi, f.eks. den i din
  download-mappe. Loggen siger, når det sker.
- **Nyt dataformat:** Kræver nye data en nyere version af programmet, fortsætter serveren med de
  data, den har, og skriver på alle sider, at programmet skal opdateres.

Begge tjek kan sættes til dagligt, ugentligt, månedligt eller fra på `/settings`, hvor de
også kan køres med det samme.

## Kommandoer

```
lovkilde                     det samme som lovkilde serve
lovkilde serve               hjemmeside og MCP-server på én port; åbner browseren
lovkilde mcp                 MCP over stdin/stdout
lovkilde search "ord"        søg fra kommandolinjen
lovkilde index build         byg søgeindekset (serve gør det selv)
lovkilde artifact download   hent det udgivne datasæt
lovkilde artifact check      kan denne version læse datasættet på disken?
lovkilde program check       er der udgivet en nyere version af programmet?
lovkilde program download    hent den; den bruges fra næste start
```

Vigtige flag til `serve` og `mcp`: `--data-dir`, `--addr` og `--no-browser` (kun `serve`), `--max-chars` (længste
MCP-svar, standard 20.000 tegn) og `--auto-update` (hvor ofte der tjekkes for nye data; `0` slår
det fra). `lovkilde --help` viser resten.

## Begrænsninger

- Konsoliderede tekster laves maskinelt og kan være forkerte. Brug dem til orientering.
- Omkring halvdelen af lovene har ingen tekst i registrets XML. For dem er teksten genskabt fra
  retsinformation.dk's webside og mærket "genskabt fra websiden".
- Nogle få dokumenter vil retsinformation.dk ikke udlevere. Antallet står på `/status`.

## Selv indsamle fra retsinformation

Datasættet bygges med `lovkilde-update`, som henter dokumenterne fra retsinformation.dk og
samler dem i én fil. Det behøver du ikke selv at gøre. Vil du alligevel, står det i
[docs/indsamling.md](docs/indsamling.md).

Hvordan Lovkilde læser og konsoliderer dokumenterne, står i [docs/opbygning.md](docs/opbygning.md).

## Juridisk

Love og bekendtgørelser er ikke omfattet af ophavsret (ophavsretslovens § 9). Brugen af data er
reguleret af [Civilstyrelsens vilkår](https://www.retsinformation.dk/api): fri brug, også
kommercielt, så længe man ikke giver indtryk af at være officiel og ikke belaster tjenesten
urimeligt.

## Licens

Koden er under [MIT-licensen](LICENSE). Dokumenterne er ikke omfattet af den; se ovenfor.
